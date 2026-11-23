package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/intake-es/intake-errors/internal/event"
	"github.com/intake-es/intake-errors/internal/store"
)

// ImportEvent guarda un evento traído de otro servicio en un issue que ya
// existe, como lo guardaría la ingesta: su release, los recuentos por
// release y entorno, las etiquetas y el usuario. El issue no cambia: su
// contador y sus fechas ya vienen del otro servicio. Devuelve false si el
// evento ya estaba.
func (in *Ingester) ImportEvent(ctx context.Context, projectID, issueID int64, ev *event.Event) (bool, error) {
	if !validID(ev.ID) {
		return false, fmt.Errorf("%w: event_id %q", event.ErrInvalid, ev.ID)
	}
	if ev.Timestamp.IsZero() {
		return false, fmt.Errorf("%w: el evento %s no tiene fecha", event.ErrInvalid, ev.ID)
	}
	data, err := json.Marshal(ev)
	if err != nil {
		return false, fmt.Errorf("serializar evento: %w", err)
	}
	now := in.now().UTC()
	created := false
	err = pgx.BeginFunc(ctx, in.pool, func(tx pgx.Tx) error {
		if ev.Release != "" {
			if _, _, err := store.EnsureRelease(ctx, tx, projectID, ev.Release, ev.Timestamp); err != nil {
				return err
			}
		}
		var id int64
		err := tx.QueryRow(ctx, `
			INSERT INTO events (issue_id, project_id, event_id, occurred_at, received_at, release, environment, data)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (project_id, event_id) DO NOTHING
			RETURNING id`,
			issueID, projectID, ev.ID, ev.Timestamp, now, nullable(ev.Release), nullable(ev.Environment), data).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("guardar evento: %w", err)
		}
		created = true
		if err := store.TrackEvent(ctx, tx, projectID, issueID, ev.Release, ev.Environment, ev.Timestamp, false); err != nil {
			return err
		}
		return in.recordTags(ctx, tx, issueID, ev)
	})
	return created, err
}
