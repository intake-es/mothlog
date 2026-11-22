package alert

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/intake-es/intake-errors/internal/event"
)

// Límites de Block Kit.
const (
	maxSectionText = 3000
	maxFieldText   = 2000
	maxButtonText  = 75
)

const (
	maxHeadline = 300
	maxCodeLine = 300
	maxValue    = 100
)

type slack struct {
	url    string
	client *http.Client
}

var slackEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

func esc(s string) string { return slackEscaper.Replace(s) }

type slackText struct {
	Type  string `json:"type"`
	Text  string `json:"text"`
	Emoji *bool  `json:"emoji,omitempty"`
}

type slackButton struct {
	Type  string    `json:"type"`
	Text  slackText `json:"text"`
	URL   string    `json:"url"`
	Style string    `json:"style,omitempty"`
}

type slackBlock struct {
	Type     string      `json:"type"`
	Text     *slackText  `json:"text,omitempty"`
	Fields   []slackText `json:"fields,omitempty"`
	Elements []any       `json:"elements,omitempty"`
}

type slackPayload struct {
	Text   string       `json:"text"`
	Blocks []slackBlock `json:"blocks"`
}

func mrkdwn(s string, limit int) slackText {
	return slackText{Type: "mrkdwn", Text: truncate(s, limit)}
}

func plain(s string, limit int) slackText {
	no := false
	return slackText{Type: "plain_text", Text: truncate(s, limit), Emoji: &no}
}

func section(s string) slackBlock {
	t := mrkdwn(s, maxSectionText)
	return slackBlock{Type: "section", Text: &t}
}

// slackMessage arma el aviso con Block Kit. ev es el último evento del issue
// y puede ser nil.
func slackMessage(p *pending, ev *event.Event, link string) slackPayload {
	headline := oneLine(errorText(ev))
	if headline == "" {
		headline = oneLine(p.title)
	}
	headline = truncate(headline, maxHeadline)

	what, fallback := kindText(p)
	meta := []string{"*" + what + "*", esc(p.project)}
	if p.environment != "" {
		meta = append(meta, esc(p.environment))
	}
	if p.kind == "regressed" && p.resolvedAt != nil {
		meta = append(meta, "se resolvió el "+spanishDate(*p.resolvedAt, p.eventAt))
	}

	msg := slackPayload{Text: esc(fallback + p.project + ": " + headline)}
	msg.Blocks = append(msg.Blocks,
		slackBlock{Type: "context", Elements: []any{mrkdwn(strings.Join(meta, " · "), maxSectionText)}},
		section("*"+esc(strings.ReplaceAll(headline, "*", "\u2217"))+"*"))

	if f := failingFrame(ev); f != nil {
		msg.Blocks = append(msg.Blocks, section(frameText(f)))
	} else if c := oneLine(p.culprit); c != "" {
		msg.Blocks = append(msg.Blocks, section("`"+esc(truncate(c, maxCodeLine))+"`"))
	}

	if fields := slackFields(p, ev); len(fields) > 0 {
		msg.Blocks = append(msg.Blocks, slackBlock{Type: "section", Fields: fields})
	}

	var buttons []any
	if link != "" {
		buttons = append(buttons, slackButton{Type: "button", Text: plain("Ver el error", maxButtonText), URL: link, Style: "primary"})
	}
	if p.linearIdentifier != "" && p.linearURL != "" {
		buttons = append(buttons, slackButton{Type: "button", Text: plain(p.linearIdentifier+" en Linear", maxButtonText), URL: p.linearURL})
	}
	if len(buttons) > 0 {
		msg.Blocks = append(msg.Blocks, slackBlock{Type: "actions", Elements: buttons})
	}
	return msg
}

// errorText es «Tipo: valor» de la excepción lanzada, o el mensaje.
func errorText(ev *event.Event) string {
	if ev == nil {
		return ""
	}
	if n := len(ev.Exceptions); n > 0 {
		exc := ev.Exceptions[n-1]
		switch {
		case exc.Type != "" && exc.Value != "":
			return exc.Type + ": " + exc.Value
		case exc.Type != "":
			return exc.Type
		case exc.Value != "":
			return exc.Value
		}
	}
	return ev.Message
}

// failingFrame es el fotograma in_app más cercano al error, el del culprit.
func failingFrame(ev *event.Event) *event.Frame {
	if ev == nil || len(ev.Exceptions) == 0 {
		return nil
	}
	frames := ev.Exceptions[len(ev.Exceptions)-1].Frames
	for i := len(frames) - 1; i >= 0; i-- {
		if f := frames[i]; f.InApp != nil && *f.InApp {
			return &frames[i]
		}
	}
	return nil
}

func frameText(f *event.Frame) string {
	file := f.Filename
	if file == "" {
		file = f.AbsPath
	}
	if file == "" {
		file = f.Module
	}
	if f.Line > 0 {
		file += ":" + strconv.Itoa(f.Line)
	}
	var b strings.Builder
	b.WriteString(code(truncate(file, maxCodeLine)))
	if fn := oneLine(f.Function); fn != "" {
		b.WriteString(" en " + code(truncate(fn, maxCodeLine)))
	}
	if line := strings.TrimSpace(f.ContextLine); line != "" {
		line = strings.ReplaceAll(truncate(oneLine(line), maxCodeLine), "```", "`\u200b``")
		b.WriteString("\n```" + esc(line) + "```")
	}
	return b.String()
}

func code(s string) string {
	return "`" + esc(strings.ReplaceAll(oneLine(s), "`", "'")) + "`"
}

var reSHA = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

// isSHA pide una letra para no tomar por SHA un número de build o una fecha.
func isSHA(s string) bool { return reSHA.MatchString(s) && strings.ContainsAny(s, "abcdef") }

func slackFields(p *pending, ev *event.Event) []slackText {
	var fields []slackText
	add := func(name, value string) {
		if value = oneLine(value); value != "" {
			fields = append(fields, mrkdwn("*"+name+"*\n"+esc(truncate(value, maxValue)), maxFieldText))
		}
	}
	release := p.release
	if isSHA(release) {
		release = release[:7]
	}
	add("Release", release)
	if ev != nil {
		if name := ev.Tags["organization_name"]; strings.TrimSpace(name) != "" {
			add("Espacio", name)
		} else if id := strings.TrimSpace(ev.Tags["organization_id"]); id != "" {
			add("Espacio", shortID(id))
		}
	}
	if p.kind == "regressed" || p.kind == "frequency" || p.kind == "users" {
		add("Eventos", strconv.FormatInt(p.count, 10)+" en total")
	}
	if ev != nil {
		if ev.User != nil && strings.TrimSpace(ev.User.ID) != "" {
			add("Usuario", ev.User.ID)
		} else if r := requestText(ev.Request); r != "" {
			add("Petición", r)
		}
	}
	return fields
}

func shortID(id string) string {
	r := []rune(id)
	if len(r) <= 8 {
		return id
	}
	return string(r[:8])
}

// requestText es el método y la ruta, sin host ni query string.
func requestText(r *event.Request) string {
	if r == nil || r.URL == "" {
		return ""
	}
	path := r.URL
	if u, err := url.Parse(r.URL); err == nil {
		path = u.EscapedPath()
	} else {
		path, _, _ = strings.Cut(path, "?")
		path, _, _ = strings.Cut(path, "#")
	}
	if path == "" {
		path = "/"
	}
	return strings.TrimSpace(strings.ToUpper(r.Method) + " " + path)
}

var spanishMonths = [...]string{"enero", "febrero", "marzo", "abril", "mayo", "junio", "julio",
	"agosto", "septiembre", "octubre", "noviembre", "diciembre"}

var madrid = func() *time.Location {
	if loc, err := time.LoadLocation("Europe/Madrid"); err == nil {
		return loc
	}
	return time.UTC
}()

// spanishDate da «3 de noviembre», con el año si no es el de ref.
func spanishDate(t, ref time.Time) string {
	t, ref = t.In(madrid), ref.In(madrid)
	s := fmt.Sprintf("%d de %s", t.Day(), spanishMonths[t.Month()-1])
	if t.Year() != ref.Year() {
		s += fmt.Sprintf(" de %d", t.Year())
	}
	return s
}

func (s *slack) send(ctx context.Context, _ pgx.Tx, p *pending) error {
	if p.cron != nil {
		return postJSON(ctx, s.client, "slack", s.url, slackCron(p), nil)
	}
	return postJSON(ctx, s.client, "slack", s.url, slackMessage(p, p.ev, p.link), nil)
}

func oneLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(s)
}
