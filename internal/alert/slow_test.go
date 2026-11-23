package alert

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/intake-es/intake-errors/internal/store"
)

// slowDestination tiene las peticiones esperando hasta que se llama a release,
// o como mucho hold, y avisa en arrived de la primera.
type slowDestination struct {
	next    http.Handler
	hold    time.Duration
	arrived chan struct{}
	release chan struct{}
	once    sync.Once
	free    sync.Once
}

func newSlowDestination(next http.Handler, hold time.Duration) *slowDestination {
	return &slowDestination{next: next, hold: hold, arrived: make(chan struct{}), release: make(chan struct{})}
}

func (s *slowDestination) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.once.Do(func() { close(s.arrived) })
	select {
	case <-s.release:
	case <-time.After(s.hold):
	}
	s.next.ServeHTTP(w, r)
}

func (s *slowDestination) open() { s.free.Do(func() { close(s.release) }) }

// maxWriteWait es lo más que puede tardar en guardarse un evento mientras el
// trabajador de avisos espera a un destino.
const maxWriteWait = time.Second

// Mientras un destino tarda en responder, el trabajador de avisos no tiene
// abierta ninguna transacción: con SQLite, que tiene una sola conexión de
// escritura, la ingesta seguiría esperando a que el destino respondiera.
func TestSlowDestinationDoesNotBlockWrites(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.SlackWebhookURL, o.LinearAPIKey = "", "" })
	hook := &recorder{}
	slow := newSlowDestination(hook, 5*time.Second)
	defer slow.open()
	e.addRule(t, webhookRule(serve(t, slow), store.RuleConditions{New: true}))
	e.ingest(t, "api-tarea-a-1")

	sent := make(chan int, 1)
	go func() {
		n, err := e.alerter.SendPending(context.Background())
		if err != nil {
			t.Error(err)
		}
		sent <- n
	}()
	select {
	case <-slow.arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("el aviso no ha llegado al destino")
	}

	start := time.Now()
	e.ingest(t, "web-typeerror-1")
	waited := time.Since(start)
	t.Logf("motor %s: guardar un evento con el destino esperando ha tardado %s", e.db.Pool().Dialect().Name(), waited)
	slow.open()
	if n := <-sent; n < 1 {
		t.Fatalf("enviados = %d", n)
	}
	if waited > maxWriteWait {
		t.Fatalf("guardar un evento ha esperado %s al destino del aviso", waited)
	}
	if got := len(hook.all()); got < 1 {
		t.Fatalf("avisos recibidos = %d", got)
	}
	rows := e.outbox(t)
	if len(rows) == 0 || !rows[0].Sent || rows[0].Attempts != 1 {
		t.Fatalf("el aviso no ha quedado apuntado como enviado: %+v", rows)
	}
}
