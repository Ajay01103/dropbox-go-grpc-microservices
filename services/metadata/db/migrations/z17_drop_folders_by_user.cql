-- B4 follow-up: drop the dead folders_by_user table.
--
-- The only writers/readers were FolderRepo.CreateFolder and
-- ListUserFolders, both unreferenced (folder hierarchy lives entirely in
-- folders_by_owner / folders_by_id / folders_by_parent). No secondary
-- index or materialized view references this table, so the drop is safe.
-- Verified by grep across the repo before adding this migration.
--
-- Re-runnable by construction - DROP TABLE IF EXISTS is a no-op on re-run.
DROP TABLE IF EXISTS folders_by_user;
