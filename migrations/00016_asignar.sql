-- +goose Up
-- Las personas que han entrado en la interfaz, para poder asignarles issues.
CREATE TABLE people (
    login         text PRIMARY KEY CHECK (login <> ''),
    name          text NOT NULL,
    avatar_url    text,
    first_seen_at timestamptz NOT NULL DEFAULT now(),
    last_login_at timestamptz NOT NULL DEFAULT now()
);

-- El asignado de cada issue: una persona (assignee es su login) o un token
-- (assignee es su nombre y assignee_token_id, el token, que sigue ahí aunque
-- se revoque).
ALTER TABLE issues
    ADD COLUMN assignee          text,
    ADD COLUMN assignee_kind     text CHECK (assignee_kind IN ('user', 'token')),
    ADD COLUMN assignee_token_id bigint REFERENCES api_tokens (id),
    ADD COLUMN assigned_at       timestamptz,
    ADD CONSTRAINT issues_assignee_check CHECK (CASE assignee_kind
        WHEN 'user' THEN assignee IS NOT NULL AND assignee_token_id IS NULL AND assigned_at IS NOT NULL
        WHEN 'token' THEN assignee IS NOT NULL AND assignee_token_id IS NOT NULL AND assigned_at IS NOT NULL
        ELSE assignee IS NULL AND assignee_token_id IS NULL AND assigned_at IS NULL END);

CREATE INDEX issues_assignee_idx ON issues (lower(assignee)) WHERE assignee IS NOT NULL;

-- Cada asignación y desasignación, con quién la hizo. to_* vacío es desasignar;
-- from_*, a quién tenía antes.
CREATE TABLE issue_assignment_changes (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    issue_id    bigint NOT NULL REFERENCES issues (id) ON DELETE CASCADE,
    author      text,
    author_kind text CHECK (author_kind IN ('user', 'token')),
    source      text NOT NULL CHECK (source IN ('ui', 'api', 'mcp')),
    from_name   text,
    from_kind   text CHECK (from_kind IN ('user', 'token')),
    to_name     text,
    to_kind     text CHECK (to_kind IN ('user', 'token')),
    created_at  timestamptz NOT NULL DEFAULT now(),
    CHECK ((author IS NULL) = (author_kind IS NULL)),
    CHECK ((from_name IS NULL) = (from_kind IS NULL)),
    CHECK ((to_name IS NULL) = (to_kind IS NULL))
);

CREATE INDEX issue_assignment_changes_issue_idx ON issue_assignment_changes (issue_id, created_at);

-- +goose Down
DROP TABLE issue_assignment_changes;
ALTER TABLE issues
    DROP CONSTRAINT issues_assignee_check,
    DROP COLUMN assigned_at,
    DROP COLUMN assignee_token_id,
    DROP COLUMN assignee_kind,
    DROP COLUMN assignee;
DROP TABLE people;
