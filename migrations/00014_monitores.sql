-- +goose Up
-- Monitores de cron: se crean con el primer check-in que trae monitor_config o
-- por la API. status es el del último check-in cerrado o pending si aún no hay.
CREATE TABLE monitors (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    project_id      bigint NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    slug            text NOT NULL,
    name            text NOT NULL,
    schedule_type   text NOT NULL CHECK (schedule_type IN ('crontab', 'interval')),
    schedule        text NOT NULL DEFAULT '',
    interval_value  int,
    interval_unit   text CHECK (interval_unit IN ('minute', 'hour', 'day', 'week', 'month', 'year')),
    timezone        text NOT NULL DEFAULT 'UTC',
    -- Minutos que puede llegar tarde un check-in antes de darlo por missed.
    checkin_margin  int NOT NULL DEFAULT 1 CHECK (checkin_margin >= 1),
    -- Minutos que puede durar una ejecución antes de darla por timeout.
    max_runtime     int NOT NULL DEFAULT 30 CHECK (max_runtime >= 1),
    -- Vacío vale cualquier entorno; si no, los check-ins de otro se ignoran.
    environment     text NOT NULL DEFAULT '',
    muted           boolean NOT NULL DEFAULT false,
    status          text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'ok', 'error', 'missed', 'timeout')),
    last_checkin_at timestamptz,
    -- Cuándo toca el siguiente; NULL hasta el primer check-in.
    next_checkin_at timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (project_id, slug)
);

CREATE INDEX monitors_next_checkin_idx ON monitors (next_checkin_at) WHERE next_checkin_at IS NOT NULL;

CREATE TABLE monitor_checkins (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    monitor_id  bigint NOT NULL REFERENCES monitors (id) ON DELETE CASCADE,
    -- El check_in_id del SDK; los missed llevan uno propio.
    check_in_id uuid NOT NULL,
    status      text NOT NULL CHECK (status IN ('in_progress', 'ok', 'error', 'missed', 'timeout')),
    duration_ms bigint,
    environment text NOT NULL DEFAULT '',
    release     text NOT NULL DEFAULT '',
    -- La ejecución que tocaba cuando llegó, o la que no llegó.
    expected_at timestamptz,
    started_at  timestamptz NOT NULL,
    finished_at timestamptz,
    -- Hasta cuándo puede seguir in_progress.
    timeout_at  timestamptz,
    received_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (monitor_id, check_in_id)
);

CREATE INDEX monitor_checkins_monitor_idx ON monitor_checkins (monitor_id, started_at DESC, id DESC);
CREATE INDEX monitor_checkins_timeout_idx ON monitor_checkins (timeout_at) WHERE status = 'in_progress';
CREATE INDEX monitor_checkins_received_idx ON monitor_checkins (received_at);

-- Los avisos de los crons van por la misma tabla de salida, sin issue.
ALTER TABLE alert_outbox
    ALTER COLUMN issue_id DROP NOT NULL,
    ADD COLUMN monitor_id bigint REFERENCES monitors (id) ON DELETE CASCADE,
    ADD COLUMN checkin_id bigint REFERENCES monitor_checkins (id) ON DELETE CASCADE,
    ADD CONSTRAINT alert_outbox_target_check CHECK ((issue_id IS NULL) <> (monitor_id IS NULL)),
    DROP CONSTRAINT alert_outbox_kind_check,
    ADD CONSTRAINT alert_outbox_kind_check CHECK (kind IN ('new', 'regressed', 'frequency', 'users',
        'cron_failed', 'cron_missed', 'cron_timeout', 'cron_recovered'));

CREATE INDEX alert_outbox_monitor_idx ON alert_outbox (monitor_id) WHERE monitor_id IS NOT NULL;
CREATE INDEX alert_outbox_checkin_idx ON alert_outbox (checkin_id) WHERE checkin_id IS NOT NULL;

-- +goose Down
DELETE FROM alert_outbox WHERE monitor_id IS NOT NULL;
ALTER TABLE alert_outbox
    DROP CONSTRAINT alert_outbox_kind_check,
    ADD CONSTRAINT alert_outbox_kind_check CHECK (kind IN ('new', 'regressed', 'frequency', 'users')),
    DROP CONSTRAINT alert_outbox_target_check,
    DROP COLUMN checkin_id,
    DROP COLUMN monitor_id,
    ALTER COLUMN issue_id SET NOT NULL;
DROP TABLE monitor_checkins;
DROP TABLE monitors;
