CREATE TABLE IF NOT EXISTS folder_items_by_name (
    folder_id     uuid,
    name          text,
    item_type     text,
    item_id       uuid,
    owner_id      uuid,
    size_bytes    bigint,
    content_type  text,
    thumbnail_key text,
    created_at    timestamp,
    updated_at    timestamp,
    is_deleted    boolean,
    current       boolean,
    PRIMARY KEY ((folder_id), name, item_type, item_id)
) WITH CLUSTERING ORDER BY (name ASC, item_type ASC, item_id ASC)
  AND gc_grace_seconds = 86400;

CREATE TABLE IF NOT EXISTS folder_items_by_updated (
    folder_id     uuid,
    updated_at    timestamp,
    item_type     text,
    item_id       uuid,
    owner_id      uuid,
    name          text,
    size_bytes    bigint,
    content_type  text,
    thumbnail_key text,
    created_at    timestamp,
    is_deleted    boolean,
    current       boolean,
    PRIMARY KEY ((folder_id), updated_at, item_type, item_id)
) WITH CLUSTERING ORDER BY (updated_at DESC, item_type ASC, item_id ASC)
  AND gc_grace_seconds = 86400;

CREATE TABLE IF NOT EXISTS folder_items_by_size (
    folder_id     uuid,
    size_bytes    bigint,
    item_type     text,
    item_id       uuid,
    owner_id      uuid,
    name          text,
    content_type  text,
    thumbnail_key text,
    created_at    timestamp,
    updated_at    timestamp,
    is_deleted    boolean,
    current       boolean,
    PRIMARY KEY ((folder_id), size_bytes, item_type, item_id)
) WITH CLUSTERING ORDER BY (size_bytes DESC, item_type ASC, item_id ASC)
  AND gc_grace_seconds = 86400;
