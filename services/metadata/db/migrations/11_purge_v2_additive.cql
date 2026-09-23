-- B2a: additive-only schema changes for the purge saga and sweeper work list.
-- No DROPs, no renames. Safe on fresh and existing clusters.

-- 1. Batching columns (written by B3, recorded here so B3 ships no DDL).
ALTER TABLE purge_jobs ADD (batch_count int, batches_done set<int>, invalid_ops set<int>);

-- 2. Terminal purge job rows self-clean after 7 days instead of eager delete.
ALTER TABLE purge_jobs WITH default_time_to_live = 604800;

-- 3. Sweeper work list, bucketed by hour of creation. Inserted at job create,
--    deleted on COMPLETE, while FAILED rows stay for the 30d TTL for alerting.
CREATE TABLE IF NOT EXISTS purge_jobs_active (
  bucket text,
  job_id uuid,
  state text,
  next_attempt_at timestamp,
  PRIMARY KEY ((bucket), job_id)
) WITH default_time_to_live = 2592000 AND gc_grace_seconds = 86400;

-- 4. Thumbnail sweeper work list (sweeper itself ships in B3 — it republishes
--    the v2 FileStored event). Created now so B3 needs no DDL.
CREATE TABLE IF NOT EXISTS thumbnail_pending (
  bucket text,
  file_id uuid,
  owner_id uuid,
  file_version bigint,
  created_at timestamp,
  PRIMARY KEY ((bucket), file_id)
) WITH default_time_to_live = 604800;
