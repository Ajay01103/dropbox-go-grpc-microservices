# Phase 5: Permanent Purge — Decrement Interface Design (v2)

## Decision: async, NATS-based decrement, dedicated BLOCK_REFS stream

Metadata publishes on `blocks.refs.decrement.requested`; upload consumes and
publishes `blocks.refs.decrement.completed`. Both live on a dedicated
`BLOCK_REFS` JetStream stream (subjects `blocks.refs.>`), separate from
`UPLOAD_EVENTS`, so upload lifecycle events, block-reference commands, and
GC audit events don't share a stream.

**The permanent-delete RPC does not block on the completion event.** It
returns once the purge job is durably in `DECREMENT_REQUESTED`. This is a
correction from v1 of this note, which left "wait for completion" ambiguous
enough to be implemented as a blocking call — that would reintroduce the
sync coupling the whole design exists to avoid. Clients that need to know
when a purge finishes poll job status by `job_id` (see "Client-facing
status" below); completion itself is handled entirely by the Phase 6
consumer, asynchronously from the original request.

## Identity: (job_id, occurrence)

A block hash can appear more than once in an ordered `block_hash_list`
(e.g. a file made of repeated chunks). The unit of work is therefore
`(job_id, occurrence)`, not `(job_id, block_hash)`. `job_id` alone identifies
the purge; `occurrence` is the index into the ordered block list, so each
logical decrement operation is unique even when the same block hash repeats.

## Operation ledger: the real source of truth

Scylla counter mutations cannot share an LWT/batch with an ordinary table,
so "insert the operation record" and "apply the counter decrement" are two
separate writes with a crash window between them. `block_decrement_operations`
resolves this by acting as a **claim-then-apply** ledger rather than a
simple dedup flag:

```
CLAIMED           -- operation row inserted via IF NOT EXISTS; claims this
                     (job_id, occurrence) so no other delivery can process it
  -> COUNTER_APPLIED   -- counter decrement has been durably applied
  -> COMPLETE           -- included in a published BlockRefDecrementCompleted
FAILED                  -- terminal, e.g. invalid block_hash
```

Processing rule on each delivery (including redeliveries):

1. Attempt `INSERT ... IF NOT EXISTS` for `(job_id, occurrence)`.
   - If it applies, this delivery owns the operation: proceed to decrement.
   - If it does not apply, read the existing row's status:
     - `CLAIMED` and stale (no progress within a threshold) → resume from
       the counter-apply step. This may apply the counter decrement twice if
       the original worker applied it before crashing; that is intentional in
       this design and is corrected by reconciliation from the ledger. Do not
       add a guard that leaves stale claims permanently stuck.
     - `COUNTER_APPLIED` or `COMPLETE` → skip the decrement entirely,
       just re-derive the result and (re)publish completion.
2. Apply the atomic counter decrement, then update the ledger row to
   `COUNTER_APPLIED`.
3. Include the operation in the next `BlockRefDecrementCompleted` publish,
   then update the ledger row to `COMPLETE`.

Because step 1's insert and step 2's counter update are not atomic with
each other, a crash between them leaves a `CLAIMED` row whose counter state
is unknown. Resuming a stale claim may therefore double-apply the decrement;
this is an intentional at-least-once projection update, not an exactly-once
guarantee. Reconciliation recomputes the counter projection from the ledger
and repairs the drift. Do not attempt to make stale-claim recovery appear
exactly-once by leaving claims stuck indefinitely.

### By-block-hash lookup for GC

GC needs to ask "is there any in-flight operation touching this block
hash?", but the ledger's primary key is `(job_id, occurrence)`. A secondary
table, `block_decrement_operations_by_hash`, is maintained alongside the
ledger for this lookup (see CQL). GC treats any non-`COMPLETE`/`FAILED` row
for a hash as a reason to skip deletion this cycle.

## Reconciliation is allowed to repair the counter projection

The counter (`block_ref_counts`) is explicitly a **projection** of the
ledger, not independent source of truth. Reconciliation is therefore
permitted to recompute and correct counter drift by summing applied
ledger operations plus current live acquisitions — this is a non-destructive
repair of a projection, distinct from the "no destructive repairs" rule,
which continues to apply to block/object *deletion*. GC must still never
delete based on a single unreconciled counter read (Phase 9 already
requires rechecking the ledger and live references before deleting).

## Superseded: `block_decrement_dedup`

The `block_decrement_dedup` table from the v1 artifact is superseded by
`block_decrement_operations` and should not be created. If the earlier
migration was already applied in a dev environment, drop it; it does not
carry enough information (no per-occurrence identity, no partial-progress
state) to safely resume a crashed operation.

## purge_jobs_by_state

`purge_jobs_by_state` is a denormalized view maintained on every state
transition, written *after* `purge_jobs` itself. This means there is a
window where `purge_jobs` reflects a new state but `purge_jobs_by_state`
still reflects the old one. This is accepted as a known, bounded
inconsistency: the Phase 11 recovery sweep scans `purge_jobs_by_state` to
find stuck jobs, and a job that's briefly missing from the correct state
bucket is simply picked up on the next sweep cycle rather than immediately.
This is safe because the recovery sweep is periodic and idempotent, not
because the two tables are kept in sync — if that ever becomes
insufficient (e.g. sweep interval too coarse for the drift window), revisit
with a stricter write order or a single-table state index instead of a
denormalized second table.

## Client-facing status

Callers of the permanent-delete RPC receive `job_id` and the job's state at
return time (`DECREMENT_REQUESTED` on the happy path). A separate
`GetPurgeJobStatus(job_id)` read is used to poll to completion if the
caller needs to know when purge fully finishes. Repeated calls to the
permanent-delete RPC for the same file/version find the existing job by a
deterministic lookup keyed by `(file_id, file_version)` and return its
current status rather than creating a new job.

## Retry and recovery configuration

Purge requests, completion handling, and ledger recovery use explicit
configuration rather than hard-coded timing values:

```text
PURGE_MAX_ATTEMPTS                 maximum attempts before FAILED
PURGE_INITIAL_RETRY_DELAY          delay before the first retry
PURGE_MAX_RETRY_DELAY              upper bound for exponential backoff
PURGE_RETRY_BACKOFF_MULTIPLIER     backoff multiplier, for example 2.0
PURGE_REQUEST_TIMEOUT              timeout for publishing/processing a request
PURGE_COMPLETION_TIMEOUT           time DECREMENT_REQUESTED may await completion
PURGE_STALE_JOB_THRESHOLD          age after which a purge job is recoverable
BLOCK_LEDGER_STALE_CLAIM_THRESHOLD age after which CLAIMED may be resumed
PURGE_RECOVERY_INTERVAL            metadata purge-job recovery interval
BLOCK_LEDGER_RECOVERY_INTERVAL     upload ledger recovery interval
```

Retries retain the original `job_id` and `(job_id, occurrence)` identities.
Backoff exhaustion moves the job to `FAILED` and emits metrics/alerts. A
stale `CLAIMED` operation is resumed according to the intentional at-least-
once projection policy described above.

## State machine

```
PENDING
  -> DECREMENT_REQUESTED       (RPC returns here — does not block further)
  -> DECREMENTED               (all operations terminal, no reconciliation errors)
  -> DECREMENTED_WITH_ERRORS   (all operations terminal, one or more errors)
  -> METADATA_REMOVED
  -> COMPLETE
(any state) -> FAILED

`DECREMENTED_WITH_ERRORS` is an explicit operator/reconciliation state. It is
not equivalent to a normally in-flight `DECREMENTED` job. By default it does
not proceed to metadata removal until the configured policy permits it or the
reconciliation issue is resolved.
```
