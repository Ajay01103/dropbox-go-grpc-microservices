-- Folders by user table
-- Partition by user_id to find all user's root folders
-- For "My Files" and other user-owned folders

CREATE TABLE IF NOT EXISTS folders_by_user (
  user_id         uuid,
  folder_id       uuid,
  folder_name     text,
  created_at      timestamp,
  PRIMARY KEY (user_id, folder_id)
) WITH CLUSTERING ORDER BY (folder_id ASC)
  AND gc_grace_seconds = 86400;
