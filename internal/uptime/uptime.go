package uptime

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/intake-es/intake-errors/internal/store"
)

const (
	// TickInterval es cada cuánto se buscan los monitores a los que les toca.
	TickInterval = 5 * time.Second
	// Retention es lo que se guardan las comprobaciones; los incidentes se quedan.
	Retention = 7 * 24 * time.Hour
	// parallel es cuántas peticiones hace a la vez.
	parallel = 8
)

// Service reserva los monitores a los que les toca, los comprueba y guarda el
// resultado. Varias réplicas a la vez reservan monitores distintos.
type Service struct {
	st      *store.Store
	checker *Checker
	notify  store.UptimeNotifier
	log     *slog.Logger
	// Now es el reloj; se cambia en los tests.
	Now func() time.Time

	// slots limita las peticiones a la vez; una réplica solo reserva los
	// monitores que puede comprobar ya, y los demás quedan para otra.
	slots chan struct{}
	// freed avisa a Run de que ha quedado un hueco.
	freed    chan struct{}
	inflight sync.WaitGroup
}

// New acepta notify nil: entonces no avisa.
func New(st *store.Store, checker *Checker, notify store.UptimeNotifier, log *slog.Logger) *Service {
	return &Service{st: st, checker: checker, notify: notify, log: log, Now: time.Now,
		slots: make(chan struct{}, parallel), freed: make(chan struct{}, 1)}
}

// Tick reserva y lanza los monitores a los que les toca ahora, tantos como
// huecos libres haya, sin esperar a que acaben; devuelve cuántos ha lanzado.
func (s *Service) Tick(ctx context.Context) (int, error) {
	n := 0
	for ctx.Err() == nil {
		select {
		case s.slots <- struct{}{}:
		default:
			return n, nil
		}
		free := cap(s.slots) - len(s.slots)
		due, err := s.st.ClaimUptimeChecks(ctx, s.Now(), 1+free)
		if err != nil || len(due) == 0 {
			<-s.slots
			return n, err
		}
		for range due[1:] {
			s.slots <- struct{}{}
		}
		n += len(due)
		for _, t := range due {
			s.inflight.Add(1)
			go func() {
				defer func() {
					<-s.slots
					s.inflight.Done()
					select {
					case s.freed <- struct{}{}:
					default:
					}
				}()
				s.check(ctx, t)
			}()
		}
	}
	return n, ctx.Err()
}

// Wait espera a que acaben las comprobaciones lanzadas.
func (s *Service) Wait() { s.inflight.Wait() }

func (s *Service) check(ctx context.Context, t store.UptimeTarget) {
	res := s.checker.Check(ctx, t, s.Now())
	if ctx.Err() != nil {
		return
	}
	kind, err := s.st.RecordUptimeCheck(ctx, t.ID, res, s.notify)
	switch {
	case err != nil:
		s.log.ErrorContext(ctx, "guardar comprobación", "monitor", t.ID, "err", err)
	case kind == store.KindUptimeDown:
		s.log.WarnContext(ctx, "monitor caído", "monitor", t.ID, "error", res.Error)
	case kind == store.KindUptimeUp:
		s.log.InfoContext(ctx, "monitor recuperado", "monitor", t.ID)
	}
}

// Run comprueba cada interval y en cuanto queda un hueco libre, y borra cada
// hora las comprobaciones de más de Retention, hasta que se cancela ctx.
func (s *Service) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	var purged time.Time
	for {
		if _, err := s.Tick(ctx); err != nil && ctx.Err() == nil {
			s.log.ErrorContext(ctx, "comprobar monitores de disponibilidad", "err", err)
		}
		if now := s.Now(); now.Sub(purged) >= time.Hour {
			purged = now
			if n, err := s.st.PurgeUptimeChecks(ctx, now.Add(-Retention)); err != nil && ctx.Err() == nil {
				s.log.ErrorContext(ctx, "retención de comprobaciones", "err", err)
			} else if n > 0 {
				s.log.InfoContext(ctx, "retención", "comprobaciones", n)
			}
		}
		select {
		case <-ctx.Done():
			s.Wait()
			return
		case <-t.C:
		case <-s.freed:
		}
	}
}
