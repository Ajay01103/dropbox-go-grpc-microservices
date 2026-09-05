CREATE TABLE IF NOT EXISTS upload_sessions (
	upload_id         uuid PRIMARY KEY,
	user_id           uuid,
	filename          text,
	total_size        bigint,
	block_size_bytes  bigint,
	chunk_block_map   map<int, text>,
	uploaded_bitmap   blob,
	content_type      text,
	content_hash      text,
	status            text,
	created_at        timestamp,
	expires_at        timestamp
) WITH gc_grace_seconds = 86400
	AND default_time_to_live = 2592000;