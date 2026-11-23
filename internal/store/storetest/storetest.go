// Package storetest prepara un Store contra Postgres para los tests de otros
// paquetes y crea issues y eventos directamente en la base.
package storetest

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/intake-es/intake-errors/internal/envelope"
	"github.com/intake-es/intake-errors/internal/event"
	"github.com/intake-es/intake-errors/internal/store"
)

type DB struct {
	*store.Store
}

func RandomHex(t testing.TB, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// New abre un Store en un esquema propio de INTAKE_ERRORS_TEST_DATABASE_URL,
// que se borra al acabar. Sin esa variable, el test se salta.
func New(t testing.TB) *store.Store {
	t.Helper()
	base := os.Getenv("INTAKE_ERRORS_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("sin INTAKE_ERRORS_TEST_DATABASE_URL")
	}
	ctx := context.Background()
	schema := "test_" + RandomHex(t, 6)

	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		_ = admin.Close(context.Background())
	})

	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()

	s, err := store.Open(ctx, u.String(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

// NewDB es New con los atajos para crear proyectos, issues y eventos a mano.
func NewDB(t testing.TB) *DB {
	t.Helper()
	return &DB{Store: New(t)}
}

func (db *DB) AddProject(t testing.TB, name string) store.Project {
	t.Helper()
	p, err := db.CreateProject(context.Background(), name, RandomHex(t, 16))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

type Issue struct {
	ProjectID         int64
	Title             string
	Culprit           string
	Type              string
	Status            string
	FirstSeen         time.Time
	LastSeen          time.Time
	Count             int64
	ResolvedInRelease *string
	LastRegression    *time.Time
	FirstRelease      *string
}

func (db *DB) AddIssue(t testing.TB, i Issue) int64 {
	t.Helper()
	if i.Status == "" {
		i.Status = "open"
	}
	if i.FirstSeen.IsZero() {
		i.FirstSeen = time.Now().Add(-time.Hour)
	}
	if i.LastSeen.IsZero() {
		i.LastSeen = i.FirstSeen
	}
	var id int64
	err := db.Pool().QueryRow(context.Background(), `
		INSERT INTO issues (project_id, fingerprint, title, culprit, type, status, resolved_in_release,
			first_seen, last_seen, event_count, last_regressed_at, first_release)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) RETURNING id`,
		i.ProjectID, RandomHex(t, 16), i.Title, i.Culprit, i.Type, i.Status, i.ResolvedInRelease,
		i.FirstSeen, i.LastSeen, i.Count, i.LastRegression, i.FirstRelease).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// AddEvent guarda ev como lo guardaría la ingesta: el evento normalizado y
// comprimido, su release y sus recuentos por release y entorno. Le pone un
// event_id nuevo y la fecha at; el issue no cambia.
func (db *DB) AddEvent(t testing.TB, projectID, issueID int64, ev event.Event, at time.Time) int64 {
	t.Helper()
	ev.ID = RandomHex(t, 16)
	ev.Timestamp = at.UTC()
	enc, err := store.EncodeEvent(&ev)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	id, err := store.InsertEvent(ctx, db.Pool(), store.NewEvent{IssueID: issueID, ProjectID: projectID, Event: &ev, ReceivedAt: time.Now(), Encoded: enc})
	if err != nil {
		t.Fatal(err)
	}
	db.track(t, projectID, issueID, ev, at)
	return id
}

// AddLegacyEvent es AddEvent con el evento en events.data, como se guardaba
// antes de comprimirlo.
func (db *DB) AddLegacyEvent(t testing.TB, projectID, issueID int64, ev event.Event, at time.Time) int64 {
	t.Helper()
	ev.ID = RandomHex(t, 16)
	ev.Timestamp = at.UTC()
	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	err = db.Pool().QueryRow(context.Background(), `
		INSERT INTO events (issue_id, project_id, event_id, occurred_at, release, environment, data)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''), NULLIF($6, ''), $7) RETURNING id`,
		issueID, projectID, ev.ID, at, ev.Release, ev.Environment, data).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	db.track(t, projectID, issueID, ev, at)
	return id
}

// MakeLegacy deja los eventos con esos ids como se guardaban antes: el mismo
// evento en events.data y sin lo comprimido ni sus columnas.
func (db *DB) MakeLegacy(t testing.TB, ids ...int64) {
	t.Helper()
	ctx := context.Background()
	for _, id := range ids {
		e, err := db.Event(ctx, strconv.FormatInt(id, 10))
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(e.Event)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Pool().Exec(ctx, `UPDATE events SET data = $2, data_zstd = NULL, user_label = NULL,
			user_ident = NULL, organization_id = NULL, summary = NULL WHERE id = $1`, id, data); err != nil {
			t.Fatal(err)
		}
	}
}

func (db *DB) track(t testing.TB, projectID, issueID int64, ev event.Event, at time.Time) {
	t.Helper()
	ctx := context.Background()
	if ev.Release != "" {
		if _, _, err := store.EnsureRelease(ctx, db.Pool(), projectID, ev.Release, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.TrackEvent(ctx, db.Pool(), projectID, issueID, ev.Release, ev.Environment, at, false); err != nil {
		t.Fatal(err)
	}
}

func testdata() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "testdata", "envelopes")
}

// Fixture lee el evento de testdata/envelopes/<name> con internal/event.
func Fixture(t testing.TB, name string) event.Event {
	t.Helper()
	base := filepath.Join(testdata(), name)
	var meta struct {
		Headers map[string]string `json:"headers"`
	}
	b, err := os.ReadFile(base + ".meta.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &meta); err != nil {
		t.Fatal(err)
	}
	h := http.Header{}
	for k, v := range meta.Headers {
		h.Set(k, v)
	}
	body, err := os.ReadFile(base + ".envelope")
	if err != nil {
		t.Fatal(err)
	}
	data, err := envelope.Decompress(bytes.NewReader(body), h.Get("Content-Encoding"), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	env, err := envelope.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	items := env.Events()
	if len(items) != 1 {
		t.Fatalf("%s: %d eventos", name, len(items))
	}
	ev, err := event.Parse(items[0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	return *ev
}
