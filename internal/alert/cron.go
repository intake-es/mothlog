package alert

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/intake-es/intake-errors/internal/cron"
	"github.com/intake-es/intake-errors/internal/store"
)

// cronInfo es el monitor y el check-in de un aviso de cron.
type cronInfo struct {
	monitorID     int64
	projectID     int64
	slug          string
	name          string
	schedule      cron.Schedule
	timezone      string
	checkinMargin int
	maxRuntime    int
	checkInID     string
	status        string
	durationMs    *int64
	expectedAt    *time.Time
	startedAt     time.Time
}

func isCronKind(kind string) bool { return strings.HasPrefix(kind, "cron_") }

// MonitorChanged encola los avisos del cambio de estado de un monitor: los de
// las reglas del proyecto con condiciones de cron o, si no tiene ninguna, uno
// por canal si falla, no llega o se pasa de tiempo. Si falla, lo registra y
// deja seguir.
func (a *Alerter) MonitorChanged(ctx context.Context, tx pgx.Tx, ch store.MonitorChange) error {
	sp, err := tx.Begin(ctx)
	if err != nil {
		a.log.ErrorContext(ctx, "encolar aviso de cron", "monitor", ch.Monitor.ID, "err", err)
		return nil
	}
	err = a.enqueueCron(ctx, sp, ch)
	if err == nil {
		err = sp.Commit(ctx)
	}
	if err != nil {
		_ = sp.Rollback(ctx)
		a.log.ErrorContext(ctx, "encolar aviso de cron", "monitor", ch.Monitor.ID, "err", err)
	}
	return nil
}

func (a *Alerter) enqueueCron(ctx context.Context, tx pgx.Tx, ch store.MonitorChange) error {
	rules, err := store.RulesOfProject(ctx, tx, ch.Monitor.ProjectID)
	if err != nil {
		return err
	}
	now := a.now().UTC()
	at := ch.CheckIn.StartedAt
	insert := func(channel string, ruleID *int64, actionID *string) (int64, error) {
		var id int64
		err := tx.QueryRow(ctx, `
			INSERT INTO alert_outbox (monitor_id, checkin_id, channel, kind, release, environment, event_at,
				next_attempt_at, created_at, rule_id, action_id)
			VALUES ($1, $2, $3, $4, NULLIF($5, ''), NULLIF($6, ''), $7, $8, $8, $9, $10)
			RETURNING id`,
			ch.Monitor.ID, ch.CheckIn.ID, channel, ch.Kind, ch.CheckIn.Release, ch.CheckIn.Environment, at, now,
			ruleID, actionID).Scan(&id)
		return id, err
	}
	var cronRules []store.Rule
	for _, r := range rules {
		if r.Conditions.Cron != nil {
			cronRules = append(cronRules, r)
		}
	}
	if len(cronRules) == 0 {
		if ch.Kind == store.KindCronRecovered {
			return nil
		}
		for _, channel := range a.order {
			if _, err := insert(channel, nil, nil); err != nil {
				return err
			}
		}
		return nil
	}
	for _, r := range cronRules {
		if !r.Enabled || !cronMatches(r.Conditions, ch) {
			continue
		}
		for _, act := range r.Actions {
			if _, err := insert(act.Type, &r.ID, &act.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

// cronMatches dice si la regla pide este aviso de este monitor en este entorno.
func cronMatches(c store.RuleConditions, ch store.MonitorChange) bool {
	cr := c.Cron
	if cr == nil || !slices.Contains(cr.Kinds(), ch.Kind) {
		return false
	}
	if len(cr.Monitors) > 0 && !slices.Contains(cr.Monitors, ch.Monitor.Slug) {
		return false
	}
	env := ch.CheckIn.Environment
	if env == "" {
		env = ch.Monitor.Environment
	}
	return len(c.Environments) == 0 || slices.Contains(c.Environments, env)
}

// loadCron lee el monitor y el check-in de un aviso de cron.
func (a *Alerter) loadCron(ctx context.Context, tx pgx.Tx, p *pending, monitorID int64, checkinID *int64) error {
	c := &cronInfo{monitorID: monitorID}
	err := tx.QueryRow(ctx, `
		SELECT p.id, p.name, p.linear_label_ids, m.slug, m.name, m.schedule_type, m.schedule,
			COALESCE(m.interval_value, 0), COALESCE(m.interval_unit, ''), m.timezone, m.checkin_margin, m.max_runtime
		FROM monitors m JOIN projects p ON p.id = m.project_id WHERE m.id = $1`, monitorID).
		Scan(&c.projectID, &p.project, &p.labels, &c.slug, &c.name, &c.schedule.Type, &c.schedule.Crontab,
			&c.schedule.Every, &c.schedule.Unit, &c.timezone, &c.checkinMargin, &c.maxRuntime)
	if errors.Is(err, pgx.ErrNoRows) {
		return errGone
	}
	if err != nil {
		return fmt.Errorf("leer monitor %d: %w", monitorID, err)
	}
	if checkinID != nil {
		err = tx.QueryRow(ctx, `
			SELECT replace(check_in_id::text, '-', ''), status, duration_ms, expected_at, started_at FROM monitor_checkins WHERE id = $1`,
			*checkinID).Scan(&c.checkInID, &c.status, &c.durationMs, &c.expectedAt, &c.startedAt)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("leer check-in %d: %w", *checkinID, err)
		}
	}
	p.cron = c
	p.title = c.name
	if a.base != "" {
		p.link = a.base + "/monitors/" + strconv.FormatInt(c.projectID, 10) + "/" + c.slug
	}
	return nil
}

func cronKindText(kind string) string {
	switch kind {
	case store.KindCronFailed:
		return "Cron fallido"
	case store.KindCronMissed:
		return "Cron sin ejecutar"
	case store.KindCronTimeout:
		return "Cron pasado de tiempo"
	case store.KindCronRecovered:
		return "Cron recuperado"
	}
	return "Cron"
}

// scheduleText es «0 3 * * * (Europe/Madrid)» o «cada 2 hour».
func scheduleText(c *cronInfo) string {
	s := c.schedule.Crontab
	if c.schedule.Type == cron.TypeInterval {
		s = fmt.Sprintf("cada %d %s", c.schedule.Every, c.schedule.Unit)
	}
	if c.timezone != "" && c.timezone != "UTC" {
		s += " (" + c.timezone + ")"
	}
	return s
}

func durationText(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	switch {
	case d < time.Second:
		return strconv.FormatInt(ms, 10) + " ms"
	case d < time.Minute:
		return strconv.FormatFloat(d.Seconds(), 'f', 1, 64) + " s"
	case d < time.Hour:
		return fmt.Sprintf("%d min %d s", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%d h %d min", int(d.Hours()), int(d.Minutes())%60)
}

func cronSummary(p *pending) summary {
	c := p.cron
	what, _ := kindText(p)
	s := summary{What: what, Headline: truncate(oneLine(c.name), maxHeadline), Meta: []string{p.project}}
	if p.environment != "" {
		s.Meta = append(s.Meta, p.environment)
	}
	s.Location = truncate(c.slug+" · "+scheduleText(c), maxCodeLine)
	add := func(name, value string) {
		if value = oneLine(value); value != "" {
			s.Fields = append(s.Fields, [2]string{name, truncate(value, maxValue)})
		}
	}
	switch p.kind {
	case store.KindCronMissed:
		if c.expectedAt != nil {
			add("Esperado", c.expectedAt.In(madrid).Format("2006-01-02 15:04 MST"))
		}
		add("Margen", strconv.Itoa(c.checkinMargin)+" min")
	case store.KindCronTimeout:
		add("Empezó", c.startedAt.In(madrid).Format("2006-01-02 15:04 MST"))
		add("Tiempo máximo", strconv.Itoa(c.maxRuntime)+" min")
	default:
		if c.durationMs != nil {
			add("Duración", durationText(*c.durationMs))
		}
	}
	release := p.release
	if isSHA(release) {
		release = release[:7]
	}
	add("Release", release)
	return s
}

func slackCron(p *pending) slackPayload {
	s := cronSummary(p)
	_, fallback := kindText(p)
	meta := append([]string{"*" + s.What + "*"}, mapEsc(s.Meta)...)
	msg := slackPayload{Text: esc(fallback + p.project + ": " + s.Headline)}
	msg.Blocks = append(msg.Blocks,
		slackBlock{Type: "context", Elements: []any{mrkdwn(strings.Join(meta, " · "), maxSectionText)}},
		section("*"+esc(strings.ReplaceAll(s.Headline, "*", "∗"))+"*"),
		section(code(s.Location)))
	var fields []slackText
	for _, f := range s.Fields {
		fields = append(fields, mrkdwn("*"+f[0]+"*\n"+esc(f[1]), maxFieldText))
	}
	if len(fields) > 0 {
		msg.Blocks = append(msg.Blocks, slackBlock{Type: "section", Fields: fields})
	}
	if p.link != "" {
		msg.Blocks = append(msg.Blocks, slackBlock{Type: "actions", Elements: []any{
			slackButton{Type: "button", Text: plain("Ver el monitor", maxButtonText), URL: p.link, Style: "primary"},
		}})
	}
	return msg
}

func mapEsc(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = esc(s)
	}
	return out
}

// cronMarkdown es el aviso de cron en Markdown, para Linear y el webhook.
func cronMarkdown(p *pending) string {
	s := cronSummary(p)
	var b strings.Builder
	fmt.Fprintf(&b, "# %s: %s\n\n", s.What, s.Headline)
	fmt.Fprintf(&b, "- Proyecto: %s\n- Monitor: `%s`\n- Horario: `%s`\n", p.project, p.cron.slug, scheduleText(p.cron))
	if p.environment != "" {
		fmt.Fprintf(&b, "- Entorno: %s\n", p.environment)
	}
	for _, f := range s.Fields {
		fmt.Fprintf(&b, "- %s: %s\n", f[0], f[1])
	}
	if p.link != "" {
		fmt.Fprintf(&b, "\n[Ver en intake-errors](%s)\n", p.link)
	}
	return b.String()
}

// createCron crea una issue de Linear por cada fallo de cron; la vuelta a ir bien no crea ninguna.
func (l *linear) createCron(ctx context.Context, p *pending) error {
	if p.kind == store.KindCronRecovered {
		return nil
	}
	what, _ := kindText(p)
	input := map[string]any{
		"id":          p.createID,
		"teamId":      l.team,
		"title":       truncate("["+p.project+"] "+what+": "+oneLine(p.cron.name), maxLinearTitle),
		"description": cronMarkdown(p),
	}
	if ids := l.labelIDs(p); len(ids) > 0 {
		input["labelIds"] = ids
	}
	var out struct {
		IssueCreate struct {
			Success bool
			Issue   linearRef
		}
	}
	err := l.do(ctx, `mutation($input: IssueCreateInput!) { issueCreate(input: $input) { success issue { id identifier url } } }`,
		map[string]any{"input": input}, &out)
	if err != nil {
		if _, found, _, lookupErr := l.lookup(ctx, p.createID); lookupErr == nil && found {
			return nil
		}
		return err
	}
	if !out.IssueCreate.Success {
		return errors.New("linear: issueCreate sin éxito")
	}
	return nil
}

type WebhookMonitor struct {
	ID            int64         `json:"id"`
	Slug          string        `json:"slug"`
	Name          string        `json:"name"`
	Schedule      cron.Schedule `json:"schedule"`
	Timezone      string        `json:"timezone"`
	CheckinMargin int           `json:"checkin_margin"`
	MaxRuntime    int           `json:"max_runtime"`
	URL           string        `json:"url,omitempty"`
}

type WebhookCheckIn struct {
	CheckInID   string     `json:"check_in_id"`
	Status      string     `json:"status"`
	DurationMs  *int64     `json:"duration_ms"`
	Environment string     `json:"environment,omitempty"`
	Release     string     `json:"release,omitempty"`
	ExpectedAt  *time.Time `json:"expected_at,omitempty"`
	StartedAt   time.Time  `json:"started_at"`
}

func (w *webhook) cronPayload(p *pending) WebhookPayload {
	c := p.cron
	return WebhookPayload{
		Type:     p.kind,
		Delivery: "outbox-" + strconv.FormatInt(p.id, 10),
		SentAt:   w.now().UTC(),
		Rule:     WebhookRule{ID: p.ruleID, Name: p.ruleName},
		Project:  p.project,
		Monitor: &WebhookMonitor{ID: c.monitorID, Slug: c.slug, Name: c.name, Schedule: c.schedule, Timezone: c.timezone,
			CheckinMargin: c.checkinMargin, MaxRuntime: c.maxRuntime, URL: p.link},
		CheckIn: &WebhookCheckIn{CheckInID: c.checkInID, Status: c.status, DurationMs: c.durationMs,
			Environment: p.environment, Release: p.release, ExpectedAt: c.expectedAt, StartedAt: c.startedAt},
		Markdown: cronMarkdown(p),
	}
}
