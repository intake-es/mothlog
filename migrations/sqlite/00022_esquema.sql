-- +goose Up
-- El esquema de SQLite, el mismo que dejan las migraciones de Postgres hasta
-- la 00022, sin repetir su historia: una base de SQLite empieza vacía. Lleva
-- el número de la última de Postgres para que una migración nueva tenga el
-- mismo número en los dos motores.
--
-- Cómo se guarda lo que en Postgres tiene tipo propio:
-- - fechas (timestamptz): enteros de microsegundos desde 1970 en UTC;
-- - dropped_events.day (date): el entero de las 00:00 UTC de ese día;
-- - uuid: texto, como lo pasa Go (event_id y check_in_id en 32 hexadecimales
--   en minúscula, debug_id y linear_create_id con guiones);
-- - text[] y jsonb: texto JSON; bytea: BLOB; boolean: 0 o 1.
-- Las tablas son STRICT: SQLite no guarda un texto en una columna de fechas.
-- now() es la función que registra internal/db al abrir la base.

CREATE TABLE projects (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    name             TEXT NOT NULL,
    public_key       TEXT NOT NULL UNIQUE,
    created_at       INTEGER NOT NULL DEFAULT (now()),
    linear_label_ids TEXT NOT NULL DEFAULT '[]'
) STRICT;

CREATE TABLE api_tokens (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    name         TEXT NOT NULL CHECK (name <> ''),
    token_sha256 BLOB NOT NULL UNIQUE,
    created_at   INTEGER NOT NULL DEFAULT (now()),
    last_used_at INTEGER,
    revoked_at   INTEGER
) STRICT;
CREATE UNIQUE INDEX api_tokens_active_name_idx ON api_tokens (name) WHERE revoked_at IS NULL;

CREATE TABLE issues (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id          INTEGER NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    fingerprint         TEXT NOT NULL,
    title               TEXT NOT NULL,
    culprit             TEXT NOT NULL DEFAULT '',
    type                TEXT NOT NULL DEFAULT '',
    level               TEXT NOT NULL DEFAULT 'error',
    status              TEXT NOT NULL DEFAULT 'open'
        CHECK (status IN ('open', 'resolved', 'resolved_next', 'silenced')),
    resolved_in_release TEXT,
    first_seen          INTEGER NOT NULL,
    last_seen           INTEGER NOT NULL,
    event_count         INTEGER NOT NULL DEFAULT 0 CHECK (event_count >= 0),
    last_regressed_at   INTEGER,
    resolved_at         INTEGER,
    stored_hour         INTEGER,
    stored_in_hour      INTEGER NOT NULL DEFAULT 0,
    last_stored_at      INTEGER,
    first_release       TEXT,
    fixed_in_release    TEXT,
    user_count          INTEGER NOT NULL DEFAULT 0 CHECK (user_count >= 0),
    assignee            TEXT,
    assignee_kind       TEXT CHECK (assignee_kind IN ('user', 'token')),
    assignee_token_id   INTEGER REFERENCES api_tokens (id),
    assigned_at         INTEGER,
    UNIQUE (project_id, fingerprint),
    CHECK (CASE assignee_kind
        WHEN 'user' THEN assignee IS NOT NULL AND assignee_token_id IS NULL AND assigned_at IS NOT NULL
        WHEN 'token' THEN assignee IS NOT NULL AND assignee_token_id IS NOT NULL AND assigned_at IS NOT NULL
        ELSE assignee IS NULL AND assignee_token_id IS NULL AND assigned_at IS NULL
    END)
) STRICT;
CREATE INDEX issues_project_last_seen_idx ON issues (project_id, last_seen DESC);
CREATE INDEX issues_project_status_last_seen_idx ON issues (project_id, status, last_seen DESC);
CREATE INDEX issues_assignee_idx ON issues (lower(assignee)) WHERE assignee IS NOT NULL;

CREATE TABLE events (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    issue_id        INTEGER NOT NULL REFERENCES issues (id) ON DELETE CASCADE,
    project_id      INTEGER NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    event_id        TEXT NOT NULL,
    occurred_at     INTEGER NOT NULL,
    received_at     INTEGER NOT NULL DEFAULT (now()),
    release         TEXT,
    environment     TEXT,
    -- El evento sin comprimir de antes de la 00022 de Postgres; aquí siempre NULL.
    data            TEXT,
    data_zstd       BLOB,
    user_label      TEXT,
    user_ident      TEXT,
    organization_id TEXT,
    summary         TEXT,
    UNIQUE (project_id, event_id),
    CHECK (data IS NOT NULL OR data_zstd IS NOT NULL)
) STRICT;
CREATE INDEX events_issue_occurred_at_idx ON events (issue_id, occurred_at DESC);
CREATE INDEX events_project_occurred_at_idx ON events (project_id, occurred_at);
CREATE INDEX events_received_at_idx ON events (received_at);

CREATE TABLE sourcemaps (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id  INTEGER NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    release     TEXT NOT NULL,
    file        TEXT NOT NULL,
    map_gzip    BLOB NOT NULL,
    size        INTEGER NOT NULL,
    uploaded_at INTEGER NOT NULL DEFAULT (now()),
    dist        TEXT NOT NULL DEFAULT '',
    url         TEXT NOT NULL DEFAULT '',
    debug_id    TEXT
) STRICT;
CREATE UNIQUE INDEX sourcemaps_debug_id_key ON sourcemaps (project_id, debug_id) WHERE debug_id IS NOT NULL;
CREATE UNIQUE INDEX sourcemaps_name_key ON sourcemaps (project_id, release, dist, file) WHERE debug_id IS NULL;
CREATE INDEX sourcemaps_release_file_idx ON sourcemaps (project_id, release, file);
CREATE INDEX sourcemaps_uploaded_at_idx ON sourcemaps (uploaded_at);

CREATE TABLE upload_chunks (
    sha1        TEXT PRIMARY KEY,
    data        BLOB NOT NULL,
    uploaded_at INTEGER NOT NULL DEFAULT (now())
) STRICT;
CREATE INDEX upload_chunks_uploaded_at_idx ON upload_chunks (uploaded_at);

CREATE TABLE artifact_bundles (
    project_id   INTEGER NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    checksum     TEXT NOT NULL,
    release      TEXT NOT NULL,
    dist         TEXT NOT NULL,
    files        INTEGER NOT NULL,
    assembled_at INTEGER NOT NULL DEFAULT (now()),
    PRIMARY KEY (project_id, checksum)
) STRICT;
CREATE INDEX artifact_bundles_assembled_at_idx ON artifact_bundles (assembled_at);

CREATE TABLE alert_rules (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id       INTEGER NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    name             TEXT NOT NULL,
    enabled          INTEGER NOT NULL DEFAULT 1,
    conditions       TEXT NOT NULL,
    actions          TEXT NOT NULL,
    throttle_minutes INTEGER NOT NULL DEFAULT 0 CHECK (throttle_minutes >= 0),
    created_at       INTEGER NOT NULL DEFAULT (now()),
    updated_at       INTEGER NOT NULL DEFAULT (now())
) STRICT;
CREATE INDEX alert_rules_project_idx ON alert_rules (project_id);

CREATE TABLE monitors (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id      INTEGER NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    slug            TEXT NOT NULL,
    name            TEXT NOT NULL,
    schedule_type   TEXT NOT NULL CHECK (schedule_type IN ('crontab', 'interval')),
    schedule        TEXT NOT NULL DEFAULT '',
    interval_value  INTEGER,
    interval_unit   TEXT CHECK (interval_unit IN ('minute', 'hour', 'day', 'week', 'month', 'year')),
    timezone        TEXT NOT NULL DEFAULT 'UTC',
    checkin_margin  INTEGER NOT NULL DEFAULT 1 CHECK (checkin_margin >= 1),
    max_runtime     INTEGER NOT NULL DEFAULT 30 CHECK (max_runtime >= 1),
    environment     TEXT NOT NULL DEFAULT '',
    muted           INTEGER NOT NULL DEFAULT 0,
    status          TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'ok', 'error', 'missed', 'timeout')),
    last_checkin_at INTEGER,
    next_checkin_at INTEGER,
    created_at      INTEGER NOT NULL DEFAULT (now()),
    updated_at      INTEGER NOT NULL DEFAULT (now()),
    UNIQUE (project_id, slug)
) STRICT;
CREATE INDEX monitors_next_checkin_idx ON monitors (next_checkin_at) WHERE next_checkin_at IS NOT NULL;

CREATE TABLE monitor_checkins (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    monitor_id  INTEGER NOT NULL REFERENCES monitors (id) ON DELETE CASCADE,
    check_in_id TEXT NOT NULL,
    status      TEXT NOT NULL CHECK (status IN ('in_progress', 'ok', 'error', 'missed', 'timeout')),
    duration_ms INTEGER,
    environment TEXT NOT NULL DEFAULT '',
    release     TEXT NOT NULL DEFAULT '',
    expected_at INTEGER,
    started_at  INTEGER NOT NULL,
    finished_at INTEGER,
    timeout_at  INTEGER,
    received_at INTEGER NOT NULL DEFAULT (now()),
    UNIQUE (monitor_id, check_in_id)
) STRICT;
CREATE INDEX monitor_checkins_monitor_idx ON monitor_checkins (monitor_id, started_at DESC, id DESC);
CREATE INDEX monitor_checkins_received_idx ON monitor_checkins (received_at);
CREATE INDEX monitor_checkins_timeout_idx ON monitor_checkins (timeout_at) WHERE status = 'in_progress';

CREATE TABLE uptime_monitors (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id           INTEGER NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    slug                 TEXT NOT NULL,
    name                 TEXT NOT NULL,
    url                  TEXT NOT NULL,
    method               TEXT NOT NULL DEFAULT 'GET' CHECK (method IN ('GET', 'HEAD')),
    interval_seconds     INTEGER NOT NULL DEFAULT 60 CHECK (interval_seconds BETWEEN 30 AND 3600),
    timeout_seconds      INTEGER NOT NULL DEFAULT 10 CHECK (timeout_seconds BETWEEN 1 AND 60),
    expected_status      TEXT NOT NULL DEFAULT '200-299',
    failure_threshold    INTEGER NOT NULL DEFAULT 2 CHECK (failure_threshold BETWEEN 1 AND 10),
    paused               INTEGER NOT NULL DEFAULT 0,
    status               TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'up', 'down')),
    consecutive_failures INTEGER NOT NULL DEFAULT 0,
    first_failure_at     INTEGER,
    first_failure_error  TEXT,
    last_checked_at      INTEGER,
    last_status_code     INTEGER,
    last_duration_ms     INTEGER,
    last_error           TEXT NOT NULL DEFAULT '',
    next_check_at        INTEGER NOT NULL DEFAULT (now()),
    created_at           INTEGER NOT NULL DEFAULT (now()),
    updated_at           INTEGER NOT NULL DEFAULT (now()),
    UNIQUE (project_id, slug)
) STRICT;
CREATE INDEX uptime_monitors_due_idx ON uptime_monitors (next_check_at) WHERE NOT paused;

CREATE TABLE uptime_checks (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    monitor_id  INTEGER NOT NULL REFERENCES uptime_monitors (id) ON DELETE CASCADE,
    checked_at  INTEGER NOT NULL,
    ok          INTEGER NOT NULL,
    status_code INTEGER,
    duration_ms INTEGER NOT NULL,
    error       TEXT NOT NULL DEFAULT ''
) STRICT;
CREATE INDEX uptime_checks_checked_idx ON uptime_checks (checked_at);
CREATE INDEX uptime_checks_monitor_idx ON uptime_checks (monitor_id, checked_at DESC, id DESC);

CREATE TABLE uptime_incidents (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    monitor_id  INTEGER NOT NULL REFERENCES uptime_monitors (id) ON DELETE CASCADE,
    started_at  INTEGER NOT NULL,
    ended_at    INTEGER,
    duration_ms INTEGER,
    first_error TEXT NOT NULL DEFAULT '',
    CHECK (ended_at IS NULL OR ended_at >= started_at)
) STRICT;
CREATE INDEX uptime_incidents_monitor_idx ON uptime_incidents (monitor_id, started_at DESC);
CREATE UNIQUE INDEX uptime_incidents_open_idx ON uptime_incidents (monitor_id) WHERE ended_at IS NULL;

CREATE TABLE alert_outbox (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    issue_id          INTEGER REFERENCES issues (id) ON DELETE CASCADE,
    channel           TEXT NOT NULL
        CHECK (channel IN ('slack', 'linear', 'email', 'discord', 'telegram', 'ntfy', 'webhook', 'github')),
    kind              TEXT NOT NULL CHECK (kind IN ('new', 'regressed', 'frequency', 'users', 'cron_failed',
        'cron_missed', 'cron_timeout', 'cron_recovered', 'uptime_down', 'uptime_up', 'digest')),
    release           TEXT,
    environment       TEXT,
    event_at          INTEGER NOT NULL,
    -- Un UUID v4 con guiones, como el gen_random_uuid() de Postgres.
    linear_create_id  TEXT NOT NULL DEFAULT (lower(
        hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)), 2) || '-' ||
        substr('89AB', 1 + (abs(random()) % 4), 1) || substr(hex(randomblob(2)), 2) || '-' || hex(randomblob(6)))),
    created_at        INTEGER NOT NULL DEFAULT (now()),
    attempts          INTEGER NOT NULL DEFAULT 0,
    next_attempt_at   INTEGER NOT NULL DEFAULT (now()),
    last_error        TEXT,
    sent_at           INTEGER,
    failed_at         INTEGER,
    resolved_at       INTEGER,
    waits_for         INTEGER REFERENCES alert_outbox (id) ON DELETE SET NULL,
    rule_id           INTEGER REFERENCES alert_rules (id) ON DELETE CASCADE,
    action_id         TEXT,
    detail            TEXT,
    monitor_id        INTEGER REFERENCES monitors (id) ON DELETE CASCADE,
    checkin_id        INTEGER REFERENCES monitor_checkins (id) ON DELETE CASCADE,
    uptime_monitor_id INTEGER REFERENCES uptime_monitors (id) ON DELETE CASCADE,
    incident_id       INTEGER REFERENCES uptime_incidents (id) ON DELETE CASCADE,
    digest_project_id INTEGER REFERENCES projects (id) ON DELETE CASCADE,
    CHECK (digest_project_id IS NULL OR (kind = 'digest' AND issue_id IS NULL AND monitor_id IS NULL)),
    CHECK (digest_project_id IS NOT NULL
        OR (issue_id IS NOT NULL) + (monitor_id IS NOT NULL) + (uptime_monitor_id IS NOT NULL) = 1)
) STRICT;
CREATE INDEX alert_outbox_pending_idx ON alert_outbox (issue_id, channel, id) WHERE sent_at IS NULL AND failed_at IS NULL;
CREATE INDEX alert_outbox_checkin_idx ON alert_outbox (checkin_id) WHERE checkin_id IS NOT NULL;
CREATE UNIQUE INDEX alert_outbox_digest_idx ON alert_outbox (digest_project_id, action_id, event_at)
    WHERE digest_project_id IS NOT NULL;
CREATE INDEX alert_outbox_incident_idx ON alert_outbox (incident_id) WHERE incident_id IS NOT NULL;
CREATE INDEX alert_outbox_monitor_idx ON alert_outbox (monitor_id) WHERE monitor_id IS NOT NULL;
CREATE INDEX alert_outbox_uptime_idx ON alert_outbox (uptime_monitor_id) WHERE uptime_monitor_id IS NOT NULL;

CREATE TABLE alert_rule_issues (
    rule_id           INTEGER NOT NULL REFERENCES alert_rules (id) ON DELETE CASCADE,
    issue_id          INTEGER NOT NULL REFERENCES issues (id) ON DELETE CASCADE,
    last_alert_at     INTEGER,
    frequency_alerted INTEGER NOT NULL DEFAULT 0,
    users_alerted     INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (rule_id, issue_id)
) STRICT;
CREATE INDEX alert_rule_issues_issue_idx ON alert_rule_issues (issue_id);

CREATE TABLE alert_rule_minutes (
    rule_id  INTEGER NOT NULL REFERENCES alert_rules (id) ON DELETE CASCADE,
    issue_id INTEGER NOT NULL REFERENCES issues (id) ON DELETE CASCADE,
    minute   INTEGER NOT NULL,
    events   INTEGER NOT NULL,
    PRIMARY KEY (rule_id, issue_id, minute)
) STRICT;
CREATE INDEX alert_rule_minutes_issue_idx ON alert_rule_minutes (issue_id);

CREATE TABLE alert_rule_users (
    rule_id  INTEGER NOT NULL REFERENCES alert_rules (id) ON DELETE CASCADE,
    issue_id INTEGER NOT NULL REFERENCES issues (id) ON DELETE CASCADE,
    user_key TEXT NOT NULL,
    PRIMARY KEY (rule_id, issue_id, user_key)
) STRICT;
CREATE INDEX alert_rule_users_issue_idx ON alert_rule_users (issue_id);

CREATE TABLE releases (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id   INTEGER NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    version      TEXT NOT NULL CHECK (version <> ''),
    first_seen   INTEGER NOT NULL DEFAULT (now()),
    finalized_at INTEGER,
    UNIQUE (project_id, version)
) STRICT;
CREATE INDEX releases_project_first_seen_idx ON releases (project_id, first_seen DESC, id DESC);

CREATE TABLE deploys (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    release_id  INTEGER NOT NULL REFERENCES releases (id) ON DELETE CASCADE,
    environment TEXT NOT NULL CHECK (environment <> ''),
    deployed_at INTEGER NOT NULL DEFAULT (now())
) STRICT;
CREATE INDEX deploys_release_idx ON deploys (release_id, deployed_at DESC);

CREATE TABLE issue_stats (
    issue_id     INTEGER NOT NULL REFERENCES issues (id) ON DELETE CASCADE,
    project_id   INTEGER NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    release      TEXT NOT NULL,
    environment  TEXT NOT NULL,
    first_seen   INTEGER NOT NULL,
    last_seen    INTEGER NOT NULL,
    event_count  INTEGER NOT NULL CHECK (event_count >= 0),
    regressed_at INTEGER,
    PRIMARY KEY (issue_id, release, environment)
) STRICT;
CREATE INDEX issue_stats_project_environment_idx ON issue_stats (project_id, environment);
CREATE INDEX issue_stats_project_release_idx ON issue_stats (project_id, release);

CREATE TABLE issue_tag_values (
    issue_id  INTEGER NOT NULL REFERENCES issues (id) ON DELETE CASCADE,
    key       TEXT NOT NULL,
    value     TEXT NOT NULL,
    count     INTEGER NOT NULL CHECK (count > 0),
    last_seen INTEGER NOT NULL,
    PRIMARY KEY (issue_id, key, value)
) STRICT;
CREATE INDEX issue_tag_values_key_value_idx ON issue_tag_values (key, value);

CREATE TABLE issue_users (
    issue_id  INTEGER NOT NULL REFERENCES issues (id) ON DELETE CASCADE,
    user_hash BLOB NOT NULL,
    PRIMARY KEY (issue_id, user_hash)
) STRICT;

CREATE TABLE user_hash_key (
    only_row INTEGER PRIMARY KEY DEFAULT 1 CHECK (only_row),
    key      BLOB NOT NULL
) STRICT;
INSERT INTO user_hash_key (key) VALUES (randomblob(32));

CREATE TABLE issue_comments (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    issue_id    INTEGER NOT NULL REFERENCES issues (id) ON DELETE CASCADE,
    author      TEXT NOT NULL,
    author_kind TEXT NOT NULL CHECK (author_kind IN ('user', 'token')),
    body        TEXT NOT NULL CHECK (body <> ''),
    created_at  INTEGER NOT NULL DEFAULT (now())
) STRICT;
CREATE INDEX issue_comments_issue_idx ON issue_comments (issue_id, created_at);

CREATE TABLE issue_status_changes (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    issue_id    INTEGER NOT NULL REFERENCES issues (id) ON DELETE CASCADE,
    author      TEXT,
    author_kind TEXT CHECK (author_kind IN ('user', 'token')),
    source      TEXT NOT NULL
        CHECK (source IN ('ui', 'api', 'mcp', 'ingest', 'deploy', 'linear', 'github', 'import')),
    from_status TEXT NOT NULL,
    to_status   TEXT NOT NULL,
    release     TEXT,
    comment_id  INTEGER REFERENCES issue_comments (id) ON DELETE SET NULL,
    created_at  INTEGER NOT NULL DEFAULT (now()),
    CHECK ((author IS NULL) = (author_kind IS NULL))
) STRICT;
CREATE INDEX issue_status_changes_issue_idx ON issue_status_changes (issue_id, created_at);
CREATE INDEX issue_status_changes_comment_idx ON issue_status_changes (comment_id) WHERE comment_id IS NOT NULL;

CREATE TABLE issue_assignment_changes (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    issue_id    INTEGER NOT NULL REFERENCES issues (id) ON DELETE CASCADE,
    author      TEXT,
    author_kind TEXT CHECK (author_kind IN ('user', 'token')),
    source      TEXT NOT NULL CHECK (source IN ('ui', 'api', 'mcp')),
    from_name   TEXT,
    from_kind   TEXT CHECK (from_kind IN ('user', 'token')),
    to_name     TEXT,
    to_kind     TEXT CHECK (to_kind IN ('user', 'token')),
    created_at  INTEGER NOT NULL DEFAULT (now()),
    CHECK ((author IS NULL) = (author_kind IS NULL)),
    CHECK ((from_name IS NULL) = (from_kind IS NULL)),
    CHECK ((to_name IS NULL) = (to_kind IS NULL))
) STRICT;
CREATE INDEX issue_assignment_changes_issue_idx ON issue_assignment_changes (issue_id, created_at);

CREATE TABLE people (
    login         TEXT PRIMARY KEY CHECK (login <> ''),
    name          TEXT NOT NULL,
    avatar_url    TEXT,
    first_seen_at INTEGER NOT NULL DEFAULT (now()),
    last_login_at INTEGER NOT NULL DEFAULT (now()),
    provider      TEXT NOT NULL CHECK (provider IN ('github', 'oidc', 'dev')),
    subject       TEXT NOT NULL CHECK (subject <> ''),
    UNIQUE (provider, subject)
) STRICT;

CREATE TABLE issue_links (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    issue_id    INTEGER NOT NULL REFERENCES issues (id) ON DELETE CASCADE,
    provider    TEXT NOT NULL CHECK (provider IN ('linear', 'github')),
    kind        TEXT NOT NULL CHECK (kind IN ('issue', 'pull_request')),
    external_id TEXT NOT NULL CHECK (external_id <> ''),
    identifier  TEXT,
    url         TEXT,
    state       TEXT CHECK (state IN ('open', 'closed', 'canceled', 'merged')),
    state_name  TEXT,
    rule_id     INTEGER REFERENCES alert_rules (id) ON DELETE SET NULL,
    action_id   TEXT,
    created_at  INTEGER NOT NULL DEFAULT (now()),
    updated_at  INTEGER NOT NULL DEFAULT (now()),
    UNIQUE (issue_id, provider, kind, external_id),
    CHECK (provider = 'github' OR kind = 'issue')
) STRICT;
CREATE INDEX issue_links_external_idx ON issue_links (provider, external_id);

CREATE TABLE issue_link_sync (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    link_id         INTEGER NOT NULL REFERENCES issue_links (id) ON DELETE CASCADE,
    status          TEXT NOT NULL CHECK (status IN ('open', 'resolved', 'resolved_next', 'silenced')),
    created_at      INTEGER NOT NULL DEFAULT (now()),
    attempts        INTEGER NOT NULL DEFAULT 0,
    next_attempt_at INTEGER NOT NULL DEFAULT (now()),
    last_error      TEXT,
    sent_at         INTEGER,
    failed_at       INTEGER,
    echoed_at       INTEGER
) STRICT;
CREATE INDEX issue_link_sync_pending_idx ON issue_link_sync (link_id, id) WHERE sent_at IS NULL AND failed_at IS NULL;
CREATE INDEX issue_link_sync_echo_idx ON issue_link_sync (link_id, id) WHERE sent_at IS NOT NULL AND echoed_at IS NULL;

CREATE TABLE webhook_deliveries (
    provider    TEXT NOT NULL CHECK (provider IN ('linear', 'github')),
    delivery_id TEXT NOT NULL,
    event       TEXT NOT NULL DEFAULT '',
    received_at INTEGER NOT NULL DEFAULT (now()),
    PRIMARY KEY (provider, delivery_id)
) STRICT;
CREATE INDEX webhook_deliveries_received_idx ON webhook_deliveries (provider, received_at);

CREATE TABLE project_filters (
    project_id                INTEGER PRIMARY KEY REFERENCES projects (id) ON DELETE CASCADE,
    messages                  TEXT NOT NULL DEFAULT '[]',
    exception_types           TEXT NOT NULL DEFAULT '[]',
    browser_extensions        INTEGER NOT NULL DEFAULT 0,
    web_crawlers              INTEGER NOT NULL DEFAULT 0,
    localhost                 INTEGER NOT NULL DEFAULT 0,
    environments              TEXT NOT NULL DEFAULT '[]',
    releases                  TEXT NOT NULL DEFAULT '[]',
    events_per_issue_per_hour INTEGER CHECK (events_per_issue_per_hour > 0),
    sample_rate               REAL NOT NULL DEFAULT 1 CHECK (sample_rate >= 0 AND sample_rate <= 1),
    rate_limit_per_minute     INTEGER CHECK (rate_limit_per_minute > 0),
    updated_at                INTEGER NOT NULL DEFAULT (now())
) STRICT;

CREATE TABLE dropped_events (
    project_id INTEGER NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    reason     TEXT NOT NULL,
    day        INTEGER NOT NULL,
    count      INTEGER NOT NULL CHECK (count >= 0),
    PRIMARY KEY (project_id, day, reason)
) STRICT;

CREATE TABLE release_sessions (
    project_id  INTEGER NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    release     TEXT NOT NULL,
    environment TEXT NOT NULL,
    hour        INTEGER NOT NULL,
    total       INTEGER NOT NULL DEFAULT 0 CHECK (total >= 0),
    errored     INTEGER NOT NULL DEFAULT 0 CHECK (errored >= 0),
    crashed     INTEGER NOT NULL DEFAULT 0 CHECK (crashed >= 0),
    abnormal    INTEGER NOT NULL DEFAULT 0 CHECK (abnormal >= 0),
    PRIMARY KEY (project_id, release, environment, hour)
) STRICT;
CREATE INDEX release_sessions_project_hour_idx ON release_sessions (project_id, hour);

CREATE TABLE release_users (
    project_id  INTEGER NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    release     TEXT NOT NULL,
    environment TEXT NOT NULL,
    user_hash   BLOB NOT NULL,
    crashed     INTEGER NOT NULL DEFAULT 0,
    last_seen   INTEGER NOT NULL,
    PRIMARY KEY (project_id, release, environment, user_hash)
) STRICT;
CREATE INDEX release_users_last_seen_idx ON release_users (last_seen);

CREATE TABLE session_states (
    project_id  INTEGER NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    sid         BLOB NOT NULL,
    release     TEXT NOT NULL,
    environment TEXT NOT NULL,
    hour        INTEGER NOT NULL,
    init_seen   INTEGER NOT NULL,
    status      TEXT NOT NULL,
    errors      INTEGER NOT NULL CHECK (errors >= 0),
    sent_at     INTEGER NOT NULL,
    user_hash   BLOB,
    updated_at  INTEGER NOT NULL DEFAULT (now()),
    PRIMARY KEY (project_id, sid)
) STRICT;
CREATE INDEX session_states_updated_idx ON session_states (updated_at);

CREATE TABLE project_digests (
    project_id      INTEGER PRIMARY KEY REFERENCES projects (id) ON DELETE CASCADE,
    enabled         INTEGER NOT NULL DEFAULT 0,
    frequency       TEXT NOT NULL DEFAULT 'daily' CHECK (frequency IN ('daily', 'weekly')),
    weekday         INTEGER NOT NULL DEFAULT 1 CHECK (weekday BETWEEN 0 AND 6),
    hour            INTEGER NOT NULL DEFAULT 9 CHECK (hour BETWEEN 0 AND 23),
    timezone        TEXT NOT NULL DEFAULT 'UTC',
    language        TEXT NOT NULL DEFAULT 'es' CHECK (language IN ('es', 'en')),
    send_empty      INTEGER NOT NULL DEFAULT 1,
    destinations    TEXT NOT NULL DEFAULT '[]',
    last_period_end INTEGER,
    last_sent_at    INTEGER,
    updated_at      INTEGER NOT NULL DEFAULT (now())
) STRICT;

CREATE TABLE import_origins (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    source      TEXT NOT NULL CHECK (source = 'sentry'),
    server      TEXT NOT NULL,
    org         TEXT NOT NULL,
    kind        TEXT NOT NULL CHECK (kind IN ('project', 'issue', 'release', 'deploy')),
    external_id TEXT NOT NULL,
    project_id  INTEGER REFERENCES projects (id) ON DELETE CASCADE,
    issue_id    INTEGER REFERENCES issues (id) ON DELETE CASCADE,
    release_id  INTEGER REFERENCES releases (id) ON DELETE CASCADE,
    deploy_id   INTEGER REFERENCES deploys (id) ON DELETE CASCADE,
    imported    TEXT NOT NULL DEFAULT '{}',
    imported_at INTEGER NOT NULL DEFAULT (now()),
    updated_at  INTEGER NOT NULL DEFAULT (now()),
    UNIQUE (source, server, org, kind, external_id),
    CHECK ((kind = 'project') = (project_id IS NOT NULL)),
    CHECK ((kind = 'issue') = (issue_id IS NOT NULL)),
    CHECK ((kind = 'release') = (release_id IS NOT NULL)),
    CHECK ((kind = 'deploy') = (deploy_id IS NOT NULL))
) STRICT;
CREATE INDEX import_origins_project_idx ON import_origins (project_id) WHERE project_id IS NOT NULL;
CREATE INDEX import_origins_issue_idx ON import_origins (issue_id) WHERE issue_id IS NOT NULL;
CREATE INDEX import_origins_release_idx ON import_origins (release_id) WHERE release_id IS NOT NULL;
CREATE INDEX import_origins_deploy_idx ON import_origins (deploy_id) WHERE deploy_id IS NOT NULL;

-- Al borrar una fila, SQLite busca las que la referencian con una clave
-- ajena; estos índices, que Postgres no tiene, se las dan sin recorrer la tabla.
CREATE INDEX alert_outbox_issue_idx ON alert_outbox (issue_id) WHERE issue_id IS NOT NULL;
CREATE INDEX alert_outbox_waits_for_idx ON alert_outbox (waits_for) WHERE waits_for IS NOT NULL;
CREATE INDEX alert_outbox_rule_idx ON alert_outbox (rule_id) WHERE rule_id IS NOT NULL;
CREATE INDEX issue_links_rule_idx ON issue_links (rule_id) WHERE rule_id IS NOT NULL;
CREATE INDEX issue_link_sync_link_idx ON issue_link_sync (link_id);

-- +goose Down
SELECT 'no se vuelve atrás del esquema inicial de SQLite';
