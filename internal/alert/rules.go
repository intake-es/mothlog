package alert

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/intake-es/mothlog/internal/db"
	"github.com/intake-es/mothlog/internal/event"
	"github.com/intake-es/mothlog/internal/ingest"
	"github.com/intake-es/mothlog/internal/lang"
	"github.com/intake-es/mothlog/internal/store"
)

// keepMinutes es cuánto se guardan las cuentas por minuto: la ventana más larga.
const keepMinutes = store.MaxWindowMinutes * time.Minute

// ruleState es lo que recuerda una regla de un issue.
type ruleState struct {
	lastAlert *time.Time
	// frequencyAlerted: ya ha avisado de que pasa del umbral; se rearma al bajar de él.
	frequencyAlerted bool
	// usersAlerted: ya ha avisado de los usuarios; no se vuelven a contar.
	usersAlerted bool
}

type candidate struct {
	kind   string
	detail *ruleDetail
}

// applyRules encola, de cada regla activa que salta con este evento, un aviso
// por acción. Un issue silenciado no avisa.
func (a *Alerter) applyRules(ctx context.Context, tx db.Tx, rules []store.Rule, ev *event.Event, res ingest.Result) error {
	var status string
	if err := tx.QueryRow(ctx, `SELECT status FROM issues WHERE id = $1`, res.IssueID).Scan(&status); err != nil {
		return err
	}
	if status == "silenced" {
		return nil
	}
	now := a.now().UTC()
	for _, r := range rules {
		if !r.Enabled || !matches(r.Conditions, ev) {
			continue
		}
		if err := a.applyRule(ctx, tx, r, ev, res, now); err != nil {
			return err
		}
	}
	return nil
}

// applyRule evalúa una regla con un evento que pasa sus filtros. Los eventos
// de la frecuencia y los usuarios se cuentan por regla, solo los que pasan sus
// filtros, y saltan mientras estén por encima del umbral y la regla no haya
// avisado aún de ese cruce: un evento que llega durante el límite de la regla
// o con el que salta antes otra condición no hace que se pierda el aviso.
func (a *Alerter) applyRule(ctx context.Context, tx db.Tx, r store.Rule, ev *event.Event, res ingest.Result, now time.Time) error {
	st, err := loadRuleState(ctx, tx, r.ID, res.IssueID)
	if err != nil {
		return err
	}
	old := st
	c := r.Conditions
	var cands []candidate
	if c.New && res.Outcome == ingest.New {
		cands = append(cands, candidate{kind: "new"})
	}
	if c.Regressed && res.Outcome == ingest.Regressed {
		cands = append(cands, candidate{kind: "regressed"})
	}
	if u := c.Users; u != nil && !st.usersAlerted {
		n, err := countUser(ctx, tx, r, res.IssueID, ev, u.Users)
		if err != nil {
			return err
		}
		if n > u.Users {
			cands = append(cands, candidate{"users", &ruleDetail{Users: u.Users}})
		}
	}
	if f := c.Frequency; f != nil {
		n, err := countEvent(ctx, tx, r.ID, res.IssueID, f.WindowMinutes, now)
		if err != nil {
			return err
		}
		switch {
		case n <= f.Events:
			st.frequencyAlerted = false
		case !st.frequencyAlerted:
			cands = append(cands, candidate{"frequency", &ruleDetail{Events: f.Events, WindowMinutes: f.WindowMinutes}})
		}
	}
	throttled := st.lastAlert != nil && r.ThrottleMinutes > 0 &&
		now.Sub(*st.lastAlert) < time.Duration(r.ThrottleMinutes)*time.Minute
	if len(cands) > 0 && !throttled {
		cand := cands[0]
		if err := a.enqueueRule(ctx, tx, r, cand.kind, cand.detail, ev, res, now); err != nil {
			return err
		}
		st.lastAlert = &now
		switch cand.kind {
		case "users":
			st.usersAlerted = true
		case "frequency":
			st.frequencyAlerted = true
		}
	}
	if st == old {
		return nil
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO alert_rule_issues (rule_id, issue_id, last_alert_at, frequency_alerted, users_alerted)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (rule_id, issue_id) DO UPDATE SET last_alert_at = EXCLUDED.last_alert_at,
			frequency_alerted = EXCLUDED.frequency_alerted, users_alerted = EXCLUDED.users_alerted`,
		r.ID, res.IssueID, st.lastAlert, st.frequencyAlerted, st.usersAlerted)
	return err
}

func loadRuleState(ctx context.Context, tx db.Tx, ruleID, issueID int64) (ruleState, error) {
	var st ruleState
	err := tx.QueryRow(ctx, `
		SELECT last_alert_at, frequency_alerted, users_alerted FROM alert_rule_issues WHERE rule_id = $1 AND issue_id = $2`,
		ruleID, issueID).Scan(&st.lastAlert, &st.frequencyAlerted, &st.usersAlerted)
	if errors.Is(err, db.ErrNoRows) {
		return st, nil
	}
	return st, err
}

// countEvent suma el evento a la cuenta por minuto de la regla y el issue y
// devuelve cuántos lleva en la ventana.
func countEvent(ctx context.Context, tx db.Tx, ruleID, issueID int64, window int, now time.Time) (int, error) {
	minute := now.Truncate(time.Minute)
	if _, err := tx.Exec(ctx, `
		INSERT INTO alert_rule_minutes (rule_id, issue_id, minute, events) VALUES ($1, $2, $3, 1)
		ON CONFLICT (rule_id, issue_id, minute) DO UPDATE SET events = alert_rule_minutes.events + 1`,
		ruleID, issueID, minute); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM alert_rule_minutes WHERE rule_id = $1 AND issue_id = $2 AND minute < $3`,
		ruleID, issueID, minute.Add(-keepMinutes)); err != nil {
		return 0, err
	}
	var n int
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(sum(events), 0) FROM alert_rule_minutes WHERE rule_id = $1 AND issue_id = $2 AND minute >= $3`,
		ruleID, issueID, minute.Add(-time.Duration(window-1)*time.Minute)).Scan(&n)
	return n, err
}

// userKey identifica al usuario del evento: el id, el correo, el nombre o la IP.
func userKey(u *event.User) string {
	if u == nil {
		return ""
	}
	for _, v := range []string{u.ID, u.Email, u.Username, u.IPAddress} {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

// countUser devuelve cuántos usuarios distintos lleva el issue para la regla.
// Sin filtros son los de user_count, también los de antes de crear la regla.
// Con filtros, apunta el usuario del evento entre los de la regla y el issue,
// así que cuenta desde que existe la regla; guarda como mucho limit+1: basta
// para saber que se ha pasado.
func countUser(ctx context.Context, tx db.Tx, r store.Rule, issueID int64, ev *event.Event, limit int) (int, error) {
	var n int
	if !r.Conditions.Filtered() {
		err := tx.QueryRow(ctx, `SELECT user_count FROM issues WHERE id = $1`, issueID).Scan(&n)
		return n, err
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM alert_rule_users WHERE rule_id = $1 AND issue_id = $2`,
		r.ID, issueID).Scan(&n); err != nil {
		return 0, err
	}
	key := userKey(ev.User)
	if key == "" || n > limit {
		return n, nil
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO alert_rule_users (rule_id, issue_id, user_key) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
		r.ID, issueID, key)
	if err != nil {
		return 0, err
	}
	return n + int(tag.RowsAffected()), nil
}

func (a *Alerter) enqueueRule(ctx context.Context, tx db.Tx, r store.Rule, kind string, detail *ruleDetail,
	ev *event.Event, res ingest.Result, now time.Time) error {
	var raw json.RawMessage
	if detail != nil {
		raw, _ = json.Marshal(detail)
	}
	var slackIDs []int64
	var linearID int64
	for _, act := range r.Actions {
		var id int64
		err := tx.QueryRow(ctx, `
			INSERT INTO alert_outbox (issue_id, channel, kind, release, environment, event_at, resolved_at,
				next_attempt_at, created_at, rule_id, action_id, detail)
			VALUES ($1, $2, $3, NULLIF($4, ''), NULLIF($5, ''), $6, $7, $8, $8, $9, $10, $11)
			RETURNING id`,
			res.IssueID, act.Type, kind, ev.Release, ev.Environment, ev.Timestamp, res.ResolvedAt, now, r.ID, act.ID, raw).Scan(&id)
		if err != nil {
			return err
		}
		switch {
		case act.Type == store.ActionSlack:
			slackIDs = append(slackIDs, id)
		case act.Type == store.ActionLinear && linearID == 0:
			linearID = id
		}
	}
	if linearID != 0 && len(slackIDs) > 0 {
		_, err := tx.Exec(ctx, `UPDATE alert_outbox SET waits_for = $2 WHERE `+tx.Dialect().InList("id", "$1"), slackIDs, linearID)
		return err
	}
	return nil
}

// matches dice si el evento pasa los filtros de entorno, nivel y etiquetas.
func matches(c store.RuleConditions, ev *event.Event) bool {
	if len(c.Environments) > 0 && !slices.Contains(c.Environments, ev.Environment) {
		return false
	}
	if len(c.Levels) > 0 && !slices.Contains(c.Levels, ev.Level) {
		return false
	}
	for _, t := range c.Tags {
		v, ok := ev.Tags[t.Key]
		if !ok || (t.Value != "" && v != t.Value) {
			return false
		}
	}
	return true
}

// ruleSender prepara el envío de un aviso de una regla con la acción como
// está ahora en la regla.
func (a *Alerter) ruleSender(ctx context.Context, tx db.Tx, p *pending) (sender, error) {
	var actions []store.RuleAction
	err := tx.QueryRow(ctx, `SELECT name, actions FROM alert_rules WHERE id = $1`, p.ruleID).Scan(&p.ruleName, &actions)
	if errors.Is(err, db.ErrNoRows) {
		return nil, errGone
	}
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(actions, func(x store.RuleAction) bool { return x.ID == p.actionID })
	if i < 0 {
		return nil, errGone
	}
	snd, err := a.actionSender(actions[i])
	if g, ok := snd.(*github); ok {
		g.ruleID = p.ruleID
	}
	return snd, err
}

func (a *Alerter) actionSender(act store.RuleAction) (sender, error) {
	switch act.Type {
	case store.ActionSlack:
		return &slack{url: act.URL, client: a.dest}, nil
	case store.ActionLinear:
		if a.linearKey == "" {
			return nil, errors.New("linear: LINEAR_API_KEY is missing")
		}
		team := act.TeamID
		if team == "" {
			team = a.linearTeam
		}
		if team == "" {
			return nil, errors.New("linear: the action has no team and LINEAR_TEAM_ID is missing")
		}
		labels := act.LabelIDs
		if len(labels) == 0 {
			labels = a.linearLabels
		}
		return a.linearFor(team, labels), nil
	case store.ActionEmail:
		if a.smtp == nil {
			return nil, errors.New("email: SMTP_URL is missing")
		}
		return &email{st: a.st, cfg: a.smtp, to: act.To}, nil
	case store.ActionDiscord:
		return &discord{url: act.URL, client: a.dest}, nil
	case store.ActionTelegram:
		return &telegram{api: a.telegramAPI, token: act.BotToken, chat: act.ChatID, client: a.client}, nil
	case store.ActionNtfy:
		return &ntfy{url: act.URL, token: act.Token, client: a.dest}, nil
	case store.ActionWebhook:
		return &webhook{st: a.st, url: act.URL, secret: act.Secret, client: a.dest, now: a.now}, nil
	case store.ActionGitHub:
		token := act.Token
		if token == "" {
			token = a.githubToken
		}
		if token == "" {
			return nil, errors.New("github: the action has no token and GITHUB_TOKEN is missing")
		}
		return &github{st: a.st, api: &githubAPI{endpoint: a.githubEndpoint, token: token, client: a.client},
			repo: act.Repo, labels: act.Labels, actionID: act.ID}, nil
	}
	return nil, fmt.Errorf("%w: %q", errGone, act.Type)
}

// linearFor da el cliente de Linear del equipo y las etiquetas; se guarda para
// no buscar cada vez su estado unstarted.
func (a *Alerter) linearFor(team string, labels []string) *linear {
	key := team + "|" + strings.Join(labels, ",")
	a.mu.Lock()
	defer a.mu.Unlock()
	if l, ok := a.linears[key]; ok {
		return l
	}
	// En el equipo del servidor, la issue de un error es una sola, la cree un aviso o se cree a mano.
	l := &linear{st: a.st, endpoint: a.linearEndpoint, key: a.linearKey, client: a.client, team: team, labels: labels,
		shared: team == a.linearTeam}
	a.linears[key] = l
	return l
}

// CheckRule comprueba que el servidor tiene lo que piden las acciones de la
// regla: LINEAR_API_KEY y un equipo para Linear, y SMTP_URL para el correo.
func (a *Alerter) CheckRule(r store.Rule) error {
	for _, act := range r.Actions {
		switch act.Type {
		case store.ActionLinear:
			if a.linearKey == "" {
				return fmt.Errorf("%w: alerting in Linear needs LINEAR_API_KEY on the server", store.ErrInvalid)
			}
			if act.TeamID == "" && a.linearTeam == "" {
				return fmt.Errorf("%w: the Linear action needs team_id: there is no LINEAR_TEAM_ID", store.ErrInvalid)
			}
		case store.ActionEmail:
			if a.smtp == nil {
				return fmt.Errorf("%w: alerting by email needs SMTP_URL on the server", store.ErrInvalid)
			}
		case store.ActionGitHub:
			if act.Token == "" && a.githubToken == "" {
				return fmt.Errorf("%w: the GitHub action needs a token: there is no GITHUB_TOKEN on the server", store.ErrInvalid)
			}
		}
	}
	return nil
}

// ErrTestFailed envuelve el fallo de un aviso de prueba.
var ErrTestFailed = errors.New("the test alert failed")

// TestAction manda ya, sin pasar por la tabla de salida, un aviso de prueba
// por la acción actionID de la regla.
func (a *Alerter) TestAction(ctx context.Context, r store.Rule, actionID string) error {
	i := slices.IndexFunc(r.Actions, func(x store.RuleAction) bool { return x.ID == actionID })
	if i < 0 {
		return store.ErrNotFound
	}
	if err := a.CheckRule(store.Rule{Actions: r.Actions[i : i+1]}); err != nil {
		return err
	}
	project, err := a.st.ProjectByID(ctx, r.ProjectID)
	if err != nil {
		return err
	}
	snd, err := a.actionSender(r.Actions[i])
	if err != nil {
		return fmt.Errorf("%w: %v", ErrTestFailed, err)
	}
	p := testPending(project, r, a.now().UTC(), a.lang)
	if a.base != "" {
		p.link = fmt.Sprintf("%s/projects/%d/settings", a.base, project.ID)
	}
	if err := snd.send(ctx, p); err != nil {
		a.log.WarnContext(ctx, "test alert failed", "rule", r.ID, "action", actionID, "err", err)
		return fmt.Errorf("%w: %s", ErrTestFailed, publicError(err))
	}
	return nil
}

func testPending(project store.Project, r store.Rule, now time.Time, l lang.Lang) *pending {
	title := l.T("Mothlog test alert", "Aviso de prueba de Mothlog")
	ev := &event.Event{
		ID: strings.ReplaceAll(newUUID(), "-", ""), Timestamp: now, Level: "info",
		Message: l.F("If you see this alert, the action of rule “%s” works.", "Si ves este aviso, la acción de la regla «%s» funciona.", r.Name),
		SDK:     event.SDK{Name: "mothlog", Version: "test"},
	}
	issue := store.Issue{ProjectID: project.ID, Project: project.Name, Title: title, Level: "info", Status: "open",
		Count: 1, FirstSeen: now, LastSeen: now}
	return &pending{
		l: l, kind: kindTest, test: true, eventAt: now, createID: newUUID(), ruleID: r.ID, ruleName: r.Name,
		project: project.Name, labels: project.LinearLabelIDs, title: title, count: 1, ev: ev,
		issue: &store.IssueDetail{Issue: issue, LatestEvent: &store.StoredEvent{EventID: ev.ID, ProjectID: project.ID,
			OccurredAt: now, ReceivedAt: now, Event: *ev}},
	}
}
