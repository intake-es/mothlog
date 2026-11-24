package store

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/intake-es/mothlog/internal/db"
	"github.com/intake-es/mothlog/migrations"
	sqlitemigrations "github.com/intake-es/mothlog/migrations/sqlite"
)

// seedDeclared deja, antes de la 00024, una release finalizada, una con
// despliegue, una importada y una que solo vio la ingesta.
func seedDeclared(t *testing.T, ctx context.Context, q db.Querier) {
	t.Helper()
	var project int64
	if err := q.QueryRow(ctx, `INSERT INTO projects (name, public_key) VALUES ('web', 'clave-web') RETURNING id`).Scan(&project); err != nil {
		t.Fatal(err)
	}
	ids := map[string]int64{}
	for _, v := range []string{"finalizada", "desplegada", "importada", "de-la-ingesta"} {
		var id int64
		if err := q.QueryRow(ctx, `INSERT INTO releases (project_id, version, first_seen) VALUES ($1, $2, $3) RETURNING id`,
			project, v, time.Now().Add(-time.Hour)).Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids[v] = id
	}
	for _, s := range []struct {
		sql  string
		args []any
	}{
		{`UPDATE releases SET finalized_at = $2 WHERE id = $1`, []any{ids["finalizada"], time.Now()}},
		{`INSERT INTO deploys (release_id, environment, deployed_at) VALUES ($1, 'production', $2)`, []any{ids["desplegada"], time.Now()}},
		{`INSERT INTO import_origins (source, server, org, kind, external_id, release_id) VALUES ('sentry', 's', 'o', 'release', '1', $1)`, []any{ids["importada"]}},
	} {
		if _, err := q.Exec(ctx, s.sql, s.args...); err != nil {
			t.Fatalf("%s: %v", s.sql, err)
		}
	}
}

func checkDeclared(t *testing.T, ctx context.Context, q db.Querier) {
	t.Helper()
	rows, err := q.Query(ctx, `SELECT version FROM releases WHERE declared_at IS NOT NULL ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.CollectRows(rows, db.RowTo[string])
	if want := []string{"desplegada", "finalizada", "importada"}; err != nil || !slices.Equal(got, want) {
		t.Errorf("declaradas = %v, %v; quiero %v", got, err, want)
	}
}

func TestMigrationDeclaredReleasesPostgres(t *testing.T) {
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
	if _, err := p.UpTo(ctx, 23); err != nil {
		t.Fatal(err)
	}
	q := db.NewPostgres(pool)
	seedDeclared(t, ctx, q)
	if _, err := p.UpTo(ctx, 24); err != nil {
		t.Fatal(err)
	}
	checkDeclared(t, ctx, q)
	if _, err := p.DownTo(ctx, 23); err != nil {
		t.Fatalf("bajar: %v", err)
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("volver a subir: %v", err)
	}
}

func TestMigrationDeclaredReleasesSQLite(t *testing.T) {
	ctx := context.Background()
	lite, err := db.OpenSQLite(ctx, "sqlite://"+filepath.Join(t.TempDir(), "mothlog.db"), db.SQLiteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(lite.Close)
	p, err := goose.NewProvider(goose.DialectSQLite3, lite.WriteDB(), sqlitemigrations.FS,
		goose.WithSlog(slog.New(slog.NewTextHandler(io.Discard, nil))), goose.WithAllowOutofOrder(true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 23); err != nil {
		t.Fatal(err)
	}
	seedDeclared(t, ctx, lite)
	if _, err := p.UpTo(ctx, 24); err != nil {
		t.Fatal(err)
	}
	checkDeclared(t, ctx, lite)
	if _, err := p.DownTo(ctx, 23); err != nil {
		t.Fatalf("bajar: %v", err)
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("volver a subir: %v", err)
	}
}

// Crear, finalizar o desplegar con un token declara la release; la ingesta no.
func TestReleaseProjectsOnlyDeclared(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	var ids []int64
	for _, name := range []string{"api", "web", "front", "otro"} {
		p, err := s.CreateProject(ctx, name, "clave-"+name)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, p.ID)
	}
	if _, _, err := EnsureRelease(ctx, s.pool, ids[0], "2.0.0", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateRelease(ctx, ids[1], "2.0.0"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinalizeRelease(ctx, ids[2], "2.0.0", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AddDeploy(ctx, ids[3], "2.0.0", "production", time.Now()); err != nil {
		t.Fatal(err)
	}
	ps, err := s.ReleaseProjects(ctx, "2.0.0")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range ps {
		names = append(names, p.Name)
	}
	if want := []string{"web", "front", "otro"}; !slices.Equal(names, want) {
		t.Errorf("proyectos = %v, quiero %v", names, want)
	}
}
