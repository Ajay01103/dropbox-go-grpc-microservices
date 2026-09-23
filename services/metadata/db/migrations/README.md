# Metadata migrations

Numbering for gocqlx migrations is **lexical**, not numeric. `migrate.FromFS`
globs `*.cql` and applies files in sorted-name order, so any `10_…`–`19_…`
name sorts **before** `2_…`–`9_…`, and `z*` names sort after everything.
That is why the newest files use a `z` prefix instead of a number: it
guarantees they run last.

Current order (`ls migrations | sort`):

```
1_files_by_folder.cql
10_purge_jobs.cql
11_purge_v2_additive.cql
2_folders_by_user.cql
3_add_thumbnail_columns.cql
4_add_storage_key.cql
5_add_block_hash_list.cql
6_drop_legacy_metadata_schema.cql
8_folder_hierarchy.cql
9_file_metadata_extensions.cql
z11_folder_items.cql
z12_recent_items.cql
z13_purge_drop_jobs_by_file_version.cql
z14_purge_drop_jobs_by_state.cql
z15_purge_jobs_drop_has_reconciliation_errors.cql
z17_drop_folders_by_user.cql
```

(The `7_` gap and the `10/11` names are pre-existing history — lexical order
still puts every numeric name before the z-files, so they are harmless.)

## Rules for new migrations

1. **Schema additions / new tables**: continue the numeric sequence
   (`12_…`, `13_…`) — safe, because additive CREATEs have no ordering
   constraint against the z-files.

2. **Drops / anything that must run LAST**: continue the **z sequence**
   (`z18_…`, `z19_…`). Never name a drop `12_…` — it would run before
   `2_folders_by_user.cql` on a fresh keyspace and fail.

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
     already gone. See `z15_purge_jobs_drop_has_reconciliation_errors.cql`.

4. **Table drops** may be plain CQL — `DROP TABLE IF EXISTS` is a no-op on
   re-run. See `z13_purge_drop_jobs_by_file_version.cql` and
   `z14_purge_drop_jobs_by_state.cql`.

4b. **Never put a `;` inside a comment line.** gocqlx splits the file into
   statements at every `;`, including ones inside `--` comments — a comment
   containing a semicolon produces a comment-only "statement" that fails CQL
   parsing and wedges the migration on fresh keyspaces (this bit
   `11_purge_v2_additive.cql` once).

5. **Confirm no secondary index or materialized view references a column
   before dropping it** — the drop is rejected (or silently breaks the view)
   otherwise.

B4 destructive history: z13/z14 drop the two purge lookup indexes,
z15 drops `purge_jobs.has_reconciliation_errors` (the Connect wire field
stays, populated from `len(invalid_ops) > 0`). Post-B4 hardening: z17 drops
the dead `folders_by_user` table (its only writers, `CreateFolder` and
`ListUserFolders`, were unreferenced).
