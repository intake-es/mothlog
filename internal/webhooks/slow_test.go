package webhooks_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/intake-es/mothlog/internal/alert"
	"github.com/intake-es/mothlog/internal/store"
	"github.com/intake-es/mothlog/internal/store/storetest"
	"github.com/intake-es/mothlog/internal/webhooks/fakes"
)

// Mientras Linear tarda en responder a un cambio de estado, el trabajador no
// tiene abierta ninguna transacción: un evento se guarda sin esperarle.
func TestSlowLinkSyncDoesNotBlockWrites(t *testing.T) {
	w := newWorld(t)
	id := w.newIssue(t, "api-tarea-a-1", "")
	w.settle(t)
	if _, err := w.db.ChangeIssueStatus(context.Background(), id, "resolved", store.StatusChange{Source: store.SourceAPI}); err != nil {
		t.Fatal(err)
	}

	arrived, release := make(chan struct{}), make(chan struct{})
	var once, free sync.Once
	open := func() { free.Do(func() { close(release) }) }
	defer open()
	next := w.linear.Handler()
	slow := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(arrived) })
		select {
		case <-release:
		case <-time.After(5 * time.Second):
		}
		next.ServeHTTP(rw, r)
	}))
	t.Cleanup(slow.Close)
	a, err := alert.New(w.db.Store, quiet(), alert.Options{PublicURL: publicURLBase,
		LinearAPIKey: linearKey, LinearAPIURL: slow.URL, LinearTeamID: fakes.LinearTeamID})
	if err != nil {
		t.Fatal(err)
	}

	synced := make(chan int, 1)
	go func() {
		n, err := a.SyncLinks(context.Background())
		if err != nil {
			t.Error(err)
		}
		synced <- n
	}()
	select {
	case <-arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("el cambio de estado no ha llegado a Linear")
	}
	start := time.Now()
	ev := storetest.Fixture(t, "web-typeerror-1")
	ev.ID = ""
	if _, err := w.ing.Ingest(context.Background(), w.project, &ev); err != nil {
		t.Fatal(err)
	}
	waited := time.Since(start)
	t.Logf("motor %s: guardar un evento con Linear esperando ha tardado %s", w.db.Pool().Dialect().Name(), waited)
	open()
	if n := <-synced; n < 1 {
		t.Fatalf("cambios llevados = %d", n)
	}
	if waited > time.Second {
		t.Fatalf("guardar un evento ha esperado %s a Linear", waited)
	}
	if l := w.linkOf(t, id, store.ProviderLinear, store.LinkIssue); l.State != store.LinkClosed {
		t.Fatalf("el enlace no ha quedado cerrado: %+v", l)
	}
}
