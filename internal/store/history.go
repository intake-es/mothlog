package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// ErrNotOwner: el comentario es de otro.
var ErrNotOwner = errors.New("solo puedes borrar tus comentarios")

// MaxCommentLen es lo más largo que puede ser un comentario, en caracteres.
const MaxCommentLen = 20000

// Tipos de autor: una persona con sesión en la interfaz o un token de la API o del MCP.
const (
	ActorUser  = "user"
	ActorToken = "token"
)

// Orígenes de un cambio de estado.
const (
	SourceUI     = "ui"
	SourceAPI    = "api"
	SourceMCP    = "mcp"
	SourceIngest = "ingest"
	SourceDeploy = "deploy"
	SourceLinear = ProviderLinear
	SourceGitHub = ProviderGitHub
)

// Actor es quien comenta o cambia el estado.
type Actor struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
}

type Comment struct {
	ID        int64     `json:"id"`
	IssueID   int64     `json:"issue_id"`
	Author    Actor     `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
	// Mine dice si el autor es quien pregunta; solo puede borrar los suyos.
	Mine bool `json:"mine"`
}

// HistoryEntry es un comentario (Type "comment"), un cambio de estado
// ("status") o una asignación ("assignment") del historial de un issue.
type HistoryEntry struct {
	Type string    `json:"type"`
	ID   int64     `json:"id"`
	At   time.Time `json:"at"`
	// Author va vacío en los cambios de la ingesta y de los despliegues.
	Author *Actor `json:"author"`
	// Body y Mine, en los comentarios.
	Body string `json:"body,omitempty"`
	Mine bool   `json:"mine,omitempty"`
	// Source, From, To, Release y Comment, en los cambios de estado.
	Source  string   `json:"source,omitempty"`
	From    string   `json:"from,omitempty"`
	To      string   `json:"to,omitempty"`
	Release *string  `json:"release,omitempty"`
	Comment *Comment `json:"comment,omitempty"`
	// Assignee y Previous, en las asignaciones: a quién se asigna (nil,
	// desasignar) y quién lo llevaba.
	Assignee *Actor `json:"assignee,omitempty"`
	Previous *Actor `json:"previous,omitempty"`
}

// StatusChange es quién pide un cambio de estado, desde dónde y con qué comentario.
type StatusChange struct {
	Actor   *Actor
	Source  string
	Comment string
}

func validComment(body string) (string, error) {
	body = strings.TrimSpace(body)
	switch {
	case body == "":
		return "", fmt.Errorf("%w: el comentario está vacío", ErrInvalid)
	case utf8.RuneCountInString(body) > MaxCommentLen:
		return "", fmt.Errorf("%w: el comentario pasa de %d caracteres", ErrInvalid, MaxCommentLen)
	}
	return body, nil
}

func validActor(a *Actor) error {
	if a == nil || strings.TrimSpace(a.Name) == "" || (a.Kind != ActorUser && a.Kind != ActorToken) {
		return fmt.Errorf("%w: falta el autor", ErrInvalid)
	}
	return nil
}

func insertComment(ctx context.Context, q Querier, issueID int64, a Actor, body string) (Comment, error) {
	c := Comment{IssueID: issueID, Author: a, Body: body, Mine: true}
	err := q.QueryRow(ctx, `
		INSERT INTO issue_comments (issue_id, author, author_kind, body) VALUES ($1, $2, $3, $4)
		RETURNING id, created_at`, issueID, a.Name, a.Kind, body).Scan(&c.ID, &c.CreatedAt)
	if isForeignKeyViolation(err) {
		return Comment{}, ErrNotFound
	}
	if err != nil {
		return Comment{}, fmt.Errorf("guardar comentario: %w", err)
	}
	return c, nil
}

// AddComment guarda un comentario en Markdown de a en el issue.
func (s *Store) AddComment(ctx context.Context, issueID int64, a Actor, body string) (Comment, error) {
	if err := validActor(&a); err != nil {
		return Comment{}, err
	}
	body, err := validComment(body)
	if err != nil {
		return Comment{}, err
	}
	return insertComment(ctx, s.pool, issueID, a, body)
}

// DeleteComment borra un comentario del issue si es de a.
func (s *Store) DeleteComment(ctx context.Context, issueID, commentID int64, a Actor) error {
	var author, kind string
	err := s.pool.QueryRow(ctx, `SELECT author, author_kind FROM issue_comments WHERE id = $1 AND issue_id = $2`,
		commentID, issueID).Scan(&author, &kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("leer comentario: %w", err)
	}
	if author != a.Name || kind != a.Kind {
		return ErrNotOwner
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM issue_comments WHERE id = $1`, commentID); err != nil {
		return fmt.Errorf("borrar comentario: %w", err)
	}
	return nil
}

// History es el historial del issue, del más viejo al más nuevo: los
// comentarios y los cambios de estado, con el comentario que se dejó con cada
// cambio dentro de él. Mine marca lo que es de viewer (nil, nada).
func (s *Store) History(ctx context.Context, issueID int64, viewer *Actor) ([]HistoryEntry, error) {
	if _, err := s.Issue(ctx, issueID); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT 'status', c.id, c.created_at, c.author, c.author_kind, '', c.source, c.from_status, c.to_status, c.release,
			m.id, m.author, m.author_kind, m.body, m.created_at
		FROM issue_status_changes c LEFT JOIN issue_comments m ON m.id = c.comment_id
		WHERE c.issue_id = $1
		UNION ALL
		SELECT 'comment', m.id, m.created_at, m.author, m.author_kind, m.body, '', '', '', NULL,
			NULL, NULL, NULL, NULL, NULL
		FROM issue_comments m
		WHERE m.issue_id = $1 AND NOT EXISTS (SELECT 1 FROM issue_status_changes c WHERE c.comment_id = m.id)
		ORDER BY 3, 1 DESC, 2`, issueID)
	if err != nil {
		return nil, fmt.Errorf("leer historial: %w", err)
	}
	mine := func(a *Actor) bool { return viewer != nil && a != nil && *a == *viewer }
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (HistoryEntry, error) {
		var e HistoryEntry
		var author, kind *string
		var cID *int64
		var cAuthor, cKind, cBody *string
		var cAt *time.Time
		err := r.Scan(&e.Type, &e.ID, &e.At, &author, &kind, &e.Body, &e.Source, &e.From, &e.To, &e.Release,
			&cID, &cAuthor, &cKind, &cBody, &cAt)
		if err != nil {
			return e, err
		}
		if author != nil && kind != nil {
			e.Author = &Actor{Name: *author, Kind: *kind}
		}
		if e.Type == "comment" {
			e.Mine = mine(e.Author)
		}
		if cID != nil {
			a := Actor{Name: *cAuthor, Kind: *cKind}
			e.Comment = &Comment{ID: *cID, IssueID: issueID, Author: a, Body: *cBody, CreatedAt: *cAt, Mine: mine(&a)}
		}
		return e, nil
	})
	if err != nil {
		return nil, fmt.Errorf("leer historial: %w", err)
	}
	assigned, err := s.assignmentHistory(ctx, issueID)
	if err != nil {
		return nil, err
	}
	out = append(out, assigned...)
	slices.SortStableFunc(out, func(a, b HistoryEntry) int { return a.At.Compare(b.At) })
	if out == nil {
		out = []HistoryEntry{}
	}
	return out, nil
}

// recordStatusChange apunta un cambio de estado en el historial.
func recordStatusChange(ctx context.Context, q Querier, issueID int64, a *Actor, source, from, to string, release *string, commentID *int64) error {
	var name, kind *string
	if a != nil {
		name, kind = &a.Name, &a.Kind
	}
	_, err := q.Exec(ctx, `
		INSERT INTO issue_status_changes (issue_id, author, author_kind, source, from_status, to_status, release, comment_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, issueID, name, kind, source, from, to, release, commentID)
	if err != nil {
		return fmt.Errorf("apuntar cambio de estado: %w", err)
	}
	return nil
}

// RecordRegression apunta en el historial que un evento de release reabrió el
// issue, que estaba en from. Lo llama la ingesta dentro de su transacción.
func RecordRegression(ctx context.Context, q Querier, issueID int64, from, release string) error {
	var rel *string
	if release != "" {
		rel = &release
	}
	if err := enqueueSync(ctx, q, issueID, "open", SourceIngest); err != nil {
		return err
	}
	return recordStatusChange(ctx, q, issueID, nil, SourceIngest, from, "open", rel, nil)
}

var changeSources = []string{SourceUI, SourceAPI, SourceMCP}

// ChangeIssueStatus cambia el estado a mano y lo apunta en el historial con
// quién, desde dónde y, si lo trae, el comentario. Con resolved_next guarda
// la release del último evento, que la ingesta compara para decidir si hay
// regresión. resolved_at guarda desde cuándo está resuelto; en otro estado
// queda vacío. Si el estado y la release no cambian, no apunta nada, y el
// comentario queda como uno suelto.
func (s *Store) ChangeIssueStatus(ctx context.Context, id int64, status string, by StatusChange) (Issue, error) {
	if !slices.Contains(Statuses, status) {
		return Issue{}, fmt.Errorf("%w: status es open, resolved, resolved_next o silenced", ErrInvalid)
	}
	if !slices.Contains(changeSources, by.Source) {
		return Issue{}, fmt.Errorf("%w: origen del cambio %q", ErrInvalid, by.Source)
	}
	comment := ""
	if strings.TrimSpace(by.Comment) != "" {
		if err := validActor(by.Actor); err != nil {
			return Issue{}, err
		}
		var err error
		if comment, err = validComment(by.Comment); err != nil {
			return Issue{}, err
		}
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := changeStatusTx(ctx, tx, id, status, by, comment, false, by.Source)
		return err
	})
	if err != nil {
		return Issue{}, err
	}
	return s.Issue(ctx, id)
}

// changeStatusTx es ChangeIssueStatus dentro de tx. Con fallbackResolved,
// resolved_next sin release en el último evento se queda en resolved. Si el
// estado cambia de categoría, lo deja para enviar a los enlaces del issue,
// salvo a los del sitio skip.
func changeStatusTx(ctx context.Context, tx pgx.Tx, id int64, status string, by StatusChange, comment string,
	fallbackResolved bool, skip string) (bool, error) {
	var from string
	var prevRelease *string
	err := tx.QueryRow(ctx, `SELECT status, resolved_in_release FROM issues WHERE id = $1 FOR UPDATE`, id).Scan(&from, &prevRelease)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, fmt.Errorf("leer issue: %w", err)
	}
	var release *string
	if status == "resolved_next" {
		err := tx.QueryRow(ctx, `SELECT NULLIF(release, '') FROM events
			WHERE issue_id = $1 ORDER BY occurred_at DESC, id DESC LIMIT 1`, id).Scan(&release)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return false, fmt.Errorf("release del último evento: %w", err)
		}
		if release == nil {
			if !fallbackResolved {
				return false, ErrNoRelease
			}
			status = "resolved"
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE issues SET status = $2, resolved_in_release = $3, fixed_in_release = NULL,
			resolved_at = CASE
				WHEN $2 NOT IN ('resolved', 'resolved_next') THEN NULL
				WHEN status IN ('resolved', 'resolved_next') THEN COALESCE(resolved_at, now())
				ELSE now() END
		WHERE id = $1`, id, status, release); err != nil {
		return false, fmt.Errorf("cambiar estado: %w", err)
	}
	changed := from != status || !equalPtr(prevRelease, release)
	var commentID *int64
	if comment != "" {
		c, err := insertComment(ctx, tx, id, *by.Actor, comment)
		if err != nil {
			return false, err
		}
		commentID = &c.ID
	}
	if !changed {
		return false, nil
	}
	if StatusCategory(from) != StatusCategory(status) {
		if err := enqueueSync(ctx, tx, id, status, skip); err != nil {
			return false, err
		}
	}
	return true, recordStatusChange(ctx, tx, id, by.Actor, by.Source, from, status, release, commentID)
}

// SetIssueStatus es ChangeIssueStatus sin autor, como desde la API.
func (s *Store) SetIssueStatus(ctx context.Context, id int64, status string) (Issue, error) {
	return s.ChangeIssueStatus(ctx, id, status, StatusChange{Source: SourceAPI})
}

func equalPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
