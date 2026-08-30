-- Files by folder table
-- Partition by folder_id for fast directory listing
-- Cluster by file_id for consistent ordering

CREATE TABLE IF NOT EXISTS files_by_folder (
  folder_id           uuid,
  file_id             uuid,
  filename            text,
  size_bytes          bigint,
  content_type        text,
  content_hash        text,        -- SHA256 of full file, used for dedup
  version             int,
  created_at          timestamp,
  owner_id            uuid,
  parent_folder_id    uuid,
  PRIMARY KEY (folder_id, file_id)
) WITH CLUSTERING ORDER BY (file_id ASC)
  AND gc_grace_seconds = 86400;
