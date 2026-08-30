-- Migration 1: Initial schema with all core tables and views.

CREATE TABLE IF NOT EXISTS users (
    id TEXT PRIMARY KEY,
    email TEXT,
    name TEXT,
    password TEXT,
    created_at TIMESTAMP,
    updated_at TIMESTAMP
) WITH compaction = {'class': 'LeveledCompactionStrategy'}
  AND caching = {'keys': 'ALL', 'rows_per_partition': '1'};

-- Materialized view for email lookups.

CREATE MATERIALIZED VIEW IF NOT EXISTS users_by_email_mv AS
    SELECT id, email, name, password, created_at, updated_at
    FROM users
    WHERE email IS NOT NULL
    PRIMARY KEY (email, id);

-- Session generation state.

CREATE TABLE IF NOT EXISTS user_sessions (
    user_id TEXT,
    session_id TEXT,
    gen BIGINT,
    device_fp TEXT,
    expires_at TIMESTAMP,
    created_at TIMESTAMP,
    updated_at TIMESTAMP,
    PRIMARY KEY (user_id, session_id)
) WITH CLUSTERING ORDER BY (session_id ASC)
  AND default_time_to_live = 604800
  AND compaction = {'class': 'LeveledCompactionStrategy'};

-- User revocation state.

CREATE TABLE IF NOT EXISTS user_revocations (
    user_id TEXT PRIMARY KEY,
    global_ver INT,
    created_at TIMESTAMP,
    updated_at TIMESTAMP
) WITH default_time_to_live = 604800;

-- EdDSA signing keys.

CREATE TABLE IF NOT EXISTS signing_keys (
    kid TEXT PRIMARY KEY,
    private_key BLOB,
    public_key BLOB,
    algorithm TEXT,
    status TEXT,
    created_at TIMESTAMP,
    expires_at TIMESTAMP
) WITH compaction = {'class': 'LeveledCompactionStrategy'}
  AND default_time_to_live = 2592000;

-- JWKS public keys cache.

CREATE TABLE IF NOT EXISTS jwks_public_keys (
    id TEXT PRIMARY KEY,
  jwks_json TEXT,
  version BIGINT,
    created_at TIMESTAMP,
    updated_at TIMESTAMP
) WITH default_time_to_live = 1209600;
