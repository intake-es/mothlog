package alert

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/intake-es/intake-errors/internal/cron"
	"github.com/intake-es/intake-errors/internal/event"
	"github.com/intake-es/intake-errors/internal/store"
)

// Lo que manda la auditoría en el valor de la excepción: menciones, un
// enlace enmascarado y un comentario HTML.
const auditValue = "token expired; @acme/oncall @dummy-user please re-auth at [portal](https://phish.test.invalid/login) <!-- hidden note -->"

// outsideCode quita de md los bloques y los spans de código.
func outsideCode(md string) string {
	var out []string
	fence := ""
	for line := range strings.SplitSeq(md, "\n") {
		t := strings.TrimSpace(line)
		run := len(t) - len(strings.TrimLeft(t, "`"))
		switch {
		case fence != "":
			if run >= len(fence) && strings.Trim(t, "`") == "" {
				fence = ""
			}
			continue
		case run >= 3:
			fence = t[:run]
			continue
		}
		out = append(out, removeSpans(line))
	}
	return strings.Join(out, "\n")
}

func removeSpans(s string) string {
	var b strings.Builder
	for {
		i := strings.Index(s, "`")
		if i < 0 {
			return b.String() + s
		}
		n := len(s[i:]) - len(strings.TrimLeft(s[i:], "`"))
		tick := s[i : i+n]
		rest := s[i+n:]
		j := -1
		for k := 0; k < len(rest); {
			m := strings.Index(rest[k:], tick)
			if m < 0 {
				break
			}
			m += k
			if (m+n == len(rest) || rest[m+n] != '`') && (m == 0 || rest[m-1] != '`') {
				j = m
				break
			}
			k = m + n
		}
		if j < 0 {
			return b.String() + s
		}
		b.WriteString(s[:i])
		s = rest[j+n:]
	}
}

var (
	liveMention = regexp.MustCompile(`(^|[^\p{L}\p{N}_])@[\p{L}\p{N}_-]`)
	liveLink    = regexp.MustCompile(`(^|[^\\])\]\(`)
	liveHTML    = regexp.MustCompile(`(^|[^\\])<`)
	liveEntity  = regexp.MustCompile(`(^|[^\\])&(#[0-9]+|#[xX][0-9a-fA-F]+|[A-Za-z][A-Za-z0-9]*);`)
	ownLink     = regexp.MustCompile(`\[Ver en intake-errors\]\([^)]*\)`)
)

func checkInert(t *testing.T, where, md string) {
	t.Helper()
	rest := ownLink.ReplaceAllString(outsideCode(md), "")
	for name, re := range map[string]*regexp.Regexp{"una mención": liveMention, "un enlace": liveLink, "HTML": liveHTML,
		"una entidad": liveEntity} {
		if loc := re.FindStringIndex(rest); loc != nil {
			t.Errorf("%s trae %s vivo del evento cerca de %q:\n%s", where, name, rest[max(0, loc[0]-40):min(len(rest), loc[1]+40)], md)
		}
	}
}

// fakeGitHub guarda las issues y los comentarios que se le mandan.
type fakeGitHub struct {
	mu       sync.Mutex
	bodies   []string
	comments []string
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var in map[string]any
	_ = json.NewDecoder(r.Body).Decode(&in)
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/comments"):
		f.comments = append(f.comments, in["body"].(string))
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
		return
	case r.Method == http.MethodPost:
		f.bodies = append(f.bodies, in["body"].(string))
		w.WriteHeader(http.StatusCreated)
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"number": 7, "html_url": "https://github.com/acme/app/issues/7", "state": "closed"})
}

// Lo que trae el evento llega a GitHub y Linear como texto: sin menciones,
// enlaces ni comentarios HTML que se publiquen con el token del operador.
// Lo nuestro, el enlace al panel y los títulos, sigue siendo Markdown.
func TestGitHubAndLinearKeepEventTextInert(t *testing.T) {
	gh := &fakeGitHub{}
	e := newEnv(t, func(o *Options) { o.GitHubAPIURL = serve(t, gh); o.GitHubToken = "ghp_prueba" })
	e.addRule(t, store.Rule{Conditions: store.RuleConditions{New: true, Regressed: true}, Actions: []store.RuleAction{
		{Type: store.ActionGitHub, Repo: "acme/app"}, {Type: store.ActionLinear},
	}})
	// A mano, como en un correo o una versión, la @ no es una mención y se deja.
	ev := func(release string) event.Event {
		return event.Event{Release: release, Environment: "production", Platform: "python",
			ServerName: "web-1 @ops [x](https://phish.test.invalid) &#64;acme/oncall &#x40;dummy-user &commat;ops",
			User:       &event.User{ID: "@admin <img src=x>", Email: "ana@example.com"},
			Request:    &event.Request{Method: "GET", URL: "https://tienda.example.com/<!-- x -->"},
			Tags:       map[string]string{"@equipo": "[a](https://phish.test.invalid)"},
			Exceptions: []event.Exception{{Type: "PermissionError", Value: auditValue, Mechanism: &event.Mechanism{Type: "@generic"},
				Frames: []event.Frame{{Filename: "api/auth.py", Function: "check @x", Line: 5}}}}}
	}
	res := e.ingestEvent(t, ev("api@2.3.0"))
	e.send(t)
	if _, err := e.db.ChangeIssueStatus(context.Background(), res.IssueID, "resolved", store.StatusChange{Source: store.SourceAPI}); err != nil {
		t.Fatal(err)
	}
	e.ingestEvent(t, ev("api@2.3.1 cc @dummy-user [aquí](https://phish.test.invalid) <!-- y -->"))
	e.send(t)

	gh.mu.Lock()
	bodies, comments := gh.bodies, gh.comments
	gh.mu.Unlock()
	issues, order := e.linear.snapshot()
	if len(bodies) != 1 || len(comments) != 1 || len(order) != 1 || len(issues[order[0]].Comments) != 1 {
		t.Fatalf("github: %d issues y %d comentarios; linear: %d issues", len(bodies), len(comments), len(order))
	}
	li := issues[order[0]]
	for where, md := range map[string]string{"el cuerpo de GitHub": bodies[0], "el comentario de GitHub": comments[0],
		"la descripción de Linear": li.Description, "el comentario de Linear": li.Comments[0]} {
		checkInert(t, where, md)
		if !strings.Contains(md, "[Ver en intake-errors](https://errores.test/issues/") {
			t.Errorf("%s no trae el enlace al panel:\n%s", where, md)
		}
	}
	for where, md := range map[string]string{"el cuerpo de GitHub": bodies[0], "la descripción de Linear": li.Description} {
		if !strings.Contains(md, "\n# PermissionError: token expired; @\u200bacme/oncall") ||
			!strings.Contains(md, `\[portal\](https://phish.test.invalid/login) \<!-- hidden note -->`) {
			t.Errorf("%s no trae el título escapado:\n%s", where, md)
		}
	}
	if !strings.Contains(bodies[0], `\&#64;acme/oncall \&#x40;dummy-user \&commat;ops`) {
		t.Errorf("github no escapa las entidades:\n%s", bodies[0])
	}
	if !strings.Contains(bodies[0], "\n## Historial\n") || !strings.Contains(bodies[0], "email ana@example.com") ||
		!strings.Contains(comments[0], "release api@2.3.1 cc @\u200bdummy-user \\[aquí\\]") {
		t.Errorf("github:\n%s\n---\n%s", bodies[0], comments[0])
	}
}

func TestCronMarkdownKeepsEventTextInert(t *testing.T) {
	p := &pending{kind: store.KindCronFailed, project: "backend", environment: "prod @ops <!-- x -->", release: "1.0 [x](https://phish.test.invalid)",
		link: "https://errores.test/crons/copia", cron: &cronInfo{slug: "copia`@x", name: "Copia @equipo [aquí](https://phish.test.invalid)",
			schedule: cron.Schedule{Type: cron.TypeCrontab, Crontab: "0 * * * * @y"}}}
	md := cronMarkdown(p)
	checkInert(t, "cronMarkdown", md)
	if !strings.Contains(md, "- Monitor: ``copia`@x``\n") || !strings.Contains(md, "[Ver en intake-errors](https://errores.test/crons/copia)") {
		t.Errorf("cronMarkdown:\n%s", md)
	}
}

// rawSMTP guarda los bytes de DATA tal cual, sin normalizar los saltos.
type rawSMTP struct {
	mu   sync.Mutex
	data [][]byte
	addr string
}

func newRawSMTP(t *testing.T) *rawSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	f := &rawSMTP{addr: ln.Addr().String()}
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

func (f *rawSMTP) serve(c net.Conn) {
	defer func() { _ = c.Close() }()
	r := bufio.NewReader(c)
	say := func(s string) { _, _ = io.WriteString(c, s+"\r\n") }
	say("220 smtp falso")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO"):
			say("250-smtp falso")
			say("250 AUTH PLAIN")
		case strings.HasPrefix(cmd, "AUTH"):
			say("235 dentro")
		case cmd == "DATA":
			say("354 adelante")
			var data []byte
			for !strings.HasSuffix(string(data), "\r\n.\r\n") {
				b, err := r.ReadByte()
				if err != nil {
					return
				}
				data = append(data, b)
			}
			f.mu.Lock()
			f.data = append(f.data, data)
			f.mu.Unlock()
			say("250 recibido")
		case cmd == "QUIT":
			say("221 adiós")
			return
		default:
			say("250 ok")
		}
	}
}

var bareCR = regexp.MustCompile("\r([^\n]|$)")

// Un CR suelto del evento no llega a SMTP: con un punto detrás, algunos
// servidores darían el mensaje por acabado y leerían lo que sigue como órdenes.
func TestEmailSendsNoBareCR(t *testing.T) {
	mail := newRawSMTP(t)
	e := newEnv(t, func(o *Options) {
		o.SMTPURL = "smtp://avisos:clave@" + mail.addr
		o.SMTPFrom = "avisos@errores.test"
	})
	e.addRule(t, store.Rule{Conditions: store.RuleConditions{New: true}, Actions: []store.RuleAction{
		{Type: store.ActionEmail, To: []string{"guardia@errores.test"}}}})
	e.ingestEvent(t, event.Event{Message: "verify-msg\r.\nNOOP vc-line-1\nNOOP vc-line-2", ServerName: "web\r.\r\nRSET",
		Exceptions: []event.Exception{{Type: "Error\r.", Value: "x\r.\rQUIT"}}})
	if n := e.send(t); n != 1 {
		t.Fatalf("enviados = %d", n)
	}
	mail.mu.Lock()
	defer mail.mu.Unlock()
	if len(mail.data) != 1 {
		t.Fatalf("correos = %d", len(mail.data))
	}
	data := string(mail.data[0])
	if loc := bareCR.FindStringIndex(data); loc != nil {
		t.Errorf("DATA lleva un CR sin LF en %d: %q", loc[0], data[max(0, loc[0]-30):min(len(data), loc[1]+30)])
	}
	if !strings.Contains(data, "verify-msg\r\n..\r\nNOOP vc-line-1\r\n") {
		t.Errorf("el mensaje no llega con el punto escapado:\n%q", data)
	}
}

func TestCRLFHasNoBareCR(t *testing.T) {
	for _, in := range []string{"Salud de 1.0\r.\nNOOP vc", "a\r\rb\r\n\r", "\r"} {
		if out := crlf(in); bareCR.MatchString(out) || strings.Contains(strings.ReplaceAll(out, "\r\n", ""), "\n") {
			t.Errorf("crlf(%q) = %q", in, out)
		}
	}
	if got := crlf("a\r.\nb"); got != "a\r\n.\r\nb\r\n" {
		t.Errorf("crlf = %q", got)
	}
}

// Si el HTML no cabe, el respaldo va sin parse_mode: el marcado del evento
// sale como texto, aunque la regla filtre por entorno.
func TestTelegramFallbackIsNotHTML(t *testing.T) {
	tg := &recorder{reply: `{"ok":true}`}
	e := newEnv(t, func(o *Options) { o.TelegramAPIURL = serve(t, tg) })
	e.addRule(t, store.Rule{Conditions: store.RuleConditions{New: true, Environments: []string{"production"}},
		Actions: []store.RuleAction{{Type: store.ActionTelegram, BotToken: "123:bot", ChatID: "-1"}}})
	q := strings.Repeat(`"`, 300)
	inApp := true
	e.ingestEvent(t, event.Event{Environment: "production", User: &event.User{ID: q[:100]},
		Exceptions: []event.Exception{{Type: "<b>Aviso de seguridad</b>", Value: `<a href="https://evil.example/login">Ver el error</a>` + q[:200],
			Frames: []event.Frame{{Filename: q, Function: "f", Line: 1, ContextLine: q, InApp: &inApp}}}}})
	e.ingestEvent(t, event.Event{Environment: "production", Exceptions: []event.Exception{{Type: "<b>Normal</b>", Value: "corto"}}})
	if n := e.send(t); n != 2 {
		t.Fatalf("enviados = %d", n)
	}
	reqs := tg.all()
	var sent []map[string]any
	for _, r := range reqs {
		var m map[string]any
		if err := json.Unmarshal(r.Body, &m); err != nil {
			t.Fatal(err)
		}
		sent = append(sent, m)
	}
	if len(sent) != 2 {
		t.Fatalf("telegram = %d", len(sent))
	}
	long, short := sent[0], sent[1]
	if _, ok := long["parse_mode"]; ok || !strings.HasPrefix(long["text"].(string), "<b>Aviso de seguridad</b>: <a href=") {
		t.Errorf("el respaldo largo = %v", long)
	}
	if short["parse_mode"] != "HTML" || !strings.Contains(short["text"].(string), "<b>&lt;b&gt;Normal&lt;/b&gt;: corto</b>") {
		t.Errorf("el aviso corto = %v", short)
	}
	for _, m := range sent {
		if m["parse_mode"] == "HTML" && (strings.Contains(m["text"].(string), "<a href=\"https://evil") || strings.Contains(m["text"].(string), "<b><b>")) {
			t.Errorf("HTML con marcado del evento: %v", m)
		}
	}
}

func TestDiscordEscapesBrackets(t *testing.T) {
	if got := discordEscaper.Replace("[portal](https://phish.test.invalid/login)"); got != `\[portal\]\(https://phish.test.invalid/login\)` {
		t.Errorf("discordEscaper = %q", got)
	}
	p := &pending{kind: "new", project: "backend", environment: "[prod](https://phish.test.invalid)", release: "[1.0](https://phish.test.invalid)",
		title: "Error", eventAt: time.Now(), ev: &event.Event{User: &event.User{ID: "[ana](https://phish.test.invalid)"}}}
	msg := discordMessage(p)
	for _, s := range append([]string{msg.Content}, func() []string {
		var v []string
		for _, f := range msg.Embeds[0].Fields {
			v = append(v, f.Value)
		}
		return v
	}()...) {
		if liveLink.MatchString(s) {
			t.Errorf("discord deja un enlace enmascarado: %q", s)
		}
	}
}
