CREATE TABLE IF NOT EXISTS files_by_folder (
	folder_id        uuid,
	file_id          uuid,
	filename         text,
	size_bytes       bigint,
	content_type     text,
	content_hash     text,
	block_hash_list  list<text>,
	version          int,
	created_at       timestamp,
	owner_id         uuid,
	parent_folder_id uuid,
	thumbnail_key    text,
	thumbnail_status text,
	PRIMARY KEY (folder_id, file_id)
) WITH CLUSTERING ORDER BY (file_id ASC)
	AND gc_grace_seconds = 86400;
