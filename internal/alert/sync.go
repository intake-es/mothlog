package alert

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/intake-es/intake-errors/internal/db"
	"github.com/intake-es/intake-errors/internal/store"
)

// SyncLinks lleva a Linear y GitHub, uno a uno, los cambios de estado
// pendientes que tocan ahora y devuelve cuántos ha intentado.
func (a *Alerter) SyncLinks(ctx context.Context) (int, error) {
	n := 0
	for ctx.Err() == nil {
		ok, err := a.syncOne(ctx)
		if err != nil || !ok {
			return n, err
		}
		n++
	}
	return n, ctx.Err()
}

func (a *Alerter) syncOne(ctx context.Context) (bool, error) {
	tx, err := a.st.Pool().Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	now := a.now().UTC()
	var id, linkID int64
	var status string
	var attempts int
	// Por enlace, en orden: el último cambio es el que queda.
	err = tx.QueryRow(ctx, `
		SELECT s.id, s.link_id, s.status, s.attempts FROM issue_link_sync s
		WHERE s.sent_at IS NULL AND s.failed_at IS NULL AND s.next_attempt_at <= $1
			AND NOT EXISTS (SELECT 1 FROM issue_link_sync b WHERE b.link_id = s.link_id AND b.id < s.id
				AND b.sent_at IS NULL AND b.failed_at IS NULL)
		ORDER BY s.next_attempt_at, s.id
		LIMIT 1`+tx.Dialect().ForUpdateSkipLocked(), now).Scan(&id, &linkID, &status, &attempts)
	if errors.Is(err, db.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("coger cambio para enlace: %w", err)
	}
	link, err := a.st.Link(ctx, linkID)
	if err != nil {
		return false, err
	}
	state, name, moved, sendErr := a.pushState(ctx, tx, link, store.StatusCategory(status))
	attempts++
	switch {
	case sendErr == nil:
		// Si allí no ha cambiado nada, no llegará ningún webhook de vuelta.
		if _, err := tx.Exec(ctx, `UPDATE issue_link_sync SET attempts = $2, sent_at = $3::timestamptz, last_error = NULL,
			echoed_at = CASE WHEN CAST($4 AS boolean) THEN NULL ELSE $3::timestamptz END WHERE id = $1`,
			id, attempts, now, moved); err != nil {
			return false, err
		}
		if _, err := tx.Exec(ctx, `UPDATE issue_links SET state = $2, state_name = COALESCE(NULLIF($3, ''), state_name), updated_at = now()
			WHERE id = $1`, linkID, state, name); err != nil {
			return false, err
		}
	case attempts >= MaxAttempts || errors.Is(sendErr, errGone) || errors.Is(sendErr, errLinearNotFound) || errors.Is(sendErr, errGitHubNotFound):
		a.log.ErrorContext(ctx, "cambio de estado abandonado", "enlace", link.Identifier, "issue", link.IssueID, "err", sendErr)
		if _, err := tx.Exec(ctx, `UPDATE issue_link_sync SET attempts = $2, failed_at = $3, last_error = $4 WHERE id = $1`,
			id, attempts, now, sendErr.Error()); err != nil {
			return false, err
		}
	default:
		next := now.Add(Backoff[min(attempts, len(Backoff))-1])
		a.log.WarnContext(ctx, "cambio de estado fallido", "enlace", link.Identifier, "issue", link.IssueID, "siguiente", next, "err", sendErr)
		if _, err := tx.Exec(ctx, `UPDATE issue_link_sync SET attempts = $2, next_attempt_at = $3, last_error = $4 WHERE id = $1`,
			id, attempts, next, sendErr.Error()); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	if sendErr == nil {
		a.log.InfoContext(ctx, "estado llevado al enlace", "enlace", link.Identifier, "issue", link.IssueID, "estado", state)
	}
	return true, nil
}

// pushState deja la issue de allí en la categoría, si no lo está ya, y
// devuelve su estado, el nombre que tiene allí y si la ha movido.
func (a *Alerter) pushState(ctx context.Context, tx db.Tx, link store.IssueLink, category string) (string, string, bool, error) {
	switch link.Provider {
	case store.ProviderLinear:
		if a.linearKey == "" {
			return "", "", false, fmt.Errorf("%w: falta LINEAR_API_KEY", errGone)
		}
		l := &linear{st: a.st, endpoint: a.linearEndpoint, key: a.linearKey, client: a.client}
		st, moved, err := l.moveTo(ctx, link.ExternalID, category)
		if err != nil {
			return "", "", false, err
		}
		return store.LinearStateCategory(st.Type), st.Name, moved, nil
	case store.ProviderGitHub:
		api, err := a.githubFor(ctx, tx, link)
		if err != nil {
			return "", "", false, err
		}
		is, err := api.issue(ctx, link.ExternalID)
		if err != nil {
			return "", "", false, err
		}
		moved := is.linkState() != category
		if moved {
			if is, err = api.setState(ctx, link.ExternalID, category); err != nil {
				return "", "", false, err
			}
		}
		return is.linkState(), is.State, moved, nil
	}
	return "", "", false, fmt.Errorf("%w: %q", errGone, link.Provider)
}

// githubFor es la API de GitHub con el token de la acción que creó la issue
// o, si no tiene, GITHUB_TOKEN.
func (a *Alerter) githubFor(ctx context.Context, tx db.Tx, link store.IssueLink) (*githubAPI, error) {
	token := a.githubToken
	if link.RuleID != nil {
		var actions []store.RuleAction
		err := tx.QueryRow(ctx, `SELECT actions FROM alert_rules WHERE id = $1`, *link.RuleID).Scan(&actions)
		if err != nil && !errors.Is(err, db.ErrNoRows) {
			return nil, err
		}
		if i := slices.IndexFunc(actions, func(x store.RuleAction) bool { return x.ID == link.ActionID }); i >= 0 && actions[i].Token != "" {
			token = actions[i].Token
		}
	}
	if token == "" {
		return nil, fmt.Errorf("%w: falta GITHUB_TOKEN", errGone)
	}
	return &githubAPI{endpoint: a.githubEndpoint, token: token, client: a.client}, nil
}
