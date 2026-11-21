// Package alert avisa en Slack y Linear de los errores nuevos y las
// regresiones. La ingesta deja los avisos en alert_outbox, en su misma
// transacción, y un bucle los envía y reintenta.
package alert

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/intake-es/intake-errors/internal/event"
	"github.com/intake-es/intake-errors/internal/ingest"
	"github.com/intake-es/intake-errors/internal/store"
)

const (
	channelSlack  = "slack"
	channelLinear = "linear"

	MaxAttempts     = 10
	DefaultInterval = 5 * time.Second

	// SlackWaitsForLinear es lo más que espera el aviso de Slack a que el de
	// Linear del mismo error se envíe, para llevar el botón de su issue.
	SlackWaitsForLinear = 2 * time.Minute
)

// Backoff es la espera tras cada intento fallido; a partir del último, se repite.
var Backoff = []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 6 * time.Hour, 12 * time.Hour, 24 * time.Hour}

type Options struct {
	// PublicURL es la base del enlace a la interfaz: <PublicURL>/issues/<id>.
	PublicURL       string
	SlackWebhookURL string
	LinearAPIKey    string
	// LinearAPIURL es el endpoint GraphQL; vacío, el de Linear.
	LinearAPIURL   string
	LinearTeamID   string
	LinearLabelIDs []string
	Interval       time.Duration
	HTTPClient     *http.Client
}

type Alerter struct {
	st       *store.Store
	log      *slog.Logger
	interval time.Duration
	channels map[string]sender
	// order fija el orden en que se encolan los canales.
	order []string
	now   func() time.Time
}

type sender interface {
	send(ctx context.Context, tx pgx.Tx, a *pending) error
}

type pending struct {
	id          int64
	issueID     int64
	kind        string
	release     string
	environment string
	eventAt     time.Time
	createID    string
	resolvedAt  *time.Time

	project          string
	labels           []string
	title            string
	culprit          string
	count            int64
	linear           string
	linearIdentifier string
	linearURL        string
}

func New(st *store.Store, log *slog.Logger, o Options) (*Alerter, error) {
	a := &Alerter{st: st, log: log, interval: o.Interval, channels: map[string]sender{}, now: time.Now}
	if a.interval <= 0 {
		a.interval = DefaultInterval
	}
	base := strings.TrimRight(o.PublicURL, "/")
	client := o.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	if o.SlackWebhookURL != "" {
		a.channels[channelSlack] = &slack{st: st, url: o.SlackWebhookURL, client: client, base: base}
		a.order = append(a.order, channelSlack)
	}
	if o.LinearAPIKey != "" {
		if o.LinearTeamID == "" {
			return nil, errors.New("con LINEAR_API_KEY hace falta LINEAR_TEAM_ID")
		}
		endpoint := o.LinearAPIURL
		if endpoint == "" {
			endpoint = LinearEndpoint
		}
		a.channels[channelLinear] = &linear{
			st: st, base: base, endpoint: endpoint, key: o.LinearAPIKey, client: client,
			team: o.LinearTeamID, labels: o.LinearLabelIDs,
		}
		a.order = append(a.order, channelLinear)
	}
	return a, nil
}

// Channels dice qué canales están configurados, en el orden en que se encolan.
func (a *Alerter) Channels() []string { return append([]string(nil), a.order...) }

// IssueChanged encola un aviso por canal si el issue es nuevo o ha vuelto.
// Si falla, lo registra y deja seguir a la ingesta.
func (a *Alerter) IssueChanged(ctx context.Context, tx pgx.Tx, ev *event.Event, res ingest.Result) error {
	issueID := res.IssueID
	if res.Outcome == ingest.Unchanged || len(a.order) == 0 {
		return nil
	}
	sp, err := tx.Begin(ctx)
	if err != nil {
		a.log.ErrorContext(ctx, "encolar aviso", "issue", issueID, "err", err)
		return nil
	}
	ids := map[string]int64{}
	for _, ch := range a.order {
		var id int64
		err = sp.QueryRow(ctx, `
			INSERT INTO alert_outbox (issue_id, channel, kind, release, environment, event_at, resolved_at, next_attempt_at, created_at)
			VALUES ($1, $2, $3, NULLIF($4, ''), NULLIF($5, ''), $6, $7, $8, $8)
			RETURNING id`,
			issueID, ch, res.Outcome.String(), ev.Release, ev.Environment, ev.Timestamp, res.ResolvedAt, a.now().UTC()).Scan(&id)
		if err != nil {
			break
		}
		ids[ch] = id
	}
	if slackID, linearID := ids[channelSlack], ids[channelLinear]; err == nil && slackID != 0 && linearID != 0 {
		_, err = sp.Exec(ctx, `UPDATE alert_outbox SET waits_for = $2 WHERE id = $1`, slackID, linearID)
	}
	if err == nil {
		err = sp.Commit(ctx)
	}
	if err != nil {
		_ = sp.Rollback(ctx)
		a.log.ErrorContext(ctx, "encolar aviso", "issue", issueID, "err", err)
	}
	return nil
}

// Run envía lo pendiente cada Interval hasta que se cancela ctx.
func (a *Alerter) Run(ctx context.Context) {
	if len(a.order) == 0 {
		return
	}
	t := time.NewTicker(a.interval)
	defer t.Stop()
	for {
		if _, err := a.SendPending(ctx); err != nil && ctx.Err() == nil {
			a.log.ErrorContext(ctx, "enviar avisos", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// SendPending envía, uno a uno, los avisos que tocan ahora y devuelve cuántos
// ha intentado.
func (a *Alerter) SendPending(ctx context.Context) (int, error) {
	n := 0
	for ctx.Err() == nil {
		ok, err := a.sendOne(ctx)
		if err != nil || !ok {
			return n, err
		}
		n++
	}
	return n, ctx.Err()
}

func (a *Alerter) sendOne(ctx context.Context) (bool, error) {
	tx, err := a.st.Pool().Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	now := a.now().UTC()
	var p pending
	var channel string
	var attempts int
	// El primer NOT EXISTS mantiene el orden por issue y canal: una regresión
	// no se manda antes de que se mande (o se abandone) el aviso de error nuevo.
	// El segundo hace que Slack espere a Linear, como mucho SlackWaitsForLinear.
	err = tx.QueryRow(ctx, `
		SELECT o.id, o.issue_id, o.channel, o.kind, COALESCE(o.release, ''), COALESCE(o.environment, ''),
			o.event_at, o.resolved_at, o.linear_create_id::text, o.attempts
		FROM alert_outbox o
		WHERE o.sent_at IS NULL AND o.failed_at IS NULL AND o.next_attempt_at <= $1
			AND o.channel = ANY($2)
			AND NOT EXISTS (SELECT 1 FROM alert_outbox b
				WHERE b.issue_id = o.issue_id AND b.channel = o.channel AND b.id < o.id
					AND b.sent_at IS NULL AND b.failed_at IS NULL)
			AND NOT (o.created_at > $3 AND EXISTS (SELECT 1 FROM alert_outbox w
				WHERE w.id = o.waits_for AND w.sent_at IS NULL AND w.failed_at IS NULL))
		ORDER BY o.next_attempt_at, o.id
		LIMIT 1
		FOR UPDATE SKIP LOCKED`, now, a.order, now.Add(-SlackWaitsForLinear)).
		Scan(&p.id, &p.issueID, &channel, &p.kind, &p.release, &p.environment, &p.eventAt, &p.resolvedAt, &p.createID, &attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("coger aviso: %w", err)
	}
	err = tx.QueryRow(ctx, `
		SELECT p.name, p.linear_label_ids, i.title, i.culprit, i.event_count, COALESCE(i.linear_issue_id, ''),
			COALESCE(i.linear_issue_identifier, ''), COALESCE(i.linear_issue_url, '')
		FROM issues i JOIN projects p ON p.id = i.project_id
		WHERE i.id = $1`, p.issueID).
		Scan(&p.project, &p.labels, &p.title, &p.culprit, &p.count, &p.linear, &p.linearIdentifier, &p.linearURL)
	if err != nil {
		return false, fmt.Errorf("leer issue %d: %w", p.issueID, err)
	}

	sendErr := a.channels[channel].send(ctx, tx, &p)
	attempts++
	switch {
	case sendErr == nil:
		_, err = tx.Exec(ctx, `UPDATE alert_outbox SET attempts = $2, sent_at = $3, last_error = NULL WHERE id = $1`,
			p.id, attempts, a.now().UTC())
	case attempts >= MaxAttempts:
		a.log.ErrorContext(ctx, "aviso abandonado", "canal", channel, "issue", p.issueID, "intentos", attempts, "err", sendErr)
		_, err = tx.Exec(ctx, `UPDATE alert_outbox SET attempts = $2, failed_at = $3, last_error = $4 WHERE id = $1`,
			p.id, attempts, a.now().UTC(), sendErr.Error())
	default:
		next := now.Add(Backoff[min(attempts, len(Backoff))-1])
		a.log.WarnContext(ctx, "aviso fallido", "canal", channel, "issue", p.issueID, "intentos", attempts, "siguiente", next, "err", sendErr)
		_, err = tx.Exec(ctx, `UPDATE alert_outbox SET attempts = $2, next_attempt_at = $3, last_error = $4 WHERE id = $1`,
			p.id, attempts, next, sendErr.Error())
	}
	if err != nil {
		return false, fmt.Errorf("guardar aviso %d: %w", p.id, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("guardar aviso %d: %w", p.id, err)
	}
	if sendErr == nil {
		a.log.InfoContext(ctx, "aviso enviado", "canal", channel, "issue", p.issueID, "tipo", p.kind)
	}
	return true, nil
}

func issueLink(base string, id int64) string {
	if base == "" {
		return ""
	}
	return base + "/issues/" + strconv.FormatInt(id, 10)
}

// cleanErr quita la URL de los errores de net/http: la del webhook de Slack es secreta.
func cleanErr(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s: %w", ue.Op, ue.Err)
	}
	return err
}

func orNone(s, none string) string {
	if s == "" {
		return none
	}
	return s
}
