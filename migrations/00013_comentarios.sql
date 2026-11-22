-- +goose Up
-- Comentarios de cada issue, en Markdown. author_kind dice si el autor es una
-- persona (el usuario de la sesión de la interfaz) o un token de la API o del MCP.
CREATE TABLE issue_comments (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    issue_id    bigint NOT NULL REFERENCES issues (id) ON DELETE CASCADE,
    author      text NOT NULL,
    author_kind text NOT NULL CHECK (author_kind IN ('user', 'token')),
    body        text NOT NULL CHECK (body <> ''),
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX issue_comments_issue_idx ON issue_comments (issue_id, created_at);

-- Los cambios de estado de cada issue. Los de la ingesta (regresiones) y los
-- despliegues (resuelto en la siguiente versión) van sin autor.
CREATE TABLE issue_status_changes (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    issue_id    bigint NOT NULL REFERENCES issues (id) ON DELETE CASCADE,
    author      text,
    author_kind text CHECK (author_kind IN ('user', 'token')),
    source      text NOT NULL CHECK (source IN ('ui', 'api', 'mcp', 'ingest', 'deploy')),
    from_status text NOT NULL,
    to_status   text NOT NULL,
    release     text,
    comment_id  bigint REFERENCES issue_comments (id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    CHECK ((author IS NULL) = (author_kind IS NULL))
);

CREATE INDEX issue_status_changes_issue_idx ON issue_status_changes (issue_id, created_at);
CREATE INDEX issue_status_changes_comment_idx ON issue_status_changes (comment_id) WHERE comment_id IS NOT NULL;

-- +goose Down
DROP TABLE issue_status_changes;
DROP TABLE issue_comments;
