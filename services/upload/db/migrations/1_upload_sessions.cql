-- Upload sessions table
-- Partition by upload_id for fast lookups
-- Stores metadata about each upload session

CREATE TABLE IF NOT EXISTS upload_sessions (
  upload_id       uuid PRIMARY KEY,
  user_id         uuid,
  filename        text,
  total_size      bigint,
  chunk_size      bigint,
  content_type    text,
  status          text,        -- pending | in_progress | completed | aborted
  created_at      timestamp,
  expires_at      timestamp
) WITH CLUSTERING ORDER BY (upload_id ASC)
  AND gc_grace_seconds = 86400
  AND default_time_to_live = 2592000;  -- 30 days
