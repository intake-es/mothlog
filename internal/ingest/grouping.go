package ingest

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/intake-es/mothlog/internal/event"
)

const maxTitle = 200

// Grouping es lo que decide a qué issue va un evento y cómo se ve.
type Grouping struct {
	Fingerprint string
	Title       string
	Culprit     string
	Type        string
}

func Group(ev *event.Event) Grouping {
	g := Grouping{Fingerprint: fingerprint(ev), Culprit: culprit(ev)}
	if exc := thrown(ev); exc != nil {
		g.Type = exc.Type
		switch {
		case exc.Type != "" && exc.Value != "":
			g.Title = exc.Type + ": " + exc.Value
		case exc.Type != "":
			g.Title = exc.Type
		case exc.Value != "":
			g.Title = exc.Value
		default:
			g.Title = ev.Message
		}
	} else {
		g.Title = ev.Message
	}
	if g.Title == "" {
		g.Title = "(no message)"
	}
	g.Title = truncate(g.Title, maxTitle)
	return g
}

// thrown es la última excepción de la cadena, la que se lanzó.
func thrown(ev *event.Event) *event.Exception {
	if len(ev.Exceptions) == 0 {
		return nil
	}
	return &ev.Exceptions[len(ev.Exceptions)-1]
}

func fingerprint(ev *event.Event) string {
	def := defaultFingerprint(ev)
	if len(ev.Fingerprint) == 0 || (len(ev.Fingerprint) == 1 && isDefault(ev.Fingerprint[0])) {
		return def
	}
	parts := []string{"custom"}
	for _, v := range ev.Fingerprint {
		if isDefault(v) {
			v = def
		}
		parts = append(parts, v)
	}
	return hash(parts)
}

func isDefault(v string) bool {
	return strings.ReplaceAll(v, " ", "") == "{{default}}"
}

func defaultFingerprint(ev *event.Event) string {
	exc := thrown(ev)
	if exc == nil {
		return hash([]string{"message", ev.Logger, NormalizeMessage(ev.Message)})
	}
	parts := []string{"exception", exc.Type}
	for _, f := range groupingFrames(exc.Frames) {
		parts = append(parts, frameLocation(f), f.Function)
	}
	return hash(parts)
}

// groupingFrames son los fotogramas in_app, o todos si no hay ninguno.
func groupingFrames(frames []event.Frame) []event.Frame {
	var app []event.Frame
	for _, f := range frames {
		if isInApp(f) {
			app = append(app, f)
		}
	}
	if len(app) == 0 {
		return frames
	}
	return app
}

func isInApp(f event.Frame) bool { return f.InApp != nil && *f.InApp }

func frameLocation(f event.Frame) string {
	if f.Module != "" {
		return f.Module
	}
	return f.Filename
}

func culprit(ev *event.Event) string {
	if exc := thrown(ev); exc != nil {
		for i := len(exc.Frames) - 1; i >= 0; i-- {
			if f := exc.Frames[i]; isInApp(f) {
				return truncate(frameLocation(f)+" in "+f.Function, maxTitle)
			}
		}
	}
	return truncate(ev.Transaction, maxTitle)
}

func hash(parts []string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

var (
	reUUID   = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	reAlnum  = regexp.MustCompile(`[0-9A-Za-z]+`)
	reNumber = regexp.MustCompile(`\d+(?:\.\d+)?`)
)

// NormalizeMessage cambia por marcadores lo que varía entre dos envíos del
// mismo log: UUIDs, tramos hexadecimales de 8 o más caracteres y números.
func NormalizeMessage(m string) string {
	m = reUUID.ReplaceAllString(m, "<uuid>")
	m = reAlnum.ReplaceAllStringFunc(m, func(s string) string {
		if isHexRun(s) {
			return "<hex>"
		}
		return s
	})
	return reNumber.ReplaceAllString(m, "<n>")
}

// isHexRun deja fuera los tramos solo de dígitos, que son números: si no, un
// valor que pasa de 7 a 8 cifras cambiaría de marcador y partiría el issue.
func isHexRun(s string) bool {
	s = strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X")
	if len(s) < 8 {
		return false
	}
	letter := false
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
			letter = true
		default:
			return false
		}
	}
	return letter
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}
