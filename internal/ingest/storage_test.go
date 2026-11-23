package ingest

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/intake-es/intake-errors/internal/store/storetest"
)

// La retención borra los eventos viejos de los dos formatos y deja los demás.
func TestPurgeBothFormats(t *testing.T) {
	storetest.OnlyPostgres(t, "los eventos de antes, en jsonb, no existen en SQLite")
	db := storetest.NewDB(t)
	ctx := context.Background()
	p := db.AddProject(t, "api")
	old := time.Now().AddDate(0, 0, -100)
	stale := db.AddIssue(t, storetest.Issue{ProjectID: p.ID, Title: "viejo", FirstSeen: old})
	live := db.AddIssue(t, storetest.Issue{ProjectID: p.ID, Title: "reciente"})
	ev := storetest.Fixture(t, "api-excepcion-1")
	db.AddEvent(t, p.ID, stale, ev, old)
	db.AddLegacyEvent(t, p.ID, stale, ev, old)
	keep := []int64{
		db.AddEvent(t, p.ID, live, ev, time.Now()),
		db.AddLegacyEvent(t, p.ID, live, ev, time.Now()),
		db.AddEvent(t, p.ID, live, ev, old),
		db.AddLegacyEvent(t, p.ID, live, ev, old),
	}
	if _, err := db.Pool().Exec(ctx, `UPDATE events SET received_at = occurred_at`); err != nil {
		t.Fatal(err)
	}
	evs, iss, err := Purge(ctx, db.Pool(), time.Now().AddDate(0, 0, -90))
	if err != nil || evs != 4 || iss != 1 {
		t.Fatalf("borrados %d eventos y %d issues, %v", evs, iss, err)
	}
	for _, id := range keep[:2] {
		e, err := db.Event(ctx, strconv.FormatInt(id, 10))
		if err != nil || e.Event.Exceptions[0].Type != ev.Exceptions[0].Type {
			t.Fatalf("evento %d: %+v, %v", id, e.Event.Exceptions, err)
		}
	}
	if _, err := db.Issue(ctx, stale); err == nil {
		t.Error("el issue sin eventos sigue")
	}
}
