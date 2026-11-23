// Package alert avisa de los errores. Sin reglas, un proyecto avisa en Slack y
// Linear de los errores nuevos y las regresiones; con reglas, de lo que digan y
// a donde digan. La ingesta deja los avisos en alert_outbox, en su misma
// transacción, y un bucle los envía y reintenta.
package alert

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/intake-es/intake-errors/internal/db"
	"github.com/intake-es/intake-errors/internal/digest"
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
	// SMTPURL es smtp://usuario:clave@host:puerto (STARTTLS si el servidor lo
	// ofrece) o smtps:// (TLS desde el principio). SMTPFrom es el remitente.
	SMTPURL  string
	SMTPFrom string
	// TelegramAPIURL vacío es la API de Telegram.
	TelegramAPIURL string
	// GitHubToken es el de las acciones de GitHub que no traen uno propio.
	GitHubToken string
	// GitHubAPIURL vacío es la API de GitHub.
	GitHubAPIURL string
	Interval     time.Duration
	HTTPClient   *http.Client
}

type Alerter struct {
	st       *store.Store
	log      *slog.Logger
	interval time.Duration
	channels map[string]sender
	// order fija el orden en que se encolan los canales.
	order []string
	now   func() time.Time

	base           string
	client         *http.Client
	linearKey      string
	linearEndpoint string
	linearTeam     string
	linearLabels   []string
	smtp           *smtpConfig
	telegramAPI    string
	githubToken    string
	githubEndpoint string

	mu      sync.Mutex
	linears map[string]*linear

	rulesMu sync.Mutex
	// hasRules dice, por proyecto, si tiene reglas y desde cuándo se sabe.
	hasRules map[int64]rulesSeen
}

type rulesSeen struct {
	has bool
	at  time.Time
}

// RulesCacheTTL es cuánto se fía el Alerter de si un proyecto tiene reglas.
// Las que se guardan por la API de esta réplica valen al momento; las de
// otra réplica o de la CLI, como mucho pasado este tiempo.
const RulesCacheTTL = 30 * time.Second

type sender interface {
	send(ctx context.Context, tx db.Tx, a *pending) error
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

	// ruleID es 0 en los avisos sin regla.
	ruleID   int64
	ruleName string
	actionID string
	detail   ruleDetail
	link     string
	ev       *event.Event
	issue    *store.IssueDetail
	// cron es el monitor de los avisos de cron y uptime el de los de
	// disponibilidad; en el resto, nil.
	cron   *cronInfo
	uptime *uptimeInfo
	// digest es el resumen de las filas de resúmenes, del proyecto digestProject.
	digest        *digest.Digest
	digestProject int64

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
	a := &Alerter{st: st, log: log, interval: o.Interval, channels: map[string]sender{}, now: time.Now,
		linears: map[string]*linear{}, hasRules: map[int64]rulesSeen{}}
	if a.interval <= 0 {
		a.interval = DefaultInterval
	}
	a.base = strings.TrimRight(o.PublicURL, "/")
	a.client = o.HTTPClient
	if a.client == nil {
		a.client = &http.Client{Timeout: 15 * time.Second}
	}
	a.linearKey, a.linearTeam, a.linearLabels = o.LinearAPIKey, o.LinearTeamID, o.LinearLabelIDs
	a.linearEndpoint = o.LinearAPIURL
	if a.linearEndpoint == "" {
		a.linearEndpoint = LinearEndpoint
	}
	a.githubToken = o.GitHubToken
	a.githubEndpoint = strings.TrimRight(o.GitHubAPIURL, "/")
	if a.githubEndpoint == "" {
		a.githubEndpoint = GitHubEndpoint
	}
	a.telegramAPI = strings.TrimRight(o.TelegramAPIURL, "/")
	if a.telegramAPI == "" {
		a.telegramAPI = TelegramEndpoint
	}
	if o.SMTPURL != "" {
		c, err := parseSMTP(o.SMTPURL, o.SMTPFrom)
		if err != nil {
			return nil, err
		}
		a.smtp = c
	}
	if o.SlackWebhookURL != "" {
		a.channels[channelSlack] = &slack{url: o.SlackWebhookURL, client: a.client}
		a.order = append(a.order, channelSlack)
	}
	if o.LinearAPIKey != "" {
		if o.LinearTeamID == "" {
			return nil, errors.New("con LINEAR_API_KEY hace falta LINEAR_TEAM_ID")
		}
		a.channels[channelLinear] = a.linearFor(o.LinearTeamID, o.LinearLabelIDs)
		a.order = append(a.order, channelLinear)
	}
	return a, nil
}

// Channels dice qué canales están configurados, en el orden en que se encolan.
func (a *Alerter) Channels() []string { return append([]string(nil), a.order...) }

// IssueChanged encola los avisos del evento: los de las reglas del proyecto o,
// si no tiene ninguna, uno por canal si el issue es nuevo o ha vuelto. Si
// falla, lo registra y deja seguir a la ingesta.
func (a *Alerter) IssueChanged(ctx context.Context, tx db.Tx, ev *event.Event, res ingest.Result) error {
	issueID := res.IssueID
	if issueID == 0 {
		return nil
	}
	// Sin reglas, como siempre: un evento que no cambia el issue no toca la base.
	if res.ProjectID != 0 {
		has, err := a.projectHasRules(ctx, tx, res.ProjectID)
		if err != nil {
			a.log.ErrorContext(ctx, "encolar aviso", "issue", issueID, "err", err)
			return nil
		}
		if !has && (res.Outcome == ingest.Unchanged || len(a.order) == 0) {
			return nil
		}
	}
	sp, err := tx.Begin(ctx)
	if err != nil {
		a.log.ErrorContext(ctx, "encolar aviso", "issue", issueID, "err", err)
		return nil
	}
	rules, err := store.RulesOfIssue(ctx, sp, issueID)
	if err == nil {
		rules = slices.DeleteFunc(rules, func(r store.Rule) bool { return !r.Conditions.ForIssues() })
		if len(rules) > 0 {
			err = a.applyRules(ctx, sp, rules, ev, res)
		} else {
			err = a.enqueueDefault(ctx, sp, ev, res)
		}
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

// projectHasRules lo pregunta a la base como mucho una vez cada RulesCacheTTL
// por proyecto. Va por la transacción de la ingesta, en un savepoint para que
// un fallo no la tumbe: pedir otra conexión al pool con ella abierta puede
// dejar la ingesta esperando cuando todas las conexiones están en uso.
func (a *Alerter) projectHasRules(ctx context.Context, tx db.Tx, projectID int64) (bool, error) {
	now := time.Now()
	a.rulesMu.Lock()
	seen, ok := a.hasRules[projectID]
	a.rulesMu.Unlock()
	if ok && now.Sub(seen.at) < RulesCacheTTL {
		return seen.has, nil
	}
	sp, err := tx.Begin(ctx)
	if err != nil {
		return false, err
	}
	var has bool
	if err := sp.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM alert_rules WHERE project_id = $1 AND `+store.IssueRulesSQL(sp.Dialect())+`)`,
		projectID).Scan(&has); err != nil {
		_ = sp.Rollback(ctx)
		return false, err
	}
	if err := sp.Commit(ctx); err != nil {
		return false, err
	}
	a.rulesMu.Lock()
	a.hasRules[projectID] = rulesSeen{has: has, at: now}
	a.rulesMu.Unlock()
	return has, nil
}

// RulesChanged olvida lo que se sabía de las reglas del proyecto: se llama al
// crearlas, editarlas o borrarlas.
func (a *Alerter) RulesChanged(projectID int64) {
	a.rulesMu.Lock()
	defer a.rulesMu.Unlock()
	delete(a.hasRules, projectID)
}

// enqueueDefault es el aviso de un proyecto sin reglas.
func (a *Alerter) enqueueDefault(ctx context.Context, tx db.Tx, ev *event.Event, res ingest.Result) error {
	if res.Outcome == ingest.Unchanged || len(a.order) == 0 {
		return nil
	}
	ids := map[string]int64{}
	for _, ch := range a.order {
		var id int64
		err := tx.QueryRow(ctx, `
			INSERT INTO alert_outbox (issue_id, channel, kind, release, environment, event_at, resolved_at, next_attempt_at, created_at)
			VALUES ($1, $2, $3, NULLIF($4, ''), NULLIF($5, ''), $6, $7, $8, $8)
			RETURNING id`,
			res.IssueID, ch, res.Outcome.String(), ev.Release, ev.Environment, ev.Timestamp, res.ResolvedAt, a.now().UTC()).Scan(&id)
		if err != nil {
			return err
		}
		ids[ch] = id
	}
	if slackID, linearID := ids[channelSlack], ids[channelLinear]; slackID != 0 && linearID != 0 {
		_, err := tx.Exec(ctx, `UPDATE alert_outbox SET waits_for = $2 WHERE id = $1`, slackID, linearID)
		return err
	}
	return nil
}

// Run envía lo pendiente cada Interval hasta que se cancela ctx: los avisos y
// los cambios de estado para Linear y GitHub.
func (a *Alerter) Run(ctx context.Context) {
	t := time.NewTicker(a.interval)
	defer t.Stop()
	for {
		if _, err := a.SendPending(ctx); err != nil && ctx.Err() == nil {
			a.log.ErrorContext(ctx, "enviar avisos", "err", err)
		}
		if _, err := a.SyncLinks(ctx); err != nil && ctx.Err() == nil {
			a.log.ErrorContext(ctx, "llevar estados a los enlaces", "err", err)
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
	var ruleID, issueID, monitorID, checkinID, uptimeID, incidentID, digestID *int64
	var detail []byte
	// El primer NOT EXISTS mantiene el orden por issue y canal (y regla y
	// acción): una regresión no se manda antes de que se mande (o se abandone)
	// el aviso de error nuevo. El segundo hace que Slack espere a Linear, como
	// mucho SlackWaitsForLinear. Los avisos sin regla ni resumen solo salen por
	// los canales configurados.
	err = tx.QueryRow(ctx, `
		SELECT o.id, o.issue_id, o.monitor_id, o.checkin_id, o.uptime_monitor_id, o.incident_id, o.channel, o.kind, COALESCE(o.release, ''), COALESCE(o.environment, ''),
			o.event_at, o.resolved_at, CAST(o.linear_create_id AS text), o.attempts, o.rule_id, COALESCE(o.action_id, ''), o.detail,
			o.digest_project_id
		FROM alert_outbox o
		WHERE o.sent_at IS NULL AND o.failed_at IS NULL AND o.next_attempt_at <= $1
			AND (o.rule_id IS NOT NULL OR o.digest_project_id IS NOT NULL OR `+tx.Dialect().InList("o.channel", "$2")+`)
			AND NOT EXISTS (SELECT 1 FROM alert_outbox b
				WHERE b.issue_id IS NOT DISTINCT FROM o.issue_id AND b.monitor_id IS NOT DISTINCT FROM o.monitor_id
					AND b.uptime_monitor_id IS NOT DISTINCT FROM o.uptime_monitor_id
					AND b.digest_project_id IS NOT DISTINCT FROM o.digest_project_id
					AND b.channel = o.channel AND b.id < o.id
					AND b.rule_id IS NOT DISTINCT FROM o.rule_id AND b.action_id IS NOT DISTINCT FROM o.action_id
					AND b.sent_at IS NULL AND b.failed_at IS NULL)
			AND NOT (o.created_at > $3 AND EXISTS (SELECT 1 FROM alert_outbox w
				WHERE w.id = o.waits_for AND w.sent_at IS NULL AND w.failed_at IS NULL))
		ORDER BY o.next_attempt_at, o.id
		LIMIT 1`+tx.Dialect().ForUpdateSkipLocked(), now, a.order, now.Add(-SlackWaitsForLinear)).
		Scan(&p.id, &issueID, &monitorID, &checkinID, &uptimeID, &incidentID, &channel, &p.kind, &p.release, &p.environment, &p.eventAt, &p.resolvedAt, &p.createID,
			&attempts, &ruleID, &p.actionID, &detail, &digestID)
	if errors.Is(err, db.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("coger aviso: %w", err)
	}
	if len(detail) > 0 && digestID == nil {
		if err := json.Unmarshal(detail, &p.detail); err != nil {
			return false, fmt.Errorf("aviso %d: %w", p.id, err)
		}
	}
	var snd sender
	var sendErr error
	if digestID != nil {
		snd, sendErr = a.loadDigest(ctx, &p, *digestID, detail)
		if sendErr == nil {
			sendErr = snd.send(ctx, tx, &p)
		}
		if sendErr == nil {
			if err := a.st.DigestSent(ctx, tx, *digestID, a.now().UTC()); err != nil {
				return false, err
			}
		}
		return a.finish(ctx, tx, &p, channel, attempts, now, sendErr)
	}
	switch {
	case monitorID != nil:
		sendErr = a.loadCron(ctx, tx, &p, *monitorID, checkinID)
	case uptimeID != nil:
		sendErr = a.loadUptime(ctx, tx, &p, *uptimeID, incidentID)
	default:
		p.issueID = *issueID
		sendErr = a.loadIssue(ctx, tx, &p)
	}
	if sendErr != nil && !errors.Is(sendErr, errGone) {
		return false, sendErr
	}
	if sendErr == nil {
		if ruleID == nil {
			snd = a.channels[channel]
		} else {
			p.ruleID = *ruleID
			snd, sendErr = a.ruleSender(ctx, tx, &p)
		}
	}
	if sendErr == nil && !p.isMonitor() {
		if latest, err := a.st.LatestEvent(ctx, p.issueID); err != nil {
			sendErr = err
		} else if latest != nil {
			p.ev = &latest.Event
		}
	}
	if sendErr == nil {
		sendErr = snd.send(ctx, tx, &p)
	}
	return a.finish(ctx, tx, &p, channel, attempts, now, sendErr)
}

func (a *Alerter) loadIssue(ctx context.Context, tx db.Tx, p *pending) error {
	err := tx.QueryRow(ctx, `
		SELECT p.name, p.linear_label_ids, i.title, i.culprit, i.event_count, COALESCE(l.external_id, ''),
			COALESCE(l.identifier, ''), COALESCE(l.url, '')
		FROM issues i JOIN projects p ON p.id = i.project_id
			LEFT JOIN LATERAL (SELECT external_id, identifier, url FROM issue_links
				WHERE issue_id = i.id AND provider = 'linear' AND kind = 'issue' ORDER BY id DESC LIMIT 1) l ON true
		WHERE i.id = $1`, p.issueID).
		Scan(&p.project, &p.labels, &p.title, &p.culprit, &p.count, &p.linear, &p.linearIdentifier, &p.linearURL)
	if err != nil {
		return fmt.Errorf("leer issue %d: %w", p.issueID, err)
	}
	p.link = issueLink(a.base, p.issueID)
	return nil
}

// finish apunta el resultado del envío: enviado, para reintentar o abandonado.
func (a *Alerter) finish(ctx context.Context, tx db.Tx, p *pending, channel string, attempts int, now time.Time, sendErr error) (bool, error) {
	var err error
	attempts++
	switch {
	case sendErr == nil:
		_, err = tx.Exec(ctx, `UPDATE alert_outbox SET attempts = $2, sent_at = $3, last_error = NULL WHERE id = $1`,
			p.id, attempts, a.now().UTC())
	case attempts >= MaxAttempts || errors.Is(sendErr, errGone):
		a.log.ErrorContext(ctx, "aviso abandonado", "canal", channel, p.target(), "intentos", attempts, "err", sendErr)
		_, err = tx.Exec(ctx, `UPDATE alert_outbox SET attempts = $2, failed_at = $3, last_error = $4 WHERE id = $1`,
			p.id, attempts, a.now().UTC(), sendErr.Error())
	default:
		next := now.Add(Backoff[min(attempts, len(Backoff))-1])
		a.log.WarnContext(ctx, "aviso fallido", "canal", channel, p.target(), "intentos", attempts, "siguiente", next, "err", sendErr)
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
		a.log.InfoContext(ctx, "aviso enviado", "canal", channel, p.target(), "tipo", p.kind, "regla", p.ruleID)
	}
	return true, nil
}

// target es de qué es el aviso, para el registro: el issue o el monitor.
func (p *pending) target() slog.Attr {
	if p.digestProject != 0 {
		return slog.Int64("resumen", p.digestProject)
	}
	if p.cron != nil {
		return slog.Int64("monitor", p.cron.monitorID)
	}
	if p.uptime != nil {
		return slog.Int64("monitor_disponibilidad", p.uptime.monitorID)
	}
	return slog.Int64("issue", p.issueID)
}

// isMonitor dice si el aviso es de un monitor y no de un issue.
func (p *pending) isMonitor() bool { return p.cron != nil || p.uptime != nil }

// issueDetail es el issue con su último evento, para el Markdown.
func issueDetail(ctx context.Context, st *store.Store, p *pending) (store.IssueDetail, error) {
	if p.issue != nil {
		return *p.issue, nil
	}
	d, err := st.IssueDetail(ctx, p.issueID, time.Now())
	if err != nil {
		return d, fmt.Errorf("leer issue %d: %w", p.issueID, err)
	}
	p.issue = &d
	return d, nil
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
