package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/intake-es/intake-errors/internal/db"
	"github.com/intake-es/intake-errors/migrations"
	sqlitemigrations "github.com/intake-es/intake-errors/migrations/sqlite"
)

func TestLoginUniqueIgnoringCase(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.SaveIdentity(ctx, Person{Login: "ana", Provider: LoginGitHub, Subject: "101"}); err != nil {
		t.Fatal(err)
	}
	// La base lo impide aunque se salte SaveIdentity.
	for _, login := range []string{"ana", "Ana", "ANA"} {
		_, err := s.pool.Exec(ctx, `INSERT INTO people (login, name, provider, subject) VALUES ($1, $1, 'oidc', $1)`, login)
		if err == nil {
			t.Errorf("cabe otra persona con login %q", login)
		}
	}

	// Muchas altas a la vez con el mismo login: cada una con el suyo.
	var wg sync.WaitGroup
	got := make([]string, 8)
	errs := make([]error, 8)
	for i := range got {
		wg.Go(func() {
			got[i], errs[i] = s.SaveIdentity(ctx, Person{Login: "Luis", Provider: LoginOIDC, Subject: fmt.Sprintf("sub-%d", i)})
		})
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	if len(slices.Compact(slices.Clone(got))) != len(got) || got[0] != "Luis" {
		t.Errorf("logins = %v", got)
	}

	// Asignarse a sí misma con un login que, sin mayúsculas, es de otra.
	p, err := s.CreateProject(ctx, "web", randomHex(t, 16))
	if err != nil {
		t.Fatal(err)
	}
	var issue int64
	if err := s.pool.QueryRow(ctx, `INSERT INTO issues (project_id, fingerprint, title, first_seen, last_seen, event_count)
		VALUES ($1, 'fp', 'TypeError', now(), now(), 1) RETURNING id`, p.ID).Scan(&issue); err != nil {
		t.Fatal(err)
	}
	me := Actor{Name: "ANA", Kind: ActorUser}
	if _, err := s.AssignIssue(ctx, issue, &me, AssignChange{Actor: &me, Source: SourceUI}); !errors.Is(err, ErrInvalid) {
		t.Errorf("ANA se asigna el issue con el login de ana = %v", err)
	}
}

func TestLegacyGitHubSubject(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `INSERT INTO people (login, name, provider, subject) VALUES ('Ana', 'Ana', 'github', 'login:Ana')`); err != nil {
		t.Fatal(err)
	}
	gh := func(id, login string) string {
		t.Helper()
		got, err := s.SaveIdentity(ctx, Person{Login: login, Provider: LoginGitHub, Subject: id, LegacySubject: LegacyGitHubSubject(login)})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	// GitHub no distingue mayúsculas en el login.
	if got := gh("101", "ana"); got != "Ana" {
		t.Errorf("la primera cuenta con ese login = %q", got)
	}
	if got := gh("202", "ana"); got != "ana~2" {
		t.Errorf("otra cuenta con el mismo login = %q", got)
	}
	if got := gh("101", "ana-nueva"); got != "Ana" {
		t.Errorf("la primera, renombrada = %q", got)
	}
	var subject string
	if err := s.pool.QueryRow(ctx, `SELECT subject FROM people WHERE login = 'Ana'`).Scan(&subject); err != nil || subject != "101" {
		t.Errorf("subject = %q, %v", subject, err)
	}
}

func TestRevokeSessions(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.SaveIdentity(ctx, Person{Login: "ana", Provider: LoginGitHub, Subject: "101"}); err != nil {
		t.Fatal(err)
	}
	since, err := s.SessionsValidSince(ctx, "ana")
	if err != nil || since.Unix() != 0 {
		t.Fatalf("al empezar = %v, %v", since, err)
	}
	at := time.Date(2026, 11, 23, 10, 0, 0, 123456789, time.UTC)
	if login, err := s.RevokeSessions(ctx, "ANA", at); err != nil || login != "ana" {
		t.Fatalf("revocar = %q, %v", login, err)
	}
	since, err = s.SessionsValidSince(ctx, "ana")
	if err != nil || since.UnixMicro() != at.UnixMicro() {
		t.Errorf("válidas desde = %v (%d), %v; quiero %d", since, since.UnixMicro(), err, at.UnixMicro())
	}
	// La sesión se busca por el login exacto, el que firma la cookie.
	if _, err := s.SessionsValidSince(ctx, "ANA"); !errors.Is(err, ErrNotFound) {
		t.Errorf("con otras mayúsculas = %v", err)
	}
	if _, err := s.RevokeSessions(ctx, "nadie", at); !errors.Is(err, ErrNotFound) {
		t.Errorf("revocar a nadie = %v", err)
	}
}

// legacyPeople son personas de antes de la 00023, con dos duplicados sin
// mirar mayúsculas y un login~2 ya ocupado, y lo que apunta a ellas.
const legacyPeople = `
	INSERT INTO people (login, name, provider, subject, first_seen_at) VALUES
		('ana', 'Ana', 'github', 'ana', %[1]s),
		('Ana', 'Ana de OIDC', 'oidc', 'sub-1', %[2]s),
		('ANA', 'Otra Ana', 'oidc', 'sub-2', %[3]s),
		('ana~2', 'Ana dos', 'oidc', 'sub-3', %[1]s),
		('luis', 'Luis', 'github', 'luis', %[1]s),
		('dev', 'Modo desarrollo', 'dev', 'dev', %[1]s);
	UPDATE issues SET assignee = 'ANA', assignee_kind = 'user', assigned_at = %[1]s;
	INSERT INTO issue_comments (issue_id, author, author_kind, body) SELECT id, 'Ana', 'user', 'de la de OIDC' FROM issues;
	INSERT INTO issue_comments (issue_id, author, author_kind, body) SELECT id, 'ana', 'user', 'de la de GitHub' FROM issues;
	INSERT INTO issue_assignment_changes (issue_id, author, author_kind, source, to_name, to_kind)
		SELECT id, 'Ana', 'user', 'ui', 'ANA', 'user' FROM issues;`

func checkMigrated(t *testing.T, ctx context.Context, q db.DB) {
	t.Helper()
	rows, err := q.Query(ctx, `SELECT login || ' ' || provider || ' ' || subject FROM people ORDER BY login`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.CollectRows(rows, func(r db.CollectableRow) (string, error) {
		var s string
		return s, r.Scan(&s)
	})
	want := []string{"ANA~4 oidc sub-2", "Ana~3 oidc sub-1", "ana github login:ana", "ana~2 oidc sub-3", "dev dev dev", "luis github login:luis"}
	slices.Sort(got)
	slices.Sort(want)
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("personas tras migrar = %v, %v; quiero %v", got, err, want)
	}
	var assignee, change string
	if err := q.QueryRow(ctx, `SELECT assignee FROM issues`).Scan(&assignee); err != nil || assignee != "ANA~4" {
		t.Errorf("asignado = %q, %v", assignee, err)
	}
	rows, err = q.Query(ctx, `SELECT author || ':' || body FROM issue_comments ORDER BY body`)
	if err != nil {
		t.Fatal(err)
	}
	cs, _ := db.CollectRows(rows, func(r db.CollectableRow) (string, error) {
		var s string
		return s, r.Scan(&s)
	})
	if !slices.Equal(cs, []string{"ana:de la de GitHub", "Ana~3:de la de OIDC"}) {
		t.Errorf("comentarios = %v", cs)
	}
	if err := q.QueryRow(ctx, `SELECT author || '>' || to_name FROM issue_assignment_changes`).Scan(&change); err != nil || change != "Ana~3>ANA~4" {
		t.Errorf("historial = %q, %v", change, err)
	}
	var since time.Time
	if err := q.QueryRow(ctx, `SELECT sessions_valid_since FROM people WHERE login = 'ana'`).Scan(&since); err != nil || since.Unix() != 0 {
		t.Errorf("sessions_valid_since = %v, %v", since, err)
	}
	if _, err := q.Exec(ctx, `INSERT INTO people (login, name, provider, subject) VALUES ('LUIS', 'x', 'oidc', 'sub-9')`); err == nil {
		t.Error("tras migrar cabe LUIS junto a luis")
	}
}

func TestMigrationSessionsPostgres(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, testSchemaURL(t, migrationsOnlyPostgres))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	sqlDB := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = sqlDB.Close() })
	p, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.FS,
		goose.WithSlog(slog.New(slog.NewTextHandler(io.Discard, nil))), goose.WithAllowOutofOrder(true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 22); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		WITH p AS (INSERT INTO projects (name, public_key) VALUES ('web', 'clave-web') RETURNING id)
		INSERT INTO issues (project_id, fingerprint, title, first_seen, last_seen, event_count)
		SELECT id, 'fp', 'TypeError', now(), now(), 1 FROM p`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, fmt.Sprintf(legacyPeople, "now() - interval '3 days'", "now() - interval '2 days'", "now() - interval '1 day'")); err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 23); err != nil {
		t.Fatal(err)
	}
	checkMigrated(t, ctx, db.NewPostgres(pool))

	if _, err := p.DownTo(ctx, 22); err != nil {
		t.Fatalf("bajar: %v", err)
	}
	var subject string
	if err := pool.QueryRow(ctx, `SELECT subject FROM people WHERE login = 'luis'`).Scan(&subject); err != nil || subject != "luis" {
		t.Errorf("tras bajar, subject = %q, %v", subject, err)
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("volver a subir: %v", err)
	}
}

// Las bases de SQLite empiezan en la 00022: la 00023 tiene que subirlas.
func TestMigrationSessionsSQLite(t *testing.T) {
	ctx := context.Background()
	lite, err := db.OpenSQLite(ctx, "sqlite://"+filepath.Join(t.TempDir(), "intake.db"), db.SQLiteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(lite.Close)
	p, err := goose.NewProvider(goose.DialectSQLite3, lite.WriteDB(), sqlitemigrations.FS,
		goose.WithSlog(slog.New(slog.NewTextHandler(io.Discard, nil))), goose.WithAllowOutofOrder(true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 22); err != nil {
		t.Fatal(err)
	}
	if _, err := lite.Exec(ctx, `
		INSERT INTO projects (name, public_key) VALUES ('web', 'clave-web');
		INSERT INTO issues (project_id, fingerprint, title, first_seen, last_seen, event_count)
		SELECT id, 'fp', 'TypeError', now(), now(), 1 FROM projects`); err != nil {
		t.Fatal(err)
	}
	day := int64(24 * time.Hour / time.Microsecond)
	if _, err := lite.Exec(ctx, fmt.Sprintf(legacyPeople, fmt.Sprintf("now() - %d", 3*day), fmt.Sprintf("now() - %d", 2*day), fmt.Sprintf("now() - %d", day))); err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 23); err != nil {
		t.Fatal(err)
	}
	checkMigrated(t, ctx, lite)
	if _, err := p.DownTo(ctx, 22); err != nil {
		t.Fatalf("bajar: %v", err)
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("volver a subir: %v", err)
	}
}
