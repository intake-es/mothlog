package alert

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/intake-es/intake-errors/internal/cron"
	"github.com/intake-es/intake-errors/internal/store"
)

func cronConfig() *store.MonitorConfig {
	runtime := 5
	return &store.MonitorConfig{Schedule: cron.Schedule{Type: cron.TypeCrontab, Crontab: "*/10 * * * *"}, MaxRuntime: &runtime}
}

func (e *env) checkIn(t *testing.T, slug, status, env string, at time.Time) store.CheckInResult {
	t.Helper()
	res, err := e.db.ProcessCheckIn(context.Background(), e.project, store.CheckInInput{Slug: slug, Status: status,
		Environment: env, Release: "1.2.0", Config: cronConfig()}, at, e.alerter)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func (e *env) sweep(t *testing.T, at time.Time) {
	t.Helper()
	if _, _, err := e.db.SweepMonitors(context.Background(), at, e.alerter); err != nil {
		t.Fatal(err)
	}
}

var cronT0 = time.Date(2026, 11, 22, 10, 0, 30, 0, time.UTC)

// Sin reglas, avisan por los canales por defecto los crons que fallan, no
// llegan o se pasan de tiempo; la vuelta a ir bien no.
func TestCronDefaultAlerts(t *testing.T) {
	e := newEnv(t, nil)
	e.checkIn(t, "falla", "error", "production", cronT0)
	e.checkIn(t, "colgado", "in_progress", "production", cronT0)
	e.checkIn(t, "deja-de-ir", "ok", "production", cronT0)
	e.sweep(t, cronT0.Add(6*time.Minute))
	e.checkIn(t, "falla", "ok", "production", cronT0.Add(7*time.Minute))
	e.sweep(t, cronT0.Add(12*time.Minute))
	// A las 10:12 no ha llegado el de las 10:10 de ninguno de los tres.
	if n := e.send(t); n != 10 {
		t.Fatalf("enviados = %d; quería 10: fallo, timeout y tres missed, por Slack y Linear", n)
	}
	var kinds []string
	for _, o := range e.outbox(t) {
		if !o.Sent {
			t.Errorf("sin enviar: %+v", o)
		}
		if o.Channel == channelSlack {
			kinds = append(kinds, o.Kind)
		}
	}
	want := []string{store.KindCronFailed, store.KindCronTimeout, store.KindCronMissed, store.KindCronMissed, store.KindCronMissed}
	if !slices.Equal(kinds, want) {
		t.Errorf("avisos = %v; quería %v", kinds, want)
	}
	msgs := e.slack.payloads(t)
	if len(msgs) != 5 || msgs[0].Text != "Cron fallido en backend: falla" || !slices.Equal(buttons(msgs[0]), []string{"Ver el monitor"}) {
		t.Fatalf("slack = %+v", msgs)
	}
	body, _ := json.Marshal(msgs[1])
	if !strings.Contains(string(body), "Cron pasado de tiempo") || !strings.Contains(string(body), "5 min") {
		t.Errorf("timeout = %s", body)
	}
	issues, order := e.linear.snapshot()
	if len(order) != 5 || !strings.HasPrefix(issues[order[0]].Title, "[backend] Cron fallido: falla") ||
		!strings.Contains(issues[order[0]].Description, fmt.Sprintf("%s/monitors/%d/falla", publicURL, e.project)) {
		t.Errorf("linear = %+v", issues[order[0]])
	}
}

// Una regla con condiciones de cron no quita los avisos por defecto de los errores.
func TestCronRuleKeepsIssueDefaults(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.LinearAPIKey = "" })
	hook := &recorder{}
	e.addRule(t, webhookRule(serve(t, hook), store.RuleConditions{Cron: &store.CronCondition{Failed: true}}))
	e.ingest(t, "api-tarea-a-1")
	e.checkIn(t, "falla", "error", "production", cronT0)
	if n := e.send(t); n != 2 {
		t.Fatalf("enviados = %d", n)
	}
	if got := e.slack.messages(); len(got) != 1 || !strings.HasPrefix(got[0], "Nuevo error en backend") {
		t.Errorf("slack = %v", got)
	}
	if reqs := hook.all(); len(reqs) != 1 || reqs[0].Header.Get(HeaderEvent) != store.KindCronFailed {
		t.Errorf("webhook = %+v", reqs)
	}
}

// Una regla solo de errores no quita los avisos por defecto de los crons.
func TestIssueRuleKeepsCronDefaults(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.LinearAPIKey = "" })
	hook := &recorder{}
	e.addRule(t, webhookRule(serve(t, hook), store.RuleConditions{New: true}))
	e.checkIn(t, "falla", "error", "production", cronT0)
	if n := e.send(t); n != 1 {
		t.Fatalf("enviados = %d", n)
	}
	if got := e.slack.messages(); len(got) != 1 || !strings.HasPrefix(got[0], "Cron fallido en backend") {
		t.Errorf("slack = %v", got)
	}
	if len(hook.all()) != 0 {
		t.Error("la regla de errores avisa de un cron")
	}
}

func TestCronRuleConditions(t *testing.T) {
	e := newEnv(t, nil)
	all, onlyA, prod := &recorder{}, &recorder{}, &recorder{}
	e.addRule(t, webhookRule(serve(t, all), store.RuleConditions{Cron: &store.CronCondition{
		Failed: true, Missed: true, Timeout: true, Recovered: true}}))
	e.addRule(t, webhookRule(serve(t, onlyA), store.RuleConditions{Cron: &store.CronCondition{
		Failed: true, Monitors: []string{"a"}}}))
	e.addRule(t, webhookRule(serve(t, prod), store.RuleConditions{Environments: []string{"production"},
		Cron: &store.CronCondition{Recovered: true}}))

	e.checkIn(t, "a", "error", "production", cronT0)
	e.checkIn(t, "b", "error", "staging", cronT0)
	e.checkIn(t, "a", "ok", "production", cronT0.Add(time.Minute))
	e.checkIn(t, "b", "ok", "staging", cronT0.Add(time.Minute))
	e.send(t)

	kinds := func(r *recorder) []string {
		var out []string
		for _, req := range r.all() {
			var pl WebhookPayload
			if err := json.Unmarshal(req.Body, &pl); err != nil {
				t.Fatal(err)
			}
			out = append(out, pl.Monitor.Slug+":"+pl.Type)
		}
		return out
	}
	if got := kinds(all); !slices.Equal(got, []string{"a:cron_failed", "b:cron_failed", "a:cron_recovered", "b:cron_recovered"}) {
		t.Errorf("todos = %v", got)
	}
	if got := kinds(onlyA); !slices.Equal(got, []string{"a:cron_failed"}) {
		t.Errorf("solo a = %v", got)
	}
	if got := kinds(prod); !slices.Equal(got, []string{"a:cron_recovered"}) {
		t.Errorf("production = %v", got)
	}
	// Con reglas de cron, los canales por defecto no avisan de los crons.
	if got := e.slack.messages(); len(got) != 0 {
		t.Errorf("slack = %v", got)
	}

	r := all.all()[0]
	checkSignature(t, r, webhookSecret)
	var pl WebhookPayload
	_ = json.Unmarshal(r.Body, &pl)
	if pl.Issue != nil || pl.Event != nil || pl.Monitor == nil || pl.CheckIn == nil || pl.Project != "backend" ||
		pl.Monitor.URL != fmt.Sprintf("%s/monitors/%d/a", publicURL, e.project) || pl.Monitor.Schedule.Crontab != "*/10 * * * *" ||
		pl.CheckIn.Status != "error" || pl.CheckIn.Release != "1.2.0" || pl.CheckIn.Environment != "production" ||
		!strings.HasPrefix(pl.Markdown, "# Cron fallido: a") {
		t.Errorf("payload = %s", r.Body)
	}
}

func TestCronRuleValidation(t *testing.T) {
	r := webhookRule("https://hook.example.com", store.RuleConditions{Cron: &store.CronCondition{}})
	r.Name = "x"
	if _, err := r.Normalize(); err == nil || !strings.Contains(err.Error(), "failed, missed, timeout o recovered") {
		t.Errorf("cron vacío: %v", err)
	}
	r.Conditions.Cron = &store.CronCondition{Missed: true, Monitors: []string{" copia ", "copia", ""}}
	if _, err := r.Normalize(); err != nil || !slices.Equal(r.Conditions.Cron.Monitors, []string{"copia"}) {
		t.Errorf("cron = %+v, %v", r.Conditions.Cron, err)
	}
}

// Cada acción manda el aviso de cron sin pedir el issue ni el evento.
func TestCronEachAction(t *testing.T) {
	slackSrv, discordSrv, telegramSrv, ntfySrv, hookSrv := &recorder{}, &recorder{}, &recorder{reply: `{"ok":true}`}, &recorder{}, &recorder{}
	mail := newFakeSMTP(t)
	telegramURL := serve(t, telegramSrv)
	e := newEnv(t, func(o *Options) {
		o.SMTPURL = "smtp://avisos:clave-smtp@" + mail.addr
		o.SMTPFrom = "avisos@errores.test"
		o.TelegramAPIURL = telegramURL
	})
	e.addRule(t, store.Rule{Conditions: store.RuleConditions{Cron: &store.CronCondition{Missed: true}}, Actions: []store.RuleAction{
		{Type: store.ActionSlack, URL: serve(t, slackSrv) + "/services/T/B/x"},
		{Type: store.ActionLinear},
		{Type: store.ActionEmail, To: []string{"guardia@errores.test"}},
		{Type: store.ActionDiscord, URL: serve(t, discordSrv) + "/api/webhooks/1/abc"},
		{Type: store.ActionTelegram, BotToken: "123:token", ChatID: "-100"},
		{Type: store.ActionNtfy, URL: serve(t, ntfySrv) + "/crons"},
		{Type: store.ActionWebhook, URL: serve(t, hookSrv) + "/hook", Secret: webhookSecret},
	}})
	e.checkIn(t, "copia", "ok", "production", cronT0)
	e.sweep(t, cronT0.Add(12*time.Minute))
	if n := e.send(t); n != 7 {
		t.Fatalf("enviados = %d", n)
	}
	for _, o := range e.outbox(t) {
		if !o.Sent {
			t.Errorf("sin enviar: %+v", o)
		}
	}
	if m := mail.messages(); len(m) != 1 || !strings.Contains(m[0].Data, "Cron sin ejecutar en backend: copia") ||
		!strings.Contains(m[0].Data, "Margen: 1 min") {
		t.Errorf("correo = %+v", m)
	}
	var dp discordPayload
	if d := discordSrv.all(); len(d) != 1 || json.Unmarshal(d[0].Body, &dp) != nil || dp.Content != "**Cron sin ejecutar** · backend · production" {
		t.Errorf("discord = %+v", dp)
	}
	if tg := telegramSrv.all(); len(tg) != 1 || !strings.Contains(string(tg[0].Body), "Ver el monitor") {
		t.Errorf("telegram = %+v", tg)
	}
	if nt := ntfySrv.all(); len(nt) != 1 || nt[0].Header.Get("Tags") != "alarm_clock" || nt[0].Header.Get("Priority") != "high" {
		t.Errorf("ntfy = %+v", nt)
	}
	if _, order := e.linear.snapshot(); len(order) != 1 {
		t.Errorf("linear = %d", len(order))
	}
}
