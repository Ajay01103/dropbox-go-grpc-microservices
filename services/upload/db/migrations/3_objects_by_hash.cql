-- Obsolete migration slot retained for migration history compatibility.
CREATE TABLE IF NOT EXISTS blocks (
  block_hash      text PRIMARY KEY,
  size_bytes      bigint,
  ref_count       bigint,
  storage_backend text,
  storage_key     text,
  etag            text,
  created_at      timestamp
);