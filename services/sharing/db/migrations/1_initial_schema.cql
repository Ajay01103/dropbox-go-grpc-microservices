CREATE TABLE IF NOT EXISTS shares_by_file (
  file_id      uuid,
  share_id     uuid,
  shared_with  uuid,
  permission   text,
  granted_by   uuid,
  expires_at   timestamp,
  PRIMARY KEY ((file_id), share_id)
) WITH CLUSTERING ORDER BY (share_id ASC);

CREATE TABLE IF NOT EXISTS shares_by_user (
  user_id      uuid,
  file_id      uuid,
  permission   text,
  expires_at   timestamp,
  PRIMARY KEY ((user_id), file_id)
);

CREATE TABLE IF NOT EXISTS public_links (
  link_token   text PRIMARY KEY,
  file_id      uuid,
  permission   text,
  expires_at   timestamp,
  created_by   uuid
);

CREATE TABLE IF NOT EXISTS owner_cache (
  file_id      uuid PRIMARY KEY,
  owner_id     uuid
);
