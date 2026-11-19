// Package markdown escribe el último evento de un issue en Markdown para que
// lo lea un modelo.
package markdown

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/intake-es/intake-errors/internal/event"
	"github.com/intake-es/intake-errors/internal/store"
)

const maxBreadcrumbs = 20

func Issue(d store.IssueDetail) string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }

	w("# %s\n\n", oneLine(d.Title))
	w("- Issue: %d, estado %s, eventos: %d, primera vez %s, última vez %s\n",
		d.ID, d.Status, d.Count, ts(d.FirstSeen), ts(d.LastSeen))
	w("- Proyecto: %s\n", d.Project)
	if d.Culprit != "" {
		w("- Culprit: %s\n", oneLine(d.Culprit))
	}
	if d.LastRegression != nil {
		w("- Última regresión: %s\n", ts(*d.LastRegression))
	}
	if d.LatestEvent == nil {
		w("\nEl issue no tiene eventos guardados.\n")
		return b.String()
	}
	se := d.LatestEvent
	e := se.Event
	w("- Último evento: %s (id %d), %s\n", se.EventID, se.ID, ts(se.OccurredAt))
	w("- Release: %s\n", orNone(e.Release))
	w("- Entorno: %s\n", orNone(e.Environment))
	w("- Usuario: %s\n", user(e.User))
	if e.Request != nil {
		w("- Request: %s\n", strings.TrimSpace(e.Request.Method+" "+e.Request.URL))
	} else {
		w("- Request: (ninguna)\n")
	}
	if e.Transaction != "" {
		w("- Transaction: %s\n", e.Transaction)
	}
	if e.Logger != "" {
		w("- Logger: %s\n", e.Logger)
	}
	if e.ServerName != "" {
		w("- Servidor: %s\n", e.ServerName)
	}
	w("- Plataforma y SDK: %s, %s %s\n", orNone(e.Platform), e.SDK.Name, e.SDK.Version)

	w("\n## Etiquetas\n\n")
	if len(e.Tags) == 0 {
		w("(ninguna)\n")
	}
	for _, k := range slices.Sorted(maps.Keys(e.Tags)) {
		w("- %s: %s\n", k, oneLine(e.Tags[k]))
	}

	if e.Message != "" {
		w("\n## Mensaje\n\n%s", codeBlock([]string{e.Message}))
	}

	if len(e.Exceptions) > 0 {
		w("\n## Excepciones\n\nDe la causa a la que se lanzó; en cada una, los fotogramas van del más externo al que falló. Los de la aplicación llevan su código.\n")
		for i, x := range e.Exceptions {
			w("\n### %d. %s\n\n", i+1, oneLine(excTitle(x)))
			if m := x.Mechanism; m != nil {
				handled := "sin saber si se manejó"
				if m.Handled != nil {
					handled = map[bool]string{true: "manejada", false: "no manejada"}[*m.Handled]
				}
				w("Mecanismo: %s, %s\n\n", orNone(m.Type), handled)
			}
			if len(x.Frames) == 0 {
				w("(sin fotogramas)\n")
			}
			writeFrames(&b, x.Frames)
		}
	}

	crumbs := breadcrumbs(e.Breadcrumbs)
	if len(crumbs) > maxBreadcrumbs {
		crumbs = crumbs[len(crumbs)-maxBreadcrumbs:]
	}
	w("\n## Breadcrumbs\n\n")
	if len(crumbs) == 0 {
		w("(ninguno)\n")
	} else {
		w("Del más viejo al más nuevo; como mucho, los %d últimos.\n\n", maxBreadcrumbs)
	}
	for _, c := range crumbs {
		w("- %s\n", c)
	}
	return b.String()
}

func writeFrames(b *strings.Builder, frames []event.Frame) {
	afterCode := false
	for i, f := range frames {
		// Un fotograma sin in_app es casi siempre código propio (el __main__ en Python).
		app := f.InApp == nil || *f.InApp
		if !app || (f.ContextLine == "" && len(f.PreContext) == 0) {
			if afterCode {
				b.WriteString("\n")
			}
			afterCode = false
			mark := ""
			if app {
				mark = " (app)"
			}
			fmt.Fprintf(b, "- %s%s\n", location(f), mark)
			continue
		}
		if i > 0 {
			b.WriteString("\n")
		}
		afterCode = true
		fmt.Fprintf(b, "#### %s (app)\n\n", location(f))
		var lines []string
		first := f.Line - len(f.PreContext)
		width := len(strconv.Itoa(f.Line + len(f.PostContext)))
		add := func(n int, mark, s string) {
			if f.Line > 0 {
				lines = append(lines, fmt.Sprintf("%s %*d | %s", mark, width, n, s))
			} else {
				lines = append(lines, mark+" "+s)
			}
		}
		for i, s := range f.PreContext {
			add(first+i, " ", s)
		}
		add(f.Line, ">", f.ContextLine)
		for i, s := range f.PostContext {
			add(f.Line+1+i, " ", s)
		}
		b.WriteString(codeBlock(lines))
	}
}

func location(f event.Frame) string {
	file := f.Filename
	if file == "" {
		file = f.AbsPath
	}
	if file == "" {
		file = f.Module
	}
	if file == "" {
		file = "?"
	}
	if f.Line > 0 {
		file += ":" + strconv.Itoa(f.Line)
		if f.Column > 0 {
			file += ":" + strconv.Itoa(f.Column)
		}
	}
	s := "`" + file + "`"
	if f.Function != "" {
		s += " en `" + f.Function + "`"
	}
	if f.Module != "" && f.Module != f.Filename {
		s += " (módulo `" + f.Module + "`)"
	}
	return s
}

func excTitle(x event.Exception) string {
	switch {
	case x.Type != "" && x.Value != "":
		return x.Type + ": " + x.Value
	case x.Type != "":
		return x.Type
	}
	return x.Value
}

// codeBlock usa una valla más larga que cualquier tira de comillas del contenido.
func codeBlock(lines []string) string {
	fence := "```"
	for _, l := range lines {
		for strings.Contains(l, fence) {
			fence += "`"
		}
	}
	return fence + "\n" + strings.Join(lines, "\n") + "\n" + fence + "\n"
}

func user(u *event.User) string {
	if u == nil {
		u = &event.User{}
	}
	var parts []string
	for _, p := range [][2]string{{"id", u.ID}, {"email", u.Email}, {"username", u.Username}, {"ip", u.IPAddress}} {
		if p[1] != "" {
			parts = append(parts, p[0]+" "+p[1])
		}
	}
	if len(parts) == 0 {
		return "(ninguno)"
	}
	return strings.Join(parts, ", ")
}

type breadcrumb struct {
	Timestamp json.RawMessage `json:"timestamp"`
	Type      string          `json:"type"`
	Category  string          `json:"category"`
	Level     string          `json:"level"`
	Message   string          `json:"message"`
	Data      json.RawMessage `json:"data"`
}

// breadcrumbs acepta {"values": [...]} o la lista directa.
func breadcrumbs(raw json.RawMessage) []string {
	var list []json.RawMessage
	var wrap struct {
		Values []json.RawMessage `json:"values"`
	}
	if json.Unmarshal(raw, &wrap) == nil && wrap.Values != nil {
		list = wrap.Values
	} else {
		_ = json.Unmarshal(raw, &list)
	}
	var out []string
	for _, r := range list {
		var c breadcrumb
		if json.Unmarshal(r, &c) != nil {
			continue
		}
		parts := []string{crumbTime(c.Timestamp)}
		if c.Category != "" || c.Type != "" {
			parts = append(parts, "["+strings.Trim(c.Category+" "+c.Type, " ")+"]")
		}
		if c.Level != "" && c.Level != "info" {
			parts = append(parts, c.Level)
		}
		if c.Message != "" {
			parts = append(parts, oneLine(c.Message))
		}
		var data bytes.Buffer
		if json.Compact(&data, c.Data) == nil && data.String() != "null" && data.String() != "{}" {
			parts = append(parts, oneLine(data.String()))
		}
		out = append(out, strings.Join(parts, " "))
	}
	return out
}

func crumbTime(raw json.RawMessage) string {
	var f float64
	if json.Unmarshal(raw, &f) == nil && f > 0 {
		sec := int64(f)
		return ts(time.Unix(sec, int64((f-float64(sec))*1e9)))
	}
	var s string
	if json.Unmarshal(raw, &s) == nil && s != "" {
		return s
	}
	return "?"
}

func ts(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func orNone(s string) string {
	if s == "" {
		return "(ninguno)"
	}
	return s
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 500 {
		s = string(r[:500]) + "…"
	}
	return s
}
