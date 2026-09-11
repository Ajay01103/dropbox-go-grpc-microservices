CREATE TABLE IF NOT EXISTS purge_jobs (
    job_id          uuid PRIMARY KEY,
    file_id         uuid,
    folder_id       uuid,
    file_version    int,
    owner_id        uuid,
    block_hash_list list<text>,
    reason          text,
    state           text,
    has_reconciliation_errors boolean,
    attempts        int,
    next_attempt_at timestamp,
    last_error      text,
    created_at      timestamp,
    updated_at      timestamp,
    completed_at    timestamp
) WITH gc_grace_seconds = 86400;


CREATE TABLE IF NOT EXISTS purge_jobs_by_file_version (
    file_id      uuid,
    file_version int,
    job_id       uuid,
    PRIMARY KEY ((file_id, file_version))
) WITH gc_grace_seconds = 86400;


CREATE TABLE IF NOT EXISTS purge_jobs_by_state (
    state       text,
    updated_at  timestamp,
    job_id      uuid,
    PRIMARY KEY ((state), updated_at, job_id)
) WITH CLUSTERING ORDER BY (updated_at ASC, job_id ASC)
  AND gc_grace_seconds = 86400;
