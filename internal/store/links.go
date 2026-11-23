package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/intake-es/intake-errors/internal/db"
)

// Sitios a los que se enlaza un issue y de los que llegan cambios.
const (
	ProviderLinear = "linear"
	ProviderGitHub = "github"

	LinkIssue       = "issue"
	LinkPullRequest = "pull_request"
)

// Estados de un enlace; los tres primeros son también las categorías de los
// estados de un issue (StatusCategory).
const (
	LinkOpen     = "open"
	LinkClosed   = "closed"
	LinkCanceled = "canceled"
	LinkMerged   = "merged"
)

// IssueLink es una issue de Linear o una issue o un PR de GitHub enlazados a un issue.
type IssueLink struct {
	ID       int64  `json:"id"`
	IssueID  int64  `json:"issue_id"`
	Provider string `json:"provider"`
	Kind     string `json:"kind"`
	// ExternalID es el id de Linear o dueño/repo#número en GitHub.
	ExternalID string `json:"external_id"`
	Identifier string `json:"identifier"`
	URL        string `json:"url"`
	// State es open, closed, canceled o merged; vacío, aún no se sabe.
	State     string    `json:"state"`
	StateName string    `json:"state_name"`
	RuleID    *int64    `json:"-"`
	ActionID  string    `json:"-"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// StatusCategory agrupa los estados de un issue como los de Linear y GitHub:
// abierto, cerrado (resuelto, ahora o en la siguiente) o cancelado (silenciado).
func StatusCategory(status string) string {
	switch status {
	case "resolved", "resolved_next":
		return LinkClosed
	case "silenced":
		return LinkCanceled
	}
	return LinkOpen
}

// LinearStateCategory traduce el tipo de un estado de Linear al de un enlace.
func LinearStateCategory(typ string) string {
	switch typ {
	case "completed":
		return LinkClosed
	case "canceled", "duplicate":
		return LinkCanceled
	}
	return LinkOpen
}

const linkColumns = `id, issue_id, provider, kind, external_id, COALESCE(identifier, ''), COALESCE(url, ''),
	COALESCE(state, ''), COALESCE(state_name, ''), rule_id, COALESCE(action_id, ''), created_at, updated_at`

func scanLink(r db.Row) (IssueLink, error) {
	var l IssueLink
	err := r.Scan(&l.ID, &l.IssueID, &l.Provider, &l.Kind, &l.ExternalID, &l.Identifier, &l.URL,
		&l.State, &l.StateName, &l.RuleID, &l.ActionID, &l.CreatedAt, &l.UpdatedAt)
	return l, err
}

func collectLinks(rows db.Rows, err error) ([]IssueLink, error) {
	if err != nil {
		return nil, fmt.Errorf("leer enlaces: %w", err)
	}
	out, err := db.CollectRows(rows, func(r db.CollectableRow) (IssueLink, error) { return scanLink(r) })
	if err != nil {
		return nil, fmt.Errorf("leer enlaces: %w", err)
	}
	if out == nil {
		out = []IssueLink{}
	}
	return out, nil
}

// IssueLinks son los enlaces del issue, del más viejo al más nuevo.
func (s *Store) IssueLinks(ctx context.Context, issueID int64) ([]IssueLink, error) {
	return IssueLinksOf(ctx, s.pool, issueID)
}

func IssueLinksOf(ctx context.Context, q Querier, issueID int64) ([]IssueLink, error) {
	rows, err := q.Query(ctx, `SELECT `+linkColumns+` FROM issue_links WHERE issue_id = $1 ORDER BY id`, issueID)
	return collectLinks(rows, err)
}

// LinksTo son los enlaces, de cualquier issue, a la issue o el PR externalID.
func (s *Store) LinksTo(ctx context.Context, provider, kind, externalID string) ([]IssueLink, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+linkColumns+` FROM issue_links
		WHERE provider = $1 AND kind = $2 AND external_id = $3 ORDER BY id`, provider, kind, externalID)
	return collectLinks(rows, err)
}

// Link es un enlace por su id.
func (s *Store) Link(ctx context.Context, id int64) (IssueLink, error) {
	l, err := scanLink(s.pool.QueryRow(ctx, `SELECT `+linkColumns+` FROM issue_links WHERE id = $1`, id))
	if errors.Is(err, db.ErrNoRows) {
		return l, ErrNotFound
	}
	return l, err
}

// SaveLink guarda el enlace o, si ya estaba, le pone el identificador, la
// URL y el estado que traiga. Con only, quita los demás del issue del mismo
// sitio y tipo: así guarda la ingesta la issue de Linear de cada error.
func SaveLink(ctx context.Context, q Querier, l IssueLink, only bool) (IssueLink, error) {
	if only {
		if _, err := q.Exec(ctx, `DELETE FROM issue_links WHERE issue_id = $1 AND provider = $2 AND kind = $3 AND external_id <> $4`,
			l.IssueID, l.Provider, l.Kind, l.ExternalID); err != nil {
			return l, fmt.Errorf("guardar enlace: %w", err)
		}
	}
	out, err := scanLink(q.QueryRow(ctx, `
		INSERT INTO issue_links (issue_id, provider, kind, external_id, identifier, url, state, state_name, rule_id, action_id)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''), NULLIF($6, ''), NULLIF($7, ''), NULLIF($8, ''), $9, NULLIF($10, ''))
		ON CONFLICT (issue_id, provider, kind, external_id) DO UPDATE SET
			identifier = COALESCE(EXCLUDED.identifier, issue_links.identifier),
			url = COALESCE(EXCLUDED.url, issue_links.url),
			state = COALESCE(EXCLUDED.state, issue_links.state),
			state_name = COALESCE(EXCLUDED.state_name, issue_links.state_name),
			rule_id = COALESCE(issue_links.rule_id, EXCLUDED.rule_id),
			action_id = COALESCE(issue_links.action_id, EXCLUDED.action_id),
			updated_at = now()
		RETURNING `+linkColumns,
		l.IssueID, l.Provider, l.Kind, l.ExternalID, l.Identifier, l.URL, l.State, l.StateName, l.RuleID, l.ActionID))
	if db.IsForeignKeyViolation(err) {
		return l, ErrNotFound
	}
	if err != nil {
		return l, fmt.Errorf("guardar enlace: %w", err)
	}
	return out, nil
}

// SetLinkState apunta el estado que tiene el enlace allí.
func (s *Store) SetLinkState(ctx context.Context, id int64, state, name string) error {
	_, err := s.pool.Exec(ctx, `UPDATE issue_links SET state = NULLIF($2, ''), state_name = COALESCE(NULLIF($3, ''), state_name),
		updated_at = now() WHERE id = $1`, id, state, name)
	if err != nil {
		return fmt.Errorf("estado del enlace: %w", err)
	}
	return nil
}

// enqueueSync deja para enviar el estado nuevo del issue a sus issues de
// Linear y GitHub, salvo a las del sitio de donde viene el cambio.
func enqueueSync(ctx context.Context, q Querier, issueID int64, status, source string) error {
	_, err := q.Exec(ctx, `
		INSERT INTO issue_link_sync (link_id, status)
		SELECT id, $2 FROM issue_links WHERE issue_id = $1 AND kind = 'issue' AND provider <> $3`,
		issueID, status, source)
	if err != nil {
		return fmt.Errorf("encolar el cambio a los enlaces: %w", err)
	}
	return nil
}

// ExternalChange es un cambio de estado que llega de Linear o de GitHub.
type ExternalChange struct {
	// Source es linear o github.
	Source string
	// Actor es quién lo hizo allí; nil si no se sabe.
	Actor *Actor
	// PullRequest: lo cambia un PR mergeado. Se manda también a las issues de
	// GitHub del error, que no son el PR y no lo devuelven.
	PullRequest bool
	// LinkID es el enlace cuyo estado ha cambiado allí; 0 en los PRs.
	LinkID int64
}

// EchoWindow es cuánto se espera el webhook que provoca un envío de la cola;
// pasado, un cambio allí al mismo estado ya no se toma por su eco.
const EchoWindow = 10 * time.Minute

// consumeEcho dice si el cambio a status que llega del enlace es el eco de
// un envío nuestro que aún no ha vuelto, y lo da por vuelto. Un eco no se
// aplica: aquí puede haber ya un cambio más nuevo (resolver y reabrir antes de
// que vuelva el «closed»), o llegar desordenado.
func consumeEcho(ctx context.Context, tx db.Tx, linkID int64, status string) (bool, error) {
	if linkID == 0 {
		return false, nil
	}
	var same []string
	for _, st := range Statuses {
		if StatusCategory(st) == StatusCategory(status) {
			same = append(same, st)
		}
	}
	tag, err := tx.Exec(ctx, `
		UPDATE issue_link_sync SET echoed_at = now()
		WHERE id = (SELECT id FROM issue_link_sync
			WHERE link_id = $1 AND sent_at IS NOT NULL AND echoed_at IS NULL AND sent_at > `+tx.Dialect().SubSeconds("now()", "$3")+`
				AND `+tx.Dialect().InList("status", "$2")+`
			ORDER BY id LIMIT 1)`, linkID, same, EchoWindow.Seconds())
	if err != nil {
		return false, fmt.Errorf("buscar el eco: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// ApplyExternalStatus pone el issue en status si su categoría es otra (un
// issue en resolved_next ya está cerrado como uno en resolved) y lo apunta en
// el historial con el origen y el autor de allí. Devuelve si lo ha cambiado.
// resolved_next sin release en el último evento se queda en resolved.
func (s *Store) ApplyExternalStatus(ctx context.Context, id int64, status string, by ExternalChange) (bool, error) {
	if by.Source != ProviderLinear && by.Source != ProviderGitHub {
		return false, fmt.Errorf("%w: origen del cambio %q", ErrInvalid, by.Source)
	}
	changed := false
	err := db.BeginFunc(ctx, s.pool, func(tx db.Tx) error {
		var from string
		err := tx.QueryRow(ctx, `SELECT status FROM issues WHERE id = $1`+tx.Dialect().ForUpdate(), id).Scan(&from)
		if errors.Is(err, db.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("leer issue: %w", err)
		}
		if echo, err := consumeEcho(ctx, tx, by.LinkID, status); err != nil || echo {
			return err
		}
		if StatusCategory(from) == StatusCategory(status) {
			return nil
		}
		skip := by.Source
		if by.PullRequest {
			skip = ""
		}
		changed, err = changeStatusTx(ctx, tx, id, status, StatusChange{Actor: by.Actor, Source: by.Source}, "", true, skip)
		return err
	})
	return changed, err
}

// WebhookDeliveryRetention es lo que se guardan las entregas de los webhooks:
// una más vieja ya no se reconocería como repetida.
const WebhookDeliveryRetention = 30 * 24 * time.Hour

// bodyKey es la clave de una entrega por su cuerpo, que va firmado; la
// cabecera con el id de la entrega no lo va.
func bodyKey(body []byte) string {
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// RecordWebhookDelivery apunta una entrega de un webhook por su id, si lo
// trae, y por el sha256 de su cuerpo, y dice si es nueva: si ya se vio
// cualquiera de las dos, es repetida. Guarda las de los últimos 30 días.
func (s *Store) RecordWebhookDelivery(ctx context.Context, provider, deliveryID string, body []byte, event string) (bool, error) {
	fresh := true
	err := db.BeginFunc(ctx, s.pool, func(tx db.Tx) error {
		keys := []string{bodyKey(body)}
		if deliveryID != "" {
			keys = append(keys, deliveryID)
		}
		for _, key := range keys {
			tag, err := tx.Exec(ctx, `INSERT INTO webhook_deliveries (provider, delivery_id, event) VALUES ($1, $2, $3)
				ON CONFLICT DO NOTHING`, provider, key, event)
			if err != nil {
				return err
			}
			fresh = fresh && tag.RowsAffected() == 1
		}
		_, err := tx.Exec(ctx, `DELETE FROM webhook_deliveries WHERE provider = $1 AND received_at < `+
			tx.Dialect().SubSeconds("now()", "$2"), provider, int64(WebhookDeliveryRetention.Seconds()))
		return err
	})
	if err != nil {
		return false, fmt.Errorf("apuntar entrega: %w", err)
	}
	return fresh, nil
}

// ForgetWebhookDelivery borra una entrega que no se pudo aplicar, por su id y por su cuerpo.
func (s *Store) ForgetWebhookDelivery(ctx context.Context, provider, deliveryID string, body []byte) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM webhook_deliveries WHERE provider = $1 AND `+
		s.pool.Dialect().InList("delivery_id", "$2"), provider, []string{deliveryID, bodyKey(body)})
	return err
}

// LastWebhookDelivery es cuándo llegó la última entrega válida del sitio; nil, ninguna.
func (s *Store) LastWebhookDelivery(ctx context.Context, provider string) (*time.Time, error) {
	var at *time.Time
	if err := s.pool.QueryRow(ctx, `SELECT max(received_at) FROM webhook_deliveries WHERE provider = $1`, provider).Scan(&at); err != nil {
		return nil, fmt.Errorf("última entrega: %w", err)
	}
	return at, nil
}
