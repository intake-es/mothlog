-- +goose Up
-- Reglas de aviso por proyecto. Un proyecto sin ninguna avisa como antes: de
-- los errores nuevos y las regresiones, a Slack y Linear de las variables de entorno.
CREATE TABLE alert_rules (
    id               bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    project_id       bigint NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    name             text NOT NULL,
    enabled          boolean NOT NULL DEFAULT true,
    conditions       jsonb NOT NULL,
    actions          jsonb NOT NULL,
    -- La misma regla no avisa del mismo issue más de una vez en este tiempo.
    throttle_minutes int NOT NULL DEFAULT 0 CHECK (throttle_minutes >= 0),
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX alert_rules_project_idx ON alert_rules (project_id);

-- Lo que cada regla recuerda de cada issue: cuándo avisó por última vez y si
-- ya ha avisado de que pasa del umbral de eventos (se rearma al bajar) o de usuarios.
CREATE TABLE alert_rule_issues (
    rule_id           bigint NOT NULL REFERENCES alert_rules (id) ON DELETE CASCADE,
    issue_id          bigint NOT NULL REFERENCES issues (id) ON DELETE CASCADE,
    last_alert_at     timestamptz,
    frequency_alerted boolean NOT NULL DEFAULT false,
    users_alerted     boolean NOT NULL DEFAULT false,
    PRIMARY KEY (rule_id, issue_id)
);

CREATE INDEX alert_rule_issues_issue_idx ON alert_rule_issues (issue_id);

-- Eventos por minuto de cada issue que pasan los filtros de una regla con la
-- condición de más de N eventos en una ventana, también los no guardados.
CREATE TABLE alert_rule_minutes (
    rule_id  bigint NOT NULL REFERENCES alert_rules (id) ON DELETE CASCADE,
    issue_id bigint NOT NULL REFERENCES issues (id) ON DELETE CASCADE,
    minute   timestamptz NOT NULL,
    events   int NOT NULL,
    PRIMARY KEY (rule_id, issue_id, minute)
);

CREATE INDEX alert_rule_minutes_issue_idx ON alert_rule_minutes (issue_id);

-- Usuarios distintos de cada issue que pasan los filtros de una regla con la
-- condición de más de N usuarios, hasta N+1.
CREATE TABLE alert_rule_users (
    rule_id  bigint NOT NULL REFERENCES alert_rules (id) ON DELETE CASCADE,
    issue_id bigint NOT NULL REFERENCES issues (id) ON DELETE CASCADE,
    user_key text NOT NULL,
    PRIMARY KEY (rule_id, issue_id, user_key)
);

CREATE INDEX alert_rule_users_issue_idx ON alert_rule_users (issue_id);

ALTER TABLE alert_outbox
    DROP CONSTRAINT alert_outbox_channel_check,
    DROP CONSTRAINT alert_outbox_kind_check,
    ADD CONSTRAINT alert_outbox_channel_check
        CHECK (channel IN ('slack', 'linear', 'email', 'discord', 'telegram', 'ntfy', 'webhook')),
    ADD CONSTRAINT alert_outbox_kind_check CHECK (kind IN ('new', 'regressed', 'frequency', 'users')),
    -- Sin regla, el aviso es de los de siempre y su canal sale de las variables de entorno.
    ADD COLUMN rule_id bigint REFERENCES alert_rules (id) ON DELETE CASCADE,
    ADD COLUMN action_id text,
    -- Lo que hizo saltar la regla: el umbral y la ventana.
    ADD COLUMN detail jsonb;

-- +goose Down
DELETE FROM alert_outbox WHERE rule_id IS NOT NULL OR kind NOT IN ('new', 'regressed');
ALTER TABLE alert_outbox
    DROP COLUMN detail,
    DROP COLUMN action_id,
    DROP COLUMN rule_id,
    DROP CONSTRAINT alert_outbox_kind_check,
    DROP CONSTRAINT alert_outbox_channel_check,
    ADD CONSTRAINT alert_outbox_channel_check CHECK (channel IN ('slack', 'linear')),
    ADD CONSTRAINT alert_outbox_kind_check CHECK (kind IN ('new', 'regressed'));
DROP TABLE alert_rule_users;
DROP TABLE alert_rule_minutes;
DROP TABLE alert_rule_issues;
DROP TABLE alert_rules;
