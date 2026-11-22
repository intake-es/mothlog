package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/intake-es/intake-errors/internal/search"
)

// Person es alguien que ha entrado en la interfaz.
type Person struct {
	Login       string    `json:"login"`
	Name        string    `json:"name"`
	AvatarURL   *string   `json:"avatar_url"`
	LastLoginAt time.Time `json:"last_login_at"`
}

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

func issueWithAssigneeSQL(envArg string) (cols, from string) {
	cols, from = issueSQL(envArg)
	return cols + assigneeCols, from + assigneeJoins
}

func scanIssueWithAssignee(r pgx.Row) (Issue, error) {
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

// SavePerson guarda o pone al día a quien entra en la interfaz.
func (s *Store) SavePerson(ctx context.Context, p Person) error {
	p.Login = strings.TrimSpace(p.Login)
	if p.Login == "" {
		return fmt.Errorf("%w: la persona necesita un login", ErrInvalid)
	}
	if strings.TrimSpace(p.Name) == "" {
		p.Name = p.Login
	}
	if p.AvatarURL != nil && *p.AvatarURL == "" {
		p.AvatarURL = nil
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO people (login, name, avatar_url) VALUES ($1, $2, $3)
		ON CONFLICT (login) DO UPDATE SET name = EXCLUDED.name, avatar_url = EXCLUDED.avatar_url, last_login_at = now()`,
		p.Login, p.Name, p.AvatarURL)
	if err != nil {
		return fmt.Errorf("guardar persona: %w", err)
	}
	return nil
}

// ensurePerson guarda a alguien con sesión que entró antes de que se
// guardaran las personas, sin tocar a quien ya está.
func (s *Store) ensurePerson(ctx context.Context, q Querier, login string) error {
	_, err := q.Exec(ctx, `INSERT INTO people (login, name) VALUES ($1, $1) ON CONFLICT (login) DO NOTHING`, login)
	if err != nil {
		return fmt.Errorf("guardar persona: %w", err)
	}
	return nil
}

// People son las personas que han entrado, por nombre.
func (s *Store) People(ctx context.Context) ([]Person, error) {
	rows, err := s.pool.Query(ctx, `SELECT login, name, avatar_url, last_login_at FROM people ORDER BY lower(name), login`)
	if err != nil {
		return nil, fmt.Errorf("listar personas: %w", err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Person, error) {
		var p Person
		return p, r.Scan(&p.Login, &p.Name, &p.AvatarURL, &p.LastLoginAt)
	})
}

// Assignees son a quienes se puede asignar un issue: las personas y los
// tokens activos, por ese orden.
func (s *Store) Assignees(ctx context.Context) ([]Assignee, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT login, 'user', CASE WHEN name <> login THEN name ELSE '' END, avatar_url FROM people
		UNION ALL
		SELECT name, 'token', '', NULL FROM api_tokens WHERE revoked_at IS NULL
		ORDER BY 2 DESC, 1`)
	if err != nil {
		return nil, fmt.Errorf("listar asignables: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Assignee, error) {
		a := Assignee{Active: true}
		return a, r.Scan(&a.Name, &a.Kind, &a.DisplayName, &a.AvatarURL)
	})
	if err != nil {
		return nil, fmt.Errorf("listar asignables: %w", err)
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
		return Actor{}, nil, fmt.Errorf("%w: falta a quién asignar", ErrInvalid)
	}
	if kind != "" && kind != ActorUser && kind != ActorToken {
		return Actor{}, nil, fmt.Errorf("%w: kind es user o token", ErrInvalid)
	}
	var login *string
	if kind != ActorToken {
		err := q.QueryRow(ctx, `SELECT login FROM people WHERE lower(login) = lower($1)`, name).Scan(&login)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return Actor{}, nil, fmt.Errorf("buscar persona: %w", err)
		}
	}
	var tokenID *int64
	if kind != ActorUser {
		err := q.QueryRow(ctx, `SELECT id FROM api_tokens WHERE name = $1 AND revoked_at IS NULL`, name).Scan(&tokenID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return Actor{}, nil, fmt.Errorf("buscar token: %w", err)
		}
	}
	switch {
	case login != nil && tokenID != nil:
		return Actor{}, nil, fmt.Errorf("%w: %q es una persona y un token; di cuál con kind", ErrInvalid, name)
	case login != nil:
		return Actor{Name: *login, Kind: ActorUser}, nil, nil
	case tokenID != nil:
		return Actor{Name: name, Kind: ActorToken}, tokenID, nil
	case kind == ActorToken:
		return Actor{}, nil, fmt.Errorf("%w: no hay ningún token activo llamado %q", ErrInvalid, name)
	}
	return Actor{}, nil, fmt.Errorf("%w: nadie llamado %q ha entrado en la interfaz ni hay un token activo con ese nombre", ErrInvalid, name)
}

// AssignIssue asigna el issue a to (nil, lo desasigna) y lo apunta en el
// historial con quién y desde dónde. Si self es true y to es una persona,
// la guarda aunque no conste que haya entrado. Si no cambia, no apunta nada.
func (s *Store) AssignIssue(ctx context.Context, id int64, to *Actor, by AssignChange) (Issue, error) {
	if !validAssignSource(by.Source) {
		return Issue{}, fmt.Errorf("%w: origen del cambio %q", ErrInvalid, by.Source)
	}
	if by.Actor != nil {
		if err := validActor(by.Actor); err != nil {
			return Issue{}, err
		}
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var fromName, fromKind *string
		var fromToken *int64
		err := tx.QueryRow(ctx, `SELECT assignee, assignee_kind, assignee_token_id FROM issues WHERE id = $1 FOR UPDATE`, id).
			Scan(&fromName, &fromKind, &fromToken)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("leer issue: %w", err)
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
				assigned_at = CASE WHEN $2::text IS NULL THEN NULL ELSE now() END
			WHERE id = $1`, id, toName, toKind, tokenID); err != nil {
			return fmt.Errorf("asignar issue: %w", err)
		}
		var author, authorKind *string
		if by.Actor != nil {
			author, authorKind = &by.Actor.Name, &by.Actor.Kind
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO issue_assignment_changes (issue_id, author, author_kind, source, from_name, from_kind, to_name, to_kind)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			id, author, authorKind, by.Source, fromName, fromKind, toName, toKind); err != nil {
			return fmt.Errorf("apuntar asignación: %w", err)
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
		SELECT id, created_at, author, author_kind, source, from_name, from_kind, to_name, to_kind
		FROM issue_assignment_changes WHERE issue_id = $1 ORDER BY created_at, id`, issueID)
	if err != nil {
		return nil, fmt.Errorf("leer asignaciones: %w", err)
	}
	actor := func(name, kind *string) *Actor {
		if name == nil || kind == nil {
			return nil
		}
		return &Actor{Name: *name, Kind: *kind}
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (HistoryEntry, error) {
		e := HistoryEntry{Type: "assignment"}
		var author, kind, fromName, fromKind, toName, toKind *string
		err := r.Scan(&e.ID, &e.At, &author, &kind, &e.Source, &fromName, &fromKind, &toName, &toKind)
		e.Author = actor(author, kind)
		e.Previous = actor(fromName, fromKind)
		e.Assignee = actor(toName, toKind)
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
			return nil, fmt.Errorf("%w: no se sabe quién eres para asignarte", ErrInvalid)
		}
		a := *me
		return &a, nil
	}
	return &Actor{Name: name, Kind: kind}, nil
}
