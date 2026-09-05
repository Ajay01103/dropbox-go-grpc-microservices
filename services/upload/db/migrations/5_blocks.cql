CREATE TABLE IF NOT EXISTS blocks (
  block_hash      text PRIMARY KEY,
  size_bytes      bigint,
  ref_count       bigint,
  storage_backend text,
  storage_key     text,
  etag            text,
  created_at      timestamp
) WITH gc_grace_seconds = 86400
  AND default_time_to_live = 0;

CREATE TABLE IF NOT EXISTS block_gc_candidates (
  block_hash  text PRIMARY KEY,
  zeroed_at   timestamp,
  eligible_at timestamp
) WITH gc_grace_seconds = 86400;