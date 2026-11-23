package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

func randomHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// testSchemaURL crea un esquema propio en la base de pruebas de Postgres,
// que se borra al acabar, y devuelve la URL que lo usa. Es para los tests que
// solo tienen sentido con Postgres (sus migraciones, varias réplicas): sin
// Postgres, el test se salta y why dice por qué.
func testSchemaURL(t *testing.T, why string) string {
	t.Helper()
	base := os.Getenv("INTAKE_ERRORS_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("solo con Postgres (INTAKE_ERRORS_TEST_DATABASE_URL): " + why)
	}
	ctx := context.Background()
	schema := "test_" + randomHex(t, 6)

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
	return u.String()
}

// testURL es un esquema propio de la base de pruebas de Postgres o, sin
// INTAKE_ERRORS_TEST_DATABASE_URL, un fichero de SQLite nuevo.
func testURL(t *testing.T) string {
	t.Helper()
	if os.Getenv("INTAKE_ERRORS_TEST_DATABASE_URL") == "" {
		return "sqlite://" + filepath.Join(t.TempDir(), "intake.db")
	}
	return testSchemaURL(t, "")
}

// onlyPostgres salta el test con SQLite; why dice por qué solo vale con Postgres.
func onlyPostgres(t *testing.T, why string) {
	t.Helper()
	if os.Getenv("INTAKE_ERRORS_TEST_DATABASE_URL") == "" {
		t.Skip("solo con Postgres (INTAKE_ERRORS_TEST_DATABASE_URL): " + why)
	}
}

// testStore abre un Store en un esquema propio de la base de pruebas.
func testStore(t *testing.T) *Store {
	t.Helper()
	ctx := context.Background()
	u := testURL(t)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s, err := Open(ctx, u, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)

	// Volver a abrir no debe fallar: las migraciones ya están aplicadas.
	again, err := OpenForCommand(ctx, u, log)
	if err != nil {
		t.Fatalf("segunda apertura: %v", err)
	}
	again.Close()
	return s
}

// pgCode es el código de Postgres del error; con SQLite, el que le
// corresponde a su código extendido.
func pgCode(err error) string {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.Code
	}
	var se *sqlite.Error
	if errors.As(err, &se) {
		switch se.Code() {
		case sqlite3.SQLITE_CONSTRAINT_UNIQUE, sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY:
			return "23505"
		case sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY:
			return "23503"
		case sqlite3.SQLITE_CONSTRAINT_CHECK:
			return "23514"
		}
	}
	return ""
}

func TestProjects(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	p, err := s.CreateProject(ctx, "backend", "b7d66645f843482faf2f696b32b16e56")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID == 0 || p.CreatedAt.IsZero() {
		t.Errorf("proyecto = %+v", p)
	}
	got, err := s.ProjectByPublicKey(ctx, p.PublicKey)
	if err != nil || got.ID != p.ID || got.Name != "backend" {
		t.Fatalf("= %+v, %v", got, err)
	}
	if _, err := s.ProjectByPublicKey(ctx, "otra"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v; quiero ErrNotFound", err)
	}
	if _, err := s.CreateProject(ctx, "repetido", p.PublicKey); pgCode(err) != "23505" {
		t.Errorf("clave repetida: err = %v", err)
	}
}

func TestIssueAndEventConstraints(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	a, err := s.CreateProject(ctx, "a", "clave-a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateProject(ctx, "b", "clave-b")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()

	insertIssue := func(project int64, fp, status string) (int64, error) {
		var id int64
		err := s.pool.QueryRow(ctx, `
			INSERT INTO issues (project_id, fingerprint, title, status, first_seen, last_seen, event_count)
			VALUES ($1, $2, 'ZeroDivisionError: division by zero', $3, $4, $4, 1) RETURNING id`,
			project, fp, status, now).Scan(&id)
		return id, err
	}
	issueA, err := insertIssue(a.ID, "fp", "open")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := insertIssue(a.ID, "fp", "open"); pgCode(err) != "23505" {
		t.Errorf("huella repetida en el mismo proyecto: err = %v", err)
	}
	issueB, err := insertIssue(b.ID, "fp", "resolved_next")
	if err != nil {
		t.Errorf("misma huella en otro proyecto: %v", err)
	}
	for _, st := range []string{"resolved", "silenced"} {
		if _, err := insertIssue(a.ID, "fp-"+st, st); err != nil {
			t.Errorf("estado %s: %v", st, err)
		}
	}
	if _, err := insertIssue(a.ID, "fp2", "ignored"); pgCode(err) != "23514" {
		t.Errorf("estado no válido: err = %v", err)
	}

	insertEvent := func(project, issue int64, eventID string) error {
		_, err := s.pool.Exec(ctx, `
			INSERT INTO events (issue_id, project_id, event_id, occurred_at, release, environment, data)
			VALUES ($1, $2, $3, $4, 'r1', 'production', $5)`,
			issue, project, eventID, now, map[string]any{"level": "error"})
		return err
	}
	id := "b383b41367824396928182ec955fe316"
	if err := insertEvent(a.ID, issueA, id); err != nil {
		t.Fatal(err)
	}
	if err := insertEvent(a.ID, issueA, id); pgCode(err) != "23505" {
		t.Errorf("event_id repetido en el mismo proyecto: err = %v", err)
	}
	if err := insertEvent(b.ID, issueB, id); err != nil {
		t.Errorf("mismo event_id en otro proyecto: %v", err)
	}

	var level string
	if err := s.pool.QueryRow(ctx, `SELECT data->>'level' FROM events WHERE project_id = $1`, a.ID).Scan(&level); err != nil || level != "error" {
		t.Errorf("jsonb: %q, %v", level, err)
	}
}

// Con SQLite no hay eventos de antes que convertir.
func TestConvertLegacyEventsNoopOnSQLite(t *testing.T) {
	if os.Getenv("INTAKE_ERRORS_TEST_DATABASE_URL") != "" {
		t.Skip("solo con SQLite")
	}
	s := testStore(t)
	n, err := s.ConvertLegacyEvents(context.Background(), DefaultConvertOptions(nil))
	if err != nil || n != 0 {
		t.Fatalf("convertidos %d, %v", n, err)
	}
}
