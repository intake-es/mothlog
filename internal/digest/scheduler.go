package digest

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/intake-es/mothlog/internal/store"
)

// TickInterval es cada cuánto mira el proceso si toca algún resumen.
const TickInterval = time.Minute

// Scheduler encola a su hora el resumen de cada proyecto que lo tiene
// encendido. El periodo encolado se apunta en la base con la fila del
// proyecto bloqueada: varias réplicas no lo duplican, un reinicio no lo
// repite y, si el servicio estaba parado a la hora, sale al arrancar (solo el
// último periodo cerrado, no los que se perdieron antes).
type Scheduler struct {
	st   *store.Store
	log  *slog.Logger
	base string
	// Now es el reloj; se cambia en los tests.
	Now func() time.Time
}

func NewScheduler(st *store.Store, log *slog.Logger, publicURL string) *Scheduler {
	return &Scheduler{st: st, log: log, base: strings.TrimRight(publicURL, "/"), Now: time.Now}
}

// Tick encola los resúmenes que tocan ahora y devuelve cuántos ha encolado.
func (s *Scheduler) Tick(ctx context.Context) (int, error) {
	now := s.Now()
	due, err := s.st.DigestsDue(ctx, now)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, c := range due {
		end := c.PeriodEnd(now)
		start := c.PeriodStart(end)
		send := c.SendEmpty
		if !send {
			d, err := Build(ctx, s.st, Options{Config: c, Start: start, End: end, Base: s.base})
			if err != nil {
				s.log.ErrorContext(ctx, "build digest", "project", c.ProjectID, "err", err)
				continue
			}
			send = !d.Empty
		}
		ok, err := s.st.EnqueueDigest(ctx, c.ProjectID, start, end, send, now)
		if err != nil {
			s.log.ErrorContext(ctx, "enqueue digest", "project", c.ProjectID, "err", err)
			continue
		}
		if ok {
			n++
			s.log.InfoContext(ctx, "digest enqueued", "project", c.ProjectID, "from", start, "to", end, "empty_not_sent", !send)
		}
	}
	return n, nil
}

// Run mira cada interval si toca algún resumen, hasta que se cancela ctx.
func (s *Scheduler) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if _, err := s.Tick(ctx); err != nil && ctx.Err() == nil {
			s.log.ErrorContext(ctx, "digests", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
