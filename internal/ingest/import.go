package ingest

import (
	"context"
	"fmt"

	"github.com/intake-es/intake-errors/internal/db"
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
	data, err := store.EncodeEvent(ev)
	if err != nil {
		return false, err
	}
	now := in.now().UTC()
	created := false
	err = db.BeginFunc(ctx, in.pool, func(tx db.Tx) error {
		if ev.Release != "" {
			if _, _, err := store.EnsureRelease(ctx, tx, projectID, ev.Release, ev.Timestamp); err != nil {
				return err
			}
		}
		id, err := store.InsertEvent(ctx, tx, store.NewEvent{IssueID: issueID, ProjectID: projectID, Event: ev, ReceivedAt: now, Encoded: data})
		if err != nil || id == 0 {
			return err
		}
		created = true
		if err := store.TrackEvent(ctx, tx, projectID, issueID, ev.Release, ev.Environment, ev.Timestamp, false); err != nil {
			return err
		}
		return in.recordTags(ctx, tx, issueID, ev)
	})
	return created, err
}
