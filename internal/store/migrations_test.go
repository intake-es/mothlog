package store

import (
	"context"
	"io"
	"log/slog"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/intake-es/intake-errors/migrations"
)

func TestMigrationProjectLabels(t *testing.T) {
	ctx := context.Background()
	u := testSchemaURL(t)
	pool, err := pgxpool.New(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	db := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = db.Close() })
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS,
		goose.WithSlog(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 6); err != nil {
		t.Fatal(err)
	}

	var back, web int64
	if err := pool.QueryRow(ctx, `INSERT INTO projects (name, public_key) VALUES ('backend', 'clave-backend') RETURNING id`).Scan(&back); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO projects (name, public_key) VALUES ('web', 'clave-web') RETURNING id`).Scan(&web); err != nil {
		t.Fatal(err)
	}
	var issue int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO issues (project_id, fingerprint, title, first_seen, last_seen, event_count, linear_issue_id)
		VALUES ($1, 'fp', 'KeyError', now(), now(), 3, 'lin-1') RETURNING id`, back).Scan(&issue); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO alert_outbox (issue_id, channel, kind, event_at) VALUES ($1, 'linear', 'new', now())`, issue); err != nil {
		t.Fatal(err)
	}

	res, err := p.Up(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Source.Version != 7 {
		t.Fatalf("aplicadas = %v", res)
	}

	rows, err := pool.Query(ctx, `SELECT id, name, public_key, linear_label_ids FROM projects ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		ID        int64
		Name, Key string
		Labels    []string
	}
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.ID, &r.Name, &r.Key, &r.Labels); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != back || got[0].Name != "backend" || got[0].Key != "clave-backend" || got[1].ID != web {
		t.Fatalf("proyectos = %+v", got)
	}
	for _, r := range got {
		if r.Labels == nil || len(r.Labels) != 0 {
			t.Errorf("%s: etiquetas = %#v; quiero vacío", r.Name, r.Labels)
		}
	}
	var n int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM issues i JOIN alert_outbox o ON o.issue_id = i.id
		WHERE i.id = $1 AND i.linear_issue_id = 'lin-1' AND i.event_count = 3`, issue).Scan(&n); err != nil || n != 1 {
		t.Errorf("issue y aviso tras migrar: %d, %v", n, err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'projects' AND column_name <> ALL($1)`,
		[]string{"id", "name", "public_key", "created_at", "linear_label_ids"}).Scan(&n); err != nil || n != 0 {
		t.Errorf("columnas de más en projects: %d, %v", n, err)
	}

	if _, err := pool.Exec(ctx, `UPDATE projects SET linear_label_ids = '{label-equipo}' WHERE id = $1`, back); err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 6); err != nil {
		t.Fatalf("bajar: %v", err)
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("volver a subir: %v", err)
	}
	var labels []string
	if err := pool.QueryRow(ctx, `SELECT linear_label_ids FROM projects WHERE id = $1`, back).Scan(&labels); err != nil || !slices.Equal(labels, []string{}) {
		t.Errorf("tras bajar y subir: %v, %v", labels, err)
	}
}
