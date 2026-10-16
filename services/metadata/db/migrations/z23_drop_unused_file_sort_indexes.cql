-- Drop the two per-folder file sort indexes that nothing writes and nothing
-- reads any more.
--
-- files_by_folder_name and files_by_folder_size came from the original
-- per-folder sort design: one index table per sort order (name, size, updated).
-- Only the updated_at-keyed one still has a reader (ListFilesByFolder, which
-- orders a page of a folder's files by it). These two have had no writer since
-- the folder item indexes landed in z11_folder_items. Verified by grep across
-- the repo before adding this migration: the only remaining references were two
-- point deletes in the purge, one per table.
--
-- The purge's deletes against these tables are removed in the same change, so
-- no live writer can fail against a dropped table. Rows an earlier revision
-- left behind go with the table.
--
-- Re-runnable by construction - DROP TABLE IF EXISTS is a no-op on re-run.
-- Keep these comments free of semicolons, which the migration runner treats as
-- statement separators even inside a comment.
DROP TABLE IF EXISTS files_by_folder_name;
DROP TABLE IF EXISTS files_by_folder_size;
