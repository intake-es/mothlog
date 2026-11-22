package ingest

import (
	"context"
	"testing"
	"time"

	"github.com/intake-es/intake-errors/internal/store"
)

// Cada origen de un cambio de estado deja su entrada en el historial: la
// interfaz, la API y el MCP con su autor, el despliegue que resuelve un
// resolved_next y la ingesta al reabrirlo como regresión, sin autor.
func TestHistoryRecordsEveryStatusChange(t *testing.T) {
	e := newTestEnv(t, 1000)
	ctx := context.Background()
	f := loadFixture(t, "web-typeerror-1")
	e.sendOK(t, f, f.withReleaseEnv(t, "web@1.0.0", "production"))
	id := e.issues(t)[0].id

	ana := &store.Actor{Name: "ana", Kind: store.ActorUser}
	agente := &store.Actor{Name: "agente", Kind: store.ActorToken}
	steps := []struct {
		status  string
		by      store.StatusChange
		comment string
	}{
		{"silenced", store.StatusChange{Actor: ana, Source: store.SourceUI}, ""},
		{"open", store.StatusChange{Actor: agente, Source: store.SourceAPI, Comment: "Vuelve a importar"}, "Vuelve a importar"},
		{"resolved_next", store.StatusChange{Actor: agente, Source: store.SourceMCP, Comment: "Arreglado en `carrito.ts`"}, "Arreglado en `carrito.ts`"},
	}
	for _, s := range steps {
		if _, err := e.s.ChangeIssueStatus(ctx, id, s.status, s.by); err != nil {
			t.Fatalf("%s: %v", s.status, err)
		}
	}
	// El mismo estado otra vez no apunta nada.
	if _, err := e.s.ChangeIssueStatus(ctx, id, "resolved_next", store.StatusChange{Actor: agente, Source: store.SourceMCP}); err != nil {
		t.Fatal(err)
	}
	if _, resolved, err := e.s.AddDeploy(ctx, 2, "web@1.1.0", "production", time.Now()); err != nil || len(resolved) != 1 {
		t.Fatalf("desplegar: %v, %v", resolved, err)
	}
	e.sendOK(t, f, f.withReleaseEnv(t, "web@1.1.0", "production"))

	h, err := e.s.History(ctx, id, agente)
	if err != nil {
		t.Fatal(err)
	}
	type row struct{ source, from, to, author, release, comment string }
	want := []row{
		{"ui", "open", "silenced", "ana", "", ""},
		{"api", "silenced", "open", "agente", "", "Vuelve a importar"},
		{"mcp", "open", "resolved_next", "agente", "web@1.0.0", "Arreglado en `carrito.ts`"},
		{"deploy", "resolved_next", "resolved", "", "web@1.1.0", ""},
		{"ingest", "resolved", "open", "", "web@1.1.0", ""},
	}
	if len(h) != len(want) {
		t.Fatalf("historial = %+v", h)
	}
	for i, en := range h {
		got := row{source: en.Source, from: en.From, to: en.To}
		if en.Type != "status" {
			t.Errorf("%d: tipo %q", i, en.Type)
		}
		if en.Author != nil {
			got.author = en.Author.Name
		}
		if en.Release != nil {
			got.release = *en.Release
		}
		if en.Comment != nil {
			got.comment = en.Comment.Body
			if !en.Comment.Mine {
				t.Errorf("%d: el comentario de agente no sale como suyo", i)
			}
		}
		if got != want[i] {
			t.Errorf("%d = %+v, quiero %+v", i, got, want[i])
		}
	}
	if h[0].Author.Kind != store.ActorUser || h[1].Author.Kind != store.ActorToken {
		t.Errorf("tipos de autor: %+v, %+v", h[0].Author, h[1].Author)
	}
}
