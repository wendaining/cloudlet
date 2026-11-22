-- +goose Up
CREATE TABLE operations (
    id UUID PRIMARY KEY,
    project_id UUID NOT NULL REFERENCES projects (id),
    action TEXT NOT NULL CHECK (octet_length(action) BETWEEN 1 AND 128),
    idempotency_key TEXT NOT NULL CHECK (octet_length(idempotency_key) BETWEEN 1 AND 256),
    request_fingerprint TEXT NOT NULL CHECK (octet_length(request_fingerprint) > 0),
    phase TEXT NOT NULL CHECK (phase IN ('received', 'saving', 'preflight', 'preparing', 'activating', 'finalizing')),
    outcome TEXT NOT NULL CHECK (outcome IN ('in_progress', 'succeeded', 'failed')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (project_id, action, idempotency_key)
);

-- +goose Down
DROP TABLE operations;
