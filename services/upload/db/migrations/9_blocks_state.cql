-- CALL ensure_blocks_state;
-- B2a: GC fence column (added idempotently by the callback - CQL has no ADD
-- COLUMN IF NOT EXISTS and a plain ALTER wedges boot when it already exists)
-- plus the ledger TTL (30 days exceeds the 7-day BLOCK_REFS MaxAge so a
-- redelivered decrement can never double-decrement after ledger expiry).
ALTER TABLE block_decrement_operations WITH default_time_to_live = 2592000;
