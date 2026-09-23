# Upload migrations

Numbering for gocqlx migrations is **lexical**, not numeric. `migrate.FromFS`
globs `*.cql` and applies files in sorted-name order, so any `10_…`–`19_…`
name sorts **before** `2_…`–`9_…`. That is why the newest files use a `z`
prefix instead of a number: it guarantees they run last.

Current order (`ls migrations | sort`):

```
1_upload_sessions.cql
2_upload_chunks.cql
3_objects_by_hash.cql
4_add_block_upload_state.cql
5_blocks.cql
6_drop_legacy_upload_schema.cql
7_drop_obsolete_tables.cql
8_block_decrement_operations.cql
9_blocks_state.cql
z10_block_decrement_operations_by_hash_drop.cql
z11_blocks_drop_storage_key.cql
z16_block_gc_candidates_ttl.cql
```

## Rules for new migrations

1. **Schema additions / new tables**: continue the numeric sequence
   (`12_…`, `13_…`) — safe, because additive CREATEs have no ordering
   constraint against the z-files.

2. **Drops / anything that must run LAST**: continue the **z sequence**
   (`z12_…`, `z13_…`). Never name a drop `13_…` — it would run before
   `2_upload_chunks.cql` on a fresh keyspace and fail.

3. **Column drops must NOT be CQL statements.** CQL has no
   `DROP COLUMN IF EXISTS`, and gocqlx runs statements one at a time without
   a transaction: if the ALTER applies but the process dies before the
   migration is recorded, the next boot re-runs it and fails with
   "column not found" — boot is stuck. Instead:
   - write a `.cql` file whose ONLY content is the `-- CALL <name>;` marker
     line — gocqlx splits statements on `;` and treats everything before it
     (including comment lines) as one statement, so any extra comment text
     in a marker file becomes unparseable CQL;
   - implement the drop in Go (`migrate.go: dropColumnIfPresent`), which
     checks `system_schema.columns` first and is a no-op when the column is
     already gone. See `z11_blocks_drop_storage_key.cql`.

4. **Table drops** may be plain CQL — `DROP TABLE IF EXISTS` is a no-op on
   re-run. See `z10_block_decrement_operations_by_hash_drop.cql`.

4b. **Never put a `;` inside a comment line.** gocqlx splits the file into
   statements at every `;`, including ones inside `--` comments — a comment
   containing a semicolon produces a comment-only "statement" that fails CQL
   parsing and wedges the migration on fresh keyspaces (this bit
   `9_blocks_state.cql` once).

5. **Confirm no secondary index or materialized view references a column
   before dropping it** — the drop is rejected (or silently breaks the view)
   otherwise.

B4 destructive history: z10 drops the by-hash decrement ledger, z11 drops
`blocks.storage_key`. Post-B4 hardening: z16 adds a 30-day TTL to
`block_gc_candidates` (bounds a wedged GC worker; see the file header).
