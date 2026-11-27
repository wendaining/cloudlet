-- +goose Up
ALTER TABLE worker_deployments ADD COLUMN compatibility_flags JSONB NOT NULL DEFAULT '[]'
    CHECK (jsonb_typeof(compatibility_flags) = 'array');

-- +goose Down
ALTER TABLE worker_deployments DROP COLUMN compatibility_flags;
