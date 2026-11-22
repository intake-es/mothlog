-- +goose Up
-- El resumen periódico de cada proyecto. Sin fila, el proyecto no manda ninguno.
CREATE TABLE project_digests (
    project_id      bigint PRIMARY KEY REFERENCES projects (id) ON DELETE CASCADE,
    enabled         boolean NOT NULL DEFAULT false,
    frequency       text NOT NULL DEFAULT 'daily' CHECK (frequency IN ('daily', 'weekly')),
    -- El día del semanal: 0 es domingo y 6 sábado, como en cron.
    weekday         int NOT NULL DEFAULT 1 CHECK (weekday BETWEEN 0 AND 6),
    hour            int NOT NULL DEFAULT 9 CHECK (hour BETWEEN 0 AND 23),
    timezone        text NOT NULL DEFAULT 'UTC',
    language        text NOT NULL DEFAULT 'es' CHECK (language IN ('es', 'en')),
    -- Un periodo sin nada manda un resumen corto que lo dice o, con false, ninguno.
    send_empty      boolean NOT NULL DEFAULT true,
    -- Como las acciones de las reglas, solo de correo, Slack y webhook.
    destinations    jsonb NOT NULL DEFAULT '[]',
    -- El final del último periodo encolado: ese y los anteriores ya no se mandan.
    last_period_end timestamptz,
    last_sent_at    timestamptz,
    updated_at      timestamptz NOT NULL DEFAULT now()
);

-- Los eventos de un proyecto en un periodo, para los totales del resumen.
CREATE INDEX events_project_occurred_at_idx ON events (project_id, occurred_at);

-- Los resúmenes van por la misma tabla de salida, sin issue ni monitor;
-- action_id es el destino y event_at, el final del periodo.
ALTER TABLE alert_outbox ADD COLUMN digest_project_id bigint REFERENCES projects (id) ON DELETE CASCADE;

CREATE UNIQUE INDEX alert_outbox_digest_idx ON alert_outbox (digest_project_id, action_id, event_at)
    WHERE digest_project_id IS NOT NULL;

-- Las condiciones de tipo y destino se amplían sobre las que haya, sin
-- reescribirlas, y la de antes queda en el comentario para el Down.
-- +goose StatementBegin
DO $$
DECLARE
    c record;
BEGIN
    FOR c IN
        SELECT conname, pg_get_constraintdef(oid) AS def,
            CASE conname WHEN 'alert_outbox_kind_check' THEN 'kind = ''digest'''
                ELSE 'digest_project_id IS NOT NULL' END AS extra
        FROM pg_constraint
        WHERE conrelid = 'alert_outbox'::regclass AND conname IN ('alert_outbox_kind_check', 'alert_outbox_target_check')
    LOOP
        EXECUTE format('ALTER TABLE alert_outbox DROP CONSTRAINT %I, ADD CONSTRAINT %I CHECK (%s OR %s)',
            c.conname, c.conname, c.extra, substring(c.def FROM 7));
        EXECUTE format('COMMENT ON CONSTRAINT %I ON alert_outbox IS %L', c.conname, c.def);
    END LOOP;
END $$;
-- +goose StatementEnd

ALTER TABLE alert_outbox ADD CONSTRAINT alert_outbox_digest_check
    CHECK (digest_project_id IS NULL OR (kind = 'digest' AND issue_id IS NULL AND monitor_id IS NULL));

-- +goose Down
DELETE FROM alert_outbox WHERE digest_project_id IS NOT NULL;
ALTER TABLE alert_outbox DROP CONSTRAINT alert_outbox_digest_check;
-- +goose StatementBegin
DO $$
DECLARE
    c record;
BEGIN
    FOR c IN
        SELECT conname, obj_description(oid, 'pg_constraint') AS def
        FROM pg_constraint
        WHERE conrelid = 'alert_outbox'::regclass AND conname IN ('alert_outbox_kind_check', 'alert_outbox_target_check')
            AND obj_description(oid, 'pg_constraint') LIKE 'CHECK %'
    LOOP
        EXECUTE format('ALTER TABLE alert_outbox DROP CONSTRAINT %I, ADD CONSTRAINT %I %s', c.conname, c.conname, c.def);
    END LOOP;
END $$;
-- +goose StatementEnd
DROP INDEX alert_outbox_digest_idx;
ALTER TABLE alert_outbox DROP COLUMN digest_project_id;
DROP INDEX events_project_occurred_at_idx;
DROP TABLE project_digests;
