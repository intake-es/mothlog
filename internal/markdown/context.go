package markdown

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/intake-es/intake-errors/internal/store"
)

// MaxContextBytes es lo más que ocupa un context.md; pasado, se recorta.
const MaxContextBytes = 64 << 10

// tagOrder pone primero las etiquetas de más uso, como la interfaz.
var tagOrder = []string{"browser", "os", "device", "runtime", "release", "environment", "level"}

const (
	contextTagValues = 3
	contextTags      = 12
)

// level es lo que se quita en cada vuelta de recorte.
type level struct {
	crumbs        int
	libsCollapsed bool
	history       int // entradas del historial; -1, todas
	note          []string
}

var levels = []level{
	{crumbs: maxBreadcrumbs, history: -1},
	{crumbs: 5, history: -1, note: []string{"breadcrumbs"}},
	{crumbs: 5, libsCollapsed: true, history: -1, note: []string{"breadcrumbs", "fotogramas de librerías"}},
	{crumbs: 0, libsCollapsed: true, history: 20, note: []string{"breadcrumbs", "fotogramas de librerías", "historial viejo"}},
	{crumbs: 0, libsCollapsed: true, history: 5, note: []string{"breadcrumbs", "fotogramas de librerías", "historial viejo"}},
}

// Context escribe en un solo documento lo que un agente necesita para
// entender un error y actuar sobre él: el resumen, las releases, los
// entornos, las etiquetas, el evento con su traza, el historial y qué puede
// hacer. url es el enlace al issue en la interfaz (vacío, sin enlace). Si
// pasa de max bytes, quita breadcrumbs, fotogramas de librerías e historial
// viejo, por ese orden, y lo dice al principio.
func Context(c store.IssueContext, url string, max int) string {
	if max <= 0 {
		max = MaxContextBytes
	}
	var out string
	for _, l := range levels {
		out = writeContext(c, url, l, max)
		if len(out) <= max {
			return out
		}
	}
	// Ni así: se corta por la última línea entera que quepa.
	tail := "\n\n> El documento seguía pasando de " + kb(max) + " y se ha cortado aquí. El resto, con `get_event` y `list_comments`.\n"
	cut := max - len(tail)
	if cut < 0 {
		cut = 0
	}
	for cut > 0 && cut < len(out) && out[cut] != '\n' {
		cut--
	}
	return out[:cut] + tail
}

func kb(n int) string { return strconv.Itoa(n>>10) + " KB" }

func writeContext(c store.IssueContext, url string, l level, max int) string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }

	w("# %s\n\n", oneLine(c.Title))
	if len(l.note) > 0 {
		w("> Recortado para no pasar de %s: %s. El evento entero, con `get_event`; el historial, con `list_comments`.\n\n",
			kb(max), strings.Join(l.note, ", "))
	}

	w("## Resumen\n\n")
	status := c.Status
	if c.Status == "open" && c.LastRegression != nil {
		status += " (regresión, " + ts(*c.LastRegression) + ")"
	}
	w("- Issue: %d\n", c.ID)
	w("- Estado: %s\n", status)
	w("- Nivel: %s\n", orNone(c.Level))
	w("- Proyecto: %s\n", c.Project)
	w("- Asignado: %s\n", assignee(c.Assignee))
	if c.Culprit != "" {
		w("- Culprit: %s\n", oneLine(c.Culprit))
	}
	scope := ""
	if c.Environment != "" {
		scope = " en " + c.Environment
	}
	w("- Eventos%s: %d; usuarios afectados: %d\n", scope, c.Count, c.UserCount)
	w("- Primera vez%s: %s; última vez: %s\n", scope, ts(c.FirstSeen), ts(c.LastSeen))
	if url != "" {
		w("- En la interfaz: %s\n", url)
	}

	w("\n## Releases\n\n")
	w("- Primera: %s\n", orNone(deref(c.FirstRelease)))
	w("- Última: %s\n", orNone(deref(c.LastRelease)))
	switch {
	case c.Status == "resolved_next" && c.ResolvedInRelease != nil:
		w("- Resuelto en la siguiente a: %s (pasa a resuelto al desplegarse una posterior)\n", *c.ResolvedInRelease)
	case c.FixedInRelease != nil:
		w("- Resuelto con: %s\n", *c.FixedInRelease)
	default:
		w("- Resuelto con: (ninguna)\n")
	}

	w("\n## Entornos\n\n")
	if len(c.Environments) == 0 {
		w("(ninguno)\n")
	}
	for _, e := range c.Environments {
		w("- %s: %d %s\n", e.Value, e.Count, plural(e.Count, "evento", "eventos"))
	}

	writeLinks(&b, c.Links)
	writeContextTags(&b, c.Tags)
	writeContextEvent(&b, c, l)
	writeHistory(&b, c.History, l.history)
	writeActions(&b, c)
	return b.String()
}

var linkNames = map[string]string{
	store.ProviderLinear + "/" + store.LinkIssue:       "Issue de Linear",
	store.ProviderGitHub + "/" + store.LinkIssue:       "Issue de GitHub",
	store.ProviderGitHub + "/" + store.LinkPullRequest: "PR de GitHub",
}

var linkStates = map[string]string{
	store.LinkOpen: "abierta", store.LinkClosed: "cerrada", store.LinkCanceled: "cancelada", store.LinkMerged: "mergeado",
}

func writeLinks(b *strings.Builder, links []store.IssueLink) {
	if len(links) == 0 {
		return
	}
	b.WriteString("\n## Enlaces\n\n")
	for _, l := range links {
		name := orNone(l.Identifier)
		if l.Identifier == "" {
			name = l.ExternalID
		}
		state := linkStates[l.State]
		if l.StateName != "" && !strings.EqualFold(l.StateName, l.State) {
			state = strings.TrimSpace(state + " (" + l.StateName + ")")
		}
		fmt.Fprintf(b, "- %s %s: %s", linkNames[l.Provider+"/"+l.Kind], name, orNone(state))
		if l.URL != "" {
			fmt.Fprintf(b, ", %s", l.URL)
		}
		b.WriteString("\n")
	}
}

func writeContextTags(b *strings.Builder, t store.IssueTags) {
	fmt.Fprintf(b, "\n## Etiquetas más frecuentes\n\n")
	if len(t.Tags) == 0 {
		b.WriteString("(ninguna)\n")
		return
	}
	b.WriteString("Sobre los eventos que traían cada etiqueta; para buscar por una, `list_issues` con `clave:valor`.\n\n")
	rank := func(k string) int {
		if i := slices.Index(tagOrder, k); i >= 0 {
			return i
		}
		return len(tagOrder)
	}
	tags := slices.Clone(t.Tags)
	slices.SortStableFunc(tags, func(a, b store.TagDistribution) int {
		if d := rank(a.Key) - rank(b.Key); d != 0 {
			return d
		}
		return strings.Compare(a.Key, b.Key)
	})
	for i, d := range tags {
		if i == contextTags {
			fmt.Fprintf(b, "- (%d etiquetas más, con `get_issue_tags`)\n", len(tags)-contextTags)
			break
		}
		var vals []string
		for j, v := range d.Values {
			if j == contextTagValues {
				break
			}
			vals = append(vals, fmt.Sprintf("%s %s %% (%d)", oneLine(v.Value), pct(v.Percent), v.Count))
		}
		more := ""
		if d.Distinct > len(vals) {
			more = fmt.Sprintf("; %d valores distintos", d.Distinct)
		}
		fmt.Fprintf(b, "- %s: %s%s\n", d.Key, strings.Join(vals, ", "), more)
	}
}

func pct(p float64) string {
	s := strconv.FormatFloat(p, 'f', 1, 64)
	return strings.TrimSuffix(s, ".0")
}

func writeContextEvent(b *strings.Builder, c store.IssueContext, l level) {
	w := func(format string, a ...any) { fmt.Fprintf(b, format, a...) }
	se := c.LatestEvent
	if se == nil {
		w("\n## Evento\n\nEl issue no tiene eventos guardados")
		if c.Environment != "" {
			w(" en %s", c.Environment)
		}
		w(".\n")
		return
	}
	which := "el último"
	if c.EventChosen {
		which = "el pedido"
	} else if c.Environment != "" {
		which = "el último en " + c.Environment
	}
	e := se.Event
	w("\n## Evento (%s)\n\n", which)
	w("- Id: %s (id interno %d), %s\n", se.EventID, se.ID, ts(se.OccurredAt))
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
	if len(e.Tags) > 0 {
		var tags []string
		for _, k := range slices.Sorted(maps.Keys(e.Tags)) {
			tags = append(tags, k+"="+oneLine(e.Tags[k]))
		}
		w("- Etiquetas: %s\n", strings.Join(tags, ", "))
	}

	if e.Message != "" {
		w("\n### Mensaje\n\n%s", codeBlock([]string{e.Message}))
	}
	if len(e.Exceptions) > 0 {
		w("\n### Excepciones\n\nDe la causa a la que se lanzó; en cada una, los fotogramas van del más externo al que falló. Los de la aplicación llevan su código.\n")
		for i, x := range e.Exceptions {
			w("\n#### %d. %s\n\n", i+1, oneLine(excTitle(x)))
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
			writeFrames(b, x.Frames, "#####", l.libsCollapsed)
		}
	}

	crumbs := breadcrumbs(e.Breadcrumbs)
	total := len(crumbs)
	if len(crumbs) > l.crumbs {
		crumbs = crumbs[len(crumbs)-l.crumbs:]
	}
	w("\n### Breadcrumbs\n\n")
	switch {
	case total == 0:
		w("(ninguno)\n")
	case len(crumbs) == 0:
		w("(%d, quitados por el tamaño)\n", total)
	case len(crumbs) < total:
		w("Del más viejo al más nuevo; los %d últimos de %d.\n\n", len(crumbs), total)
	default:
		w("Del más viejo al más nuevo.\n\n")
	}
	for _, c := range crumbs {
		w("- %s\n", c)
	}
}

var sourceNames = map[string]string{
	store.SourceUI:     "desde la interfaz",
	store.SourceAPI:    "desde la API",
	store.SourceMCP:    "desde el MCP",
	store.SourceIngest: "al llegar un evento",
	store.SourceDeploy: "al desplegar",
	store.SourceLinear: "desde Linear",
	store.SourceGitHub: "desde GitHub",
}

func actor(a *store.Actor) string {
	switch {
	case a == nil:
		return "intake-errors"
	case a.Kind == store.ActorToken:
		return a.Name + " (token)"
	}
	return a.Name
}

func assignee(a *store.Assignee) string {
	if a == nil {
		return "(nadie)"
	}
	s := a.Name
	switch {
	case a.Kind == store.ActorToken && !a.Active:
		s += " (token revocado)"
	case a.Kind == store.ActorToken:
		s += " (token)"
	case a.DisplayName != "":
		s += " (" + a.DisplayName + ")"
	}
	return s
}

func writeHistory(b *strings.Builder, h []store.HistoryEntry, keep int) {
	b.WriteString("\n## Historial\n\n")
	if len(h) == 0 {
		b.WriteString("(sin comentarios ni cambios de estado)\n")
		return
	}
	b.WriteString("Del más viejo al más nuevo.\n\n")
	if keep >= 0 && len(h) > keep {
		fmt.Fprintf(b, "- (%d entradas más viejas, quitadas por el tamaño)\n", len(h)-keep)
		h = h[len(h)-keep:]
	}
	for _, e := range h {
		switch e.Type {
		case "comment":
			fmt.Fprintf(b, "- %s, %s comentó:\n", ts(e.At), actor(e.Author))
			quote(b, e.Body)
		case "assignment":
			if e.Assignee == nil {
				fmt.Fprintf(b, "- %s, %s %s: desasignó a %s\n", ts(e.At), actor(e.Author), sourceNames[e.Source], actor(e.Previous))
			} else {
				fmt.Fprintf(b, "- %s, %s %s: asignó a %s\n", ts(e.At), actor(e.Author), sourceNames[e.Source], actor(e.Assignee))
			}
		default:
			fmt.Fprintf(b, "- %s, %s %s: %s → %s", ts(e.At), actor(e.Author), sourceNames[e.Source], e.From, e.To)
			if e.Release != nil {
				fmt.Fprintf(b, " (release %s)", *e.Release)
			}
			b.WriteString("\n")
			if e.Comment != nil {
				quote(b, e.Comment.Body)
			}
		}
	}
}

// quote pone el comentario como cita dentro del punto de la lista, para que
// sus títulos o listas no se mezclen con los del documento.
func quote(b *strings.Builder, body string) {
	for line := range strings.SplitSeq(strings.TrimRight(body, "\n"), "\n") {
		b.WriteString(strings.TrimRight("  > "+line, " ") + "\n")
	}
}

func writeActions(b *strings.Builder, c store.IssueContext) {
	id := c.ID
	w := func(format string, a ...any) { fmt.Fprintf(b, format, a...) }
	w("\n## Qué puedes hacer\n\n")
	w("Con el MCP de intake-errors (entre paréntesis, la misma acción con la API):\n\n")
	w("- Asignártelo al ponerte con él: `assign_issue` con `{\"issue_id\": %d, \"assignee\": \"me\"}` (`POST /api/0/issues/%d/assignee`). Los tuyos, con `list_issues` y `assigned:me`.\n", id, id)
	release := ""
	if c.LatestEvent != nil {
		release = c.LatestEvent.Event.Release
	}
	if release != "" {
		w("- Cuando el arreglo esté hecho pero aún sin desplegar, marcarlo resuelto en la siguiente release: `set_issue_status` con `{\"issue_id\": %d, \"status\": \"resolved_next\", \"comment\": \"qué se arregló y dónde\"}` (`POST /api/0/issues/%d/status`). Pasa a resuelto al desplegarse una release posterior a %s; si vuelve a pasar en una posterior, se reabre como regresión.\n", id, id, release)
	} else {
		w("- El último evento no trae release, así que no se puede marcar resuelto en la siguiente: márcalo resuelto con `set_issue_status` y `\"status\": \"resolved\"` cuando el arreglo esté desplegado.\n")
	}
	w("- Marcarlo resuelto ya (arreglo desplegado): `set_issue_status` con `{\"issue_id\": %d, \"status\": \"resolved\", \"comment\": \"...\"}`.\n", id)
	w("- Dejar constancia de lo que has visto o hecho: `add_comment` con `{\"issue_id\": %d, \"body\": \"...\"}` en Markdown (`POST /api/0/issues/%d/comments/`).\n", id, id)
	w("- Silenciarlo si no hay nada que arreglar: `set_issue_status` con `\"status\": \"silenced\"` y un `comment` que diga por qué.\n")
	w("- Para saber más: `list_events` y `get_event` (otros eventos), `get_issue_tags` (todas las etiquetas), `get_release` (lo que trajo una release) y `list_comments` (el historial).\n")
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func plural(n int64, one, other string) string {
	if n == 1 {
		return one
	}
	return other
}
