package ingest

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/intake-es/intake-errors/internal/db"
)

// Purge borra los eventos recibidos antes de cutoff y los issues que se quedan
// sin eventos.
func Purge(ctx context.Context, pool db.DB, cutoff time.Time) (events, issues int64, err error) {
	tag, err := pool.Exec(ctx, `DELETE FROM events WHERE received_at < $1`, cutoff)
	if err != nil {
		return 0, 0, fmt.Errorf("borrar eventos: %w", err)
	}
	events = tag.RowsAffected()
	issues, err = purgeEmptyIssues(ctx, pool)
	if err != nil {
		return events, 0, err
	}
	return events, issues, nil
}

// purgeEmptyIssues salta los issues que una ingesta tiene bloqueados y vuelve a
// mirar sus eventos ya con el cerrojo, en una sentencia nueva, para ver los de
// cualquier ingesta que haya terminado justo antes.
func purgeEmptyIssues(ctx context.Context, pool db.DB) (int64, error) {
	var n int64
	err := db.BeginFunc(ctx, pool, func(tx db.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id FROM issues i
			WHERE NOT EXISTS (SELECT 1 FROM events e WHERE e.issue_id = i.id)`+tx.Dialect().ForUpdateSkipLocked())
		if err != nil {
			return err
		}
		ids, err := db.CollectRows(rows, db.RowTo[int64])
		if err != nil || len(ids) == 0 {
			return err
		}
		tag, err := tx.Exec(ctx, `
			DELETE FROM issues AS i
			WHERE `+tx.Dialect().InList("i.id", "$1")+`
			  AND NOT EXISTS (SELECT 1 FROM events e WHERE e.issue_id = i.id)`, ids)
		n = tag.RowsAffected()
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("borrar issues: %w", err)
	}
	return n, nil
}

// RunRetention purga eventos y sesiones al arrancar y luego cada hora hasta que se cancela ctx.
func RunRetention(ctx context.Context, pool db.DB, days int, log *slog.Logger) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		cutoff := time.Now().AddDate(0, 0, -days)
		ev, is, err := Purge(ctx, pool, cutoff)
		if err != nil {
			log.ErrorContext(ctx, "retención", "err", err)
		} else if ev > 0 || is > 0 {
			log.InfoContext(ctx, "retención", "eventos", ev, "issues", is)
		}
		if n, err := PurgeSessions(ctx, pool, cutoff, time.Now()); err != nil {
			log.ErrorContext(ctx, "retención de sesiones", "err", err)
		} else if n > 0 {
			log.InfoContext(ctx, "retención de sesiones", "filas", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
