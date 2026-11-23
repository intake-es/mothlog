package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
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
	b := &pgx.Batch{}
	b.Queue(`
		SELECT channel,
			count(*) FILTER (WHERE sent_at IS NULL AND failed_at IS NULL),
			count(*) FILTER (WHERE sent_at IS NOT NULL),
			count(*) FILTER (WHERE sent_at IS NULL AND failed_at IS NOT NULL),
			count(*) FILTER (WHERE kind = 'digest' AND sent_at IS NOT NULL),
			COALESCE(EXTRACT(EPOCH FROM now() - min(created_at) FILTER (WHERE sent_at IS NULL AND failed_at IS NULL)), 0)::float8
		FROM alert_outbox GROUP BY channel ORDER BY channel`).Query(func(rows pgx.Rows) error {
		var err error
		snap.Outbox, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (OutboxStats, error) {
			var o OutboxStats
			err := r.Scan(&o.Channel, &o.Pending, &o.Sent, &o.Failed, &o.DigestsSent, &o.OldestPendingSeconds)
			return o, err
		})
		return err
	})
	byStatus := func(m map[string]int64) func(pgx.Rows) error {
		return func(rows pgx.Rows) error {
			var status string
			var n int64
			_, err := pgx.ForEachRow(rows, []any{&status, &n}, func() error {
				m[status] = n
				return nil
			})
			return err
		}
	}
	b.Queue(`SELECT status, count(*) FROM monitors GROUP BY status`).Query(byStatus(snap.CronMonitors))
	b.Queue(`SELECT CASE WHEN paused THEN 'paused' ELSE status END, count(*) FROM uptime_monitors GROUP BY 1`).
		Query(byStatus(snap.UptimeMonitors))
	b.Queue(`SELECT id, name FROM projects`).Query(func(rows pgx.Rows) error {
		var id int64
		var name string
		_, err := pgx.ForEachRow(rows, []any{&id, &name}, func() error {
			snap.Projects[id] = name
			return nil
		})
		return err
	})
	if err := s.pool.SendBatch(ctx, b).Close(); err != nil {
		return MetricsSnapshot{}, fmt.Errorf("leer métricas: %w", err)
	}
	return snap, nil
}
