ALTER TABLE files_by_folder ADD updated_at timestamp;
ALTER TABLE files_by_folder ADD is_deleted boolean;
ALTER TABLE files_by_folder ADD deleted_at timestamp;
ALTER TABLE files_by_folder ADD current boolean;
ALTER TABLE files_by_folder ADD deleted_batch_id uuid;

CREATE TABLE IF NOT EXISTS files_by_id (
  file_id          uuid PRIMARY KEY,
  folder_id        uuid,
  owner_id         uuid,
  filename         text,
  size_bytes       bigint,
  content_type     text,
  content_hash     text,
  block_hash_list  list<text>,
  version          int,
  created_at       timestamp,
  updated_at       timestamp,
  thumbnail_key    text,
  thumbnail_status text,
  is_deleted       boolean,
  deleted_at       timestamp,
  deleted_batch_id uuid,
  current          boolean
) WITH gc_grace_seconds = 86400;

CREATE TABLE IF NOT EXISTS files_by_folder_updated (
  folder_id        uuid,
  updated_at       timestamp,
  file_id          uuid,
  owner_id         uuid,
  filename         text,
  size_bytes       bigint,
  content_type     text,
  content_hash     text,
  version          int,
  created_at       timestamp,
  is_deleted       boolean,
  current          boolean,
  PRIMARY KEY ((folder_id), updated_at, file_id)
) WITH CLUSTERING ORDER BY (updated_at DESC, file_id ASC)
  AND gc_grace_seconds = 86400;

CREATE TABLE IF NOT EXISTS files_by_folder_name (
  folder_id        uuid,
  filename          text,
  file_id           uuid,
  owner_id         uuid,
  size_bytes       bigint,
  content_type     text,
  content_hash     text,
  version          int,
  created_at       timestamp,
  updated_at       timestamp,
  is_deleted       boolean,
  current          boolean,
  PRIMARY KEY ((folder_id), filename, file_id)
) WITH CLUSTERING ORDER BY (filename ASC, file_id ASC)
  AND gc_grace_seconds = 86400;

CREATE TABLE IF NOT EXISTS files_by_folder_size (
  folder_id        uuid,
  size_bytes       bigint,
  file_id          uuid,
  owner_id         uuid,
  filename         text,
  content_type     text,
  content_hash     text,
  version          int,
  created_at       timestamp,
  updated_at       timestamp,
  is_deleted       boolean,
  current          boolean,
  PRIMARY KEY ((folder_id), size_bytes, file_id)
) WITH CLUSTERING ORDER BY (size_bytes DESC, file_id ASC)
  AND gc_grace_seconds = 86400;
