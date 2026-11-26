-- +goose Up
ALTER TABLE worker_executions
    ADD COLUMN observation_version BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN last_exit_reason TEXT NOT NULL DEFAULT '',
    ADD COLUMN last_exit_code INTEGER,
    ADD COLUMN last_exit_finished_at TIMESTAMPTZ,
    ADD COLUMN last_exit_observed_at TIMESTAMPTZ,
    ADD COLUMN last_exit_detail TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE worker_executions
    DROP COLUMN last_exit_detail,
    DROP COLUMN last_exit_observed_at,
    DROP COLUMN last_exit_finished_at,
    DROP COLUMN last_exit_code,
    DROP COLUMN last_exit_reason,
    DROP COLUMN observation_version;
