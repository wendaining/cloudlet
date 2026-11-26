-- +goose Up
ALTER TABLE containers DROP CONSTRAINT IF EXISTS containers_state_check;
ALTER TABLE containers ADD CONSTRAINT containers_state_check
    CHECK (state IN ('pending', 'active', 'stopped', 'failed'));

-- +goose Down
ALTER TABLE containers DROP CONSTRAINT IF EXISTS containers_state_check;
ALTER TABLE containers ADD CONSTRAINT containers_state_check
    CHECK (state IN ('pending', 'active', 'failed'));
