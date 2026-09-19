CREATE TABLE IF NOT EXISTS recent_items_by_owner (
    owner_id     uuid,
    event_at     timestamp,
    item_id      uuid,
    item_type    text,
    folder_id    uuid,
    name         text,
    content_type text,
    size_bytes   bigint,
    is_deleted   boolean,
    PRIMARY KEY ((owner_id), event_at, item_id)
) WITH CLUSTERING ORDER BY (event_at DESC, item_id ASC)
  AND default_time_to_live = 5184000
  AND compaction = {
      'class': 'TimeWindowCompactionStrategy',
      'compaction_window_unit': 'DAYS',
      'compaction_window_size': 2
  }
  AND gc_grace_seconds = 86400;
