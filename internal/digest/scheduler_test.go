package digest

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/intake-es/intake-errors/internal/digest/digesttest"
	"github.com/intake-es/intake-errors/internal/store"
	"github.com/intake-es/intake-errors/internal/store/storetest"
)

type outboxRow struct {
	Channel, Action string
	End, Start      time.Time
}

func outbox(t *testing.T, db *storetest.DB) []outboxRow {
	t.Helper()
	rows, err := db.Pool().Query(context.Background(), `
		SELECT channel, action_id, event_at, (detail->>'period_start')::timestamptz FROM alert_outbox
		WHERE digest_project_id IS NOT NULL ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	var out []outboxRow
	for rows.Next() {
		var r outboxRow
		if err := rows.Scan(&r.Channel, &r.Action, &r.End, &r.Start); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func enable(t *testing.T, db *storetest.DB, projectID int64, at time.Time, change func(*store.DigestConfig)) {
	t.Helper()
	c := store.DefaultDigest(projectID)
	c.Enabled, c.Timezone, c.Hour = true, "Europe/Madrid", 9
	c.Destinations = []store.RuleAction{
		{ID: "correo", Type: store.ActionEmail, To: []string{"equipo@example.com"}},
		{ID: "hook", Type: store.ActionWebhook, URL: "https://agente.example.com/resumen"},
	}
	if change != nil {
		change(&c)
	}
	if _, _, err := db.SetDigest(context.Background(), projectID, c, at); err != nil {
		t.Fatal(err)
	}
}

func scheduler(db *storetest.DB, now time.Time) *Scheduler {
	s := NewScheduler(db.Store, slog.New(slog.NewTextHandler(io.Discard, nil)), base)
	s.Now = func() time.Time { return now }
	return s
}

func TestSchedulerWaitsForTheNextHour(t *testing.T) {
	db := storetest.NewDB(t)
	p := db.AddProject(t, "tienda")
	// Encendido a las 12 de Madrid: el periodo que acabó a las 9 no se manda.
	saved := time.Date(2026, 11, 21, 11, 0, 0, 0, time.UTC)
	enable(t, db, p.ID, saved, nil)
	if n, err := scheduler(db, saved.Add(time.Hour)).Tick(context.Background()); err != nil || n != 0 {
		t.Fatalf("antes de la hora: %d %v", n, err)
	}
	at := time.Date(2026, 11, 22, 8, 0, 30, 0, time.UTC)
	if n, err := scheduler(db, at).Tick(context.Background()); err != nil || n != 1 {
		t.Fatalf("a la hora: %d %v", n, err)
	}
	rows := outbox(t, db)
	want := []outboxRow{
		{"email", "correo", time.Date(2026, 11, 22, 8, 0, 0, 0, time.UTC), time.Date(2026, 11, 21, 8, 0, 0, 0, time.UTC)},
		{"webhook", "hook", time.Date(2026, 11, 22, 8, 0, 0, 0, time.UTC), time.Date(2026, 11, 21, 8, 0, 0, 0, time.UTC)},
	}
	if len(rows) != 2 || !equalRows(rows, want) {
		t.Fatalf("encolados = %+v", rows)
	}
	c, err := db.Digest(context.Background(), p.ID, at)
	if err != nil || c.LastPeriodEnd == nil || !c.LastPeriodEnd.Equal(want[0].End) || !c.NextSendAt.Equal(want[0].End.Add(24*time.Hour)) {
		t.Errorf("config = %+v %v", c, err)
	}
}

func equalRows(a, b []outboxRow) bool {
	for i := range a {
		if a[i].Channel != b[i].Channel || a[i].Action != b[i].Action || !a[i].End.Equal(b[i].End) || !a[i].Start.Equal(b[i].Start) {
			return false
		}
	}
	return len(a) == len(b)
}

func TestSchedulerNoDuplicatesAcrossReplicasAndRestarts(t *testing.T) {
	db := storetest.NewDB(t)
	p := db.AddProject(t, "tienda")
	enable(t, db, p.ID, time.Date(2026, 11, 21, 11, 0, 0, 0, time.UTC), nil)
	at := time.Date(2026, 11, 22, 8, 1, 0, 0, time.UTC)

	// Dos réplicas, cada una con varios bucles a la vez.
	var wg sync.WaitGroup
	total := make(chan int, 16)
	for range 2 {
		s := scheduler(db, at)
		for range 8 {
			wg.Go(func() {
				n, err := s.Tick(context.Background())
				if err != nil {
					t.Error(err)
				}
				total <- n
			})
		}
	}
	wg.Wait()
	close(total)
	sum := 0
	for n := range total {
		sum += n
	}
	if sum != 1 || len(outbox(t, db)) != 2 {
		t.Fatalf("encolados %d veces, %d filas", sum, len(outbox(t, db)))
	}

	// Un reinicio a la misma hora o un poco después no lo repite.
	for _, later := range []time.Duration{0, time.Minute, 23 * time.Hour} {
		if n, err := scheduler(db, at.Add(later)).Tick(context.Background()); err != nil || n != 0 {
			t.Fatalf("tras reiniciar (+%s): %d %v", later, n, err)
		}
	}
	if len(outbox(t, db)) != 2 {
		t.Fatalf("filas = %d", len(outbox(t, db)))
	}
}

func TestSchedulerCatchesUpAfterDowntime(t *testing.T) {
	db := storetest.NewDB(t)
	p := db.AddProject(t, "tienda")
	enable(t, db, p.ID, time.Date(2026, 11, 18, 11, 0, 0, 0, time.UTC), nil)
	// Parado desde el 18 hasta el 22 a las 10:30 de Madrid: sale uno, el del último periodo cerrado.
	at := time.Date(2026, 11, 22, 9, 30, 0, 0, time.UTC)
	if n, err := scheduler(db, at).Tick(context.Background()); err != nil || n != 1 {
		t.Fatalf("al arrancar: %d %v", n, err)
	}
	rows := outbox(t, db)
	if len(rows) != 2 || !rows[0].End.Equal(time.Date(2026, 11, 22, 8, 0, 0, 0, time.UTC)) ||
		!rows[0].Start.Equal(time.Date(2026, 11, 21, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("encolados = %+v", rows)
	}
}

func TestSchedulerQuietPeriodWithoutSendEmpty(t *testing.T) {
	db := storetest.NewDB(t)
	p := db.AddProject(t, "tienda")
	enable(t, db, p.ID, time.Date(2026, 11, 20, 11, 0, 0, 0, time.UTC), func(c *store.DigestConfig) { c.SendEmpty = false })
	quiet := time.Date(2026, 11, 21, 8, 0, 0, 0, time.UTC)
	if _, err := scheduler(db, quiet).Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rows := outbox(t, db); len(rows) != 0 {
		t.Fatalf("sin nada y sin send_empty: %+v", rows)
	}
	c, _ := db.Digest(context.Background(), p.ID, quiet)
	if c.LastPeriodEnd == nil || !c.LastPeriodEnd.Equal(quiet) {
		t.Errorf("el periodo vacío no queda apuntado: %v", c.LastPeriodEnd)
	}

	end := time.Date(2026, 11, 22, 8, 0, 0, 0, time.UTC)
	digesttest.Seed(t, db, p.ID, end)
	if n, err := scheduler(db, end).Tick(context.Background()); err != nil || n != 1 || len(outbox(t, db)) != 2 {
		t.Fatalf("con datos: %d %v", n, err)
	}
}

func TestSchedulerIgnoresDisabled(t *testing.T) {
	db := storetest.NewDB(t)
	p := db.AddProject(t, "tienda")
	enable(t, db, p.ID, time.Date(2026, 11, 20, 11, 0, 0, 0, time.UTC), func(c *store.DigestConfig) { c.Enabled = false })
	if n, err := scheduler(db, time.Date(2026, 11, 25, 8, 0, 0, 0, time.UTC)).Tick(context.Background()); err != nil || n != 0 {
		t.Fatalf("apagado: %d %v", n, err)
	}
}
