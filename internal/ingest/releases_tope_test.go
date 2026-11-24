package ingest

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/intake-es/mothlog/internal/db"
	"github.com/intake-es/mothlog/internal/store"
	"github.com/intake-es/mothlog/internal/store/storetest"
)

// Las sesiones no crean releases por encima del tope; los eventos sí, para no
// perder los de una release nueva de verdad.
func TestSessionReleaseCap(t *testing.T) {
	e := newTestEnv(t, 1000)
	e.ing.maxReleases = 3
	for i := range 5 {
		if rec := e.send(rawEnvelope(sessionPayload(fmt.Sprintf("s-%d", i), fmt.Sprintf("r-%d", i)))); rec.Code != http.StatusOK {
			t.Fatalf("código %d", rec.Code)
		}
	}
	if rec := e.send(rawEnvelope(aggregatesItem("agg", 2))); rec.Code != http.StatusOK {
		t.Fatalf("código %d", rec.Code)
	}
	t.Logf("5 sesiones y un agregado con tope 3: %s", e.sessionRows(t))
	if got := e.sessionRows(t); got != "releases=3 session_states=3 release_users=3 release_sessions=3" {
		t.Errorf("filas = %s", got)
	}
	if n := e.dropped(t, store.DropSessionReleaseLimit); n != 3 {
		t.Errorf("%d sesiones descartadas por el tope, quiero 3", n)
	}
	// Una sesión de una release que ya existe sigue contando.
	if rec := e.send(rawEnvelope(sessionPayload("s-otra", "r-0"))); rec.Code != http.StatusOK {
		t.Fatalf("código %d", rec.Code)
	}
	if n := e.count(t, `SELECT count(*) FROM session_states`); n != 4 {
		t.Errorf("%d sesiones", n)
	}
	if rec := e.send(rawEnvelope(`{"type":"event"}`, `{"message":"release nueva","level":"error","release":"r-evento"}`)); rec.Code != http.StatusOK {
		t.Fatalf("evento de una release nueva con el tope lleno: %d", rec.Code)
	}
	if n := e.count(t, `SELECT count(*) FROM releases WHERE version = 'r-evento'`); n != 1 {
		t.Error("el evento no creó su release")
	}
}

// La retención borra las releases sin finalizar, viejas y sin nada que las
// use; las demás se quedan.
func TestPurgeReleases(t *testing.T) {
	d := storetest.NewDB(t)
	ctx := context.Background()
	q := d.Pool()
	p := d.AddProject(t, "web")
	old := time.Now().AddDate(-1, 0, 0)
	cutoff := time.Now().AddDate(0, 0, -90)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := q.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	release := func(version string, at time.Time) int64 {
		t.Helper()
		id, _, err := store.EnsureRelease(ctx, q, p.ID, version, at)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	for _, v := range []string{"huerfana-1", "huerfana-2", "huerfana-de-sesiones"} {
		release(v, old)
	}
	release("reciente", time.Now())
	exec(`UPDATE releases SET finalized_at = $2 WHERE id = $1`, release("finalizada", old), old)

	issue := d.AddIssue(t, storetest.Issue{ProjectID: p.ID, Title: "x"})
	ev := storetest.Fixture(t, "api-excepcion-1")
	ev.Release = "con-evento"
	d.AddEvent(t, p.ID, issue, ev, time.Now())
	// Solo el evento: sin sus recuentos.
	exec(`DELETE FROM issue_stats WHERE release = 'con-evento'`)
	exec(`UPDATE releases SET first_seen = $1 WHERE version = 'con-evento'`, old)

	release("con-recuentos", old)
	exec(`INSERT INTO issue_stats (issue_id, project_id, release, environment, first_seen, last_seen, event_count)
		VALUES ($1, $2, 'con-recuentos', '', $3, $3, 1)`, issue, p.ID, old)
	release("con-sesiones", old)
	exec(`INSERT INTO release_sessions (project_id, release, environment, hour, total, errored, crashed, abnormal)
		VALUES ($1, 'con-sesiones', '', $2, 1, 0, 0, 0)`, p.ID, time.Now().Truncate(time.Hour))
	release("con-usuarios", old)
	exec(`INSERT INTO release_users (project_id, release, environment, user_hash, crashed, last_seen)
		VALUES ($1, 'con-usuarios', '', $2, false, $3)`, p.ID, []byte("u"), time.Now())
	exec(`INSERT INTO deploys (release_id, environment, deployed_at) VALUES ($1, 'production', $2)`, release("con-deploy", old), old)
	release("con-sourcemap", old)
	exec(`INSERT INTO sourcemaps (project_id, release, file, map_gzip, size) VALUES ($1, 'con-sourcemap', 'app.js.map', $2, 1)`, p.ID, []byte("x"))
	release("con-bundle", old)
	exec(`INSERT INTO artifact_bundles (project_id, checksum, release, dist, files) VALUES ($1, 'abc', 'con-bundle', '', 1)`, p.ID)
	for _, col := range []string{"first_release", "resolved_in_release", "fixed_in_release"} {
		v := "en-" + col
		release(v, old)
		other := d.AddIssue(t, storetest.Issue{ProjectID: p.ID, Title: col})
		exec(`UPDATE issues SET `+col+` = $2 WHERE id = $1`, other, v)
	}
	release("con-sesion-abierta", old)
	exec(`INSERT INTO session_states (project_id, sid, release, environment, hour, init_seen, status, errors, sent_at)
		VALUES ($1, $2, 'con-sesion-abierta', '', $3, false, 'ok', 0, $3)`, p.ID, []byte("sid-abierta-12345"), time.Now())

	versions := func() []string {
		rows, err := q.Query(ctx, `SELECT version FROM releases ORDER BY version`)
		if err != nil {
			t.Fatal(err)
		}
		vs, err := db.CollectRows(rows, db.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		return vs
	}
	before := versions()
	n, err := PurgeReleases(ctx, q, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	after := versions()
	t.Logf("antes: %v", before)
	t.Logf("borradas %d; quedan: %v", n, after)
	want := slices.DeleteFunc(slices.Clone(before), func(v string) bool {
		return v == "huerfana-1" || v == "huerfana-2" || v == "huerfana-de-sesiones"
	})
	if n != 3 || !slices.Equal(after, want) {
		t.Errorf("borradas %d, quedan %v; quiero %v", n, after, want)
	}
	// Otra vuelta no borra nada más.
	if n, err := PurgeReleases(ctx, q, cutoff); n != 0 || err != nil {
		t.Errorf("segunda vuelta: %d, %v", n, err)
	}
}

// Con más candidatas que un lote, la retención las recorre todas.
func TestPurgeReleasesBatches(t *testing.T) {
	d := storetest.NewDB(t)
	ctx := context.Background()
	p := d.AddProject(t, "web")
	old := time.Now().AddDate(-1, 0, 0)
	versions := make([]string, purgeReleasesBatch+10)
	for i := range versions {
		versions[i] = fmt.Sprintf("v%d", i)
	}
	issue := d.AddIssue(t, storetest.Issue{ProjectID: p.ID, Title: "x"})
	if _, err := d.Pool().Exec(ctx, `INSERT INTO releases (project_id, version, first_seen)
		SELECT $1, value, $3 FROM `+jsonOrUnnest(d.Pool().Dialect()), p.ID, versions, old); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Pool().Exec(ctx, `UPDATE issues SET first_release = 'v5' WHERE id = $1`, issue); err != nil {
		t.Fatal(err)
	}
	n, err := PurgeReleases(ctx, d.Pool(), time.Now().AddDate(0, 0, -90))
	if err != nil || n != int64(len(versions)-1) {
		t.Errorf("borradas %d, %v; quiero %d", n, err, len(versions)-1)
	}
}

func jsonOrUnnest(d db.Dialect) string {
	return db.ByEngine(d, `unnest(CAST($2 AS text[])) AS value`, `json_each($2)`)
}
