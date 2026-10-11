-- Tier B (consolidated object + presigned URL download fast path):
-- registry of per-file 'stitched' S3 objects. One row per file. The
-- background stitcher claims rows ('stitching'), performs the server-side
-- UploadPartCopy assembly, and marks them 'ready'. The metadata download
-- RPC consults this table: ready rows get a presigned S3 GET (CDN-fast
-- path). Everyone else falls back to live block reconstruction.
CREATE TABLE IF NOT EXISTS stitched_objects (
  file_id      uuid PRIMARY KEY,
  state        text,     -- stitching | ready | failed
  object_key   text,     -- s3 key of the consolidated object (state=ready)
  size_bytes   bigint,
  etag         text,
  attempts     int,
  claimed_at   timestamp,
  completed_at timestamp
) WITH gc_grace_seconds = 86400;
