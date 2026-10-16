-- Version history for in-browser editing (PDF / DOCX / XLSX).
--
-- (file_id, version) rows are IMMUTABLE: an edit commits a new row and moves a
-- head pointer, it never rewrites the previous version's bytes. That is what
-- makes "your previous version is kept" true, and what lets a save detect that
-- it lost a race instead of silently overwriting someone else's work.
--
-- Additive only, and deliberately tolerant of files that predate it: a file
-- with no row here and no file_head row is simply version 1, described by its
-- own row in files_by_folder/files_by_id (see the lazy migration in
-- versions_repo.go). Nothing has to be backfilled before this is useful.
--
-- Re-runnable by construction - CREATE TABLE IF NOT EXISTS is a no-op on re-run.

CREATE TABLE IF NOT EXISTS file_versions (
  file_id         uuid,
  version         int,
  block_hash_list list<text>,
  block_size      bigint,
  size_bytes      bigint,
  content_hash    text,
  content_type    text,
  filename        text,
  sniffed_type    text,
  base_version    int,
  editor          text,
  commit_id       text,
  created_by      uuid,
  created_at      timestamp,
  PRIMARY KEY ((file_id), version)
) WITH CLUSTERING ORDER BY (version DESC)
  AND gc_grace_seconds = 86400;

CREATE TABLE IF NOT EXISTS file_head (
  file_id         uuid PRIMARY KEY,
  current_version int,
  content_hash    text,
  updated_at      timestamp
) WITH gc_grace_seconds = 86400;
