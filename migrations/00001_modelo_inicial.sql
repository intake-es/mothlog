-- +goose Up
CREATE TABLE projects (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name       text NOT NULL,
    public_key text NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE issues (
    id                  bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    project_id          bigint NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    fingerprint         text NOT NULL,
    title               text NOT NULL,
    culprit             text NOT NULL DEFAULT '',
    type                text NOT NULL DEFAULT '',
    level               text NOT NULL DEFAULT 'error',
    status              text NOT NULL DEFAULT 'open'
        CHECK (status IN ('open', 'resolved', 'resolved_next', 'silenced')),
    resolved_in_release text,
    first_seen          timestamptz NOT NULL,
    last_seen           timestamptz NOT NULL,
    event_count         bigint NOT NULL DEFAULT 0 CHECK (event_count >= 0),
    UNIQUE (project_id, fingerprint)
);

CREATE INDEX issues_project_status_last_seen_idx ON issues (project_id, status, last_seen DESC);
CREATE INDEX issues_project_last_seen_idx ON issues (project_id, last_seen DESC);

CREATE TABLE events (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    issue_id    bigint NOT NULL REFERENCES issues (id) ON DELETE CASCADE,
    project_id  bigint NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    event_id    uuid NOT NULL,
    occurred_at timestamptz NOT NULL,
    received_at timestamptz NOT NULL DEFAULT now(),
    release     text,
    environment text,
    data        jsonb NOT NULL,
    UNIQUE (project_id, event_id)
);

CREATE INDEX events_issue_occurred_at_idx ON events (issue_id, occurred_at DESC);

-- +goose Down
DROP TABLE events;
DROP TABLE issues;
DROP TABLE projects;
