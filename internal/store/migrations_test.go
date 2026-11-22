package store

import (
	"context"
	"io"
	"log/slog"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
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

	res, err := p.UpTo(ctx, 7)
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

func TestMigrationSourceMapsSentryCLI(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, testSchemaURL(t))
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
	if _, err := p.UpTo(ctx, 7); err != nil {
		t.Fatal(err)
	}
	var web int64
	if err := pool.QueryRow(ctx, `INSERT INTO projects (name, public_key) VALUES ('web', 'clave-web') RETURNING id`).Scan(&web); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO sourcemaps (project_id, release, file, map_gzip, size) VALUES ($1, 'r1', 'a.js', 'viejo', 5)`, web); err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 8); err != nil {
		t.Fatal(err)
	}
	var dist, url string
	var debugID *string
	if err := pool.QueryRow(ctx, `SELECT dist, url, debug_id::text FROM sourcemaps WHERE file = 'a.js'`).Scan(&dist, &url, &debugID); err != nil || dist != "" || url != "" || debugID != nil {
		t.Fatalf("mapa de antes: %q %q %v, %v", dist, url, debugID, err)
	}
	// Dos del mismo release y fichero con debug ID, y uno con dist.
	if _, err := pool.Exec(ctx, `
		INSERT INTO sourcemaps (project_id, release, file, map_gzip, size, debug_id, uploaded_at) VALUES
			($1, 'r1', 'a.js', 'id1', 3, '174ab4b5-75a1-56fb-be73-185871cd4267', now() + interval '1 minute'),
			($1, 'r1', 'a.js', 'id2', 3, '86c80c38-f83a-593e-aa45-9e741c94fda4', now())`, web); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO sourcemaps (project_id, release, dist, file, map_gzip, size) VALUES ($1, 'r1', 'd1', 'a.js', 'd1', 2)`, web); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO sourcemaps (project_id, release, file, map_gzip, size) VALUES ($1, 'r1', 'a.js', 'x', 1)`, web); err == nil {
		t.Error("dos mapas sin debug ID con el mismo release, dist y fichero")
	}

	if _, err := p.DownTo(ctx, 7); err != nil {
		t.Fatalf("bajar: %v", err)
	}
	var body string
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*), max(convert_from(map_gzip, 'UTF8')) FROM sourcemaps`).Scan(&n, &body); err != nil || n != 1 || body != "id1" {
		t.Errorf("tras bajar: %d filas, %q, %v; quiero solo el más nuevo", n, body, err)
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("volver a subir: %v", err)
	}
}

func TestMigrationReleases(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, testSchemaURL(t))
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
	if _, err := p.UpTo(ctx, 9); err != nil {
		t.Fatal(err)
	}
	var project, issue int64
	if err := pool.QueryRow(ctx, `INSERT INTO projects (name, public_key) VALUES ('web', 'clave') RETURNING id`).Scan(&project); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO issues (project_id, fingerprint, title, first_seen, last_seen, event_count)
		VALUES ($1, 'fp', 'TypeError', now(), now(), 3) RETURNING id`, project).Scan(&issue); err != nil {
		t.Fatal(err)
	}
	for i, ev := range []struct{ release, env any }{{"v1", "production"}, {"v2", "production"}, {nil, "staging"}} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO events (issue_id, project_id, event_id, occurred_at, received_at, release, environment, data)
			VALUES ($1, $2, gen_random_uuid(), now() - make_interval(hours => 3 - $5), now() - make_interval(hours => 3 - $5), $3, $4, '{}')`,
			issue, project, ev.release, ev.env, i); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := p.UpTo(ctx, 10); err != nil {
		t.Fatal(err)
	}
	rows, err := pool.Query(ctx, `SELECT version FROM releases ORDER BY first_seen, id`)
	if err != nil {
		t.Fatal(err)
	}
	versions, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil || !slices.Equal(versions, []string{"v1", "v2"}) {
		t.Errorf("releases = %v, %v", versions, err)
	}
	var stats int
	var first *string
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM issue_stats), first_release FROM issues`).Scan(&stats, &first); err != nil {
		t.Fatal(err)
	}
	if stats != 3 || first == nil || *first != "v1" {
		t.Errorf("issue_stats = %d, first_release = %v", stats, first)
	}
	if _, err := p.DownTo(ctx, 9); err != nil {
		t.Fatalf("bajar: %v", err)
	}
}

func TestMigrationAlertRules(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, testSchemaURL(t))
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
	if _, err := p.UpTo(ctx, 11); err != nil {
		t.Fatal(err)
	}
	var issue int64
	if err := pool.QueryRow(ctx, `
		WITH p AS (INSERT INTO projects (name, public_key) VALUES ('web', 'clave-web') RETURNING id)
		INSERT INTO issues (project_id, fingerprint, title, first_seen, last_seen, event_count)
		SELECT id, 'fp', 'TypeError', now(), now(), 1 FROM p RETURNING id`).Scan(&issue); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO alert_outbox (issue_id, channel, kind, event_at) VALUES ($1, 'slack', 'new', now())`, issue); err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 12); err != nil {
		t.Fatal(err)
	}
	var rule int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO alert_rules (project_id, name, conditions, actions)
		SELECT project_id, 'r', '{"new": true}', '[{"id": "a", "type": "webhook"}]' FROM issues WHERE id = $1 RETURNING id`, issue).Scan(&rule); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO alert_outbox (issue_id, channel, kind, event_at, rule_id, action_id) VALUES ($1, 'webhook', 'frequency', now(), $2, 'a')`, issue, rule); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO alert_outbox (issue_id, channel, kind, event_at) VALUES ($1, 'sms', 'new', now())`, issue); err == nil {
		t.Error("el canal sms no debería caber")
	}
	if _, err := p.DownTo(ctx, 11); err != nil {
		t.Fatalf("bajar: %v", err)
	}
	var n int
	var channel string
	if err := pool.QueryRow(ctx, `SELECT count(*), max(channel) FROM alert_outbox`).Scan(&n, &channel); err != nil || n != 1 || channel != "slack" {
		t.Errorf("tras bajar: %d avisos, %q, %v; quiero solo el de Slack de antes", n, channel, err)
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("volver a subir: %v", err)
	}
}

func TestMigrationComments(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, testSchemaURL(t))
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
	if _, err := p.UpTo(ctx, 12); err != nil {
		t.Fatal(err)
	}
	var issue int64
	if err := pool.QueryRow(ctx, `
		WITH p AS (INSERT INTO projects (name, public_key) VALUES ('web', 'clave-web') RETURNING id)
		INSERT INTO issues (project_id, fingerprint, title, first_seen, last_seen, event_count)
		SELECT id, 'fp', 'TypeError', now(), now(), 1 FROM p RETURNING id`).Scan(&issue); err != nil {
		t.Fatal(err)
	}
	res, err := p.UpTo(ctx, 13)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Source.Version != 13 {
		t.Fatalf("aplicadas = %v", res)
	}
	var comment int64
	if err := pool.QueryRow(ctx, `INSERT INTO issue_comments (issue_id, author, author_kind, body)
		VALUES ($1, 'agente', 'token', 'mirado') RETURNING id`, issue).Scan(&comment); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO issue_status_changes (issue_id, author, author_kind, source, from_status, to_status, comment_id)
		VALUES ($1, 'agente', 'token', 'mcp', 'open', 'resolved', $2)`, issue, comment); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO issue_status_changes (issue_id, source, from_status, to_status, release)
		VALUES ($1, 'ingest', 'resolved', 'open', 'v2')`, issue); err != nil {
		t.Fatalf("un cambio sin autor: %v", err)
	}
	for _, bad := range []string{
		`INSERT INTO issue_comments (issue_id, author, author_kind, body) VALUES ($1, 'x', 'robot', 'hola')`,
		`INSERT INTO issue_comments (issue_id, author, author_kind, body) VALUES ($1, 'x', 'user', '')`,
		`INSERT INTO issue_status_changes (issue_id, author, source, from_status, to_status) VALUES ($1, 'x', 'mcp', 'open', 'resolved')`,
		`INSERT INTO issue_status_changes (issue_id, source, from_status, to_status) VALUES ($1, 'cli', 'open', 'resolved')`,
	} {
		if _, err := pool.Exec(ctx, bad, issue); err == nil {
			t.Errorf("no debería caber: %s", bad)
		}
	}
	// Borrar el comentario deja el cambio; borrar el issue se lleva los dos.
	if _, err := pool.Exec(ctx, `DELETE FROM issue_comments WHERE id = $1`, comment); err != nil {
		t.Fatal(err)
	}
	var changes int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM issue_status_changes WHERE issue_id = $1 AND comment_id IS NULL`, issue).Scan(&changes); err != nil || changes != 2 {
		t.Errorf("cambios tras borrar el comentario = %d, %v", changes, err)
	}

	if _, err := p.DownTo(ctx, 12); err != nil {
		t.Fatalf("bajar: %v", err)
	}
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('issue_comments') IS NOT NULL OR to_regclass('issue_status_changes') IS NOT NULL`).Scan(&exists); err != nil || exists {
		t.Errorf("tras bajar siguen las tablas: %v, %v", exists, err)
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("volver a subir: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM issues WHERE id = $1`, issue); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationAssign(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, testSchemaURL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	db := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = db.Close() })
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS,
		goose.WithSlog(slog.New(slog.NewTextHandler(io.Discard, nil))), goose.WithAllowOutofOrder(true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 13); err != nil {
		t.Fatal(err)
	}
	var issue, token int64
	if err := pool.QueryRow(ctx, `
		WITH p AS (INSERT INTO projects (name, public_key) VALUES ('web', 'clave-web') RETURNING id)
		INSERT INTO issues (project_id, fingerprint, title, first_seen, last_seen, event_count)
		SELECT id, 'fp', 'TypeError', now(), now(), 1 FROM p RETURNING id`).Scan(&issue); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO api_tokens (name, token_sha256) VALUES ('agente', '\x01') RETURNING id`).Scan(&token); err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 16); err != nil {
		t.Fatal(err)
	}
	var assignee *string
	if err := pool.QueryRow(ctx, `SELECT assignee FROM issues WHERE id = $1`, issue).Scan(&assignee); err != nil || assignee != nil {
		t.Fatalf("los issues de antes quedan sin asignar: %v, %v", assignee, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE issues SET assignee = 'agente', assignee_kind = 'token', assignee_token_id = $2, assigned_at = now() WHERE id = $1`, issue, token); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		`UPDATE issues SET assignee = 'ana', assignee_kind = NULL, assignee_token_id = NULL, assigned_at = now() WHERE id = $1`,
		`UPDATE issues SET assignee = 'ana', assignee_kind = 'token', assignee_token_id = NULL, assigned_at = now() WHERE id = $1`,
		`UPDATE issues SET assignee = 'ana', assignee_kind = 'robot', assignee_token_id = NULL, assigned_at = now() WHERE id = $1`,
		`INSERT INTO issue_assignment_changes (issue_id, source, to_name) VALUES ($1, 'mcp', 'ana')`,
		`INSERT INTO issue_assignment_changes (issue_id, source, to_name, to_kind) VALUES ($1, 'ingest', 'ana', 'user')`,
		`INSERT INTO people (login, name) VALUES ('', 'nadie')`,
	} {
		if _, err := pool.Exec(ctx, bad, issue); err == nil {
			t.Errorf("no debería caber: %s", bad)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO issue_assignment_changes (issue_id, author, author_kind, source, to_name, to_kind)
		VALUES ($1, 'agente', 'token', 'mcp', 'agente', 'token')`, issue); err != nil {
		t.Fatal(err)
	}

	if _, err := p.DownTo(ctx, 15); err != nil {
		t.Fatalf("bajar: %v", err)
	}
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('people') IS NOT NULL OR to_regclass('issue_assignment_changes') IS NOT NULL
		OR EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'issues' AND column_name = 'assignee' AND table_schema = current_schema())`).Scan(&exists); err != nil || exists {
		t.Errorf("tras bajar sigue lo de asignar: %v, %v", exists, err)
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("volver a subir: %v", err)
	}
}
