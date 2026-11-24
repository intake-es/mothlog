package markdown

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/intake-es/mothlog/internal/lang"
	"github.com/intake-es/mothlog/internal/store"
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
	note          int // cuántas cosas de trimmed se han quitado
}

var levels = []level{
	{crumbs: maxBreadcrumbs, history: -1},
	{crumbs: 5, history: -1, note: 1},
	{crumbs: 5, libsCollapsed: true, history: -1, note: 2},
	{crumbs: 0, libsCollapsed: true, history: 20, note: 3},
	{crumbs: 0, libsCollapsed: true, history: 5, note: 3},
}

// trimmed es lo que se quita, por orden, para que el documento quepa.
func trimmed(l lang.Lang, n int) string {
	parts := []string{"breadcrumbs", l.T("library frames", "fotogramas de librerías"), l.T("old history", "historial viejo")}
	return strings.Join(parts[:n], ", ")
}

// Context escribe en un solo documento lo que un agente necesita para
// entender un error y actuar sobre él: el resumen, las releases, los
// entornos, las etiquetas, el evento con su traza, el historial y qué puede
// hacer. url es el enlace al issue en la interfaz (vacío, sin enlace). Si
// pasa de max bytes, quita breadcrumbs, fotogramas de librerías e historial
// viejo, por ese orden, y lo dice al principio.
func Context(c store.IssueContext, url string, max int, l lang.Lang) string {
	return contextDoc(c, url, max, style{l: l})
}

// ContextInert es Context para publicar en GitHub o Linear: además, las
// menciones, los enlaces y el HTML que trae el evento quedan como texto.
func ContextInert(c store.IssueContext, url string, max int, l lang.Lang) string {
	return contextDoc(c, url, max, style{inert: true, l: l})
}

func contextDoc(c store.IssueContext, url string, max int, st style) string {
	if max <= 0 {
		max = MaxContextBytes
	}
	var out string
	for _, l := range levels {
		out = writeContext(c, url, l, max, st)
		if len(out) <= max {
			return out
		}
	}
	// Ni así: se corta por la última línea entera que quepa.
	tail := st.l.F("\n\n> The document was still over %s and has been cut here. The rest, with `get_event` and `list_comments`.\n",
		"\n\n> El documento seguía pasando de %s y se ha cortado aquí. El resto, con `get_event` y `list_comments`.\n", kb(max))
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

func writeContext(c store.IssueContext, url string, l level, max int, st style) string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	t := st.text
	tr := st.l.T

	w("# %s\n\n", t(c.Title))
	if l.note > 0 {
		w(tr("> Trimmed to stay under %s: %s. The whole event, with `get_event`; the history, with `list_comments`.\n\n",
			"> Recortado para no pasar de %s: %s. El evento entero, con `get_event`; el historial, con `list_comments`.\n\n"),
			kb(max), trimmed(st.l, l.note))
	}

	b.WriteString(tr("## Summary\n\n", "## Resumen\n\n"))
	status := c.Status
	if c.Status == "open" && c.LastRegression != nil {
		status += tr(" (regression, ", " (regresión, ") + ts(*c.LastRegression) + ")"
	}
	w("- Issue: %d\n", c.ID)
	w(tr("- Status: %s\n", "- Estado: %s\n"), status)
	w(tr("- Level: %s\n", "- Nivel: %s\n"), st.orNone(t(c.Level)))
	w(tr("- Project: %s\n", "- Proyecto: %s\n"), c.Project)
	w(tr("- Assignee: %s\n", "- Asignado: %s\n"), assignee(c.Assignee, st))
	if c.Culprit != "" {
		w("- Culprit: %s\n", t(c.Culprit))
	}
	scope := ""
	if env := t(c.Environment); env != "" {
		scope = tr(" in ", " en ") + env
	}
	w(tr("- Events%s: %d; affected users: %d\n", "- Eventos%s: %d; usuarios afectados: %d\n"), scope, c.Count, c.UserCount)
	w(tr("- First seen%s: %s; last seen: %s\n", "- Primera vez%s: %s; última vez: %s\n"), scope, ts(c.FirstSeen), ts(c.LastSeen))
	if url != "" {
		w(tr("- In the UI: %s\n", "- En la interfaz: %s\n"), url)
	}

	w("\n## Releases\n\n")
	w(tr("- First: %s\n", "- Primera: %s\n"), st.orNone(t(deref(c.FirstRelease))))
	w(tr("- Last: %s\n", "- Última: %s\n"), st.orNone(t(deref(c.LastRelease))))
	switch {
	case c.Status == "resolved_next" && c.ResolvedInRelease != nil:
		w(tr("- Resolved in the release after: %s (becomes resolved when a later one is deployed)\n",
			"- Resuelto en la siguiente a: %s (pasa a resuelto al desplegarse una posterior)\n"), t(*c.ResolvedInRelease))
	case c.FixedInRelease != nil:
		w(tr("- Resolved in: %s\n", "- Resuelto con: %s\n"), t(*c.FixedInRelease))
	default:
		b.WriteString(tr("- Resolved in: (none)\n", "- Resuelto con: (ninguna)\n"))
	}

	b.WriteString(tr("\n## Environments\n\n", "\n## Entornos\n\n"))
	if len(c.Environments) == 0 {
		b.WriteString(tr("(none)\n", "(ninguno)\n"))
	}
	for _, e := range c.Environments {
		w("- %s: %d %s\n", t(e.Value), e.Count, st.l.N(e.Count, "event", "events", "evento", "eventos"))
	}

	writeLinks(&b, c.Links, st)
	writeContextTags(&b, c.Tags, st)
	writeContextEvent(&b, c, l, st)
	writeHistory(&b, c.History, l.history, st)
	writeActions(&b, c, st)
	return b.String()
}

var linkNames = map[string][2]string{
	store.ProviderLinear + "/" + store.LinkIssue:       {"Linear issue", "Issue de Linear"},
	store.ProviderGitHub + "/" + store.LinkIssue:       {"GitHub issue", "Issue de GitHub"},
	store.ProviderGitHub + "/" + store.LinkPullRequest: {"GitHub PR", "PR de GitHub"},
}

var linkStates = map[string][2]string{
	store.LinkOpen: {"open", "abierta"}, store.LinkClosed: {"closed", "cerrada"},
	store.LinkCanceled: {"canceled", "cancelada"}, store.LinkMerged: {"merged", "mergeado"},
}

// pick es el texto de un par {en, es} en el idioma; el par vacío, vacío.
func pick(l lang.Lang, p [2]string) string { return l.T(p[0], p[1]) }

func writeLinks(b *strings.Builder, links []store.IssueLink, st style) {
	if len(links) == 0 {
		return
	}
	b.WriteString(st.l.T("\n## Links\n\n", "\n## Enlaces\n\n"))
	for _, l := range links {
		name := st.text(l.Identifier)
		if name == "" {
			name = st.text(l.ExternalID)
		}
		state := pick(st.l, linkStates[l.State])
		if sn := st.text(l.StateName); sn != "" && !strings.EqualFold(sn, l.State) {
			state = strings.TrimSpace(state + " (" + sn + ")")
		}
		fmt.Fprintf(b, "- %s %s: %s", pick(st.l, linkNames[l.Provider+"/"+l.Kind]), name, st.orNone(state))
		if l.URL != "" {
			fmt.Fprintf(b, ", %s", oneLine(l.URL))
		}
		b.WriteString("\n")
	}
}

func writeContextTags(b *strings.Builder, t store.IssueTags, st style) {
	tr := st.l.T
	b.WriteString(tr("\n## Most frequent tags\n\n", "\n## Etiquetas más frecuentes\n\n"))
	if len(t.Tags) == 0 {
		b.WriteString(tr("(none)\n", "(ninguna)\n"))
		return
	}
	b.WriteString(tr("Out of the events that had each tag; to search by one, `list_issues` with `key:value`.\n\n",
		"Sobre los eventos que traían cada etiqueta; para buscar por una, `list_issues` con `clave:valor`.\n\n"))
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
			fmt.Fprintf(b, tr("- (%d more tags, with `get_issue_tags`)\n", "- (%d etiquetas más, con `get_issue_tags`)\n"), len(tags)-contextTags)
			break
		}
		var vals []string
		for j, v := range d.Values {
			if j == contextTagValues {
				break
			}
			vals = append(vals, fmt.Sprintf("%s %s %% (%d)", st.text(v.Value), pct(v.Percent), v.Count))
		}
		more := ""
		if d.Distinct > len(vals) {
			more = fmt.Sprintf(tr("; %d distinct values", "; %d valores distintos"), d.Distinct)
		}
		fmt.Fprintf(b, "- %s: %s%s\n", st.text(d.Key), strings.Join(vals, ", "), more)
	}
}

func pct(p float64) string {
	s := strconv.FormatFloat(p, 'f', 1, 64)
	return strings.TrimSuffix(s, ".0")
}

func writeContextEvent(b *strings.Builder, c store.IssueContext, l level, st style) {
	w := func(format string, a ...any) { fmt.Fprintf(b, format, a...) }
	t := st.text
	tr := st.l.T
	env := t(c.Environment)
	se := c.LatestEvent
	if se == nil {
		b.WriteString(tr("\n## Event\n\nThe issue has no stored events", "\n## Evento\n\nEl issue no tiene eventos guardados"))
		if env != "" {
			w(tr(" in %s", " en %s"), env)
		}
		w(".\n")
		return
	}
	which := tr("the latest", "el último")
	if c.EventChosen {
		which = tr("the requested one", "el pedido")
	} else if env != "" {
		which = tr("the latest in ", "el último en ") + env
	}
	e := se.Event
	w(tr("\n## Event (%s)\n\n", "\n## Evento (%s)\n\n"), which)
	w(tr("- Id: %s (internal id %d), %s\n", "- Id: %s (id interno %d), %s\n"), t(se.EventID), se.ID, ts(se.OccurredAt))
	writeEventFields(b, e, st)
	if len(e.Tags) > 0 {
		var tags []string
		for _, k := range slices.Sorted(maps.Keys(e.Tags)) {
			tags = append(tags, t(k)+"="+t(e.Tags[k]))
		}
		w(tr("- Tags: %s\n", "- Etiquetas: %s\n"), strings.Join(tags, ", "))
	}

	if e.Message != "" {
		w(tr("\n### Message\n\n%s", "\n### Mensaje\n\n%s"), codeBlock([]string{e.Message}))
	}
	if len(e.Exceptions) > 0 {
		w("\n### %s", exceptionsIntro(st.l))
		for i, x := range e.Exceptions {
			w("\n#### %d. %s\n\n", i+1, t(excTitle(x)))
			writeMechanism(b, x.Mechanism, st)
			if len(x.Frames) == 0 {
				b.WriteString(tr("(no frames)\n", "(sin fotogramas)\n"))
			}
			writeFrames(b, st, x.Frames, "#####", l.libsCollapsed)
		}
	}

	crumbs := breadcrumbs(e.Breadcrumbs, st)
	total := len(crumbs)
	if len(crumbs) > l.crumbs {
		crumbs = crumbs[len(crumbs)-l.crumbs:]
	}
	w("\n### Breadcrumbs\n\n")
	switch {
	case total == 0:
		b.WriteString(tr("(none)\n", "(ninguno)\n"))
	case len(crumbs) == 0:
		w(tr("(%d, removed for size)\n", "(%d, quitados por el tamaño)\n"), total)
	case len(crumbs) < total:
		w(tr("Oldest to newest; the last %d of %d.\n\n", "Del más viejo al más nuevo; los %d últimos de %d.\n\n"), len(crumbs), total)
	default:
		b.WriteString(tr("Oldest to newest.\n\n", "Del más viejo al más nuevo.\n\n"))
	}
	for _, c := range crumbs {
		w("- %s\n", c)
	}
}

var sourceNames = map[string][2]string{
	store.SourceUI:     {"from the UI", "desde la interfaz"},
	store.SourceAPI:    {"from the API", "desde la API"},
	store.SourceMCP:    {"from the MCP", "desde el MCP"},
	store.SourceIngest: {"when an event came in", "al llegar un evento"},
	store.SourceDeploy: {"on deploy", "al desplegar"},
	store.SourceLinear: {"from Linear", "desde Linear"},
	store.SourceGitHub: {"from GitHub", "desde GitHub"},
}

func actor(a *store.Actor, st style) string {
	switch {
	case a == nil:
		return "Mothlog"
	case a.Kind == store.ActorToken:
		return st.text(a.Name) + " (token)"
	case a.Via != "":
		return st.text(a.Name) + st.l.T(" via ", " vía ") + st.text(a.Via)
	}
	return st.text(a.Name)
}

func assignee(a *store.Assignee, st style) string {
	if a == nil {
		return st.l.T("(nobody)", "(nadie)")
	}
	s := st.text(a.Name)
	switch {
	case a.Kind == store.ActorToken && !a.Active:
		s += st.l.T(" (revoked token)", " (token revocado)")
	case a.Kind == store.ActorToken:
		s += " (token)"
	case a.DisplayName != "":
		s += " (" + st.text(a.DisplayName) + ")"
	}
	return s
}

func writeHistory(b *strings.Builder, h []store.HistoryEntry, keep int, st style) {
	tr := st.l.T
	b.WriteString(tr("\n## History\n\n", "\n## Historial\n\n"))
	if len(h) == 0 {
		b.WriteString(tr("(no comments or status changes)\n", "(sin comentarios ni cambios de estado)\n"))
		return
	}
	b.WriteString(tr("Oldest to newest.\n\n", "Del más viejo al más nuevo.\n\n"))
	if keep >= 0 && len(h) > keep {
		fmt.Fprintf(b, tr("- (%d older entries, removed for size)\n", "- (%d entradas más viejas, quitadas por el tamaño)\n"), len(h)-keep)
		h = h[len(h)-keep:]
	}
	for _, e := range h {
		source := pick(st.l, sourceNames[e.Source])
		switch e.Type {
		case "comment":
			fmt.Fprintf(b, tr("- %s, %s commented:\n", "- %s, %s comentó:\n"), ts(e.At), actor(e.Author, st))
			quote(b, e.Body)
		case "assignment":
			if e.Assignee == nil {
				fmt.Fprintf(b, tr("- %s, %s %s: unassigned %s\n", "- %s, %s %s: desasignó a %s\n"), ts(e.At), actor(e.Author, st), source, actor(e.Previous, st))
			} else {
				fmt.Fprintf(b, tr("- %s, %s %s: assigned %s\n", "- %s, %s %s: asignó a %s\n"), ts(e.At), actor(e.Author, st), source, actor(e.Assignee, st))
			}
		default:
			fmt.Fprintf(b, "- %s, %s %s: %s → %s", ts(e.At), actor(e.Author, st), source, e.From, e.To)
			if e.Release != nil {
				fmt.Fprintf(b, " (release %s)", st.text(*e.Release))
			}
			b.WriteString("\n")
			if e.Comment != nil {
				quote(b, e.Comment.Body)
			}
		}
	}
}

// quote pone el comentario como cita dentro del punto de la lista, para que
// sus títulos o listas no se mezclen con los del documento. Un CR suelto
// también acaba la línea en Markdown, así que cuenta como salto.
func quote(b *strings.Builder, body string) {
	body = strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\r", "\n")
	for line := range strings.SplitSeq(strings.TrimRight(body, "\n"), "\n") {
		b.WriteString(strings.TrimRight("  > "+line, " ") + "\n")
	}
}

func writeActions(b *strings.Builder, c store.IssueContext, st style) {
	id := c.ID
	tr := st.l.T
	w := func(format string, a ...any) { fmt.Fprintf(b, format, a...) }
	b.WriteString(tr("\n## What you can do\n\n", "\n## Qué puedes hacer\n\n"))
	b.WriteString(tr("With the Mothlog MCP (in parentheses, the same action with the API):\n\n",
		"Con el MCP de Mothlog (entre paréntesis, la misma acción con la API):\n\n"))
	w(tr("- Assign it to yourself when you start on it: `assign_issue` with `{\"issue_id\": %d, \"assignee\": \"me\"}` (`POST /api/0/issues/%d/assignee`). Yours, with `list_issues` and `assigned:me`.\n",
		"- Asignártelo al ponerte con él: `assign_issue` con `{\"issue_id\": %d, \"assignee\": \"me\"}` (`POST /api/0/issues/%d/assignee`). Los tuyos, con `list_issues` y `assigned:me`.\n"), id, id)
	release := ""
	if c.LatestEvent != nil {
		release = st.text(c.LatestEvent.Event.Release)
	}
	if release != "" {
		w(tr("- When the fix is done but not deployed yet, mark it resolved in the next release: `set_issue_status` with `{\"issue_id\": %d, \"status\": \"resolved_next\", \"comment\": \"what was fixed and where\"}` (`POST /api/0/issues/%d/status`). It becomes resolved when a release later than %s is deployed; if it happens again in a later one, it reopens as a regression.\n",
			"- Cuando el arreglo esté hecho pero aún sin desplegar, marcarlo resuelto en la siguiente release: `set_issue_status` con `{\"issue_id\": %d, \"status\": \"resolved_next\", \"comment\": \"qué se arregló y dónde\"}` (`POST /api/0/issues/%d/status`). Pasa a resuelto al desplegarse una release posterior a %s; si vuelve a pasar en una posterior, se reabre como regresión.\n"), id, id, release)
	} else {
		b.WriteString(tr("- The latest event has no release, so it can't be marked resolved in the next one: mark it resolved with `set_issue_status` and `\"status\": \"resolved\"` once the fix is deployed.\n",
			"- El último evento no trae release, así que no se puede marcar resuelto en la siguiente: márcalo resuelto con `set_issue_status` y `\"status\": \"resolved\"` cuando el arreglo esté desplegado.\n"))
	}
	w(tr("- Mark it resolved now (fix deployed): `set_issue_status` with `{\"issue_id\": %d, \"status\": \"resolved\", \"comment\": \"...\"}`.\n",
		"- Marcarlo resuelto ya (arreglo desplegado): `set_issue_status` con `{\"issue_id\": %d, \"status\": \"resolved\", \"comment\": \"...\"}`.\n"), id)
	w(tr("- Record what you saw or did: `add_comment` with `{\"issue_id\": %d, \"body\": \"...\"}` in Markdown (`POST /api/0/issues/%d/comments/`).\n",
		"- Dejar constancia de lo que has visto o hecho: `add_comment` con `{\"issue_id\": %d, \"body\": \"...\"}` en Markdown (`POST /api/0/issues/%d/comments/`).\n"), id, id)
	b.WriteString(tr("- Silence it if there is nothing to fix: `set_issue_status` with `\"status\": \"silenced\"` and a `comment` saying why.\n",
		"- Silenciarlo si no hay nada que arreglar: `set_issue_status` con `\"status\": \"silenced\"` y un `comment` que diga por qué.\n"))
	b.WriteString(tr("- To learn more: `list_events` and `get_event` (other events), `get_issue_tags` (all tags), `get_release` (what a release brought) and `list_comments` (the history).\n",
		"- Para saber más: `list_events` y `get_event` (otros eventos), `get_issue_tags` (todas las etiquetas), `get_release` (lo que trajo una release) y `list_comments` (el historial).\n"))
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
