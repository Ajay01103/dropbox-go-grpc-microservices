CREATE TABLE IF NOT EXISTS folders_by_owner (
  owner_id       uuid,
  folder_id      uuid,
  parent_id      uuid,
  name           text,
  path_cache     text,
  created_at     timestamp,
  updated_at     timestamp,
  is_deleted     boolean,
  deleted_at     timestamp,
  deleted_batch_id uuid,
  PRIMARY KEY ((owner_id), folder_id)
) WITH CLUSTERING ORDER BY (folder_id ASC)
  AND gc_grace_seconds = 86400;

CREATE TABLE IF NOT EXISTS folders_by_id (
  folder_id      uuid PRIMARY KEY,
  owner_id       uuid,
  parent_id      uuid,
  name           text,
  path_cache     text,
  created_at     timestamp,
  updated_at     timestamp,
  is_deleted     boolean,
  deleted_at     timestamp,
  deleted_batch_id uuid
) WITH gc_grace_seconds = 86400;

CREATE TABLE IF NOT EXISTS folders_by_parent (
  parent_id      uuid,
  folder_id      uuid,
  owner_id       uuid,
  name           text,
  path_cache     text,
  created_at     timestamp,
  updated_at     timestamp,
  is_deleted     boolean,
  deleted_at     timestamp,
  deleted_batch_id uuid,
  PRIMARY KEY ((parent_id), folder_id)
) WITH CLUSTERING ORDER BY (folder_id ASC)
  AND gc_grace_seconds = 86400;
