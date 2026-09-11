# File & Folder Service Design (Block-Storage Aware)

This builds on `UPLOAD_FLOW.md`. The upload path already exists and is content-addressed:
a file is not a blob, it's an ordered `block_hash_list` of 4 MB SHA-256 blocks stored at
`blocks/<h[0:2]>/<h[2:4]>/<h>`, shared across files/users, ref-counted in Scylla.

Any Files/Folders design must not break that dedup model. That constraint drives decision #1 below.

---

## 1. S3 Organization — why `user-[id]/folder/...` is the wrong key scheme here

The instinct to lay out S3 like `user-[id]/anyfolder/file` is the right instinct for a
**blob-per-file** system. This system is not that — it's block-per-content. If block keys are
made user- or folder-scoped, two users uploading the same 4 MB chunk (e.g. the same stock PDF,
the same video intro, the same OS installer) would each get their own physical copy in S3. That
destroys the entire point of `blockRepo`/`ref_count` and silently multiplies storage cost.

**Recommendation: keep two separate namespaces in the same bucket, one physical, one logical.**

```
s3://<bucket>/
├── blocks/<h[0:2]>/<h[2:4]>/<h>          content-addressed, global, immutable, dedup'd
│                                          (this already exists — do not change it)
├── thumbnails/<sha256-of-hashlist>.jpg   content-addressed, global, immutable
├── staging/<user_id>/<upload_id>/...     OPTIONAL — see §1.3 (native multipart / large-file path)
└── exports/<user_id>/<job_id>/<name>     OPTIONAL — zip/export jobs, see §7.4
```

Physical S3 keys stay **content-addressed and user-agnostic**. "Which user owns which file, in
which folder" is a **metadata-layer concern**, resolved entirely in Scylla via `owner_id` /
`folder_id` columns on the `files` row — never encoded into the block's S3 key. This is exactly
how Dropbox, Google Drive and git itself separate "object storage" (content-addressed, shared)
from "tree/ref storage" (per-user, mutable, pointers into the object store).

So the mental model is:

```
User's view (virtual, DB-only):        Physical storage (S3, real):
/Alice/Photos/beach.jpg          --->   file row: block_hash_list = [a1f..., 9c2..., ...]
/Bob/Shared/beach.jpg            --->   file row: block_hash_list = [a1f..., 9c2..., ...]  (same blocks!)
```

Both files can point at the _same_ underlying blocks if the bytes are identical, cross-user, at
zero extra storage cost, with zero cross-user data leakage (a user can only reach a block through
their own file's `block_hash_list`, which is authorized at the metadata layer, not the S3 layer).

### 1.1 Why not just prefix with user id anyway "for organization"?

Three concrete costs, no benefit:

- **Breaks dedup** as described above (biggest issue).
- **S3 has no folders.** `user-42/Photos/beach.jpg` is not a directory — it's one flat key with
  slashes in it. You get zero real benefit (no atomic folder rename, no folder-level ACL) versus
  keeping it in Scylla, and you inherit S3's well-known **prefix hot-partitioning** problem: if
  key layout mirrors user activity (e.g. sequential `user_id`s uploading at the same time), you
  can hit a single partition's request-rate limit. A hash-prefixed key (`h[0:2]/h[2:4]/...`) is
  exactly the fix S3 itself recommends, and the existing block path already does it.
- **Folder rename/move becomes an S3 operation** (copy+delete every object under the "folder"),
  instead of a single `UPDATE folders SET parent_id = ...` — O(files) instead of O(1).

### 1.2 What if you genuinely need per-user physical isolation (compliance, BYOK, tenant export)?

If a future requirement forces it (e.g. enterprise customer needs their own KMS key, or "delete
my account" must be a single S3 prefix delete for legal reasons), don't re-key blocks. Instead:

- Keep the shared `blocks/` pool as the default tier.
- Add an optional **per-tenant bucket or prefix** (`s3://<bucket>/tenant-<id>/blocks/...`) that a
  tenant is pinned to at upload time (dedup then scopes to that tenant only, not globally). This
  is a deliberate cost/isolation trade-off — document it, don't default to it.

### 1.3 Staging area for large/native uploads (optional, forward-looking)

The current path already chunks client-side into 4 MB blocks before write, so a raw "staging"
area isn't required today. If you later add native multipart uploads that bypass block hashing
(e.g. mobile clients streaming straight to S3 multipart), stage those under
`staging/<user_id>/<upload_id>/part-<n>`, and only content-address + move into `blocks/` once the
object is fully received and can be chunked/hashed. Treat `staging/` as ephemeral — a lifecycle
rule should expire anything older than ~24h that never finalized.

### 1.4 Bucket lifecycle & versioning

- **Bucket versioning: off** for `blocks/` and `thumbnails/` — content-addressed keys are
  immutable by construction (same hash ⇒ same bytes), so S3 object versioning adds cost with no
  benefit there. Deletion is handled by the app (ref-count GC, §6), not by S3 lifecycle rules.
- **`staging/`**: lifecycle rule to expire objects after 24–48h.
- **`exports/`**: lifecycle rule to expire after 7 days (matches presigned URL practice below).
- Enable **SSE-S3 or SSE-KMS** bucket-wide by default; see §8 for per-tenant KMS.

---

## 2. Data Model Extensions

Building on the existing `blocks` and `files_by_folder` Scylla tables.

### 2.1 `folders` table (new)

```text
folder_id       uuid            PK
owner_id        uuid            partition key component (or use owner_id as full partition key
                                 with folder_id as clustering key, so ListFoldersByOwner is cheap)
parent_id       uuid            NULL for root; root folder is auto-created per user at signup
                                 and is otherwise the same as today's convention
                                 (session.UserID == folder_id) for the root case only
name            text
path_cache      text            materialized "/Photos/2024/" for breadcrumb + fast display
                                 (recomputed on rename/move — see §5.3)
created_at      timestamp
updated_at      timestamp
is_deleted      boolean         soft delete (trash)
deleted_at      timestamp       null unless is_deleted
```

Secondary access pattern needed: "list child folders of folder X" →
`folders_by_parent (parent_id, folder_id) PRIMARY KEY (parent_id, folder_id)` as a materialized
view / second table, since Scylla can't efficiently query `WHERE parent_id = ?` on the primary
table without it.

### 2.2 `files_by_folder` — additions to the existing table

The existing table already has `file_id, version, owner_id, block_hash_list, thumbnail_key,
thumbnail_status`. Add:

```text
folder_id       uuid            replaces the current "folder_id = user_id root convention" —
                                 becomes a real FK into `folders`
filename        text
size_bytes      bigint
content_type    text
content_hash    text            full-file sha256 (already used for InitUpload dedup)
is_deleted      boolean         soft delete / trash
deleted_at      timestamp
version         int             already exists — see §4.6 for versioning semantics
current         boolean         true for the "live" version row of a file_id
```

### 2.3 Trash semantics

Both files and folders soft-delete: `is_deleted = true, deleted_at = now()`. Nothing touches S3
or `ref_count` at soft-delete time. A background sweep (§6.2) purges rows past a retention window
(e.g. 30 days) and only then decrements block ref counts. This mirrors Dropbox's own "deleted
files reappear in Trash for 30 days" behavior and gives you free accidental-delete recovery.

---

## 3. Files Service (CRUD)

Extends `proto/metadata/metadata.proto`. `CreateFile` already exists (called by the upload
service at finalization) — this section covers the rest.

### 3.1 Proto sketch

```protobuf
service FileService {
  rpc GetFile(GetFileRequest) returns (File);
  rpc ListFiles(ListFilesRequest) returns (ListFilesResponse);   // paginated, folder-scoped
  rpc RenameFile(RenameFileRequest) returns (File);
  rpc MoveFile(MoveFileRequest) returns (File);                  // change folder_id
  rpc DeleteFile(DeleteFileRequest) returns (DeleteFileResponse); // soft delete -> trash
  rpc RestoreFile(RestoreFileRequest) returns (File);
  rpc PermanentlyDeleteFile(PermanentlyDeleteFileRequest) returns (google.protobuf.Empty);
  rpc DownloadFile(DownloadFileRequest) returns (stream DownloadChunk); // see §7
  rpc ListTrash(ListTrashRequest) returns (ListTrashResponse);
}
```

### 3.2 Get / List

- `GetFile(file_id)`: auth check `owner_id == user_id` (or later, ACL check once sharing exists),
  return metadata row. Do not return `block_hash_list` to the client on a plain `GetFile` — that's
  an internal detail only the download path needs; leaking it externally lets a client "read" a
  file's byte layout with no reason to.
- `ListFiles(folder_id, cursor, page_size, sort)`: paginate via Scylla's native paging state
  (opaque cursor), not `OFFSET`. Default sort by `updated_at DESC`; support `name`, `size`.

### 3.3 Rename

`RenameFile(file_id, new_name)` — metadata-only write, no S3/block interaction. Validate name
(no `/`, length limit, reserved chars) and uniqueness within the folder if you want Dropbox-style
"a file already exists" conflict resolution (`beach (1).jpg`).

### 3.4 Move

`MoveFile(file_id, new_folder_id)` — validate the destination folder exists, belongs to the same
owner (or is shared-with-write-access, once sharing exists), and isn't in trash. Metadata-only
write (`UPDATE files_by_folder SET folder_id = ?`). No block/S3 changes — this is the payoff of
keeping physical storage decoupled from logical location.

### 3.5 Delete (soft) / Restore / Permanent delete

- **Soft delete**: `UPDATE ... SET is_deleted = true, deleted_at = now()`. File disappears from
  normal listings, appears in `ListTrash`. Fast, reversible, no S3 touch.
- **Restore**: `is_deleted = false`. If the original folder was itself deleted/gone, restore into
  root and surface that to the client (Dropbox does the same).
- **Permanent delete**: explicit user action ("Delete forever") or automatic sweep after the
  retention window. This is the only path that touches `ref_count` — see §6.

### 3.6 Versioning (if/when you want it)

`files_by_folder` already carries a `version` int, so this is close to free: `RenameFile`/content
edits don't need a new version, but a _content_ change (re-upload same filename) should insert a
new row with `version = current + 1`, `current = true`, and flip the previous row's `current` to
false rather than overwriting it — each version keeps its own `block_hash_list`, so old blocks
stay referenced (ref_count intact) until that specific version is purged. This gives you
Dropbox-style "restore previous version" for free from the block model.

---

## 4. Folders Service (CRUD)

### 4.1 Proto sketch

```protobuf
service FolderService {
  rpc CreateFolder(CreateFolderRequest) returns (Folder);
  rpc GetFolder(GetFolderRequest) returns (Folder);
  rpc ListFolderContents(ListFolderContentsRequest) returns (ListFolderContentsResponse); // files + subfolders
  rpc RenameFolder(RenameFolderRequest) returns (Folder);
  rpc MoveFolder(MoveFolderRequest) returns (Folder);
  rpc DeleteFolder(DeleteFolderRequest) returns (DeleteFolderResponse);   // recursive soft delete
  rpc RestoreFolder(RestoreFolderRequest) returns (Folder);
  rpc GetBreadcrumbs(GetBreadcrumbsRequest) returns (GetBreadcrumbsResponse);
}
```

### 4.2 Create

`CreateFolder(parent_id, name)` — validate parent exists, belongs to caller, isn't trashed;
validate name uniqueness among siblings. Every user gets a root folder auto-provisioned at
signup (`parent_id = NULL`), matching today's "folder_id = user_id" convention, except now it's a
real row in `folders` rather than an implicit convention baked into upload code.

### 4.3 Move — cycle detection

The one genuinely tricky part of folder CRUD: moving folder A into folder B must reject the case
where B is a descendant of A (that would create a cycle and orphan the whole subtree). On move,
walk `parent_id` up from the destination to the root (bounded by max folder depth, e.g. 20) and
reject if `folder_id` (the one being moved) appears in that chain. With `path_cache` materialized
(§2.1), this is a cheap string-prefix check instead of N lookups: reject if
`dest.path_cache` starts with `moving.path_cache`.

### 4.4 Rename / Move — path_cache maintenance

Renaming or moving a folder invalidates `path_cache` for that folder **and every descendant**.
Two options:

- **Recompute lazily**: don't store `path_cache` at all, compute breadcrumbs on read by walking
  `parent_id` up (bounded depth, cacheable in Redis per folder_id with short TTL). Simpler, no
  write fan-out, slightly more read latency — recommended default.
- **Materialize eagerly**: on rename/move, walk the subtree and rewrite `path_cache` for every
  descendant folder. Only worth it if breadcrumb reads are extremely hot and folders are shallow;
  the write fan-out is unbounded in the worst case (someone renames their root folder).

Recommendation: **lazy + Redis cache**, matching the existing pattern (`upload:<id>:last_offset`
Redis usage already establishes that convention in this codebase).

### 4.5 Delete — recursive soft delete

`DeleteFolder(folder_id)` marks the folder `is_deleted = true` **and cascades the flag** to every
file and subfolder beneath it, so `ListFiles`/`ListFolderContents` on a live parent never surfaces
orphaned children. Two implementation options:

- **Explicit cascade write** at delete time: walk the subtree, batch-update `is_deleted = true` on
  every folder and file found. Bounded cost proportional to subtree size, paid once, at delete
  time — recommended, since it keeps reads simple (no runtime "is any ancestor deleted?" check).
- **Implicit cascade at read time**: only flag the top folder, and have `ListFiles`/`GetFile`
  check ancestor chain for `is_deleted`. Avoids the fan-out write but pushes cost onto every read
  forever — not recommended.

Restore of a folder should NOT automatically un-delete children that were independently deleted
before the folder delete — track deletion provenance (or, simpler for v1: restoring a folder
restores everything that was deleted as part of that same cascade operation, using a shared
`deleted_batch_id`).

---

## 5. Garbage Collection & Block Ref-Counting

This is the part unique to the block-storage design and the part most likely to leak storage
cost or, worse, corrupt data if built casually.

### 5.1 Fix the non-atomic ref count first

`UPLOAD_FLOW.md` already flags this: `IncrementRefCount`/`DecrementRefCount` do read-then-write,
which races under concurrent uploads/deletes sharing a block. Before wiring deletes into it,
switch to one of:

- **Scylla counter columns** (`ref_count counter`) — atomic increment/decrement, no read-modify-
  write race, native to Scylla. Simplest fix, recommended.
- **Application-level lock** (Redis `SETNX` per block hash) — works, but adds a network hop and a
  failure mode (lock holder crashes) that counters avoid entirely.

### 5.2 GC worker (new)

A scheduled job (cron / k8s CronJob), analogous to the existing thumbnail worker:

1. On **permanent file/version delete**, decrement `ref_count` for every hash in that version's
   `block_hash_list` (via the atomic counter from §5.1).
2. A separate sweep queries `blocks WHERE ref_count = 0` (needs a secondary index or a
   `blocks_by_refcount` materialized view, since Scylla doesn't do ad-hoc `WHERE` well) and, for
   blocks that have been at zero for longer than a grace window (e.g. 24h — protects against a
   race where a new upload is mid-flight referencing the same hash before its `InsertBlock`
   lands), deletes the S3 object and removes the `blocks` row.
3. Emit a NATS event (`blocks.gc.deleted`) for observability/audit, same pattern as
   `uploads.object.stored`.

The grace window matters: without it, a block hitting `ref_count = 0` transiently (e.g. last file
referencing it was just deleted, and a new upload with the same content is InitUpload'd a second
later) could be GC'd out from under the new upload. 24h is generous and cheap (blocks are small).

### 5.3 Reconciliation

Same philosophy already used for the NATS publish failure ("reconciliation will repair"): the GC
worker should also periodically cross-check `blocks` rows against actual S3 objects (and vice
versa) to catch drift from any failure mode above, logging/alerting on mismatches rather than
silently auto-repairing destructive operations.

---

## 6. Download Design

Nothing in the current backend serves a reconstructed file back out — this needs to be built.
Two viable architectures; recommend starting with the first and adding the second only if profiling
demands it.

### 6.1 Server-side streaming proxy (recommended default)

```text
GET /api/rpc/files/{file_id}/download   (Range header optional)
  -> Next proxy -> file/download handler (new, or extend metadata service)
  -> auth check: owner_id == user_id (or shared-with-read)
  -> load file row -> block_hash_list, size_bytes, content_type
  -> for each block hash in order (or the subset covered by Range):
       readBlock(hash)   // same helper the thumbnail worker already uses
       write bytes to response stream
  -> response: Content-Type, Content-Length, Content-Disposition: attachment; filename=...
```

- **HTTP Range support** (needed for video/audio seeking and resumable downloads): since blocks
  are fixed 4 MB, mapping a byte range to blocks is arithmetic, not a scan —
  `startBlock = floor(rangeStart / blockSize)`, `endBlock = floor(rangeEnd / blockSize)`. Stream
  full blocks in between, trim the first/last block to the requested byte offsets, respond
  `206 Partial Content` with `Content-Range`.
- This reuses the exact `readBlock` abstraction the thumbnail worker already has (S3 `GetObject`
  or local file read) — no new storage code path, just a new consumer of it.
- Keeps `block_hash_list` server-side only; the client never needs to know about blocks.

### 6.2 Client-assembled download (optional optimization, later)

For very large files or to reduce backend egress/CPU, you could return a short-lived manifest of
presigned S3 GET URLs (one per block) and have the browser fetch blocks directly from S3 in
parallel, assembling them client-side (e.g. via the Streams API / `ReadableStream`, or server-side
`Content-Type: multipart` isn't well supported by browsers, so client reconstruction typically
means either (a) sequential fetch + Blob concatenation, or (b) a Service Worker intercepting a
virtual URL and streaming chunks into it). This shifts bandwidth cost to direct S3 egress
(cheaper than proxying through your compute) at the cost of significant client complexity and of
briefly exposing block existence/hashes to the client (low sensitivity, since block hashes reveal
nothing about content, but still worth noting). **Don't build this until the proxy approach is a
measured bottleneck.**

### 6.3 Zip/multi-file export (optional, later)

"Download folder as zip" needs a job, not a request/response: enqueue a job, stream blocks for
every file in the folder into a zip writer, upload the result to `exports/<user_id>/<job_id>/`,
notify the client (poll or websocket) when ready, serve via presigned URL with a short TTL (e.g.
1 hour), and let the `exports/` lifecycle rule (§1.4) clean it up.

---

## 7. Security & Multi-Tenancy

- **No direct client → S3 access to `blocks/`.** All reads/writes go through your services, which
  is already true today (browser never touches RustFS/S3 directly, only the upload proxy). Keep
  it that way for downloads too (§6.1) — don't hand out presigned URLs into the shared `blocks/`
  pool per-block unless you've deliberately chosen the §6.2 trade-off, since a leaked presigned
  URL to a shared block is a leaked URL to _every_ file, past and future, that happens to contain
  that exact 4 MB chunk.
- **Every RPC re-validates `owner_id == user_id`** (or ACL, once sharing exists) at the metadata
  layer — the S3 layer has no concept of ownership by design (§1), so authorization must live
  entirely in the services, same pattern the existing auth interceptor already establishes.
  Folder-scoped operations (move/delete) must check ownership of _both_ the source and
  destination folder.
- **Encryption**: bucket-default SSE-S3/SSE-KMS (§1.4) covers most needs. If a tenant requires
  their own key, that's the isolated-bucket/prefix path from §1.2 — dedup naturally scopes to
  "within one KMS domain," which is the correct behavior for that requirement anyway (you don't
  want tenant-KMS-A blocks physically decryptable via a tenant-KMS-B reference).

---

## 8. Event Bus Additions

Extend the existing NATS `UPLOAD_EVENTS` pattern (schema-versioned JSON, `nats.MsgId` for dedup)
with new subjects, so downstream consumers (search indexer, activity feed, thumbnail worker
which already exists) can react without polling:

```text
files.file.renamed
files.file.moved
files.file.deleted        (soft)
files.file.restored
files.file.purged         (permanent — triggers block ref-count decrement, §5.2)
folders.folder.created
folders.folder.renamed
folders.folder.moved
folders.folder.deleted    (cascade soft delete)
blocks.gc.deleted         (§5.2, observability)
```

Same durability posture as today: publish failure logs a warning and never fails the primary
metadata write ("reconciliation will repair"), consistent with the existing
`publishObjectStoredEvent` behavior.

---

## 9. Rollout Plan

1. **Migration**: add `folders` table + `folders_by_parent` view; add new columns to
   `files_by_folder` (§2). Backfill: for every existing user, create a root `folders` row and
   point their existing files' `folder_id` at it (today's convention already has `folder_id =
user_id`, so this backfill is a 1:1 mapping, not a guess).
2. **Fix ref-count atomicity** (§5.1) _before_ wiring any delete path into it — deletes are the
   first thing that will expose the existing race under real concurrency.
3. **Ship Files CRUD** (§3) — Get/List/Rename/Move/soft-Delete/Restore. Wire into the existing
   Files page UI (rename/move/delete actions, trash view).
4. **Ship download** (§6.1, server-proxy first). This is very likely the most user-visible gap.
5. **Ship Folders CRUD** (§4) — enables real hierarchy instead of the flat root-only convention.
6. **Ship GC worker** (§5.2) once permanent delete exists and ref-count is atomic — don't run GC
   against the racy read-modify-write implementation.
7. **Optional/later**: versioning (§3.6), zip export (§6.3), client-assembled download (§6.2),
   per-tenant isolated storage (§1.2).

---

## 10. Summary: S3 Key Scheme (one-line reference)

```text
blocks/<h[0:2]>/<h[2:4]>/<h>              content-addressed, global, unchanged from today
thumbnails/<sha256-of-hashlist>.jpg       content-addressed, global, unchanged from today
staging/<user_id>/<upload_id>/part-<n>    optional, ephemeral, only if native multipart is added
exports/<user_id>/<job_id>/<name>         optional, TTL'd, only for zip/export jobs

Ownership, folder structure, and "which file is where" live ENTIRELY in Scylla
(files_by_folder.folder_id, folders.parent_id) — never in the S3 key.
```
