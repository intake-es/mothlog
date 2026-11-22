package alert

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/intake-es/intake-errors/internal/digest"
	"github.com/intake-es/intake-errors/internal/digest/digesttest"
	"github.com/intake-es/intake-errors/internal/store"
)

type digestEnv struct {
	*env
	mail *fakeSMTP
	hook *recorder
	cfg  store.DigestConfig
}

const digestSecret = "secreto-del-resumen"

// newDigestEnv prepara un proyecto con datos de un día y su resumen diario a
// las 9 de Madrid por correo, Slack y webhook, en el idioma lang.
func newDigestEnv(t *testing.T, lang string) *digestEnv {
	t.Helper()
	m := newFakeSMTP(t)
	e := &digestEnv{mail: m, hook: &recorder{}}
	e.env = newEnv(t, func(o *Options) {
		o.SMTPURL = "smtp://avisos:clave-smtp@" + m.addr
		o.SMTPFrom = "intake-errors <avisos@errores.test>"
	})
	e.clock.set(time.Date(2026, 11, 21, 11, 0, 0, 0, time.UTC))
	c := store.DefaultDigest(e.project)
	c.Enabled, c.Timezone, c.Hour, c.Language = true, "Europe/Madrid", 9, lang
	c.Destinations = []store.RuleAction{
		{ID: "correo", Type: store.ActionEmail, To: []string{"equipo@example.com"}},
		{ID: "slack", Type: store.ActionSlack, URL: e.opts.SlackWebhookURL},
		{ID: "hook", Type: store.ActionWebhook, URL: serve(t, e.hook) + "/resumen", Secret: digestSecret},
	}
	cfg, _, err := e.db.SetDigest(context.Background(), e.project, c, e.clock.now())
	if err != nil {
		t.Fatal(err)
	}
	e.cfg = cfg
	digesttest.Seed(t, e.db, e.project, time.Date(2026, 11, 22, 8, 0, 0, 0, time.UTC))
	return e
}

// tick hace lo que hace el servicio a la hora: encolar y enviar.
func (e *digestEnv) tick(t *testing.T, at time.Time) int {
	t.Helper()
	e.clock.set(at)
	s := digest.NewScheduler(e.db.Store, slog.New(slog.NewTextHandler(io.Discard, nil)), publicURL)
	s.Now = e.clock.now
	if _, err := s.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	n, err := e.alerter.SendPending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// mailParts lee el correo como un cliente: el asunto y sus partes por tipo.
func mailParts(t *testing.T, raw string) (string, map[string]string) {
	t.Helper()
	msg, err := mail.ReadMessage(strings.NewReader(strings.ReplaceAll(raw, "\n", "\r\n") + "\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	subject, err := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
	if err != nil {
		t.Fatal(err)
	}
	mt, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/alternative" {
		t.Fatalf("Content-Type = %q", msg.Header.Get("Content-Type"))
	}
	parts := map[string]string{}
	r := multipart.NewReader(msg.Body, params["boundary"])
	for {
		p, err := r.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(p)
		typ, _, _ := mime.ParseMediaType(p.Header.Get("Content-Type"))
		parts[typ] = string(b)
	}
	return subject, parts
}

// checkSignatureAt comprueba la firma como quien recibe el webhook, con el reloj del test.
func checkSignatureAt(t *testing.T, r recorded, secret string, now time.Time) {
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
	if err != nil || now.Sub(time.Unix(unix, 0)).Abs() > 5*time.Minute {
		t.Errorf("fecha de la firma = %q", ts)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "."))
	mac.Write(r.Body)
	if want := hex.EncodeToString(mac.Sum(nil)); !hmac.Equal([]byte(sig), []byte(want)) {
		t.Errorf("firma = %q, quiero %q", sig, want)
	}
}

func TestDigestReachesEachDestination(t *testing.T) {
	for _, tc := range []struct {
		lang, subject, heading, newTitle, settings string
	}{
		{"es", "[intake-errors] Resumen diario de backend: 1 nuevo, 1 ha vuelto, 2 crons con problemas",
			"Del sáb 21 nov, 09:00 al dom 22 nov, 09:00 (Europe/Madrid)", "Errores nuevos", "Cambiar el resumen"},
		{"en", "[intake-errors] Daily digest for backend: 1 new, 1 regression, 2 cron problems",
			"From Sat, Nov 21, 09:00 to Sun, Nov 22, 09:00 (Europe/Madrid)", "New errors", "Change the digest"},
	} {
		t.Run(tc.lang, func(t *testing.T) {
			e := newDigestEnv(t, tc.lang)
			if n := e.tick(t, time.Date(2026, 11, 22, 8, 0, 20, 0, time.UTC)); n != 3 {
				t.Fatalf("enviados %d", n)
			}

			msgs := e.mail.messages()
			if len(msgs) != 1 || msgs[0].To[0] != "equipo@example.com" || msgs[0].Auth != "|avisos|clave-smtp" {
				t.Fatalf("correos = %+v", msgs)
			}
			subject, parts := mailParts(t, msgs[0].Data)
			if subject != tc.subject {
				t.Errorf("asunto = %q", subject)
			}
			text, html := parts["text/plain"], parts["text/html"]
			for _, w := range []string{tc.heading, publicURL + "/issues/", tc.settings} {
				if !strings.Contains(text, w) || !strings.Contains(html, w) {
					t.Errorf("el correo no trae %q:\n%s\n%s", w, text, html)
				}
			}
			if !strings.Contains(html, `<html lang="`+tc.lang+`">`) || !strings.Contains(html, ">"+tc.newTitle+"</h2>") {
				t.Errorf("HTML:\n%s", html)
			}

			slack := e.slack.payloads(t)
			if len(slack) != 1 {
				t.Fatalf("Slack = %d", len(slack))
			}
			raw, _ := json.Marshal(slack[0])
			if !strings.Contains(string(raw), `"type":"header"`) || !strings.Contains(string(raw), tc.newTitle) ||
				!strings.Contains(slack[0].Text, strings.TrimPrefix(tc.subject, "[intake-errors] ")) {
				t.Errorf("Slack = %s", raw)
			}

			hooks := e.hook.all()
			if len(hooks) != 1 {
				t.Fatalf("webhook = %d", len(hooks))
			}
			checkSignatureAt(t, hooks[0], digestSecret, e.clock.now())
			if hooks[0].Header.Get(HeaderEvent) != "digest" || !strings.HasPrefix(hooks[0].Header.Get(HeaderDelivery), "outbox-") {
				t.Errorf("cabeceras = %v", hooks[0].Header)
			}
			var pl digest.WebhookPayload
			if err := json.Unmarshal(hooks[0].Body, &pl); err != nil {
				t.Fatal(err)
			}
			if pl.Type != "digest" || pl.Language != tc.lang || pl.Project.Name != "backend" || len(pl.Sections) != 6 ||
				!pl.Period.End.Equal(time.Date(2026, 11, 22, 8, 0, 0, 0, time.UTC)) ||
				!strings.Contains(pl.Markdown, "## "+tc.newTitle) || pl.Delivery != hooks[0].Header.Get(HeaderDelivery) {
				t.Errorf("payload = %+v", pl)
			}

			c, err := e.db.Digest(context.Background(), e.project, e.clock.now())
			if err != nil || c.LastSentAt == nil {
				t.Errorf("sin last_sent_at: %+v %v", c, err)
			}
			// Una hora después, nada nuevo.
			if n := e.tick(t, time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)); n != 0 {
				t.Errorf("repite: %d", n)
			}
		})
	}
}

func TestDigestRetriesAndRemovedDestination(t *testing.T) {
	e := newDigestEnv(t, "es")
	e.slack.setFail(http.StatusInternalServerError)
	at := time.Date(2026, 11, 22, 8, 0, 20, 0, time.UTC)
	e.tick(t, at)
	if len(e.slack.messages()) != 0 || len(e.hook.all()) != 1 {
		t.Fatalf("slack %d, webhook %d", len(e.slack.messages()), len(e.hook.all()))
	}
	e.slack.setFail(0)
	e.tick(t, at.Add(30*time.Second))
	if len(e.slack.messages()) != 0 {
		t.Fatal("reintenta antes de tiempo")
	}
	e.tick(t, at.Add(2*time.Minute))
	if len(e.slack.messages()) != 1 {
		t.Fatalf("tras el reintento, slack %d", len(e.slack.messages()))
	}
	if len(e.hook.all()) != 1 || len(e.mail.messages()) != 1 {
		t.Errorf("los otros destinos se repiten: %d %d", len(e.hook.all()), len(e.mail.messages()))
	}

	// Un destino que se quita antes de enviarse se abandona.
	next := at.Add(24 * time.Hour)
	e.slack.setFail(http.StatusInternalServerError)
	e.tick(t, next)
	c := e.cfg
	c.Destinations = c.Destinations[:1]
	if _, _, err := e.db.SetDigest(context.Background(), e.project, c, next); err != nil {
		t.Fatal(err)
	}
	e.tick(t, next.Add(10*time.Minute))
	var failed int
	if err := e.db.Pool().QueryRow(context.Background(), `SELECT count(*) FROM alert_outbox
		WHERE digest_project_id IS NOT NULL AND failed_at IS NOT NULL AND action_id = 'slack'`).Scan(&failed); err != nil || failed != 1 {
		t.Errorf("abandonados = %d %v", failed, err)
	}
}

func TestDigestTurnedOffIsAbandoned(t *testing.T) {
	e := newDigestEnv(t, "es")
	e.slack.setFail(http.StatusInternalServerError)
	at := time.Date(2026, 11, 22, 8, 0, 20, 0, time.UTC)
	e.tick(t, at)
	if len(e.slack.messages()) != 0 {
		t.Fatal("slack no debería haber recibido nada")
	}
	c := e.cfg
	c.Enabled = false
	if _, _, err := e.db.SetDigest(context.Background(), e.project, c, at); err != nil {
		t.Fatal(err)
	}
	e.slack.setFail(0)
	e.tick(t, at.Add(10*time.Minute))
	if n := len(e.slack.messages()); n != 0 {
		t.Errorf("apagado, el reintento sale igual: %d", n)
	}
	var failed int
	if err := e.db.Pool().QueryRow(context.Background(), `SELECT count(*) FROM alert_outbox
		WHERE digest_project_id IS NOT NULL AND failed_at IS NOT NULL AND action_id = 'slack'`).Scan(&failed); err != nil || failed != 1 {
		t.Errorf("abandonados = %d %v", failed, err)
	}
}

func TestSendDigestNow(t *testing.T) {
	e := newDigestEnv(t, "en")
	e.clock.set(time.Date(2026, 11, 22, 8, 30, 0, 0, time.UTC))
	res, err := e.alerter.SendDigestNow(context.Background(), e.project)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if !r.OK {
			t.Errorf("%s: %s", r.ID, r.Error)
		}
	}
	if len(res) != 3 || len(e.mail.messages()) != 1 || len(e.slack.messages()) != 1 || len(e.hook.all()) != 1 {
		t.Fatalf("resultados = %+v", res)
	}
	checkSignatureAt(t, e.hook.all()[0], digestSecret, e.clock.now())
	if d := e.hook.all()[0].Header.Get(HeaderDelivery); !strings.HasPrefix(d, "test-") {
		t.Errorf("delivery = %q", d)
	}
	var queued int
	_ = e.db.Pool().QueryRow(context.Background(), `SELECT count(*) FROM alert_outbox WHERE digest_project_id IS NOT NULL`).Scan(&queued)
	if queued != 0 {
		t.Errorf("mandar uno ahora no pasa por la tabla de salida: %d", queued)
	}

	// Sin SMTP, el correo falla y lo dice; el resto sale.
	e.opts.SMTPURL = ""
	a := e.newAlerter(t)
	res, err = a.SendDigestNow(context.Background(), e.project)
	if err != nil || res[0].OK || !strings.Contains(res[0].Error, "SMTP_URL") || !res[1].OK || !res[2].OK {
		t.Errorf("sin SMTP = %+v %v", res, err)
	}
}
