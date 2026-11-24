package markdown

import (
	"strings"
	"testing"
	"time"

	"github.com/intake-es/intake-errors/internal/event"
	"github.com/intake-es/intake-errors/internal/store"
)

func contextWith(frames []event.Frame, history []store.HistoryEntry) store.IssueContext {
	at := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)
	rel := "web@1.2.0"
	return store.IssueContext{
		IssueDetail: store.IssueDetail{
			Issue: store.Issue{ID: 7, Project: "web", Title: "TypeError: x is undefined", Level: "error", Status: "open",
				Count: 3, UserCount: 2, FirstSeen: at, LastSeen: at, FirstRelease: &rel, LastRelease: &rel},
			LatestEvent: &store.StoredEvent{ID: 1, EventID: "abc", OccurredAt: at, Event: event.Event{
				Release:    rel,
				Exceptions: []event.Exception{{Type: "TypeError", Value: "x is undefined", Frames: frames}},
			}},
		},
		History: history,
	}
}

func app(b bool) *bool { return &b }

func TestContextCollapsesLibraryFrames(t *testing.T) {
	frames := []event.Frame{
		{Filename: "node_modules/a.js", Function: "a", InApp: app(false)},
		{Filename: "node_modules/b.js", Function: "b", InApp: app(false)},
		{Filename: "src/c.ts", Function: "c", Line: 3, ContextLine: "c()", InApp: app(true)},
		{Filename: "node_modules/d.js", Function: "d", InApp: app(false)},
	}
	full := Context(contextWith(frames, nil), "", MaxContextBytes)
	if !strings.Contains(full, "- `node_modules/a.js` en `a`\n") || strings.Contains(full, "Recortado") {
		t.Errorf("sin pasar del tamaño quita fotogramas:\n%s", full)
	}
	var b strings.Builder
	writeFrames(&b, style{}, frames, "#####", true)
	want := "- (2 fotogramas de librerías, sin poner)\n\n##### `src/c.ts:3` en `c` (app)\n\n```\n> 3 | c()\n```\n\n- (1 fotograma de librerías, sin poner)\n"
	if b.String() != want {
		t.Errorf("fotogramas recogidos =\n%s\nquiero\n%s", b.String(), want)
	}
}

// Si ni quitando todo lo que se puede quitar cabe, se corta por una línea y lo dice.
func TestContextHardCut(t *testing.T) {
	var frames []event.Frame
	for range 200 {
		frames = append(frames, event.Frame{Filename: "src/a.ts", Function: "f", Line: 10, ContextLine: strings.Repeat("x", 2000), InApp: app(true)})
	}
	md := Context(contextWith(frames, nil), "", 32<<10)
	if len(md) > 32<<10 {
		t.Errorf("ocupa %d bytes", len(md))
	}
	if !strings.Contains(md, "> Recortado para no pasar de 32 KB") || !strings.HasSuffix(md, "se ha cortado aquí. El resto, con `get_event` y `list_comments`.\n") {
		t.Errorf("no dice que lo ha cortado:\n%s", md[len(md)-300:])
	}
}

func TestContextHistory(t *testing.T) {
	at := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)
	rel := "web@1.3.0"
	h := []store.HistoryEntry{
		{Type: "comment", At: at, Author: &store.Actor{Name: "ana", Kind: store.ActorUser}, Body: "# Causa\n\n- una\n"},
		{Type: "status", At: at, Author: &store.Actor{Name: "agente", Kind: store.ActorToken}, Source: store.SourceMCP, From: "open", To: "resolved_next",
			Release: &rel, Comment: &store.Comment{Body: "Arreglado"}},
		{Type: "status", At: at, Source: store.SourceIngest, From: "resolved", To: "open", Release: &rel},
	}
	md := Context(contextWith(nil, h), "https://errors.example.com/issues/7", MaxContextBytes)
	for _, want := range []string{
		"- En la interfaz: https://errors.example.com/issues/7\n",
		"- 2026-11-22T10:00:00.000Z, ana comentó:\n  > # Causa\n  >\n  > - una\n",
		"- 2026-11-22T10:00:00.000Z, agente (token) desde el MCP: open → resolved_next (release web@1.3.0)\n  > Arreglado\n",
		"- 2026-11-22T10:00:00.000Z, intake-errors al llegar un evento: resolved → open (release web@1.3.0)\n",
		"Pasa a resuelto al desplegarse una release posterior a web@1.2.0",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("no lleva %q:\n%s", want, md)
		}
	}
}

func TestContextLinks(t *testing.T) {
	c := contextWith(nil, nil)
	if strings.Contains(Context(c, "", 0), "## Enlaces") {
		t.Fatal("sin enlaces no debería haber sección")
	}
	c.Links = []store.IssueLink{
		{Provider: store.ProviderLinear, Kind: store.LinkIssue, ExternalID: "uuid", Identifier: "ERR-7",
			URL: "https://linear.app/x/issue/ERR-7", State: store.LinkClosed, StateName: "Done"},
		{Provider: store.ProviderGitHub, Kind: store.LinkPullRequest, ExternalID: "o/r#9", Identifier: "o/r#9",
			URL: "https://github.com/o/r/pull/9", State: store.LinkMerged, StateName: "merged"},
	}
	out := Context(c, "", 0)
	for _, want := range []string{
		"## Enlaces",
		"- Issue de Linear ERR-7: cerrada (Done), https://linear.app/x/issue/ERR-7",
		"- PR de GitHub o/r#9: mergeado, https://github.com/o/r/pull/9",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("falta %q en:\n%s", want, out)
		}
	}
}
