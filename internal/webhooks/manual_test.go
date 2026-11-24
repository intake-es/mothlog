package webhooks_test

import (
	"context"
	"testing"

	"github.com/intake-es/mothlog/internal/store"
	"github.com/intake-es/mothlog/internal/webhooks/fakes"
)

// Una issue de Linear creada a mano desde el detalle del error se sincroniza
// en los dos sentidos como la de un aviso.
func TestManualLinearIssueSyncs(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	if _, err := w.db.SetDefaultAlerts(ctx, w.project, store.DefaultAlerts{Slack: true, Linear: false}); err != nil {
		t.Fatal(err)
	}
	w.alerter.DefaultsChanged(w.project)

	id := w.newIssue(t, "api-tarea-a-1", "")
	w.settle(t)
	if n := len(w.linear.Issues()); n != 0 {
		t.Fatalf("con Linear apagado se han creado %d issues", n)
	}

	by := store.Actor{Name: "ana", Kind: store.ActorUser}
	link, created, err := w.alerter.CreateLinear(ctx, id, by)
	if err != nil || !created {
		t.Fatalf("crear: %v, %v", created, err)
	}
	if again, created, err := w.alerter.CreateLinear(ctx, id, by); err != nil || created || again.ID != link.ID {
		t.Fatalf("repetir: %+v, %v, %v", again, created, err)
	}
	if n := len(w.linear.Issues()); n != 1 {
		t.Fatalf("issues de Linear = %d", n)
	}
	li, _ := w.linear.Issue(link.ExternalID)
	if li.StateID != "state-triage" {
		t.Fatalf("estado inicial = %s", li.StateID)
	}

	// Resolverlo aquí la cierra allí.
	if _, err := w.db.ChangeIssueStatus(ctx, id, "resolved", store.StatusChange{Actor: &by, Source: store.SourceUI}); err != nil {
		t.Fatal(err)
	}
	w.settle(t)
	if li, _ := w.linear.Issue(link.ExternalID); li.StateID != "state-done" {
		t.Fatalf("Linear tras resolver: %s", li.StateID)
	}
	if l := w.linkOf(t, id, store.ProviderLinear, store.LinkIssue); l.State != store.LinkClosed || l.Author == nil || l.Author.Name != "ana" {
		t.Fatalf("enlace: %+v", l)
	}

	// Reabrirla allí lo reabre aquí.
	if err := w.linear.SetState(link.ExternalID, "In Progress", fakes.LinearActor{ID: "user-ana", Name: "Ana Ruiz"}); err != nil {
		t.Fatal(err)
	}
	w.settle(t)
	if s := w.status(t, id); s != "open" {
		t.Fatalf("tras reabrir en Linear: %s", s)
	}
}
