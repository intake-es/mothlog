-- +goose Up
ALTER TABLE issues
    ADD COLUMN linear_issue_identifier text,
    ADD COLUMN linear_issue_url text,
    ADD COLUMN resolved_at timestamptz;

-- En una regresión, cuándo se había resuelto el issue.
ALTER TABLE alert_outbox ADD COLUMN resolved_at timestamptz;

-- El aviso de Linear del mismo error que el de Slack espera.
ALTER TABLE alert_outbox ADD COLUMN waits_for bigint REFERENCES alert_outbox (id) ON DELETE SET NULL;

-- +goose Down
ALTER TABLE alert_outbox DROP COLUMN waits_for;
ALTER TABLE alert_outbox DROP COLUMN resolved_at;
ALTER TABLE issues
    DROP COLUMN resolved_at,
    DROP COLUMN linear_issue_url,
    DROP COLUMN linear_issue_identifier;
