package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/intake-es/mothlog/internal/db"
	"github.com/intake-es/mothlog/internal/search"
)

// Person es alguien que ha entrado en la interfaz. Login es como se la nombra
// en los asignados y en el historial; Provider y Subject, quién es de verdad.
type Person struct {
	Login       string    `json:"login"`
	Name        string    `json:"name"`
	AvatarURL   *string   `json:"avatar_url"`
	Provider    string    `json:"provider"`
	Subject     string    `json:"-"`
	LastLoginAt time.Time `json:"last_login_at"`
	// LegacySubject es el subject con el que pudo guardarse antes: si no hay
	// fila con Subject y sí con este, esa fila pasa a Subject.
	LegacySubject string `json:"-"`
	// SessionsValidSince: las sesiones emitidas hasta este momento no valen.
	SessionsValidSince time.Time `json:"-"`
}

// Por dónde entra cada persona, en people.provider.
const (
	LoginGitHub = "github"
	LoginOIDC   = "oidc"
	LoginDev    = "dev"
)

// maxLoginSuffix es cuántos sufijos se prueban antes de rendirse si el login
// ya es de otras personas.
const maxLoginSuffix = 50

// Assignee es quien lleva un issue: una persona o un token. Active es false
// si el token se ha revocado; la asignación conserva el nombre.
type Assignee struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	// DisplayName y AvatarURL, de las personas.
	DisplayName string  `json:"display_name,omitempty"`
	AvatarURL   *string `json:"avatar_url,omitempty"`
	Active      bool    `json:"active"`
}

// AssignChange es quién asigna y desde dónde.
type AssignChange struct {
	Actor  *Actor
	Source string
}

// assigneeCols y assigneeJoins añaden el asignado a issueSQL; van aparte
// para no cambiar las columnas que leen otras consultas.
const (
	assigneeCols  = `, i.assignee, i.assignee_kind, ap.name, ap.avatar_url, (atk.id IS NULL OR atk.revoked_at IS NULL)`
	assigneeJoins = ` LEFT JOIN people ap ON i.assignee_kind = 'user' AND ap.login = i.assignee
		LEFT JOIN api_tokens atk ON atk.id = i.assignee_token_id`
)

func issueWithAssigneeSQL(d db.Dialect, envArg string) (cols, from string) {
	cols, from = issueSQL(d, envArg)
	return cols + assigneeCols, from + assigneeJoins
}

func scanIssueWithAssignee(r db.Row) (Issue, error) {
	var i Issue
	var name, kind, display, avatar *string
	var active bool
	err := r.Scan(&i.ID, &i.ProjectID, &i.Project, &i.Title, &i.Culprit, &i.Type, &i.Level, &i.Status,
		&i.Count, &i.UserCount, &i.FirstSeen, &i.LastSeen, &i.LastRelease, &i.ResolvedInRelease, &i.LastRegression,
		&i.FirstRelease, &i.FixedInRelease, &name, &kind, &display, &avatar, &active)
	if err == nil && name != nil && kind != nil {
		i.Assignee = &Assignee{Name: *name, Kind: *kind, AvatarURL: avatar, Active: active}
		if display != nil && *display != *name {
			i.Assignee.DisplayName = *display
		}
	}
	return i, err
}

// SavePerson guarda o pone al día a quien entra en la interfaz. Sin
// Provider, es de GitHub, y sin Subject, Subject es el login.
func (s *Store) SavePerson(ctx context.Context, p Person) error {
	_, err := s.SaveIdentity(ctx, p)
	return err
}

// SaveIdentity guarda o pone al día a quien entra por (Provider, Subject) y
// devuelve su login. La primera vez toma p.Login si nadie lo tiene, sin
// mirar mayúsculas; si ya es de otra persona, p.Login~2, p.Login~3... El
// índice único sobre lower(login) hace que dos altas a la vez no lo compartan.
func (s *Store) SaveIdentity(ctx context.Context, p Person) (string, error) {
	p.Login = strings.TrimSpace(p.Login)
	if p.Login == "" {
		return "", fmt.Errorf("%w: the person needs a login", ErrInvalid)
	}
	if p.Provider == "" {
		p.Provider = LoginGitHub
	}
	if p.Subject == "" {
		p.Subject = p.Login
	}
	if strings.TrimSpace(p.Name) == "" {
		p.Name = p.Login
	}
	if p.AvatarURL != nil && *p.AvatarURL == "" {
		p.AvatarURL = nil
	}
	for n := 1; n <= maxLoginSuffix; n++ {
		var login string
		err := s.pool.QueryRow(ctx, `
			UPDATE people SET name = $3, avatar_url = $4, last_login_at = now()
			WHERE provider = $1 AND subject = $2 RETURNING login`,
			p.Provider, p.Subject, p.Name, p.AvatarURL).Scan(&login)
		if err == nil {
			return login, nil
		}
		if !errors.Is(err, db.ErrNoRows) {
			return "", fmt.Errorf("save person: %w", err)
		}
		if p.LegacySubject != "" {
			// Si hay varias, la que coincide en mayúsculas y luego la más antigua.
			err := s.pool.QueryRow(ctx, `
				UPDATE people SET subject = $2, name = $4, avatar_url = $5, last_login_at = now()
				WHERE provider = $1 AND subject = (
					SELECT subject FROM people WHERE provider = $1 AND lower(subject) = lower($3)
					ORDER BY CASE WHEN subject = $3 THEN 0 ELSE 1 END, first_seen_at LIMIT 1)
				AND NOT EXISTS (SELECT 1 FROM people WHERE provider = $1 AND subject = $2)
				RETURNING login`,
				p.Provider, p.Subject, p.LegacySubject, p.Name, p.AvatarURL).Scan(&login)
			if err == nil {
				return login, nil
			}
			if !errors.Is(err, db.ErrNoRows) {
				return "", fmt.Errorf("save person: %w", err)
			}
		}
		candidate := p.Login
		if n > 1 {
			candidate = fmt.Sprintf("%s~%d", p.Login, n)
		}
		err = s.pool.QueryRow(ctx, `
			INSERT INTO people (login, name, avatar_url, provider, subject) VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT DO NOTHING RETURNING login`,
			candidate, p.Name, p.AvatarURL, p.Provider, p.Subject).Scan(&login)
		if err == nil {
			return login, nil
		}
		if !errors.Is(err, db.ErrNoRows) {
			return "", fmt.Errorf("save person: %w", err)
		}
	}
	return "", fmt.Errorf("save person: %q and its %d variants already belong to other people", p.Login, maxLoginSuffix)
}

// ensurePerson guarda a alguien con sesión que no consta en people, sin
// tocar a quien ya está. Falla si el login, sin mirar mayúsculas, es de otra
// persona: entonces asignarle el issue se lo daría a ella.
func (s *Store) ensurePerson(ctx context.Context, q Querier, login string) error {
	provider, subject := LoginGitHub, LegacyGitHubSubject(login)
	if login == "dev" {
		provider, subject = LoginDev, login
	}
	_, err := q.Exec(ctx, `INSERT INTO people (login, name, provider, subject) VALUES ($1, $1, $2, $3) ON CONFLICT DO NOTHING`, login, provider, subject)
	if err != nil {
		return fmt.Errorf("save person: %w", err)
	}
	var got string
	if err := q.QueryRow(ctx, `SELECT login FROM people WHERE lower(login) = lower($1)`, login).Scan(&got); err != nil {
		return fmt.Errorf("save person: %w", err)
	}
	if got != login {
		return fmt.Errorf("%w: the login %q belongs to another person (%q)", ErrInvalid, login, got)
	}
	return nil
}

// LegacyGitHubSubject es el subject de una persona de GitHub guardada por su
// login, antes de identificarla por el id.
func LegacyGitHubSubject(login string) string { return "login:" + login }

// People son las personas que han entrado, por nombre.
func (s *Store) People(ctx context.Context) ([]Person, error) {
	rows, err := s.pool.Query(ctx, `SELECT login, name, avatar_url, provider, subject, last_login_at, sessions_valid_since FROM people ORDER BY `+s.pool.Dialect().Collate("lower(name)")+`, `+s.pool.Dialect().Collate("login"))
	if err != nil {
		return nil, fmt.Errorf("list people: %w", err)
	}
	return db.CollectRows(rows, func(r db.CollectableRow) (Person, error) {
		var p Person
		return p, r.Scan(&p.Login, &p.Name, &p.AvatarURL, &p.Provider, &p.Subject, &p.LastLoginAt, &p.SessionsValidSince)
	})
}

// Assignees son a quienes se puede asignar un issue: las personas y los
// tokens activos, por ese orden.
func (s *Store) Assignees(ctx context.Context) ([]Assignee, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT login, 'user', CASE WHEN name <> login THEN name ELSE '' END, avatar_url FROM people
		UNION ALL
		SELECT name, 'token', '', NULL FROM api_tokens WHERE revoked_at IS NULL
		ORDER BY 2 DESC, `+s.pool.Dialect().Collate("1"))
	if err != nil {
		return nil, fmt.Errorf("list assignees: %w", err)
	}
	out, err := db.CollectRows(rows, func(r db.CollectableRow) (Assignee, error) {
		a := Assignee{Active: true}
		return a, r.Scan(&a.Name, &a.Kind, &a.DisplayName, &a.AvatarURL)
	})
	if err != nil {
		return nil, fmt.Errorf("list assignees: %w", err)
	}
	if out == nil {
		out = []Assignee{}
	}
	return out, nil
}

// resolveAssignee busca a quién asignar: kind vacío prueba primero las
// personas y luego los tokens activos, y falla si el nombre es de los dos.
func resolveAssignee(ctx context.Context, q Querier, name, kind string) (Actor, *int64, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Actor{}, nil, fmt.Errorf("%w: missing who to assign", ErrInvalid)
	}
	if kind != "" && kind != ActorUser && kind != ActorToken {
		return Actor{}, nil, fmt.Errorf("%w: kind is user or token", ErrInvalid)
	}
	var login *string
	if kind != ActorToken {
		err := q.QueryRow(ctx, `SELECT login FROM people WHERE lower(login) = lower($1)`, name).Scan(&login)
		if err != nil && !errors.Is(err, db.ErrNoRows) {
			return Actor{}, nil, fmt.Errorf("find person: %w", err)
		}
	}
	var tokenID *int64
	if kind != ActorUser {
		err := q.QueryRow(ctx, `SELECT id FROM api_tokens WHERE name = $1 AND revoked_at IS NULL`, name).Scan(&tokenID)
		if err != nil && !errors.Is(err, db.ErrNoRows) {
			return Actor{}, nil, fmt.Errorf("find token: %w", err)
		}
	}
	switch {
	case login != nil && tokenID != nil:
		return Actor{}, nil, fmt.Errorf("%w: %q is both a person and a token; say which with kind", ErrInvalid, name)
	case login != nil:
		return Actor{Name: *login, Kind: ActorUser}, nil, nil
	case tokenID != nil:
		return Actor{Name: name, Kind: ActorToken}, tokenID, nil
	case kind == ActorToken:
		return Actor{}, nil, fmt.Errorf("%w: there is no active token named %q", ErrInvalid, name)
	}
	return Actor{}, nil, fmt.Errorf("%w: nobody named %q has signed in to the UI and there is no active token with that name", ErrInvalid, name)
}

// AssignIssue asigna el issue a to (nil, lo desasigna) y lo apunta en el
// historial con quién y desde dónde. Si self es true y to es una persona,
// la guarda aunque no conste que haya entrado. Si no cambia, no apunta nada.
func (s *Store) AssignIssue(ctx context.Context, id int64, to *Actor, by AssignChange) (Issue, error) {
	if !validAssignSource(by.Source) {
		return Issue{}, fmt.Errorf("%w: change source %q", ErrInvalid, by.Source)
	}
	if by.Actor != nil {
		if err := validActor(by.Actor); err != nil {
			return Issue{}, err
		}
	}
	err := db.BeginFunc(ctx, s.pool, func(tx db.Tx) error {
		var fromName, fromKind *string
		var fromToken *int64
		err := tx.QueryRow(ctx, `SELECT assignee, assignee_kind, assignee_token_id FROM issues WHERE id = $1`+tx.Dialect().ForUpdate(), id).
			Scan(&fromName, &fromKind, &fromToken)
		if errors.Is(err, db.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("read issue: %w", err)
		}
		var target *Actor
		var tokenID *int64
		if to != nil {
			if by.Actor != nil && *to == *by.Actor && to.Kind == ActorUser {
				if err := s.ensurePerson(ctx, tx, to.Name); err != nil {
					return err
				}
			}
			a, tid, err := resolveAssignee(ctx, tx, to.Name, to.Kind)
			if err != nil {
				return err
			}
			target, tokenID = &a, tid
		}
		// Un token revocado y otro nuevo con su nombre no son el mismo asignado.
		same := (fromName == nil && target == nil) ||
			(fromName != nil && target != nil && *fromName == target.Name && *fromKind == target.Kind &&
				(target.Kind != ActorToken || (fromToken != nil && tokenID != nil && *fromToken == *tokenID)))
		if same {
			return nil
		}
		var toName, toKind *string
		if target != nil {
			toName, toKind = &target.Name, &target.Kind
		}
		if _, err := tx.Exec(ctx, `
			UPDATE issues SET assignee = $2, assignee_kind = $3, assignee_token_id = $4,
				assigned_at = CASE WHEN CAST($2 AS text) IS NULL THEN NULL ELSE now() END
			WHERE id = $1`, id, toName, toKind, tokenID); err != nil {
			return fmt.Errorf("assign issue: %w", err)
		}
		var author, authorKind *string
		if by.Actor != nil {
			author, authorKind = &by.Actor.Name, &by.Actor.Kind
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO issue_assignment_changes (issue_id, author, author_kind, author_via, source, from_name, from_kind, to_name, to_kind)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			id, author, authorKind, via(by.Actor), by.Source, fromName, fromKind, toName, toKind); err != nil {
			return fmt.Errorf("record assignment: %w", err)
		}
		return nil
	})
	if err != nil {
		return Issue{}, err
	}
	return s.Issue(ctx, id)
}

func validAssignSource(s string) bool {
	return s == SourceUI || s == SourceAPI || s == SourceMCP
}

// assignmentHistory son las asignaciones del issue, de la más vieja a la más nueva.
func (s *Store) assignmentHistory(ctx context.Context, issueID int64) ([]HistoryEntry, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, created_at, author, author_kind, author_via, source, from_name, from_kind, to_name, to_kind
		FROM issue_assignment_changes WHERE issue_id = $1 ORDER BY created_at, id`, issueID)
	if err != nil {
		return nil, fmt.Errorf("read assignments: %w", err)
	}
	return db.CollectRows(rows, func(r db.CollectableRow) (HistoryEntry, error) {
		e := HistoryEntry{Type: "assignment"}
		var author, kind, authorVia, fromName, fromKind, toName, toKind *string
		err := r.Scan(&e.ID, &e.At, &author, &kind, &authorVia, &e.Source, &fromName, &fromKind, &toName, &toKind)
		e.Author = actorOf(author, kind, authorVia)
		e.Previous = actorOf(fromName, fromKind, nil)
		e.Assignee = actorOf(toName, toKind, nil)
		return e, err
	})
}

// AssigneeRef lee a quién asignar como lo dan la API y el MCP: vacío es
// desasignar y "me" es me, quien hace la petición.
func AssigneeRef(name, kind string, me *Actor) (*Actor, error) {
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		return nil, nil
	case strings.EqualFold(name, search.Me):
		if me == nil {
			return nil, fmt.Errorf("%w: unknown caller, can't assign to you", ErrInvalid)
		}
		return &Actor{Name: me.Name, Kind: me.Kind}, nil
	}
	return &Actor{Name: name, Kind: kind}, nil
}
