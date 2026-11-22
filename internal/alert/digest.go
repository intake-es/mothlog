package alert

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/intake-es/intake-errors/internal/digest"
	"github.com/intake-es/intake-errors/internal/store"
)

// loadDigest arma el resumen de una fila de la tabla de salida y da el envío
// por su destino como está ahora en la configuración del proyecto.
func (a *Alerter) loadDigest(ctx context.Context, p *pending, projectID int64, detail []byte) (sender, error) {
	cfg, err := a.st.Digest(ctx, projectID, a.now())
	if errors.Is(err, store.ErrNotFound) {
		return nil, errGone
	}
	if err != nil {
		return nil, err
	}
	if !cfg.Enabled {
		return nil, fmt.Errorf("%w: el resumen está apagado", errGone)
	}
	i := slices.IndexFunc(cfg.Destinations, func(x store.RuleAction) bool { return x.ID == p.actionID })
	if i < 0 {
		return nil, fmt.Errorf("%w: el destino ya no está en el resumen", errGone)
	}
	var dd store.DigestDetail
	if err := json.Unmarshal(detail, &dd); err != nil {
		return nil, fmt.Errorf("resumen %d: %w", p.id, err)
	}
	d, err := digest.Build(ctx, a.st, digest.Options{Config: cfg, Start: dd.PeriodStart, End: p.eventAt, Base: a.base})
	if err != nil {
		return nil, err
	}
	p.digest, p.digestProject, p.project = d, projectID, d.Project
	return a.actionSender(cfg.Destinations[i])
}

// CheckDigest comprueba que el servidor tiene lo que piden los destinos del resumen.
func (a *Alerter) CheckDigest(c store.DigestConfig) error {
	return a.CheckRule(store.Rule{Actions: c.Destinations})
}

// DigestResult es cómo ha ido el envío a un destino del resumen.
type DigestResult struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// SendDigestNow manda ya, sin pasar por la tabla de salida, el resumen del
// periodo que acaba ahora (24 horas o 7 días) a cada destino del proyecto.
func (a *Alerter) SendDigestNow(ctx context.Context, projectID int64) ([]DigestResult, error) {
	now := a.now()
	cfg, err := a.st.Digest(ctx, projectID, now)
	if err != nil {
		return nil, err
	}
	if len(cfg.Destinations) == 0 {
		return nil, fmt.Errorf("%w: el resumen no tiene ningún destino", store.ErrInvalid)
	}
	d, err := digest.Build(ctx, a.st, digest.Options{Config: cfg, Start: now.Add(-cfg.Length()), End: now, Base: a.base})
	if err != nil {
		return nil, err
	}
	out := make([]DigestResult, 0, len(cfg.Destinations))
	for _, dest := range cfg.Destinations {
		r := DigestResult{ID: dest.ID, Type: dest.Type}
		err := a.CheckRule(store.Rule{Actions: []store.RuleAction{dest}})
		var snd sender
		if err == nil {
			snd, err = a.actionSender(dest)
		}
		if err == nil {
			p := &pending{kind: store.KindDigest, digest: d, digestProject: projectID, project: d.Project, eventAt: now}
			err = snd.send(ctx, nil, p)
		}
		if err != nil {
			r.Error = strings.TrimPrefix(err.Error(), store.ErrInvalid.Error()+": ")
		} else {
			r.OK = true
		}
		out = append(out, r)
	}
	return out, nil
}

// delivery es el id del envío del webhook: el de la fila, o uno nuevo en el que se manda a mano.
func (p *pending) delivery() string {
	if p.id == 0 {
		return "test-" + newUUID()
	}
	return "outbox-" + strconv.FormatInt(p.id, 10)
}

// digestMessage es el correo del resumen: el texto y el HTML como alternativas.
func (e *email) digestMessage(d *digest.Digest, now time.Time) []byte {
	boundary := "ie-" + strings.ReplaceAll(newUUID(), "-", "")
	var m strings.Builder
	header := func(k, v string) { m.WriteString(k + ": " + v + "\r\n") }
	header("From", e.cfg.from)
	header("To", strings.Join(e.to, ", "))
	header("Subject", mime.QEncoding.Encode("utf-8", "[intake-errors] "+d.Subject()))
	header("Date", now.Format(time.RFC1123Z))
	header("Message-ID", "<"+newUUID()+"@intake-errors>")
	header("MIME-Version", "1.0")
	header("Content-Type", `multipart/alternative; boundary="`+boundary+`"`)
	m.WriteString("\r\n")
	for _, part := range []struct{ typ, body string }{{"text/plain", d.Text()}, {"text/html", d.HTML()}} {
		m.WriteString("--" + boundary + "\r\n")
		header("Content-Type", part.typ+"; charset=utf-8")
		header("Content-Transfer-Encoding", "8bit")
		m.WriteString("\r\n")
		m.WriteString(crlf(part.body))
		m.WriteString("\r\n")
	}
	m.WriteString("--" + boundary + "--\r\n")
	return []byte(m.String())
}

// crlf deja los saltos en CRLF y parte las líneas de más de 998 bytes, que SMTP no admite.
func crlf(s string) string {
	var b strings.Builder
	for line := range strings.SplitSeq(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		for len(line) > 900 {
			cut := strings.LastIndexAny(line[:900], " >")
			for cut <= 0 || !utf8.RuneStart(line[cut+1]) {
				if cut <= 0 {
					cut = 899
				} else {
					cut--
				}
			}
			b.WriteString(line[:cut+1] + "\r\n")
			line = line[cut+1:]
		}
		b.WriteString(line + "\r\n")
	}
	return b.String()
}

func (w *webhook) sendDigest(ctx context.Context, p *pending) error {
	pl := p.digest.Webhook(p.delivery(), w.now())
	return w.post(ctx, pl.Type, pl.Delivery, pl.SentAt, pl)
}
