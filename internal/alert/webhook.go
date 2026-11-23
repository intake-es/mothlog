package alert

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/intake-es/intake-errors/internal/markdown"
	"github.com/intake-es/intake-errors/internal/store"
)

// Cabeceras del webhook genérico.
const (
	HeaderSignature = "X-Intake-Errors-Signature"
	HeaderEvent     = "X-Intake-Errors-Event"
	HeaderDelivery  = "X-Intake-Errors-Delivery"
)

// Sign firma el cuerpo del webhook: HMAC-SHA256, con el secreto de la acción,
// de «<t>.<cuerpo>», con t la fecha en segundos Unix. Va en HeaderSignature
// como «t=<t>,v1=<hex>».
func Sign(secret string, t time.Time, body []byte) string {
	ts := strconv.FormatInt(t.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "."))
	mac.Write(body)
	return "t=" + ts + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

type WebhookPayload struct {
	// Type es new, regressed, frequency, users, test o, en los crons,
	// cron_failed, cron_missed, cron_timeout y cron_recovered, y en los
	// monitores de disponibilidad, uptime_down y uptime_up.
	Type string `json:"type"`
	// Delivery es el mismo en los reintentos del mismo aviso.
	Delivery string      `json:"delivery"`
	SentAt   time.Time   `json:"sent_at"`
	Rule     WebhookRule `json:"rule"`
	Project  string      `json:"project"`
	// Issue va en los avisos de errores; Monitor y CheckIn, en los de crons;
	// Uptime e Incident, en los de disponibilidad.
	Issue    *WebhookIssue    `json:"issue,omitempty"`
	Monitor  *WebhookMonitor  `json:"monitor,omitempty"`
	CheckIn  *WebhookCheckIn  `json:"check_in,omitempty"`
	Uptime   *WebhookUptime   `json:"uptime,omitempty"`
	Incident *WebhookIncident `json:"incident,omitempty"`
	Event    *WebhookEvent    `json:"event"`
	Trigger  *WebhookTrigger  `json:"trigger,omitempty"`
	// ResolvedAt es, en una regresión, cuándo se había resuelto.
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
	// Markdown es el error como en stacktrace.md.
	Markdown string `json:"markdown"`
}

type WebhookRule struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type WebhookIssue struct {
	ID        int64     `json:"id"`
	Title     string    `json:"title"`
	Culprit   string    `json:"culprit"`
	Level     string    `json:"level"`
	Status    string    `json:"status"`
	Count     int64     `json:"count"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	URL       string    `json:"url,omitempty"`
}

type WebhookEvent struct {
	EventID     string            `json:"event_id"`
	Timestamp   time.Time         `json:"timestamp"`
	Level       string            `json:"level"`
	Release     string            `json:"release,omitempty"`
	Environment string            `json:"environment,omitempty"`
	Error       string            `json:"error"`
	Location    string            `json:"location,omitempty"`
	User        string            `json:"user,omitempty"`
	Request     string            `json:"request,omitempty"`
	Tags        map[string]string `json:"tags,omitempty"`
}

type WebhookTrigger struct {
	Events        int `json:"events,omitempty"`
	WindowMinutes int `json:"window_minutes,omitempty"`
	Users         int `json:"users,omitempty"`
}

type webhook struct {
	st     *store.Store
	url    string
	secret string
	client *http.Client
	now    func() time.Time
}

func (w *webhook) payload(ctx context.Context, p *pending) (WebhookPayload, error) {
	d, err := issueDetail(ctx, w.st, p)
	if err != nil {
		return WebhookPayload{}, err
	}
	out := WebhookPayload{
		Type:     p.kind,
		Delivery: "outbox-" + strconv.FormatInt(p.id, 10),
		SentAt:   w.now().UTC(),
		Rule:     WebhookRule{ID: p.ruleID, Name: p.ruleName},
		Project:  p.project,
		Issue: &WebhookIssue{ID: d.ID, Title: d.Title, Culprit: d.Culprit, Level: d.Level, Status: d.Status,
			Count: d.Count, FirstSeen: d.FirstSeen, LastSeen: d.LastSeen, URL: p.link},
		ResolvedAt: p.resolvedAt,
		Markdown:   markdown.Issue(d),
	}
	if p.kind == kindTest {
		out.Delivery = "test-" + newUUID()
	}
	if t := p.detail; t != (ruleDetail{}) {
		out.Trigger = &WebhookTrigger{Events: t.Events, WindowMinutes: t.WindowMinutes, Users: t.Users}
	}
	if se := d.LatestEvent; se != nil {
		e := se.Event
		we := &WebhookEvent{EventID: se.EventID, Timestamp: se.OccurredAt, Level: e.Level, Release: e.Release,
			Environment: e.Environment, Error: errorText(&e), Tags: e.Tags, Request: requestText(e.Request)}
		if f := failingFrame(&e); f != nil {
			we.Location = frameLocation(f)
		}
		if e.User != nil {
			we.User = e.User.ID
		}
		out.Event = we
	}
	return out, nil
}

func (w *webhook) send(ctx context.Context, p *pending) error {
	if p.digest != nil {
		return w.sendDigest(ctx, p)
	}
	var pl WebhookPayload
	var err error
	if p.cron != nil {
		pl = w.cronPayload(p)
	} else if p.uptime != nil {
		pl = w.uptimePayload(p)
	} else if pl, err = w.payload(ctx, p); err != nil {
		return err
	}
	return w.post(ctx, pl.Type, pl.Delivery, pl.SentAt, pl)
}

// post manda el cuerpo firmado con las cabeceras del webhook.
func (w *webhook) post(ctx context.Context, typ, delivery string, sentAt time.Time, payload any) error {
	var body bytes.Buffer
	enc := json.NewEncoder(&body)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(payload); err != nil {
		return err
	}
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set(HeaderEvent, typ)
	h.Set(HeaderDelivery, delivery)
	h.Set(HeaderSignature, Sign(w.secret, sentAt, body.Bytes()))
	return post(ctx, w.client, "webhook", w.url, body.Bytes(), h)
}
