-- B4 (Part C phase 5): drop the purge_jobs_by_file_version lookup index.
--
-- FindByFileVersion (its only reader) had no callers and is deleted in the
-- same PR, along with the indexFileVersion dual-writer. Job lookup is by
-- job_id (the derived uuid.NewSHA1(Nil, "<file_id>:<version>") id) or the
-- purge_jobs_active work list.
--
-- Safe to re-run after a partial failure: DROP TABLE IF EXISTS is a no-op
-- when the table is already gone.
DROP TABLE IF EXISTS purge_jobs_by_file_version;
