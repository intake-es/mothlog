// Package digesttest deja en la base un periodo con algo de cada sección del
// resumen, para los tests de quien lo arma y de quien lo manda.
package digesttest

import (
	"context"
	"testing"
	"time"

	"github.com/intake-es/intake-errors/internal/event"
	"github.com/intake-es/intake-errors/internal/store/storetest"
)

// Issues son los ids de lo que deja Seed.
type Issues struct {
	New, Regressed, Growing, Steady, Resolved int64
}

// Release es la versión con sesiones que deja Seed.
const Release = "web@1.4.1"

// Seed deja en el proyecto, en el periodo [end-24h, end): un error nuevo con 3
// eventos de 2 usuarios, una regresión con 2 eventos, uno que pasa de 2 a 12
// eventos, otro que se queda en 3, uno resuelto, un cron con 2 fallos y otro
// que no llegó, y la release web@1.4.1 con 200 sesiones, 3 con crash.
func Seed(t testing.TB, db *storetest.DB, projectID int64, end time.Time) Issues {
	t.Helper()
	ctx := context.Background()
	start := end.Add(-24 * time.Hour)
	prev := start.Add(-24 * time.Hour)
	old := prev.Add(-72 * time.Hour)
	at := func(base time.Time, h int) time.Time { return base.Add(time.Duration(h) * time.Hour) }
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Pool().Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO releases (project_id, version, first_seen) VALUES ($1, $2, $3)`, projectID, Release, at(start, 1))
	var is Issues
	issue := func(title, culprit string, first time.Time) int64 {
		return db.AddIssue(t, storetest.Issue{ProjectID: projectID, Title: title, Culprit: culprit, FirstSeen: first, LastSeen: first})
	}
	events := func(issue int64, n int, base time.Time, users ...string) {
		for i := range n {
			ev := event.Event{Level: "error", Message: "fallo", Release: Release, Environment: "production"}
			if len(users) > 0 {
				ev.User = &event.User{ID: users[i%len(users)]}
			}
			db.AddEvent(t, projectID, issue, ev, base.Add(time.Duration(i)*time.Minute))
		}
	}
	is.New = issue("TypeError: Cannot read properties of undefined (reading 'total')", "checkout/cart.ts", at(start, 2))
	events(is.New, 3, at(start, 2), "ana", "luis")

	is.Regressed = issue("KeyError: 'currency'", "billing/invoice.py in render", old)
	events(is.Regressed, 2, at(start, 5), "marta")
	exec(`INSERT INTO issue_status_changes (issue_id, source, from_status, to_status, release, created_at)
		VALUES ($1, 'ingest', 'resolved', 'open', $2, $3)`, is.Regressed, Release, at(start, 5))

	is.Growing = issue("TimeoutError: upstream took too long", "api/search.go in Query", old)
	events(is.Growing, 2, at(prev, 3))
	events(is.Growing, 12, at(start, 6), "ana")

	is.Steady = issue("Warning: slow render", "", old)
	events(is.Steady, 3, at(prev, 3))
	events(is.Steady, 3, at(start, 3))

	is.Resolved = issue("ZeroDivisionError: division by zero", "stats.py", old)
	exec(`INSERT INTO issue_status_changes (issue_id, author, author_kind, source, from_status, to_status, created_at)
		VALUES ($1, 'ana', 'user', 'ui', 'open', 'resolved', $2)`, is.Resolved, at(start, 8))

	var backup, invoices int64
	if err := db.Pool().QueryRow(ctx, `INSERT INTO monitors (project_id, slug, name, schedule_type, schedule, status)
		VALUES ($1, 'copia-seguridad', 'Copia de seguridad', 'crontab', '0 3 * * *', 'ok') RETURNING id`, projectID).Scan(&backup); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool().QueryRow(ctx, `INSERT INTO monitors (project_id, slug, name, schedule_type, schedule, status)
		VALUES ($1, 'enviar-facturas', 'Enviar facturas', 'crontab', '0 * * * *', 'missed') RETURNING id`, projectID).Scan(&invoices); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		monitor int64
		status  string
		at      time.Time
	}{
		{backup, "error", at(start, 3)}, {backup, "error", at(start, 4)}, {backup, "ok", at(start, 5)},
		{invoices, "missed", at(start, 9)}, {backup, "error", at(prev, 3)},
	} {
		exec(`INSERT INTO monitor_checkins (monitor_id, check_in_id, status, started_at, expected_at)
			VALUES ($1, gen_random_uuid(), $2, $3, $3)`, c.monitor, c.status, c.at)
	}

	exec(`INSERT INTO release_sessions (project_id, release, environment, hour, total, errored, crashed)
		VALUES ($1, $2, 'production', $3, 200, 9, 3)`, projectID, Release, at(start, 2).Truncate(time.Hour))
	for i, crashed := range []bool{false, false, false, true} {
		exec(`INSERT INTO release_users (project_id, release, environment, user_hash, crashed, last_seen)
			VALUES ($1, $2, 'production', $3, $4, $5)`, projectID, Release, []byte{byte(i)}, crashed, at(start, 3))
	}
	return is
}
