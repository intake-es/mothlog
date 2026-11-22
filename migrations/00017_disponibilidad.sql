-- +goose Up
-- Monitores de disponibilidad: el servicio pide la URL cada interval_seconds y
-- la da por caída tras failure_threshold fallos seguidos.
CREATE TABLE uptime_monitors (
    id                  bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    project_id          bigint NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    slug                text NOT NULL,
    name                text NOT NULL,
    url                 text NOT NULL,
    method              text NOT NULL DEFAULT 'GET' CHECK (method IN ('GET', 'HEAD')),
    interval_seconds    int NOT NULL DEFAULT 60 CHECK (interval_seconds BETWEEN 30 AND 3600),
    timeout_seconds     int NOT NULL DEFAULT 10 CHECK (timeout_seconds BETWEEN 1 AND 60),
    -- Los códigos que cuentan como bien: «200-299», «200-299,301» o «2xx,404».
    expected_status     text NOT NULL DEFAULT '200-299',
    failure_threshold   int NOT NULL DEFAULT 2 CHECK (failure_threshold BETWEEN 1 AND 10),
    paused              boolean NOT NULL DEFAULT false,
    status              text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'up', 'down')),
    consecutive_failures int NOT NULL DEFAULT 0,
    -- El primer fallo de la racha: será el principio del incidente.
    first_failure_at    timestamptz,
    first_failure_error text,
    last_checked_at     timestamptz,
    last_status_code    int,
    last_duration_ms    int,
    last_error          text NOT NULL DEFAULT '',
    -- Una réplica lo reserva moviendo next_check_at antes de comprobarlo.
    next_check_at       timestamptz NOT NULL DEFAULT now(),
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    UNIQUE (project_id, slug)
);

CREATE INDEX uptime_monitors_due_idx ON uptime_monitors (next_check_at) WHERE NOT paused;

-- Cada comprobación, sin el cuerpo; se borran a los pocos días.
CREATE TABLE uptime_checks (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    monitor_id  bigint NOT NULL REFERENCES uptime_monitors (id) ON DELETE CASCADE,
    checked_at  timestamptz NOT NULL,
    ok          boolean NOT NULL,
    status_code int,
    duration_ms int NOT NULL,
    error       text NOT NULL DEFAULT ''
);

CREATE INDEX uptime_checks_monitor_idx ON uptime_checks (monitor_id, checked_at DESC, id DESC);
CREATE INDEX uptime_checks_checked_idx ON uptime_checks (checked_at);

CREATE TABLE uptime_incidents (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    monitor_id  bigint NOT NULL REFERENCES uptime_monitors (id) ON DELETE CASCADE,
    started_at  timestamptz NOT NULL,
    ended_at    timestamptz,
    duration_ms bigint,
    first_error text NOT NULL DEFAULT '',
    CHECK (ended_at IS NULL OR ended_at >= started_at)
);

-- Como mucho un incidente abierto por monitor.
CREATE UNIQUE INDEX uptime_incidents_open_idx ON uptime_incidents (monitor_id) WHERE ended_at IS NULL;
CREATE INDEX uptime_incidents_monitor_idx ON uptime_incidents (monitor_id, started_at DESC);

ALTER TABLE alert_outbox
    ADD COLUMN uptime_monitor_id bigint REFERENCES uptime_monitors (id) ON DELETE CASCADE,
    ADD COLUMN incident_id bigint REFERENCES uptime_incidents (id) ON DELETE CASCADE,
    DROP CONSTRAINT alert_outbox_target_check,
    ADD CONSTRAINT alert_outbox_target_check CHECK (num_nonnulls(issue_id, monitor_id, uptime_monitor_id) = 1),
    DROP CONSTRAINT alert_outbox_kind_check,
    ADD CONSTRAINT alert_outbox_kind_check CHECK (kind IN ('new', 'regressed', 'frequency', 'users',
        'cron_failed', 'cron_missed', 'cron_timeout', 'cron_recovered', 'uptime_down', 'uptime_up'));

CREATE INDEX alert_outbox_uptime_idx ON alert_outbox (uptime_monitor_id) WHERE uptime_monitor_id IS NOT NULL;
CREATE INDEX alert_outbox_incident_idx ON alert_outbox (incident_id) WHERE incident_id IS NOT NULL;

-- +goose Down
DELETE FROM alert_outbox WHERE uptime_monitor_id IS NOT NULL;
ALTER TABLE alert_outbox
    DROP CONSTRAINT alert_outbox_kind_check,
    ADD CONSTRAINT alert_outbox_kind_check CHECK (kind IN ('new', 'regressed', 'frequency', 'users',
        'cron_failed', 'cron_missed', 'cron_timeout', 'cron_recovered')),
    DROP CONSTRAINT alert_outbox_target_check,
    ADD CONSTRAINT alert_outbox_target_check CHECK ((issue_id IS NULL) <> (monitor_id IS NULL)),
    DROP COLUMN incident_id,
    DROP COLUMN uptime_monitor_id;
DROP TABLE uptime_incidents;
DROP TABLE uptime_checks;
DROP TABLE uptime_monitors;
