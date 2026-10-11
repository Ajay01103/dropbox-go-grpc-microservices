-- Tier B work index for the stitcher sweeper.
--
-- stitched_objects is keyed by file_id, which is what the download path needs
-- (a point read per download). That key cannot answer the sweeper's question —
-- "every file awaiting consolidation" — without a WHERE state = ? scan, which
-- Scylla refuses outright (it might involve data filtering). This index is
-- partitioned by state instead, so the sweep is one read per actionable state,
-- mirroring the purge_jobs_by_state pattern.
--
-- Only actionable states are mirrored (pending | stitching | failed). A file's
-- row is removed from this index the moment it becomes 'ready', so the index
-- stays proportional to outstanding work rather than growing with the whole
-- file population.
--
-- The base table remains the source of truth: a stale index row costs the
-- sweeper one wasted claim that the base row immediately declines, never a
-- wrong download.
CREATE TABLE IF NOT EXISTS stitched_objects_by_state (
  state    text,
  file_id  uuid,
  PRIMARY KEY ((state), file_id)
) WITH gc_grace_seconds = 86400;
