-- Migration 2: Create users_by_email lookup table for unique constraint.

CREATE TABLE IF NOT EXISTS users_by_email (
    email TEXT PRIMARY KEY,
    user_id TEXT
);
