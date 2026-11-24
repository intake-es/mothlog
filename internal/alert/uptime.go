package alert

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/intake-es/mothlog/internal/db"
	"github.com/intake-es/mothlog/internal/lang"
	"github.com/intake-es/mothlog/internal/store"
)

// uptimeInfo es el monitor y el incidente de un aviso de disponibilidad.
type uptimeInfo struct {
	monitorID  int64
	projectID  int64
	slug       string
	name       string
	url        string
	method     string
	threshold  int
	statusCode *int
	incident   store.UptimeIncident
}

// UptimeChanged encola los avisos de una caída o una vuelta: los de las
// reglas del proyecto con condiciones de disponibilidad o, si no tiene
// ninguna, uno por canal. Si falla, lo registra y deja seguir.
func (a *Alerter) UptimeChanged(ctx context.Context, tx db.Tx, ch store.UptimeChange) error {
	sp, err := tx.Begin(ctx)
	if err != nil {
		a.log.ErrorContext(ctx, "enqueue uptime alert", "monitor", ch.Monitor.ID, "err", err)
		return nil
	}
	err = a.enqueueUptime(ctx, sp, ch)
	if err == nil {
		err = sp.Commit(ctx)
	}
	if err != nil {
		_ = sp.Rollback(ctx)
		a.log.ErrorContext(ctx, "enqueue uptime alert", "monitor", ch.Monitor.ID, "err", err)
	}
	return nil
}

func (a *Alerter) enqueueUptime(ctx context.Context, tx db.Tx, ch store.UptimeChange) error {
	rules, err := store.RulesOfProject(ctx, tx, ch.Monitor.ProjectID)
	if err != nil {
		return err
	}
	now := a.now().UTC()
	at := ch.Incident.StartedAt
	if ch.Kind == store.KindUptimeUp && ch.Incident.EndedAt != nil {
		at = *ch.Incident.EndedAt
	}
	insert := func(channel string, ruleID *int64, actionID *string) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO alert_outbox (uptime_monitor_id, incident_id, channel, kind, event_at, next_attempt_at,
				created_at, rule_id, action_id)
			VALUES ($1, $2, $3, $4, $5, $6, $6, $7, $8)`,
			ch.Monitor.ID, ch.Incident.ID, channel, ch.Kind, at, now, ruleID, actionID)
		return err
	}
	var uptimeRules []store.Rule
	for _, r := range rules {
		if r.Conditions.Uptime != nil {
			uptimeRules = append(uptimeRules, r)
		}
	}
	if len(uptimeRules) == 0 {
		for _, channel := range a.order {
			if err := insert(channel, nil, nil); err != nil {
				return err
			}
		}
		return nil
	}
	for _, r := range uptimeRules {
		up := r.Conditions.Uptime
		if !r.Enabled || !slices.Contains(up.Kinds(), ch.Kind) ||
			(len(up.Monitors) > 0 && !slices.Contains(up.Monitors, ch.Monitor.Slug)) {
			continue
		}
		for _, act := range r.Actions {
			if err := insert(act.Type, &r.ID, &act.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

// loadUptime lee el monitor y el incidente de un aviso de disponibilidad.
func (a *Alerter) loadUptime(ctx context.Context, tx db.Tx, p *pending, monitorID int64, incidentID *int64) error {
	u := &uptimeInfo{monitorID: monitorID}
	err := tx.QueryRow(ctx, `
		SELECT p.id, p.name, p.linear_label_ids, m.slug, m.name, m.url, m.method, m.failure_threshold, m.last_status_code
		FROM uptime_monitors m JOIN projects p ON p.id = m.project_id WHERE m.id = $1`, monitorID).
		Scan(&u.projectID, &p.project, &p.labels, &u.slug, &u.name, &u.url, &u.method, &u.threshold, &u.statusCode)
	if errors.Is(err, db.ErrNoRows) {
		return errGone
	}
	if err != nil {
		return fmt.Errorf("read uptime monitor %d: %w", monitorID, err)
	}
	if incidentID != nil {
		i := &u.incident
		err = tx.QueryRow(ctx, `SELECT id, monitor_id, started_at, ended_at, duration_ms, first_error
			FROM uptime_incidents WHERE id = $1`, *incidentID).
			Scan(&i.ID, &i.MonitorID, &i.StartedAt, &i.EndedAt, &i.DurationMs, &i.FirstError)
		if err != nil && !errors.Is(err, db.ErrNoRows) {
			return fmt.Errorf("read incident %d: %w", *incidentID, err)
		}
		// El aviso de la caída cuenta el incidente como estaba al caer, aunque salga tarde.
		if p.kind == store.KindUptimeDown {
			i.EndedAt, i.DurationMs = nil, nil
		}
	}
	p.uptime = u
	p.title = u.name
	if a.base != "" {
		p.link = a.base + "/monitors/" + strconv.FormatInt(u.projectID, 10) + "/uptime/" + u.slug
	}
	return nil
}

func uptimeKindText(kind string, l lang.Lang) string {
	if kind == store.KindUptimeUp {
		return l.T("Monitor up", "Monitor recuperado")
	}
	return l.T("Monitor down", "Monitor caído")
}

func uptimeSummary(p *pending) summary {
	u := p.uptime
	what, _ := kindText(p)
	s := summary{What: what, Headline: truncate(oneLine(u.name), maxHeadline), Meta: []string{p.project}}
	s.Location = truncate(u.method+" "+u.url, maxCodeLine)
	add := func(name, value string) {
		if value = oneLine(value); value != "" {
			s.Fields = append(s.Fields, [2]string{name, truncate(value, maxValue)})
		}
	}
	inc := u.incident
	tr := p.l.T
	if p.kind == store.KindUptimeUp {
		if inc.DurationMs != nil {
			add(tr("Down for", "Caído durante"), durationText(*inc.DurationMs))
		}
		if !inc.StartedAt.IsZero() {
			add(tr("Since", "Desde"), inc.StartedAt.In(madrid).Format("2006-01-02 15:04:05 MST"))
		}
		if inc.EndedAt != nil {
			add(tr("Until", "Hasta"), inc.EndedAt.In(madrid).Format("2006-01-02 15:04:05 MST"))
		}
	} else {
		if !inc.StartedAt.IsZero() {
			add(tr("Since", "Desde"), inc.StartedAt.In(madrid).Format("2006-01-02 15:04:05 MST"))
		}
		add("Error", inc.FirstError)
		add(tr("Consecutive failures", "Fallos seguidos"), strconv.Itoa(u.threshold))
	}
	return s
}

// uptimeMarkdown es el aviso de disponibilidad en Markdown, para Linear y el webhook.
func uptimeMarkdown(p *pending) string {
	s := uptimeSummary(p)
	var b strings.Builder
	fmt.Fprintf(&b, "# %s: %s\n\n", s.What, s.Headline)
	fmt.Fprintf(&b, p.l.T("- Project: %s\n- Monitor: `%s`\n- URL: `%s %s`\n", "- Proyecto: %s\n- Monitor: `%s`\n- URL: `%s %s`\n"),
		p.project, p.uptime.slug, p.uptime.method, p.uptime.url)
	for _, f := range s.Fields {
		fmt.Fprintf(&b, "- %s: %s\n", f[0], f[1])
	}
	if p.link != "" {
		fmt.Fprintf(&b, "\n%s\n", viewIn(p.l, p.link))
	}
	return b.String()
}

type WebhookUptime struct {
	ID       int64  `json:"id"`
	Slug     string `json:"slug"`
	Name     string `json:"name"`
	CheckURL string `json:"check_url"`
	Method   string `json:"method"`
	URL      string `json:"url,omitempty"`
}

type WebhookIncident struct {
	ID         int64      `json:"id"`
	StartedAt  time.Time  `json:"started_at"`
	EndedAt    *time.Time `json:"ended_at"`
	DurationMs *int64     `json:"duration_ms"`
	FirstError string     `json:"first_error"`
}

func (w *webhook) uptimePayload(p *pending) WebhookPayload {
	u := p.uptime
	i := u.incident
	return WebhookPayload{
		Type:     p.kind,
		Delivery: "outbox-" + strconv.FormatInt(p.id, 10),
		SentAt:   w.now().UTC(),
		Rule:     WebhookRule{ID: p.ruleID, Name: p.ruleName},
		Project:  p.project,
		Uptime:   &WebhookUptime{ID: u.monitorID, Slug: u.slug, Name: u.name, CheckURL: u.url, Method: u.method, URL: p.link},
		Incident: &WebhookIncident{ID: i.ID, StartedAt: i.StartedAt, EndedAt: i.EndedAt, DurationMs: i.DurationMs,
			FirstError: i.FirstError},
		Markdown: uptimeMarkdown(p),
	}
}
