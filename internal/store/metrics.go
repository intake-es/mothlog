package store

import (
	"context"
	"fmt"

	"github.com/intake-es/mothlog/internal/db"
)

// OutboxStats son las filas de alert_outbox de un canal.
type OutboxStats struct {
	Channel               string
	Pending, Sent, Failed int64
	DigestsSent           int64
	OldestPendingSeconds  float64
}

// MetricsSnapshot es lo que /metrics lee de la base, en una sola ida y vuelta.
type MetricsSnapshot struct {
	Outbox         []OutboxStats
	CronMonitors   map[string]int64
	UptimeMonitors map[string]int64
	Projects       map[int64]string
}

func (s *Store) MetricsSnapshot(ctx context.Context) (MetricsSnapshot, error) {
	snap := MetricsSnapshot{CronMonitors: map[string]int64{}, UptimeMonitors: map[string]int64{}, Projects: map[int64]string{}}
	b := &db.Batch{}
	b.Queue(`
		SELECT channel,
			count(*) FILTER (WHERE sent_at IS NULL AND failed_at IS NULL),
			count(*) FILTER (WHERE sent_at IS NOT NULL),
			count(*) FILTER (WHERE sent_at IS NULL AND failed_at IS NOT NULL),
			count(*) FILTER (WHERE kind = 'digest' AND sent_at IS NOT NULL),
			CAST(COALESCE(` + s.pool.Dialect().SecondsBetween("now()", "min(created_at) FILTER (WHERE sent_at IS NULL AND failed_at IS NULL)") + `, 0) AS double precision)
		FROM alert_outbox GROUP BY channel ORDER BY channel`).Query(func(rows db.Rows) error {
		var err error
		snap.Outbox, err = db.CollectRows(rows, func(r db.CollectableRow) (OutboxStats, error) {
			var o OutboxStats
			err := r.Scan(&o.Channel, &o.Pending, &o.Sent, &o.Failed, &o.DigestsSent, &o.OldestPendingSeconds)
			return o, err
		})
		return err
	})
	byStatus := func(m map[string]int64) func(db.Rows) error {
		return func(rows db.Rows) error {
			var status string
			var n int64
			return db.ForEachRow(rows, []any{&status, &n}, func() error {
				m[status] = n
				return nil
			})
		}
	}
	b.Queue(`SELECT status, count(*) FROM monitors GROUP BY status`).Query(byStatus(snap.CronMonitors))
	b.Queue(`SELECT CASE WHEN paused THEN 'paused' ELSE status END, count(*) FROM uptime_monitors GROUP BY 1`).
		Query(byStatus(snap.UptimeMonitors))
	b.Queue(`SELECT id, name FROM projects`).Query(func(rows db.Rows) error {
		var id int64
		var name string
		return db.ForEachRow(rows, []any{&id, &name}, func() error {
			snap.Projects[id] = name
			return nil
		})
	})
	if err := s.pool.SendBatch(ctx, b); err != nil {
		return MetricsSnapshot{}, fmt.Errorf("read metrics: %w", err)
	}
	return snap, nil
}
