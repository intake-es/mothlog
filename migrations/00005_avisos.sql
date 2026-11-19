-- +goose Up
CREATE TABLE alert_outbox (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    issue_id        bigint NOT NULL REFERENCES issues (id) ON DELETE CASCADE,
    channel         text NOT NULL CHECK (channel IN ('slack', 'linear')),
    kind            text NOT NULL CHECK (kind IN ('new', 'regressed')),
    release         text,
    environment     text,
    event_at        timestamptz NOT NULL,
    -- id que se pide a Linear al crear la issue, para que un reintento no la duplique.
    linear_create_id uuid NOT NULL DEFAULT gen_random_uuid(),
    created_at      timestamptz NOT NULL DEFAULT now(),
    attempts        int NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    last_error      text,
    sent_at         timestamptz,
    failed_at       timestamptz
);

CREATE INDEX alert_outbox_pending_idx ON alert_outbox (issue_id, channel, id)
    WHERE sent_at IS NULL AND failed_at IS NULL;

ALTER TABLE issues ADD COLUMN linear_issue_id text;

-- Si la issue de Linear lleva también la etiqueta extra.
ALTER TABLE projects ADD COLUMN linear_extra_label boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE projects DROP COLUMN linear_extra_label;
ALTER TABLE issues DROP COLUMN linear_issue_id;
DROP TABLE alert_outbox;
