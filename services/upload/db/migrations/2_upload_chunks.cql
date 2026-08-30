-- Upload chunks tracking table
-- Partition by upload_id, cluster by offset for fast range queries
-- Tracks which chunks have been persisted and their checksums

CREATE TABLE IF NOT EXISTS upload_chunks (
  upload_id       uuid,
  offset          bigint,
  sha256_checksum text,
  persisted_at    timestamp,
  PRIMARY KEY (upload_id, offset)
) WITH CLUSTERING ORDER BY (offset ASC)
  AND gc_grace_seconds = 86400
  AND default_time_to_live = 2592000;  -- 30 days
