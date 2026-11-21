package store

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/intake-es/intake-errors/migrations"
)

func TestFilters(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, err := s.CreateProject(ctx, "web", randomHex(t, 16))
	if err != nil {
		t.Fatal(err)
	}

	f, err := s.Filters(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	def := DefaultFilters()
	if !slices.Equal(f.Messages, def.Messages) || f.Messages == nil || f.SampleRate != 1 || f.EventsPerIssuePerHour != nil ||
		f.RateLimitPerMinute != nil || f.BrowserExtensions || f.PerIssuePerHour() != DefaultEventsPerIssuePerHour {
		t.Fatalf("por defecto = %+v", f)
	}
	if _, err := s.Filters(ctx, p.ID+1000); !errors.Is(err, ErrNotFound) {
		t.Errorf("proyecto que no existe: %v", err)
	}

	ten, sixty := 10, 60
	f.Messages = []string{`^ResizeObserver loop`, `^ResizeObserver loop`, ` con espacios `}
	f.ExceptionTypes = []string{" KeyError "}
	f.Releases = []string{"1.*"}
	f.WebCrawlers = true
	f.EventsPerIssuePerHour = &ten
	f.RateLimitPerMinute = &sixty
	f.SampleRate = 0.25
	if _, err := s.SetFilters(ctx, p.ID, f); err != nil {
		t.Fatal(err)
	}
	got, err := s.Filters(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Messages, []string{`^ResizeObserver loop`, ` con espacios `}) || !slices.Equal(got.ExceptionTypes, []string{"KeyError"}) ||
		!got.WebCrawlers || got.PerIssuePerHour() != 10 || *got.RateLimitPerMinute != 60 || got.SampleRate != 0.25 ||
		!slices.Equal(got.Releases, []string{"1.*"}) {
		t.Fatalf("guardado = %+v", got)
	}

	zero, nan := 0, 0.0
	for name, bad := range map[string]func(*Filters){
		"regex rota":      func(f *Filters) { f.Messages = []string{"(abc"} },
		"valor vacío":     func(f *Filters) { f.Environments = []string{" "} },
		"muestreo > 1":    func(f *Filters) { f.SampleRate = 1.5 },
		"muestreo < 0":    func(f *Filters) { f.SampleRate = -0.1 },
		"muestreo NaN":    func(f *Filters) { f.SampleRate = nan / nan },
		"tope 0":          func(f *Filters) { f.EventsPerIssuePerHour = &zero },
		"límite 0":        func(f *Filters) { f.RateLimitPerMinute = &zero },
		"demasiados":      func(f *Filters) { f.Releases = make([]string, 101); fill(f.Releases) },
		"valor muy largo": func(f *Filters) { f.ExceptionTypes = []string{string(make([]byte, 1001))} },
	} {
		f := got
		bad(&f)
		if _, err := s.SetFilters(ctx, p.ID, f); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v; quiero ErrInvalid", name, err)
		}
	}
	if again, _ := s.Filters(ctx, p.ID); !slices.Equal(again.Messages, got.Messages) || again.SampleRate != 0.25 {
		t.Errorf("un cambio no válido ha tocado lo guardado: %+v", again)
	}
	if _, err := s.SetFilters(ctx, p.ID+1000, DefaultFilters()); !errors.Is(err, ErrNotFound) {
		t.Errorf("guardar en un proyecto que no existe: %v", err)
	}
}

func fill(l []string) {
	for i := range l {
		l[i] = time.Duration(i).String()
	}
}

func TestDropped(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, err := s.CreateProject(ctx, "web", randomHex(t, 16))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 21, 23, 30, 0, 0, time.UTC)
	yesterday := now.AddDate(0, 0, -1)
	old := now.AddDate(0, 0, -40)
	if err := s.AddDropped(ctx, []Dropped{
		{p.ID, DropWebCrawler, now, 3},
		{p.ID, DropWebCrawler, now.Add(-time.Hour), 2},
		{p.ID, DropIssueLimit, yesterday, 7},
		{p.ID, DropSampled, old, 100},
		{p.ID + 1000, DropSampled, now, 1},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddDropped(ctx, []Dropped{{p.ID, DropWebCrawler, now, 1}}); err != nil {
		t.Fatal(err)
	}
	st, err := s.DroppedSince(ctx, p.ID, 30, now)
	if err != nil {
		t.Fatal(err)
	}
	if st.Since != "2026-10-23" || st.Total != 13 || st.Reasons[DropWebCrawler] != 6 || st.Reasons[DropIssueLimit] != 7 ||
		st.Reasons[DropSampled] != 0 || len(st.Reasons) != len(DropReasons) {
		t.Fatalf("= %+v", st)
	}
	want := []DroppedDay{{"2026-11-20", DropIssueLimit, 7}, {"2026-11-21", DropWebCrawler, 6}}
	if !slices.Equal(st.Daily, want) {
		t.Errorf("daily = %+v", st.Daily)
	}
	if st, err := s.DroppedSince(ctx, p.ID, 1, now); err != nil || st.Total != 6 {
		t.Errorf("solo hoy: %+v, %v", st, err)
	}
	if _, err := s.DroppedSince(ctx, p.ID, 0, now); !errors.Is(err, ErrInvalid) {
		t.Errorf("0 días: %v", err)
	}
	if _, err := s.DroppedSince(ctx, p.ID+1000, 30, now); !errors.Is(err, ErrNotFound) {
		t.Errorf("proyecto que no existe: %v", err)
	}
}

func TestMigrationFilters(t *testing.T) {
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
	var project, issue int64
	if err := pool.QueryRow(ctx, `INSERT INTO projects (name, public_key) VALUES ('web', 'clave') RETURNING id`).Scan(&project); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO issues (project_id, fingerprint, title, first_seen, last_seen, event_count)
		VALUES ($1, 'fp', 'TypeError', now(), now(), 42) RETURNING id`, project).Scan(&issue); err != nil {
		t.Fatal(err)
	}

	res, err := p.UpTo(ctx, 9)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) == 0 || res[len(res)-1].Source.Version != 9 {
		t.Fatalf("aplicadas = %v", res)
	}
	var count int64
	var hour *time.Time
	var inHour int
	if err := pool.QueryRow(ctx, `SELECT event_count, stored_hour, stored_in_hour FROM issues WHERE id = $1`, issue).Scan(&count, &hour, &inHour); err != nil ||
		count != 42 || hour != nil || inHour != 0 {
		t.Errorf("issue tras migrar: %d, %v, %d, %v", count, hour, inHour, err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO project_filters (project_id, sample_rate) VALUES ($1, 1.5)`, project); err == nil {
		t.Error("sample_rate 1,5 aceptado")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO project_filters (project_id, web_crawlers) VALUES ($1, true)`, project); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO dropped_events VALUES ($1, 'web_crawler', current_date, 3)`, project); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM projects WHERE id = $1`, project); err != nil {
		t.Fatalf("borrar el proyecto con filtros y cuentas: %v", err)
	}

	if _, err := p.DownTo(ctx, 7); err != nil {
		t.Fatalf("bajar: %v", err)
	}
	if _, err := p.UpTo(ctx, 9); err != nil {
		t.Fatalf("volver a subir: %v", err)
	}
}
