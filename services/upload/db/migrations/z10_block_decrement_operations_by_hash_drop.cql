-- B4 (Part C phase 5): drop the v1 per-hash decrement ledger.
--
-- The v2 batched worker addresses operations solely by (job_id, occurrence)
-- in block_decrement_operations. The denormalized by-hash copy has no
-- readers and its writer was removed in the same PR.
--
-- Safe to re-run after a partial failure: DROP TABLE IF EXISTS is a no-op
-- when the table is already gone, so gocqlx re-running this file before the
-- migration is recorded cannot wedge boot.
DROP TABLE IF EXISTS block_decrement_operations_by_hash;
