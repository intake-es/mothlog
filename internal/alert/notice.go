package alert

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/intake-es/intake-errors/internal/event"
	"github.com/intake-es/intake-errors/internal/store"
)

// errGone es un aviso que ya no se puede enviar nunca, como el de una acción
// que se ha quitado de su regla: se abandona sin reintentos.
var errGone = errors.New("la acción ya no existe en la regla")

// ruleDetail es lo que hizo saltar la regla, para escribirlo en el aviso.
type ruleDetail struct {
	Events        int `json:"events,omitempty"`
	WindowMinutes int `json:"window_minutes,omitempty"`
	Users         int `json:"users,omitempty"`
}

const kindTest = "test"

// kindText da qué ha pasado («Nuevo error») y el principio del texto de
// respaldo («Nuevo error en »), al que sigue el proyecto.
func kindText(p *pending) (string, string) {
	switch p.kind {
	case "regressed":
		return "Ha vuelto", "Ha vuelto un error en "
	case "frequency":
		s := fmt.Sprintf("Más de %d eventos en %s", p.detail.Events, windowText(p.detail.WindowMinutes))
		return s, s + " en "
	case "users":
		s := fmt.Sprintf("Más de %d usuarios afectados", p.detail.Users)
		return s, s + " en "
	case kindTest:
		return "Aviso de prueba", "Aviso de prueba de "
	case store.KindCronFailed, store.KindCronMissed, store.KindCronTimeout, store.KindCronRecovered:
		s := cronKindText(p.kind)
		return s, s + " en "
	default:
		return "Nuevo error", "Nuevo error en "
	}
}

func windowText(m int) string {
	switch {
	case m >= 60 && m%60 == 0:
		return strconv.Itoa(m/60) + " h"
	case m > 60:
		return fmt.Sprintf("%d h %d min", m/60, m%60)
	default:
		return strconv.Itoa(m) + " min"
	}
}

// headline es «Tipo: valor» del último evento o, sin él, el título del issue.
func headline(p *pending) string {
	h := oneLine(errorText(p.ev))
	if h == "" {
		h = oneLine(p.title)
	}
	return truncate(h, maxHeadline)
}

// summary son las líneas de un aviso en texto: qué pasa, el error, dónde falla y los datos.
type summary struct {
	What     string
	Meta     []string
	Headline string
	Location string
	Code     string
	Fields   [][2]string
}

func summarize(p *pending) summary {
	if p.cron != nil {
		return cronSummary(p)
	}
	what, _ := kindText(p)
	s := summary{What: what, Headline: headline(p), Meta: []string{p.project}}
	if p.environment != "" {
		s.Meta = append(s.Meta, p.environment)
	}
	if p.kind == "regressed" && p.resolvedAt != nil {
		s.Meta = append(s.Meta, "se resolvió el "+spanishDate(*p.resolvedAt, p.eventAt))
	}
	if f := failingFrame(p.ev); f != nil {
		s.Location = frameLocation(f)
		s.Code = strings.TrimSpace(oneLine(f.ContextLine))
	} else if c := oneLine(p.culprit); c != "" {
		s.Location = c
	}
	s.Location = truncate(s.Location, maxCodeLine)
	s.Code = truncate(s.Code, maxCodeLine)
	add := func(name, value string) {
		if value = oneLine(value); value != "" {
			s.Fields = append(s.Fields, [2]string{name, truncate(value, maxValue)})
		}
	}
	release := p.release
	if isSHA(release) {
		release = release[:7]
	}
	add("Release", release)
	if p.kind == "regressed" || p.kind == "frequency" || p.kind == "users" {
		add("Eventos", strconv.FormatInt(p.count, 10)+" en total")
	}
	if ev := p.ev; ev != nil {
		if ev.User != nil && strings.TrimSpace(ev.User.ID) != "" {
			add("Usuario", ev.User.ID)
		} else if r := requestText(ev.Request); r != "" {
			add("Petición", r)
		}
	}
	return s
}

// frameLocation es «fichero:línea en función».
func frameLocation(f *event.Frame) string {
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
	if fn := oneLine(f.Function); fn != "" {
		return file + " en " + fn
	}
	return file
}

// text es el aviso en texto plano, para ntfy y el correo.
func (s summary) text(link string) string {
	var b strings.Builder
	b.WriteString(s.Headline + "\n")
	b.WriteString(s.What + " · " + strings.Join(s.Meta, " · ") + "\n")
	if s.Location != "" {
		b.WriteString("\n" + s.Location + "\n")
	}
	if s.Code != "" {
		b.WriteString("    " + s.Code + "\n")
	}
	if len(s.Fields) > 0 {
		b.WriteString("\n")
		for _, f := range s.Fields {
			b.WriteString(f[0] + ": " + f[1] + "\n")
		}
	}
	if link != "" {
		b.WriteString("\n" + link + "\n")
	}
	return b.String()
}

func postJSON(ctx context.Context, client *http.Client, name, url string, payload any, header http.Header) error {
	var body bytes.Buffer
	enc := json.NewEncoder(&body)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(payload); err != nil {
		return err
	}
	if header == nil {
		header = http.Header{}
	}
	header.Set("Content-Type", "application/json")
	return post(ctx, client, name, url, body.Bytes(), header)
}

func post(ctx context.Context, client *http.Client, name, url string, body []byte, header http.Header) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("%s: %w", name, cleanErr(err))
	}
	for k, v := range header {
		req.Header[k] = v
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "intake-errors")
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", name, cleanErr(err))
	}
	defer func() { _ = resp.Body.Close() }()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s: %s: %s", name, resp.Status, strings.TrimSpace(string(msg)))
	}
	return nil
}

func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}
