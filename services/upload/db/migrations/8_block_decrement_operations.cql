CREATE TABLE IF NOT EXISTS block_decrement_operations (
    job_id             uuid,
    occurrence         int,
    block_hash         text,
    status             text,
    new_ref_count      bigint,
    became_gc_candidate boolean,
    error              text,
    claimed_at         timestamp,
    updated_at         timestamp,
    PRIMARY KEY ((job_id), occurrence)
) WITH gc_grace_seconds = 86400;


CREATE TABLE IF NOT EXISTS block_decrement_operations_by_hash (
    block_hash  text,
    job_id      uuid,
    occurrence  int,
    status      text,
    updated_at  timestamp,
    PRIMARY KEY ((block_hash), job_id, occurrence)
) WITH CLUSTERING ORDER BY (job_id ASC, occurrence ASC)
  AND gc_grace_seconds = 86400;
