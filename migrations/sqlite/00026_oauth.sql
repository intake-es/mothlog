-- +goose Up
-- OAuth 2.1 del MCP. Los clientes registrados con el registro dinámico; los
-- que se identifican con una URL (Client ID Metadata Document) no se guardan.
-- Los que no llegan a usarse se borran solos.
CREATE TABLE oauth_clients (
    client_id     TEXT PRIMARY KEY,
    name          TEXT NOT NULL,
    redirect_uris TEXT NOT NULL,
    created_at    INTEGER NOT NULL,
    used_at       INTEGER
) STRICT;
CREATE INDEX oauth_clients_unused_idx ON oauth_clients (created_at) WHERE used_at IS NULL;

-- Una concesión: lo que una persona dejó hacer a un cliente. Sigue a la
-- persona si cambia de login y cae con ella.
CREATE TABLE oauth_grants (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    login        TEXT NOT NULL REFERENCES people (login) ON UPDATE CASCADE ON DELETE CASCADE,
    client_id    TEXT NOT NULL,
    client_name  TEXT NOT NULL,
    redirect_uri TEXT NOT NULL,
    scopes       TEXT NOT NULL,
    resource     TEXT NOT NULL,
    created_at   INTEGER NOT NULL,
    last_used_at INTEGER,
    last_used_ip TEXT,
    revoked_at   INTEGER
) STRICT;
CREATE INDEX oauth_grants_login_idx ON oauth_grants (login, created_at DESC);

-- Una petición de autorización, desde que el cliente manda a la persona al
-- consentimiento hasta que canjea el código. Del código solo el sha256.
CREATE TABLE oauth_requests (
    id              TEXT PRIMARY KEY,
    client_id       TEXT NOT NULL,
    client_name     TEXT NOT NULL,
    redirect_uri    TEXT NOT NULL,
    scopes          TEXT NOT NULL,
    state           TEXT NOT NULL,
    code_challenge  TEXT NOT NULL,
    resource        TEXT NOT NULL,
    created_at      INTEGER NOT NULL,
    expires_at      INTEGER NOT NULL,
    login           TEXT REFERENCES people (login) ON UPDATE CASCADE ON DELETE CASCADE,
    granted_scopes  TEXT,
    code_sha256     BLOB UNIQUE,
    code_expires_at INTEGER,
    used_at         INTEGER,
    grant_id        INTEGER REFERENCES oauth_grants (id) ON DELETE SET NULL
) STRICT;
CREATE INDEX oauth_requests_expires_idx ON oauth_requests (expires_at);

-- Tokens de acceso y de refresco, solo su sha256. Un refresco canjeado se
-- guarda con used_at hasta que caduca, para reconocer que se reusa.
CREATE TABLE oauth_tokens (
    token_sha256 BLOB PRIMARY KEY,
    grant_id     INTEGER NOT NULL REFERENCES oauth_grants (id) ON DELETE CASCADE,
    kind         TEXT NOT NULL CHECK (kind IN ('access', 'refresh')),
    scopes       TEXT NOT NULL,
    created_at   INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL,
    used_at      INTEGER
) STRICT;
CREATE INDEX oauth_tokens_grant_idx ON oauth_tokens (grant_id);
CREATE INDEX oauth_tokens_expires_idx ON oauth_tokens (expires_at);

-- El cliente por el que una persona comentó, cambió el estado o asignó.
ALTER TABLE issue_comments ADD COLUMN author_via TEXT;
ALTER TABLE issue_status_changes ADD COLUMN author_via TEXT;
ALTER TABLE issue_assignment_changes ADD COLUMN author_via TEXT;

-- +goose Down
ALTER TABLE issue_assignment_changes DROP COLUMN author_via;
ALTER TABLE issue_status_changes DROP COLUMN author_via;
ALTER TABLE issue_comments DROP COLUMN author_via;
DROP TABLE oauth_tokens;
DROP TABLE oauth_requests;
DROP TABLE oauth_grants;
DROP TABLE oauth_clients;
