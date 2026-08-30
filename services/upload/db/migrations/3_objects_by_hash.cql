-- Dedup index: content hash -> canonical storage object
-- Maps SHA256 hash to storage reference and ref count
-- Used in Build Order 4 for content deduplication

CREATE TABLE IF NOT EXISTS objects_by_hash (
  content_hash    text PRIMARY KEY,
  storage_key     text,
  created_at      timestamp,
  ref_count       counter
) WITH gc_grace_seconds = 86400
  AND default_time_to_live = 2592000;  -- 30 days
