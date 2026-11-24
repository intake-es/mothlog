package ingest

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/intake-es/mothlog/internal/store"
)

type dropKey struct {
	project int64
	reason  string
	day     time.Time
}

// Drops cuenta en memoria los eventos descartados y los guarda de vez en
// cuando, para que un pico de descartes no sea también un pico de escrituras.
type Drops struct {
	st  *store.Store
	now func() time.Time

	mu     sync.Mutex
	counts map[dropKey]int64
}

func NewDrops(st *store.Store) *Drops {
	return &Drops{st: st, now: time.Now, counts: map[dropKey]int64{}}
}

func (d *Drops) Add(projectID int64, reason string, n int) {
	if d == nil || n <= 0 {
		return
	}
	k := dropKey{projectID, reason, d.now().UTC().Truncate(24 * time.Hour)}
	d.mu.Lock()
	d.counts[k] += int64(n)
	d.mu.Unlock()
}

// Flush guarda lo contado; si falla, lo devuelve a la cuenta para el siguiente.
func (d *Drops) Flush(ctx context.Context) error {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	counts := d.counts
	d.counts = map[dropKey]int64{}
	d.mu.Unlock()
	if len(counts) == 0 {
		return nil
	}
	rows := make([]store.Dropped, 0, len(counts))
	for k, n := range counts {
		rows = append(rows, store.Dropped{ProjectID: k.project, Reason: k.reason, Day: k.day, Count: n})
	}
	err := d.st.AddDropped(ctx, rows)
	if err != nil {
		d.mu.Lock()
		for k, n := range counts {
			d.counts[k] += n
		}
		d.mu.Unlock()
	}
	return err
}

// Run guarda lo contado cada every y una última vez al cancelarse ctx.
func (d *Drops) Run(ctx context.Context, every time.Duration, log *slog.Logger) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			flushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			if err := d.Flush(flushCtx); err != nil {
				log.Error("save dropped", "err", err)
			}
			cancel()
			return
		case <-t.C:
			if err := d.Flush(ctx); err != nil {
				log.ErrorContext(ctx, "save dropped", "err", err)
			}
		}
	}
}
