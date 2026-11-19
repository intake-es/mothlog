-- +goose Up
ALTER TABLE issues ADD COLUMN last_regressed_at timestamptz;

CREATE INDEX events_received_at_idx ON events (received_at);

-- +goose Down
DROP INDEX events_received_at_idx;
ALTER TABLE issues DROP COLUMN last_regressed_at;
