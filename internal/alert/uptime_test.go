package alert

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/intake-es/intake-errors/internal/store"
)

func (e *env) uptimeMonitor(t *testing.T, slug string) store.UptimeMonitor {
	t.Helper()
	u, threshold := "https://example.com/"+slug, 1
	m, err := e.db.CreateUptimeMonitor(context.Background(), e.project, store.UptimeInput{Slug: &slug, URL: &u,
		FailureThreshold: &threshold})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func (e *env) uptimeCheck(t *testing.T, m store.UptimeMonitor, ok bool, at time.Time) {
	t.Helper()
	code := 200
	c := store.UptimeCheck{CheckedAt: at, OK: ok, StatusCode: &code, DurationMs: 80}
	if !ok {
		code = 502
		c.Error = "código 502"
	}
	if _, err := e.db.RecordUptimeCheck(context.Background(), m.ID, c, e.alerter); err != nil {
		t.Fatal(err)
	}
}

var uptimeT0 = time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)

// Sin reglas de disponibilidad avisan los canales por defecto, al caer y al
// volver; Linear solo crea la issue de la caída.
func TestUptimeDefaultAlerts(t *testing.T) {
	e := newEnv(t, nil)
	e.addRule(t, webhookRule(serve(t, &recorder{}), store.RuleConditions{Cron: &store.CronCondition{Failed: true}}))
	m := e.uptimeMonitor(t, "home")
	e.uptimeCheck(t, m, false, uptimeT0)
	e.uptimeCheck(t, m, true, uptimeT0.Add(90*time.Second))
	if n := e.send(t); n != 4 {
		t.Fatalf("enviados = %d; quería caída y vuelta por Slack y Linear", n)
	}
	msgs := e.slack.payloads(t)
	if len(msgs) != 2 || msgs[0].Text != "Monitor caído en backend: home" || msgs[1].Text != "Monitor recuperado en backend: home" {
		t.Fatalf("slack = %+v", msgs)
	}
	body, _ := json.Marshal(msgs[1])
	if !strings.Contains(string(body), "Caído durante") || !strings.Contains(string(body), "1 min 30 s") {
		t.Errorf("vuelta = %s", body)
	}
	if !slices.Equal(buttons(msgs[0]), []string{"Ver el monitor"}) {
		t.Errorf("botones = %v", buttons(msgs[0]))
	}
	issues, order := e.linear.snapshot()
	if len(order) != 1 || !strings.HasPrefix(issues[order[0]].Title, "[backend] Monitor caído: home") ||
		!strings.Contains(issues[order[0]].Description, "GET https://example.com/home") {
		t.Errorf("linear = %+v", issues)
	}
}

// Una regla de disponibilidad no quita los avisos por defecto de los errores
// ni de los crons, y avisa solo de los monitores y los tipos que pide.
func TestUptimeRuleConditions(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.LinearAPIKey = "" })
	downOnly, home := &recorder{}, &recorder{}
	e.addRule(t, webhookRule(serve(t, downOnly), store.RuleConditions{Uptime: &store.UptimeCondition{Down: true}}))
	e.addRule(t, webhookRule(serve(t, home), store.RuleConditions{Uptime: &store.UptimeCondition{Down: true, Up: true,
		Monitors: []string{"home"}}}))
	a, b := e.uptimeMonitor(t, "home"), e.uptimeMonitor(t, "api")
	e.uptimeCheck(t, a, false, uptimeT0)
	e.uptimeCheck(t, b, false, uptimeT0)
	e.uptimeCheck(t, a, true, uptimeT0.Add(time.Minute))
	e.uptimeCheck(t, b, true, uptimeT0.Add(time.Minute))
	e.checkIn(t, "copia", "error", "production", cronT0)
	e.send(t)

	kinds := func(r *recorder) []string {
		var out []string
		for _, req := range r.all() {
			var pl WebhookPayload
			if err := json.Unmarshal(req.Body, &pl); err != nil {
				t.Fatal(err)
			}
			out = append(out, pl.Uptime.Slug+":"+pl.Type)
		}
		return out
	}
	if got := kinds(downOnly); !slices.Equal(got, []string{"home:uptime_down", "api:uptime_down"}) {
		t.Errorf("caídas = %v", got)
	}
	if got := kinds(home); !slices.Equal(got, []string{"home:uptime_down", "home:uptime_up"}) {
		t.Errorf("home = %v", got)
	}
	if got := e.slack.messages(); len(got) != 1 || !strings.HasPrefix(got[0], "Cron fallido en backend") {
		t.Errorf("slack = %v", got)
	}
	r := home.all()[1]
	checkSignature(t, r, webhookSecret)
	if r.Header.Get(HeaderEvent) != store.KindUptimeUp {
		t.Errorf("cabecera = %q", r.Header.Get(HeaderEvent))
	}
}

func TestUptimeRuleValidation(t *testing.T) {
	r := webhookRule("https://hook.example.com", store.RuleConditions{Uptime: &store.UptimeCondition{}})
	r.Name = "x"
	if _, err := r.Normalize(); err == nil || !strings.Contains(err.Error(), "down o up") {
		t.Errorf("uptime vacío: %v", err)
	}
	r.Conditions.Uptime = &store.UptimeCondition{Up: true, Monitors: []string{" web ", "web", ""}}
	if _, err := r.Normalize(); err != nil || !slices.Equal(r.Conditions.Uptime.Monitors, []string{"web"}) {
		t.Errorf("uptime = %+v, %v", r.Conditions.Uptime, err)
	}
}

// Cada acción manda la caída sin pedir el issue ni el evento.
func TestUptimeEachAction(t *testing.T) {
	discordSrv, telegramSrv, ntfySrv := &recorder{}, &recorder{reply: `{"ok":true}`}, &recorder{}
	mail := newFakeSMTP(t)
	telegramURL := serve(t, telegramSrv)
	e := newEnv(t, func(o *Options) {
		o.SMTPURL = "smtp://avisos:clave-smtp@" + mail.addr
		o.SMTPFrom = "avisos@errores.test"
		o.TelegramAPIURL = telegramURL
	})
	e.addRule(t, store.Rule{Conditions: store.RuleConditions{Uptime: &store.UptimeCondition{Down: true}}, Actions: []store.RuleAction{
		{Type: store.ActionEmail, To: []string{"guardia@errores.test"}},
		{Type: store.ActionDiscord, URL: serve(t, discordSrv) + "/api/webhooks/1/abc"},
		{Type: store.ActionTelegram, BotToken: "123:token", ChatID: "-100"},
		{Type: store.ActionNtfy, URL: serve(t, ntfySrv) + "/web"},
	}})
	m := e.uptimeMonitor(t, "home")
	e.uptimeCheck(t, m, false, uptimeT0)
	if n := e.send(t); n != 4 {
		t.Fatalf("enviados = %d", n)
	}
	for _, o := range e.outbox(t) {
		if !o.Sent {
			t.Errorf("sin enviar: %+v", o)
		}
	}
	if got := mail.messages(); len(got) != 1 || !strings.Contains(got[0].Data, "Monitor ca") ||
		!strings.Contains(got[0].Data, "Error: c=C3=B3digo 502") && !strings.Contains(got[0].Data, "Error: código 502") {
		t.Errorf("correo = %+v", got)
	}
	var dp discordPayload
	if d := discordSrv.all(); len(d) != 1 || json.Unmarshal(d[0].Body, &dp) != nil || dp.Content != "**Monitor caído** · backend" {
		t.Errorf("discord = %+v", dp)
	}
	if tg := telegramSrv.all(); len(tg) != 1 || !strings.Contains(string(tg[0].Body), "Ver el monitor") {
		t.Errorf("telegram = %+v", tg)
	}
	if nt := ntfySrv.all(); len(nt) != 1 || nt[0].Header.Get("Tags") != "red_circle" || nt[0].Header.Get("Priority") != "high" {
		t.Errorf("ntfy = %+v", nt)
	}
}
