package alert

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/intake-es/intake-errors/internal/event"
	"github.com/intake-es/intake-errors/internal/ingest"
	"github.com/intake-es/intake-errors/internal/store"
	"github.com/intake-es/intake-errors/internal/store/storetest"
)

type recorded struct {
	Path   string
	Header http.Header
	Body   []byte
}

// recorder es un servidor falso que guarda lo que recibe, también lo que rechaza.
type recorder struct {
	mu       sync.Mutex
	reqs     []recorded
	failWith int
	reply    string
}

func (f *recorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, recorded{Path: r.URL.Path, Header: r.Header.Clone(), Body: body})
	if f.failWith != 0 {
		http.Error(w, "fallo de prueba", f.failWith)
		return
	}
	if f.reply != "" {
		_, _ = w.Write([]byte(f.reply))
	}
}

func (f *recorder) all() []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.reqs)
}

func (f *recorder) setFail(code int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failWith = code
}

func serve(t *testing.T, h http.Handler) string {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv.URL
}

type smtpMsg struct {
	From string
	To   []string
	Auth string
	Data string
}

// fakeSMTP habla lo justo de SMTP para recibir un correo con AUTH PLAIN.
type fakeSMTP struct {
	mu   sync.Mutex
	msgs []smtpMsg
	addr string
}

func newFakeSMTP(t *testing.T) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	f := &fakeSMTP{addr: ln.Addr().String()}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	return f
}

func (f *fakeSMTP) serve(c net.Conn) {
	defer func() { _ = c.Close() }()
	tp := textproto.NewConn(c)
	var m smtpMsg
	_ = tp.PrintfLine("220 smtp falso")
	for {
		line, err := tp.ReadLine()
		if err != nil {
			return
		}
		cmd := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(cmd, "EHLO"):
			_ = tp.PrintfLine("250-smtp falso")
			_ = tp.PrintfLine("250 AUTH PLAIN")
		case strings.HasPrefix(cmd, "AUTH PLAIN "):
			raw, _ := base64.StdEncoding.DecodeString(line[len("AUTH PLAIN "):])
			m.Auth = strings.ReplaceAll(string(raw), "\x00", "|")
			_ = tp.PrintfLine("235 dentro")
		case strings.HasPrefix(cmd, "MAIL FROM:"):
			m.From = strings.Trim(line[len("MAIL FROM:"):], "<> ")
			_ = tp.PrintfLine("250 ok")
		case strings.HasPrefix(cmd, "RCPT TO:"):
			m.To = append(m.To, strings.Trim(line[len("RCPT TO:"):], "<> "))
			_ = tp.PrintfLine("250 ok")
		case cmd == "DATA":
			_ = tp.PrintfLine("354 adelante")
			lines, err := tp.ReadDotLines()
			if err != nil {
				return
			}
			m.Data = strings.Join(lines, "\n")
			f.mu.Lock()
			f.msgs = append(f.msgs, m)
			f.mu.Unlock()
			m = smtpMsg{}
			_ = tp.PrintfLine("250 recibido")
		case cmd == "QUIT":
			_ = tp.PrintfLine("221 adiós")
			return
		default:
			_ = tp.PrintfLine("250 ok")
		}
	}
}

func (f *fakeSMTP) messages() []smtpMsg {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.msgs)
}

func (e *env) addRule(t *testing.T, r store.Rule) store.Rule {
	t.Helper()
	if r.Name == "" {
		r.Name = "regla de prueba"
	}
	r.Enabled = true
	out, _, err := e.db.CreateRule(context.Background(), e.project, r)
	if err != nil {
		t.Fatal(err)
	}
	e.alerter.RulesChanged(e.project)
	return out
}

func (e *env) ingestEvent(t *testing.T, ev event.Event) ingest.Result {
	t.Helper()
	if ev.Level == "" {
		ev.Level = "error"
	}
	if ev.Message == "" {
		ev.Message = "fallo de prueba"
	}
	res, err := e.ing.Ingest(context.Background(), e.project, &ev)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

const webhookSecret = "secreto-de-la-regla"

func webhookRule(url string, c store.RuleConditions) store.Rule {
	return store.Rule{Conditions: c, Actions: []store.RuleAction{{Type: store.ActionWebhook, URL: url, Secret: webhookSecret}}}
}

// checkSignature comprueba la firma como lo haría quien recibe el webhook.
func checkSignature(t *testing.T, r recorded, secret string) {
	t.Helper()
	var ts, sig string
	for part := range strings.SplitSeq(r.Header.Get(HeaderSignature), ",") {
		k, v, _ := strings.Cut(part, "=")
		switch k {
		case "t":
			ts = v
		case "v1":
			sig = v
		}
	}
	unix, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		t.Fatalf("firma sin fecha: %q", r.Header.Get(HeaderSignature))
	}
	if d := time.Since(time.Unix(unix, 0)).Abs(); d > 5*time.Minute {
		t.Errorf("la fecha de la firma está a %v", d)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "."))
	mac.Write(r.Body)
	want := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(sig), []byte(want)) {
		t.Errorf("firma = %q, quiero %q", sig, want)
	}
}

func TestRuleWebhookSigned(t *testing.T) {
	e := newEnv(t, nil)
	hook := &recorder{}
	e.addRule(t, webhookRule(serve(t, hook)+"/hook", store.RuleConditions{New: true}))
	res := e.ingest(t, "api-tarea-a-1")
	if n := e.send(t); n != 1 {
		t.Fatalf("enviados = %d", n)
	}
	reqs := hook.all()
	if len(reqs) != 1 {
		t.Fatalf("webhook = %d peticiones", len(reqs))
	}
	r := reqs[0]
	checkSignature(t, r, webhookSecret)
	if r.Header.Get("Content-Type") != "application/json" || r.Header.Get(HeaderEvent) != "new" || r.Header.Get(HeaderDelivery) == "" {
		t.Errorf("cabeceras = %v", r.Header)
	}
	var pl WebhookPayload
	if err := json.Unmarshal(r.Body, &pl); err != nil {
		t.Fatal(err)
	}
	link := fmt.Sprintf("%s/issues/%d", publicURL, res.IssueID)
	if pl.Type != "new" || pl.Project != "backend" || pl.Issue.ID != res.IssueID || pl.Issue.URL != link || pl.Issue.Count != 1 ||
		pl.Rule.Name != "regla de prueba" || pl.Event == nil || pl.Event.Environment != "production" ||
		!strings.HasPrefix(pl.Event.Error, "task failed id=88dfc065281046dfa0098d0fa91bbdf4") || pl.Event.Release != "fixture-release-1" {
		t.Errorf("payload = %+v", pl)
	}
	if !strings.HasPrefix(pl.Markdown, "# task failed") || !strings.Contains(pl.Markdown, "- Proyecto: backend") {
		t.Errorf("markdown:\n%s", pl.Markdown)
	}
	// Una firma con otro secreto no vale.
	if Sign("otro", time.Now(), r.Body) == r.Header.Get(HeaderSignature) {
		t.Error("la firma no depende del secreto")
	}
}

// TestRuleEachAction manda un error nuevo por cada tipo de acción, cada una
// contra su servidor falso.
func TestRuleEachAction(t *testing.T) {
	slackSrv, discordSrv, telegramSrv, ntfySrv, hookSrv := &recorder{}, &recorder{}, &recorder{reply: `{"ok":true}`}, &recorder{}, &recorder{}
	mail := newFakeSMTP(t)
	telegramURL := serve(t, telegramSrv)
	e := newEnv(t, func(o *Options) {
		o.SMTPURL = "smtp://avisos:clave-smtp@" + mail.addr
		o.SMTPFrom = "intake-errors <avisos@errores.test>"
		o.TelegramAPIURL = telegramURL
	})
	if err := e.db.SetProjectLabels(context.Background(), "backend", []string{"label-proyecto"}); err != nil {
		t.Fatal(err)
	}
	e.addRule(t, store.Rule{Conditions: store.RuleConditions{New: true}, Actions: []store.RuleAction{
		{Type: store.ActionSlack, URL: serve(t, slackSrv) + "/services/T/B/x"},
		{Type: store.ActionLinear, LabelIDs: []string{"label-regla"}},
		{Type: store.ActionEmail, To: []string{"guardia@errores.test", "equipo@errores.test"}},
		{Type: store.ActionDiscord, URL: serve(t, discordSrv) + "/api/webhooks/1/abc"},
		{Type: store.ActionTelegram, BotToken: "123:token-del-bot", ChatID: "-100200"},
		{Type: store.ActionNtfy, URL: serve(t, ntfySrv) + "/errores-backend", Token: "tk_ntfy"},
		{Type: store.ActionWebhook, URL: serve(t, hookSrv) + "/hook", Secret: webhookSecret},
	}})
	res := e.ingest(t, "api-tarea-a-1")
	if n := e.send(t); n != 7 {
		t.Fatalf("enviados = %d, quiero 7", n)
	}
	for _, o := range e.outbox(t) {
		if !o.Sent {
			t.Errorf("sin enviar: %+v", o)
		}
	}
	link := fmt.Sprintf("%s/issues/%d", publicURL, res.IssueID)
	head := "task failed id=88dfc065281046dfa0098d0fa91bbdf4"
	evidence := map[string]string{}

	// Slack: el mismo mensaje que sin reglas, con el botón de Linear porque espera a su issue.
	sl := slackSrv.all()
	if len(sl) != 1 {
		t.Fatalf("slack = %d", len(sl))
	}
	var sp slackPayload
	_ = json.Unmarshal(sl[0].Body, &sp)
	if !strings.HasPrefix(sp.Text, "Nuevo error en backend: "+head) || !slices.Equal(buttons(sp), []string{"Ver el error", "ERR-101 en Linear"}) {
		t.Errorf("slack = %s", sl[0].Body)
	}
	evidence["slack"] = string(sl[0].Body)

	issues, order := e.linear.snapshot()
	if len(order) != 1 {
		t.Fatalf("linear = %d issues", len(order))
	}
	li := issues[order[0]]
	if !slices.Equal(li.Labels, []string{"label-regla", "label-proyecto"}) || !strings.HasPrefix(li.Title, "[backend] "+head) {
		t.Errorf("linear = %+v", li)
	}
	evidence["linear"] = li.Title + "\netiquetas: " + strings.Join(li.Labels, ", ") + "\n\n" + li.Description

	msgs := mail.messages()
	if len(msgs) != 1 {
		t.Fatalf("correos = %d", len(msgs))
	}
	m := msgs[0]
	if m.From != "avisos@errores.test" || !slices.Equal(m.To, []string{"guardia@errores.test", "equipo@errores.test"}) ||
		m.Auth != "|avisos|clave-smtp" {
		t.Errorf("correo = %+v", m)
	}
	for _, want := range []string{"Subject: [intake-errors] Nuevo error en backend: task failed", "To: guardia@errores.test, equipo@errores.test",
		"Content-Type: text/plain; charset=utf-8", head, "Nuevo error · backend · production", link, "# task failed"} {
		if !strings.Contains(m.Data, want) {
			t.Errorf("el correo no lleva %q:\n%s", want, m.Data)
		}
	}
	evidence["correo"] = "MAIL FROM: " + m.From + "\nRCPT TO: " + strings.Join(m.To, ", ") + "\n\n" + m.Data

	dc := discordSrv.all()
	var dp discordPayload
	if len(dc) != 1 || json.Unmarshal(dc[0].Body, &dp) != nil {
		t.Fatalf("discord = %v", dc)
	}
	if dp.Content != "**Nuevo error** · backend · production" || len(dp.Embeds) != 1 || !strings.HasPrefix(dp.Embeds[0].Title, head) ||
		dp.Embeds[0].URL != link || dp.AllowedMentions["parse"] == nil {
		t.Errorf("discord = %s", dc[0].Body)
	}
	evidence["discord"] = string(dc[0].Body)

	tg := telegramSrv.all()
	if len(tg) != 1 || tg[0].Path != "/bot123:token-del-bot/sendMessage" {
		t.Fatalf("telegram = %+v", tg)
	}
	var tp struct {
		ChatID    string `json:"chat_id"`
		Text      string
		ParseMode string `json:"parse_mode"`
	}
	_ = json.Unmarshal(tg[0].Body, &tp)
	if tp.ChatID != "-100200" || tp.ParseMode != "HTML" || !strings.Contains(tp.Text, "<b>Nuevo error</b> · backend · production") ||
		!strings.Contains(tp.Text, `<a href="`+link+`">Ver el error</a>`) {
		t.Errorf("telegram = %s", tg[0].Body)
	}
	evidence["telegram"] = tg[0].Path + "\n" + string(tg[0].Body)

	nt := ntfySrv.all()
	if len(nt) != 1 {
		t.Fatalf("ntfy = %d", len(nt))
	}
	h := nt[0].Header
	if nt[0].Path != "/errores-backend" || h.Get("Authorization") != "Bearer tk_ntfy" || h.Get("Click") != link ||
		h.Get("Priority") != "high" || h.Get("Tags") != "rotating_light" || !strings.Contains(h.Get("Title"), "Nuevo_error") ||
		!strings.HasPrefix(string(nt[0].Body), head) {
		t.Errorf("ntfy = %v\n%s", h, nt[0].Body)
	}
	evidence["ntfy"] = fmt.Sprintf("Title: %s\nTags: %s\nPriority: %s\nClick: %s\n\n%s", h.Get("Title"), h.Get("Tags"), h.Get("Priority"), h.Get("Click"), nt[0].Body)

	wh := hookSrv.all()
	if len(wh) != 1 {
		t.Fatalf("webhook = %d", len(wh))
	}
	checkSignature(t, wh[0], webhookSecret)
	evidence["webhook"] = HeaderSignature + ": " + wh[0].Header.Get(HeaderSignature) + "\n" + HeaderEvent + ": " +
		wh[0].Header.Get(HeaderEvent) + "\n\n" + string(wh[0].Body)

	if dir := os.Getenv("TEST_EVIDENCE_DIR"); dir != "" {
		for name, body := range evidence {
			if err := os.WriteFile(filepath.Join(dir, "servidor-falso-"+name+".txt"), []byte(body+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestRuleConditionsNewAndRegressed(t *testing.T) {
	e := newEnv(t, nil)
	newHook, regHook := &recorder{}, &recorder{}
	e.addRule(t, webhookRule(serve(t, newHook), store.RuleConditions{New: true}))
	e.addRule(t, webhookRule(serve(t, regHook), store.RuleConditions{Regressed: true}))
	first := e.ingest(t, "api-tarea-a-1")
	e.ingest(t, "api-tarea-a-1")
	e.send(t)
	if len(newHook.all()) != 1 || len(regHook.all()) != 0 {
		t.Fatalf("tras un error nuevo: nuevo %d, regresión %d", len(newHook.all()), len(regHook.all()))
	}
	if _, err := e.db.SetIssueStatus(context.Background(), first.IssueID, "resolved"); err != nil {
		t.Fatal(err)
	}
	e.ingest(t, "api-tarea-b-1")
	e.send(t)
	if len(newHook.all()) != 1 || len(regHook.all()) != 1 {
		t.Fatalf("tras la regresión: nuevo %d, regresión %d", len(newHook.all()), len(regHook.all()))
	}
	var pl WebhookPayload
	_ = json.Unmarshal(regHook.all()[0].Body, &pl)
	if pl.Type != "regressed" || pl.ResolvedAt == nil {
		t.Errorf("regresión = %+v", pl)
	}
}

func TestRuleFrequency(t *testing.T) {
	e := newEnv(t, nil)
	hook := &recorder{}
	e.addRule(t, webhookRule(serve(t, hook), store.RuleConditions{Frequency: &store.FrequencyCondition{Events: 3, WindowMinutes: 10}}))
	ev := event.Event{Fingerprint: []string{"frecuencia"}}
	for i := range 6 {
		e.ingestEvent(t, ev)
		e.send(t)
		want := 0
		if i >= 3 {
			want = 1
		}
		if got := len(hook.all()); got != want {
			t.Fatalf("evento %d: avisos = %d, quiero %d", i+1, got, want)
		}
	}
	var pl WebhookPayload
	_ = json.Unmarshal(hook.all()[0].Body, &pl)
	if pl.Type != "frequency" || pl.Trigger == nil || pl.Trigger.Events != 3 || pl.Trigger.WindowMinutes != 10 || pl.Issue.Count != 4 {
		t.Errorf("payload = %+v", pl)
	}
	// Pasada la ventana, la cuenta vuelve a empezar.
	e.clock.set(e.clock.now().Add(11 * time.Minute))
	for range 4 {
		e.ingestEvent(t, ev)
	}
	e.send(t)
	if got := len(hook.all()); got != 2 {
		t.Fatalf("tras la ventana: avisos = %d, quiero 2", got)
	}
	// Los eventos de otra hora de la ventana se suman: 2 ahora y 2 dentro de 9 minutos.
	other := event.Event{Fingerprint: []string{"ventana"}}
	e.ingestEvent(t, other)
	e.ingestEvent(t, other)
	e.clock.set(e.clock.now().Add(9 * time.Minute))
	e.ingestEvent(t, other)
	e.send(t)
	if got := len(hook.all()); got != 2 {
		t.Fatalf("3 eventos no pasan de 3: avisos = %d", got)
	}
	e.ingestEvent(t, other)
	e.send(t)
	if got := len(hook.all()); got != 3 {
		t.Fatalf("4 eventos en 10 minutos: avisos = %d, quiero 3", got)
	}
}

func TestRuleFrequencyCountsDroppedEvents(t *testing.T) {
	e := newEnv(t, nil)
	hook := &recorder{}
	e.addRule(t, webhookRule(serve(t, hook), store.RuleConditions{Frequency: &store.FrequencyCondition{Events: 4, WindowMinutes: 60}}))
	ev := event.Event{Level: "error", Message: "x", Fingerprint: []string{"tope"}}
	for range 5 {
		ev := ev
		if _, err := e.ing.IngestWith(context.Background(), e.project, &ev, ingest.Limits{PerIssuePerHour: 1, SampleRate: 1}); err != nil {
			t.Fatal(err)
		}
	}
	e.send(t)
	if got := len(hook.all()); got != 1 {
		t.Fatalf("avisos = %d: los eventos pasados el tope también cuentan", got)
	}
}

func TestRuleUsers(t *testing.T) {
	e := newEnv(t, nil)
	hook := &recorder{}
	e.addRule(t, webhookRule(serve(t, hook), store.RuleConditions{Users: &store.UsersCondition{Users: 2}}))
	for i, u := range []string{"u1", "u1", "u2", "", "u3", "u2", "u4"} {
		ev := event.Event{Fingerprint: []string{"usuarios"}}
		if u != "" {
			ev.User = &event.User{ID: u}
		}
		e.ingestEvent(t, ev)
		e.send(t)
		want := 0
		if i >= 4 {
			want = 1
		}
		if got := len(hook.all()); got != want {
			t.Fatalf("evento %d (%q): avisos = %d, quiero %d", i+1, u, got, want)
		}
	}
	var pl WebhookPayload
	_ = json.Unmarshal(hook.all()[0].Body, &pl)
	if pl.Type != "users" || pl.Trigger == nil || pl.Trigger.Users != 2 || pl.Event.User != "u3" {
		t.Errorf("payload = %+v", pl)
	}
}

func TestRuleFilters(t *testing.T) {
	e := newEnv(t, nil)
	hook := &recorder{}
	e.addRule(t, webhookRule(serve(t, hook), store.RuleConditions{
		New: true, Environments: []string{"production"}, Levels: []string{"error", "fatal"},
		Tags: []store.TagFilter{{Key: "area", Value: "pagos"}, {Key: "cliente"}},
	}))
	good := event.Event{Environment: "production", Level: "error", Tags: map[string]string{"area": "pagos", "cliente": "acme"}}
	cases := map[string]func(*event.Event){
		"vale":            func(*event.Event) {},
		"otro entorno":    func(ev *event.Event) { ev.Environment = "staging" },
		"otro nivel":      func(ev *event.Event) { ev.Level = "warning" },
		"otra etiqueta":   func(ev *event.Event) { ev.Tags = map[string]string{"area": "envios", "cliente": "acme"} },
		"sin la etiqueta": func(ev *event.Event) { ev.Tags = map[string]string{"area": "pagos"} },
	}
	for name, change := range cases {
		ev := good
		ev.Tags = map[string]string{"area": "pagos", "cliente": "acme"}
		change(&ev)
		ev.Fingerprint = []string{name}
		ev.Message = name
		e.ingestEvent(t, ev)
	}
	e.send(t)
	reqs := hook.all()
	if len(reqs) != 1 {
		t.Fatalf("avisos = %d, quiero 1", len(reqs))
	}
	var pl WebhookPayload
	_ = json.Unmarshal(reqs[0].Body, &pl)
	if pl.Issue.Title != "vale" {
		t.Errorf("ha avisado de %q", pl.Issue.Title)
	}
}

func TestRuleThrottle(t *testing.T) {
	e := newEnv(t, nil)
	hook := &recorder{}
	r := webhookRule(serve(t, hook), store.RuleConditions{New: true, Regressed: true})
	r.ThrottleMinutes = 60
	e.addRule(t, r)
	ctx := context.Background()
	first := e.ingest(t, "api-tarea-a-1")
	regress := func() {
		t.Helper()
		if _, err := e.db.SetIssueStatus(ctx, first.IssueID, "resolved"); err != nil {
			t.Fatal(err)
		}
		if res := e.ingest(t, "api-tarea-b-1"); res.Outcome != ingest.Regressed {
			t.Fatalf("res = %+v", res)
		}
		e.send(t)
	}
	e.send(t)
	start := e.clock.now()
	e.clock.set(start.Add(59 * time.Minute))
	regress()
	if got := len(hook.all()); got != 1 {
		t.Fatalf("dentro del límite: avisos = %d, quiero 1", got)
	}
	e.clock.set(start.Add(61 * time.Minute))
	regress()
	if got := len(hook.all()); got != 2 {
		t.Fatalf("pasado el límite: avisos = %d, quiero 2", got)
	}
}

func TestRuleRetries(t *testing.T) {
	e := newEnv(t, nil)
	hook := &recorder{}
	hook.setFail(http.StatusInternalServerError)
	e.addRule(t, webhookRule(serve(t, hook), store.RuleConditions{New: true}))
	e.ingest(t, "api-tarea-a-1")
	now := e.clock.now()
	e.send(t)
	rows := e.outbox(t)
	if len(rows) != 1 || rows[0].Channel != "webhook" || rows[0].Attempts != 1 || rows[0].Sent || !strings.Contains(rows[0].LastError, "500") ||
		rows[0].Next.Sub(now.Add(time.Minute)).Abs() > time.Microsecond {
		t.Fatalf("tras fallar = %+v", rows)
	}
	e.clock.set(now.Add(59 * time.Second))
	if n := e.send(t); n != 0 {
		t.Fatal("reintenta antes de tiempo")
	}
	hook.setFail(0)
	e.clock.set(now.Add(time.Minute))
	if n := e.send(t); n != 1 {
		t.Fatalf("enviados = %d", n)
	}
	reqs := hook.all()
	if len(reqs) != 2 || reqs[0].Header.Get(HeaderDelivery) != reqs[1].Header.Get(HeaderDelivery) {
		t.Errorf("el reintento debe llevar el mismo id de entrega: %v", reqs)
	}
	if rows := e.outbox(t); !rows[0].Sent || rows[0].Attempts != 2 {
		t.Errorf("fila = %+v", rows[0])
	}
}

func TestRulesReplaceDefaultAlerts(t *testing.T) {
	e := newEnv(t, nil)
	hook := &recorder{}
	e.addRule(t, webhookRule(serve(t, hook), store.RuleConditions{New: true}))
	e.ingest(t, "api-tarea-a-1")
	web := e.db.AddProject(t, "web")
	ev := storetest.Fixture(t, "web-typeerror-1")
	if _, err := e.ing.Ingest(context.Background(), web.ID, &ev); err != nil {
		t.Fatal(err)
	}
	if n := e.send(t); n != 3 {
		t.Fatalf("enviados = %d, quiero 3", n)
	}
	if got := e.slack.messages(); len(got) != 1 || !strings.Contains(got[0], "Nuevo error en web") {
		t.Errorf("slack de las variables = %q; solo el proyecto sin reglas avisa ahí", got)
	}
	if issues, order := e.linear.snapshot(); len(order) != 1 || !strings.HasPrefix(issues[order[0]].Title, "[web]") {
		t.Errorf("linear = %+v", issues)
	}
	if len(hook.all()) != 1 {
		t.Errorf("webhook = %d", len(hook.all()))
	}
}

func TestDisabledRulesAlertNothing(t *testing.T) {
	e := newEnv(t, nil)
	hook := &recorder{}
	r := e.addRule(t, webhookRule(serve(t, hook), store.RuleConditions{New: true}))
	r.Enabled = false
	if _, _, err := e.db.UpdateRule(context.Background(), e.project, r); err != nil {
		t.Fatal(err)
	}
	e.ingest(t, "api-tarea-a-1")
	if n := e.send(t); n != 0 || len(e.outbox(t)) != 0 {
		t.Fatalf("una regla desactivada ha avisado o ha vuelto el aviso por defecto: %+v", e.outbox(t))
	}
}

func TestSilencedIssueDoesNotAlert(t *testing.T) {
	e := newEnv(t, nil)
	hook := &recorder{}
	e.addRule(t, webhookRule(serve(t, hook), store.RuleConditions{Frequency: &store.FrequencyCondition{Events: 1, WindowMinutes: 5}}))
	ev := event.Event{Fingerprint: []string{"silenciado"}}
	res := e.ingestEvent(t, ev)
	if _, err := e.db.SetIssueStatus(context.Background(), res.IssueID, "silenced"); err != nil {
		t.Fatal(err)
	}
	e.ingestEvent(t, ev)
	if n := e.send(t); n != 0 {
		t.Fatalf("un issue silenciado ha avisado")
	}
}

func TestRemovedActionIsAbandoned(t *testing.T) {
	e := newEnv(t, nil)
	hook := &recorder{}
	hook.setFail(http.StatusBadGateway)
	r := e.addRule(t, webhookRule(serve(t, hook), store.RuleConditions{New: true}))
	e.ingest(t, "api-tarea-a-1")
	e.send(t)
	r.Actions = []store.RuleAction{{Type: store.ActionNtfy, URL: "http://127.0.0.1:1/tema"}}
	if _, _, err := e.db.UpdateRule(context.Background(), e.project, r); err != nil {
		t.Fatal(err)
	}
	e.clock.set(e.clock.now().Add(time.Hour))
	e.send(t)
	rows := e.outbox(t)
	if len(rows) != 1 || !rows[0].Failed || rows[0].Attempts != 2 || !strings.Contains(rows[0].LastError, "ya no existe") {
		t.Fatalf("filas = %+v", rows)
	}
}

func TestRuleSlackAndLinearOfSameRule(t *testing.T) {
	e := newEnv(t, nil)
	slackSrv := &fakeSlack{}
	e.linear.setFail(http.StatusInternalServerError)
	e.addRule(t, store.Rule{Conditions: store.RuleConditions{New: true}, Actions: []store.RuleAction{
		{Type: store.ActionSlack, URL: serve(t, slackSrv)}, {Type: store.ActionLinear},
	}})
	e.ingest(t, "api-tarea-a-1")
	if n := e.send(t); n != 1 || len(slackSrv.messages()) != 0 {
		t.Fatalf("Slack de la regla no ha esperado a su Linear: %d", n)
	}
	e.linear.setFail(0)
	e.clock.set(e.clock.now().Add(time.Minute))
	e.send(t)
	if msgs := slackSrv.payloads(t); len(msgs) != 1 || !slices.Equal(buttons(msgs[0]), []string{"Ver el error", "ERR-101 en Linear"}) {
		t.Fatalf("slack = %+v", msgs)
	}
}

func TestLinearCommentsOnFrequency(t *testing.T) {
	e := newEnv(t, nil)
	e.addRule(t, store.Rule{Conditions: store.RuleConditions{New: true, Frequency: &store.FrequencyCondition{Events: 2, WindowMinutes: 30}},
		Actions: []store.RuleAction{{Type: store.ActionLinear}}})
	ev := event.Event{Fingerprint: []string{"linear"}}
	for range 3 {
		e.ingestEvent(t, ev)
		e.send(t)
	}
	issues, order := e.linear.snapshot()
	if len(order) != 1 {
		t.Fatalf("issues = %d", len(order))
	}
	li := issues[order[0]]
	if len(li.Comments) != 1 || !strings.HasPrefix(li.Comments[0], "Más de 2 eventos en 30 min: ") || li.State != "state-triage" {
		t.Errorf("issue = %+v: la frecuencia comenta sin cambiar el estado", li)
	}
}

func TestTestAction(t *testing.T) {
	e := newEnv(t, nil)
	hook, slackSrv := &recorder{}, &fakeSlack{}
	r := e.addRule(t, store.Rule{Name: "Pagos", Conditions: store.RuleConditions{New: true}, Actions: []store.RuleAction{
		{ID: "hook", Type: store.ActionWebhook, URL: serve(t, hook), Secret: webhookSecret},
		{ID: "slack", Type: store.ActionSlack, URL: serve(t, slackSrv)},
		{ID: "roto", Type: store.ActionDiscord, URL: "http://127.0.0.1:1/x"},
		{ID: "correo", Type: store.ActionEmail, To: []string{"a@errores.test"}},
	}})
	ctx := context.Background()
	if err := e.alerter.TestAction(ctx, r, "hook"); err != nil {
		t.Fatal(err)
	}
	reqs := hook.all()
	if len(reqs) != 1 {
		t.Fatalf("webhook = %d", len(reqs))
	}
	checkSignature(t, reqs[0], webhookSecret)
	var pl WebhookPayload
	_ = json.Unmarshal(reqs[0].Body, &pl)
	if pl.Type != "test" || pl.Rule.Name != "Pagos" || pl.Project != "backend" || !strings.HasPrefix(pl.Delivery, "test-") ||
		!strings.Contains(pl.Markdown, "Si ves este aviso") || pl.Issue.URL != fmt.Sprintf("%s/projects/%d/settings", publicURL, e.project) {
		t.Errorf("payload = %+v", pl)
	}
	if err := e.alerter.TestAction(ctx, r, "slack"); err != nil {
		t.Fatal(err)
	}
	if msgs := slackSrv.messages(); len(msgs) != 1 || !strings.HasPrefix(msgs[0], "Aviso de prueba de backend") {
		t.Errorf("slack = %q", msgs)
	}
	if err := e.alerter.TestAction(ctx, r, "roto"); !errors.Is(err, ErrTestFailed) || strings.Contains(err.Error(), "127.0.0.1:1/x") {
		t.Errorf("acción rota = %v", err)
	}
	if err := e.alerter.TestAction(ctx, r, "correo"); !errors.Is(err, store.ErrInvalid) || !strings.Contains(err.Error(), "SMTP_URL") {
		t.Errorf("correo sin SMTP_URL = %v", err)
	}
	if err := e.alerter.TestAction(ctx, r, "no-existe"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("acción que no existe = %v", err)
	}
	if rows := e.outbox(t); len(rows) != 0 {
		t.Errorf("la prueba ha dejado avisos en la tabla: %+v", rows)
	}
}

func TestCheckRule(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.LinearAPIKey = "" })
	lin := store.Rule{Actions: []store.RuleAction{{Type: store.ActionLinear}}}
	if err := e.alerter.CheckRule(lin); !errors.Is(err, store.ErrInvalid) || !strings.Contains(err.Error(), "LINEAR_API_KEY") {
		t.Errorf("linear sin clave = %v", err)
	}
	withKey := newEnv(t, func(o *Options) { o.LinearTeamID = ""; o.LinearAPIKey = "" })
	withKey.alerter.linearKey = linearKey
	if err := withKey.alerter.CheckRule(lin); err == nil || !strings.Contains(err.Error(), "team_id") {
		t.Errorf("linear sin equipo = %v", err)
	}
	lin.Actions[0].TeamID = teamID
	if err := withKey.alerter.CheckRule(lin); err != nil {
		t.Errorf("linear con equipo = %v", err)
	}
}

func TestSMTPConfig(t *testing.T) {
	for _, c := range []struct{ url, from, want string }{
		{"smtp://u:p@correo.test", "a@b.test", "correo.test:587"},
		{"smtps://correo.test", "a@b.test", "correo.test:465"},
		{"smtp://correo.test:2525", "Avisos <a@b.test>", "correo.test:2525"},
	} {
		cfg, err := parseSMTP(c.url, c.from)
		if err != nil || cfg.addr != c.want {
			t.Errorf("%s: %+v, %v", c.url, cfg, err)
		}
	}
	for _, c := range [][2]string{{"http://correo.test", "a@b.test"}, {"smtp://correo.test", ""}, {"smtp://correo.test", "no es"}} {
		if _, err := New(nil, quiet(), Options{SMTPURL: c[0], SMTPFrom: c[1]}); err == nil {
			t.Errorf("%v: debería fallar", c)
		}
	}
}

func TestSignKnownValue(t *testing.T) {
	got := Sign("secreto", time.Unix(1700000000, 0), []byte(`{"type":"test"}`))
	mac := hmac.New(sha256.New, []byte("secreto"))
	mac.Write([]byte(`1700000000.{"type":"test"}`))
	if want := "t=1700000000,v1=" + hex.EncodeToString(mac.Sum(nil)); got != want {
		t.Errorf("Sign = %s, quiero %s", got, want)
	}
	if !bytes.HasPrefix([]byte(got), []byte("t=1700000000,v1=")) {
		t.Error(got)
	}
}

// La frecuencia cuenta solo los eventos que pasan los filtros de la regla, y
// un evento que no los pasa no hace que se pierda el cruce.
func TestRuleFrequencyCountsOnlyMatchingEvents(t *testing.T) {
	e := newEnv(t, nil)
	hook := &recorder{}
	e.addRule(t, webhookRule(serve(t, hook), store.RuleConditions{
		Frequency: &store.FrequencyCondition{Events: 2, WindowMinutes: 10}, Environments: []string{"production"},
	}))
	prod := event.Event{Fingerprint: []string{"filtrada"}, Environment: "production"}
	staging := event.Event{Fingerprint: []string{"filtrada"}, Environment: "staging"}
	for i, ev := range []event.Event{staging, staging, prod, staging, prod} {
		e.ingestEvent(t, ev)
		e.send(t)
		if got := len(hook.all()); got != 0 {
			t.Fatalf("evento %d: avisos = %d; los de staging no cuentan", i+1, got)
		}
	}
	e.ingestEvent(t, prod)
	e.send(t)
	if got := len(hook.all()); got != 1 {
		t.Fatalf("3 eventos de production: avisos = %d, quiero 1", got)
	}
	for range 3 {
		e.ingestEvent(t, prod)
	}
	e.send(t)
	if got := len(hook.all()); got != 1 {
		t.Fatalf("sigue por encima del umbral: avisos = %d, quiero 1", got)
	}
}

// Si el cruce llega durante el límite de la regla o con un error nuevo, avisa
// con el primer evento en que puede.
func TestRuleFrequencyNotLostWhileThrottled(t *testing.T) {
	e := newEnv(t, nil)
	hook := &recorder{}
	r := webhookRule(serve(t, hook), store.RuleConditions{New: true, Frequency: &store.FrequencyCondition{Events: 2, WindowMinutes: 60}})
	r.ThrottleMinutes = 10
	e.addRule(t, r)
	ev := event.Event{Fingerprint: []string{"limitada"}}
	for range 3 {
		e.ingestEvent(t, ev)
	}
	e.send(t)
	if got := len(hook.all()); got != 1 {
		t.Fatalf("avisos = %d; solo el de error nuevo, el resto lo para el límite", got)
	}
	e.clock.set(e.clock.now().Add(11 * time.Minute))
	e.ingestEvent(t, ev)
	e.send(t)
	reqs := hook.all()
	if len(reqs) != 2 {
		t.Fatalf("pasado el límite: avisos = %d, quiero 2", len(reqs))
	}
	var pl WebhookPayload
	_ = json.Unmarshal(reqs[1].Body, &pl)
	if pl.Type != "frequency" {
		t.Errorf("tipo = %q", pl.Type)
	}
}

func TestRuleUsersCountsOnlyMatchingAndStops(t *testing.T) {
	e := newEnv(t, nil)
	hook := &recorder{}
	r := e.addRule(t, webhookRule(serve(t, hook), store.RuleConditions{Users: &store.UsersCondition{Users: 2}, Levels: []string{"error"}}))
	ingest := func(user, level string) {
		e.ingestEvent(t, event.Event{Fingerprint: []string{"usuarios-filtrados"}, Level: level, User: &event.User{ID: user}})
	}
	ingest("w1", "warning")
	ingest("w2", "warning")
	ingest("u1", "error")
	ingest("w3", "warning")
	ingest("u2", "error")
	e.send(t)
	if got := len(hook.all()); got != 0 {
		t.Fatalf("avisos = %d; los usuarios de warning no cuentan", got)
	}
	ingest("u3", "error")
	for i := range 20 {
		ingest(fmt.Sprint("otro-", i), "error")
	}
	e.send(t)
	if got := len(hook.all()); got != 1 {
		t.Fatalf("avisos = %d, quiero 1", got)
	}
	var n int
	if err := e.db.Pool().QueryRow(context.Background(), `SELECT count(*) FROM alert_rule_users WHERE rule_id = $1`, r.ID).Scan(&n); err != nil || n != 3 {
		t.Errorf("usuarios apuntados = %d, %v; pasado el umbral no se siguen contando", n, err)
	}
}

// Al cambiar las condiciones, la regla vuelve a contar desde cero y puede
// volver a avisar; cambiar solo el nombre no toca las cuentas.
func TestRuleEditResetsCounts(t *testing.T) {
	e := newEnv(t, nil)
	hook := &recorder{}
	r := e.addRule(t, webhookRule(serve(t, hook), store.RuleConditions{
		Users: &store.UsersCondition{Users: 2}, Frequency: &store.FrequencyCondition{Events: 50, WindowMinutes: 60},
	}))
	ctx := context.Background()
	users := func() int {
		var n int
		if err := e.db.Pool().QueryRow(ctx, `SELECT count(*) FROM alert_rule_users WHERE rule_id = $1`, r.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	ingestUsers := func(ids ...string) {
		for _, u := range ids {
			e.ingestEvent(t, event.Event{Fingerprint: []string{"editada"}, User: &event.User{ID: u}})
		}
		e.send(t)
	}
	ingestUsers("u1", "u2", "u3")
	if got := len(hook.all()); got != 1 || users() != 3 {
		t.Fatalf("avisos = %d, usuarios = %d", got, users())
	}

	r.Name = "Otro nombre"
	r, _, err := e.db.UpdateRule(ctx, e.project, r)
	if err != nil {
		t.Fatal(err)
	}
	if users() != 3 {
		t.Fatalf("cambiar el nombre ha borrado las cuentas: %d", users())
	}

	r.Conditions.Users.Users = 3
	if _, _, err := e.db.UpdateRule(ctx, e.project, r); err != nil {
		t.Fatal(err)
	}
	var minutes int
	var usersAlerted bool
	if err := e.db.Pool().QueryRow(ctx, `
		SELECT (SELECT count(*) FROM alert_rule_minutes WHERE rule_id = $1),
			(SELECT users_alerted FROM alert_rule_issues WHERE rule_id = $1)`, r.ID).Scan(&minutes, &usersAlerted); err != nil {
		t.Fatal(err)
	}
	if users() != 0 || minutes != 0 || usersAlerted {
		t.Fatalf("tras subir el umbral: usuarios %d, minutos %d, users_alerted %v", users(), minutes, usersAlerted)
	}
	ingestUsers("u1", "u2", "u3")
	if got := len(hook.all()); got != 1 {
		t.Fatalf("3 usuarios con umbral 3: avisos = %d", got)
	}
	ingestUsers("u4")
	if got := len(hook.all()); got != 2 {
		t.Fatalf("4 usuarios con umbral 3: avisos = %d, quiero 2", got)
	}
}

// Sin reglas, un evento que no cambia el issue no abre savepoint ni consulta
// las reglas en la transacción: aquí no hay transacción y no debe hacer falta.
func TestNoRulesUnchangedSkipsTransaction(t *testing.T) {
	e := newEnv(t, nil)
	res := e.ingest(t, "api-tarea-a-1")
	res.Outcome = ingest.Unchanged
	if err := e.alerter.IssueChanged(context.Background(), nil, &event.Event{}, res); err != nil {
		t.Fatal(err)
	}
	if res.ProjectID != e.project {
		t.Errorf("ProjectID = %d, quiero %d", res.ProjectID, e.project)
	}
	// Con una regla nueva, RulesChanged hace que se tenga en cuenta al momento.
	hook := &recorder{}
	e.addRule(t, webhookRule(serve(t, hook), store.RuleConditions{Frequency: &store.FrequencyCondition{Events: 1, WindowMinutes: 5}}))
	e.ingest(t, "api-tarea-a-1")
	e.ingest(t, "api-tarea-a-1")
	e.send(t)
	if got := len(hook.all()); got != 1 {
		t.Fatalf("avisos de la regla nueva = %d, quiero 1", got)
	}
}

// Con todas las conexiones del pool en uso, mirar si el proyecto tiene reglas
// no puede esperar a otra: va por la transacción de la ingesta.
func TestRulesLookupUsesIngestTransaction(t *testing.T) {
	e := newEnv(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	pool := e.db.Pool()
	issue := e.db.AddIssue(t, storetest.Issue{ProjectID: e.project, Title: "TypeError"})
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	for range pool.Stat().MaxConns() - 1 {
		c, err := pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Release()
	}
	e.alerter.RulesChanged(e.project)
	res := ingest.Result{IssueID: issue, ProjectID: e.project, Outcome: ingest.New}
	if err := e.alerter.IssueChanged(ctx, tx, &event.Event{Message: "fallo de prueba"}, res); err != nil {
		t.Fatal(err)
	}
	if ctx.Err() != nil {
		t.Fatal("IssueChanged se quedó esperando otra conexión del pool")
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM alert_outbox WHERE issue_id = $1`, issue).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Error("no ha encolado los avisos del issue nuevo")
	}
}
