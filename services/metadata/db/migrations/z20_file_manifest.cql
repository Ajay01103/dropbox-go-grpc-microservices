-- Dense download manifest: one row per (file_id, chunk_index), carrying the
-- block hash AND its exact byte size, so range-mapped reconstruction reads a
-- few rows instead of doing one GetBlock size lookup per block (the N+1 the
-- plain block_hash_list forces). Content-addressed and immutable per file
-- version. Rows are only written at CreateFile and deleted by the purge
-- coordinator when the file is permanently deleted.
CREATE TABLE IF NOT EXISTS file_manifest (
  file_id    uuid,
  chunk_index int,
  hash       text,
  size_bytes bigint,
  total_size bigint,
  content_type text,
  content_hash text,
  PRIMARY KEY ((file_id), chunk_index)
) WITH CLUSTERING ORDER BY (chunk_index ASC)
  AND gc_grace_seconds = 86400;
