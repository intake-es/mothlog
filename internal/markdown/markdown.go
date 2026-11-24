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
	"unicode"

	"github.com/intake-es/intake-errors/internal/event"
	"github.com/intake-es/intake-errors/internal/store"
)

const maxBreadcrumbs = 20

// Issue escribe el issue para un agente. Los datos del evento van en una sola
// línea o en bloques de código, para que no puedan abrir secciones propias.
func Issue(d store.IssueDetail) string { return issue(d, style{}) }

// IssueInert es Issue para publicar en GitHub o Linear: además, las menciones,
// los enlaces y el HTML que trae el evento quedan como texto.
func IssueInert(d store.IssueDetail) string { return issue(d, style{inert: true}) }

func issue(d store.IssueDetail, st style) string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	t := st.text

	w("# %s\n\n", t(d.Title))
	w("- Issue: %d, estado %s, eventos: %d, primera vez %s, última vez %s\n",
		d.ID, d.Status, d.Count, ts(d.FirstSeen), ts(d.LastSeen))
	w("- Proyecto: %s\n", d.Project)
	if d.Culprit != "" {
		w("- Culprit: %s\n", t(d.Culprit))
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
	w("- Último evento: %s (id %d), %s\n", t(se.EventID), se.ID, ts(se.OccurredAt))
	writeEventFields(&b, e, st)

	w("\n## Etiquetas\n\n")
	if len(e.Tags) == 0 {
		w("(ninguna)\n")
	}
	for _, k := range slices.Sorted(maps.Keys(e.Tags)) {
		w("- %s: %s\n", t(k), t(e.Tags[k]))
	}

	if e.Message != "" {
		w("\n## Mensaje\n\n%s", codeBlock([]string{e.Message}))
	}

	if len(e.Exceptions) > 0 {
		w("\n## Excepciones\n\nDe la causa a la que se lanzó; en cada una, los fotogramas van del más externo al que falló. Los de la aplicación llevan su código.\n")
		for i, x := range e.Exceptions {
			w("\n### %d. %s\n\n", i+1, t(excTitle(x)))
			writeMechanism(&b, x.Mechanism, st)
			if len(x.Frames) == 0 {
				w("(sin fotogramas)\n")
			}
			writeFrames(&b, st, x.Frames, "####", false)
		}
	}

	crumbs := breadcrumbs(e.Breadcrumbs, st)
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

// writeEventFields son las líneas del evento que van en Issue y en Context.
func writeEventFields(b *strings.Builder, e event.Event, st style) {
	w := func(format string, a ...any) { fmt.Fprintf(b, format, a...) }
	t := st.text
	w("- Release: %s\n", orNone(t(e.Release)))
	w("- Entorno: %s\n", orNone(t(e.Environment)))
	w("- Usuario: %s\n", user(e.User, st))
	if e.Request != nil {
		w("- Request: %s\n", t(e.Request.Method+" "+e.Request.URL))
	} else {
		w("- Request: (ninguna)\n")
	}
	if v := t(e.Transaction); v != "" {
		w("- Transaction: %s\n", v)
	}
	if v := t(e.Logger); v != "" {
		w("- Logger: %s\n", v)
	}
	if v := t(e.ServerName); v != "" {
		w("- Servidor: %s\n", v)
	}
	w("- Plataforma y SDK: %s, %s %s\n", orNone(t(e.Platform)), t(e.SDK.Name), t(e.SDK.Version))
}

func writeMechanism(b *strings.Builder, m *event.Mechanism, st style) {
	if m == nil {
		return
	}
	handled := "sin saber si se manejó"
	if m.Handled != nil {
		handled = map[bool]string{true: "manejada", false: "no manejada"}[*m.Handled]
	}
	fmt.Fprintf(b, "Mecanismo: %s, %s\n\n", orNone(st.text(m.Type)), handled)
}

// writeFrames pone los fotogramas con código bajo un título de nivel heading.
// Con libsCollapsed, cada tira de fotogramas de librerías es una sola línea.
func writeFrames(b *strings.Builder, st style, frames []event.Frame, heading string, libsCollapsed bool) {
	afterCode := false
	libs := 0
	flushLibs := func() {
		if libs > 0 {
			fmt.Fprintf(b, "- (%d %s de librerías, sin poner)\n", libs, map[bool]string{true: "fotograma", false: "fotogramas"}[libs == 1])
			libs = 0
		}
	}
	for i, f := range frames {
		// Un fotograma sin in_app es casi siempre código propio (el __main__ en Python).
		app := f.InApp == nil || *f.InApp
		if !app && libsCollapsed {
			if libs == 0 && afterCode {
				b.WriteString("\n")
			}
			afterCode = false
			libs++
			continue
		}
		flushLibs()
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
		fmt.Fprintf(b, "%s %s (app)\n\n", heading, location(f))
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
	flushLibs()
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
	s := codeSpan(file)
	if f.Function != "" {
		s += " en " + codeSpan(f.Function)
	}
	if f.Module != "" && f.Module != f.Filename {
		s += " (módulo " + codeSpan(f.Module) + ")"
	}
	return s
}

// codeSpan pone s en un span de código con más acentos graves que la tira
// más larga de s, como codeBlock, y en una sola línea.
func codeSpan(s string) string {
	s = oneLine(s)
	run, longest := 0, 0
	for _, r := range s {
		if r == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	tick := strings.Repeat("`", longest+1)
	if strings.HasPrefix(s, "`") || strings.HasSuffix(s, "`") {
		s = " " + s + " "
	}
	return tick + s + tick
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

func user(u *event.User, st style) string {
	if u == nil {
		u = &event.User{}
	}
	var parts []string
	for _, p := range [][2]string{{"id", u.ID}, {"email", u.Email}, {"username", u.Username}, {"ip", u.IPAddress}} {
		if v := st.text(p[1]); v != "" {
			parts = append(parts, p[0]+" "+v)
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
func breadcrumbs(raw json.RawMessage, st style) []string {
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
		parts := []string{st.text(crumbTime(c.Timestamp))}
		if what := st.text(c.Category + " " + c.Type); what != "" {
			// Entre corchetes de texto: en GitHub y Linear, escapados.
			parts = append(parts, st.bracket("[")+what+st.bracket("]"))
		}
		if level := st.text(c.Level); level != "" && level != "info" {
			parts = append(parts, level)
		}
		if m := st.text(c.Message); m != "" {
			parts = append(parts, m)
		}
		var data bytes.Buffer
		if json.Compact(&data, c.Data) == nil && data.String() != "null" && data.String() != "{}" {
			parts = append(parts, st.text(data.String()))
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

// style dice cómo va un dato del evento fuera de los bloques de código.
type style struct{ inert bool }

// inertEscaper deja como texto los enlaces, el HTML y las entidades, que
// GitHub convertiría después en una @ de mención (&#64;).
var inertEscaper = strings.NewReplacer(`\`, `\\`, "[", `\[`, "]", `\]`, "<", `\<`, "&", `\&`)

func (st style) text(s string) string {
	s = oneLine(s)
	if st.inert {
		s = noMentions(inertEscaper.Replace(s))
	}
	return s
}

// noMentions pone un espacio de anchura cero tras cada @ que GitHub tomaría
// por mención, como hace el aviso de Discord. Tras una letra o un número
// (un correo, api@2.0) no lo es, y se deja.
func noMentions(s string) string {
	if !strings.Contains(s, "@") {
		return s
	}
	var b strings.Builder
	prev := ' '
	for _, r := range s {
		b.WriteRune(r)
		if r == '@' && !unicode.IsLetter(prev) && !unicode.IsDigit(prev) && prev != '_' {
			b.WriteRune('\u200b')
		}
		prev = r
	}
	return b.String()
}

func (st style) bracket(s string) string {
	if st.inert {
		return `\` + s
	}
	return s
}

// oneLine deja s en una línea: sin saltos ni caracteres de control, que en
// el Markdown abrirían secciones o puntos de lista que no son nuestros.
func oneLine(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 500 {
		s = string(r[:500]) + "…"
	}
	return s
}

// InertText es un dato del evento como lo ponen IssueInert y ContextInert: en
// una línea y sin menciones, enlaces ni HTML vivos.
func InertText(s string) string { return style{inert: true}.text(s) }

// CodeSpan pone s en una línea dentro de un span de código que no se puede
// cerrar desde dentro.
func CodeSpan(s string) string { return codeSpan(s) }
