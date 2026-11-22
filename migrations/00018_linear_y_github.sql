-- +goose Up
-- Las issues de Linear y las issues y PRs de GitHub enlazadas a cada issue.
-- external_id es el id con el que se habla con su API: el de Linear, o
-- dueño/repo#número en GitHub. state es su estado normalizado (open, closed,
-- canceled o merged) y state_name, el que se ve allí ("Done", "In Progress").
-- rule_id y action_id dicen qué acción de regla la creó, para usar su token.
CREATE TABLE issue_links (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    issue_id    bigint NOT NULL REFERENCES issues (id) ON DELETE CASCADE,
    provider    text NOT NULL CHECK (provider IN ('linear', 'github')),
    kind        text NOT NULL CHECK (kind IN ('issue', 'pull_request')),
    external_id text NOT NULL CHECK (external_id <> ''),
    identifier  text,
    url         text,
    state       text CHECK (state IN ('open', 'closed', 'canceled', 'merged')),
    state_name  text,
    rule_id     bigint REFERENCES alert_rules (id) ON DELETE SET NULL,
    action_id   text,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CHECK (provider = 'github' OR kind = 'issue'),
    UNIQUE (issue_id, provider, kind, external_id)
);

CREATE INDEX issue_links_external_idx ON issue_links (provider, external_id);

INSERT INTO issue_links (issue_id, provider, kind, external_id, identifier, url)
SELECT id, 'linear', 'issue', linear_issue_id, linear_issue_identifier, linear_issue_url
FROM issues WHERE linear_issue_id IS NOT NULL AND linear_issue_id <> '';

ALTER TABLE issues
    DROP COLUMN linear_issue_id,
    DROP COLUMN linear_issue_identifier,
    DROP COLUMN linear_issue_url;

-- Los cambios de estado de aquí que faltan por llevar a cada enlace: status es
-- el estado del issue al encolarlo. echoed_at es cuándo llegó el webhook que
-- provocó el envío (o, si no cambió nada allí, el propio envío): mientras
-- falta, el webhook con ese estado es el eco de lo nuestro y no se aplica.
CREATE TABLE issue_link_sync (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    link_id         bigint NOT NULL REFERENCES issue_links (id) ON DELETE CASCADE,
    status          text NOT NULL CHECK (status IN ('open', 'resolved', 'resolved_next', 'silenced')),
    created_at      timestamptz NOT NULL DEFAULT now(),
    attempts        int NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    last_error      text,
    sent_at         timestamptz,
    failed_at       timestamptz,
    echoed_at       timestamptz
);

CREATE INDEX issue_link_sync_echo_idx ON issue_link_sync (link_id, id)
    WHERE sent_at IS NOT NULL AND echoed_at IS NULL;

CREATE INDEX issue_link_sync_pending_idx ON issue_link_sync (link_id, id)
    WHERE sent_at IS NULL AND failed_at IS NULL;

-- Las entregas de webhooks ya recibidas, para no aplicar dos veces la misma.
CREATE TABLE webhook_deliveries (
    provider    text NOT NULL CHECK (provider IN ('linear', 'github')),
    delivery_id text NOT NULL,
    event       text NOT NULL DEFAULT '',
    received_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (provider, delivery_id)
);

CREATE INDEX webhook_deliveries_received_idx ON webhook_deliveries (provider, received_at);

ALTER TABLE issue_status_changes
    DROP CONSTRAINT issue_status_changes_source_check,
    ADD CONSTRAINT issue_status_changes_source_check
        CHECK (source IN ('ui', 'api', 'mcp', 'ingest', 'deploy', 'linear', 'github'));

ALTER TABLE alert_outbox
    DROP CONSTRAINT alert_outbox_channel_check,
    ADD CONSTRAINT alert_outbox_channel_check
        CHECK (channel IN ('slack', 'linear', 'email', 'discord', 'telegram', 'ntfy', 'webhook', 'github'));

-- +goose Down
DELETE FROM alert_outbox WHERE channel = 'github';
ALTER TABLE alert_outbox
    DROP CONSTRAINT alert_outbox_channel_check,
    ADD CONSTRAINT alert_outbox_channel_check
        CHECK (channel IN ('slack', 'linear', 'email', 'discord', 'telegram', 'ntfy', 'webhook'));

DELETE FROM issue_status_changes WHERE source IN ('linear', 'github');
ALTER TABLE issue_status_changes
    DROP CONSTRAINT issue_status_changes_source_check,
    ADD CONSTRAINT issue_status_changes_source_check
        CHECK (source IN ('ui', 'api', 'mcp', 'ingest', 'deploy'));

DROP TABLE webhook_deliveries;
DROP TABLE issue_link_sync;

ALTER TABLE issues
    ADD COLUMN linear_issue_id text,
    ADD COLUMN linear_issue_identifier text,
    ADD COLUMN linear_issue_url text;

UPDATE issues i SET linear_issue_id = l.external_id, linear_issue_identifier = l.identifier, linear_issue_url = l.url
FROM (SELECT DISTINCT ON (issue_id) issue_id, external_id, identifier, url
      FROM issue_links WHERE provider = 'linear' AND kind = 'issue'
      ORDER BY issue_id, id DESC) l
WHERE l.issue_id = i.id;

DROP TABLE issue_links;
