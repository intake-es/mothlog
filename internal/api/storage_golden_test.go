package api_test

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/intake-es/intake-errors/internal/api"
	"github.com/intake-es/intake-errors/internal/digest"
	"github.com/intake-es/intake-errors/internal/envelope"
	"github.com/intake-es/intake-errors/internal/event"
	"github.com/intake-es/intake-errors/internal/ingest"
	"github.com/intake-es/intake-errors/internal/store"
	"github.com/intake-es/intake-errors/internal/store/storetest"
)

// Las salidas de referencia se generaron con el código anterior a guardar el
// evento comprimido; UPDATE_GOLDEN=1 las reescribe.
const goldenFile = "testdata/fixtures_golden.txt"

const fixturesDir = "../../testdata/envelopes"

var fixtureProjects = []struct {
	id   int64
	name string
	key  string
}{
	{1, "api", "5b6d3a35fd6eb6e0f9d410fc5c0f6cbc"},
	{2, "web", "ce01335f125960aa6957a133d15b5f3a"},
	{3, "front", "7438a4393be53fabe513ce19bec380e9"},
}

type goldenEnv struct {
	env
	ing *ingest.Ingester
}

// setupFixtures carga todos los envíos grabados por la ingesta HTTP y, además,
// variantes de sus eventos con usuarios, organization_id y contexts poco
// corrientes, para que las consultas de usuarios y organizaciones tengan datos.
func setupFixtures(t *testing.T) *goldenEnv {
	t.Helper()
	// Las fechas salen en la zona local; fija para que la referencia no dependa de la máquina.
	local := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = local })
	db := storetest.NewDB(t)
	ctx := context.Background()
	for _, p := range fixtureProjects {
		if _, err := db.Pool().Exec(ctx,
			`INSERT INTO projects (id, name, public_key) OVERRIDING SYSTEM VALUE VALUES ($1, $2, $3)`,
			p.id, p.name, p.key); err != nil {
			t.Fatal(err)
		}
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ing := ingest.NewIngester(db.Pool(), nil, nil, log)
	mux := http.NewServeMux()
	mux.Handle(ingest.Pattern, ingest.NewHandler(db.Store, ing, ingest.NewRateLimiter(10000), ingest.NewDrops(db.Store), log))
	if err := api.Register(mux, db.Store, api.Options{PublicURL: "https://errors.example.com"}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	_, token, err := db.CreateToken(ctx, "tests")
	if err != nil {
		t.Fatal(err)
	}
	e := &goldenEnv{env: env{db: db, srv: srv, token: token}, ing: ing}

	names := fixtureNames(t)
	for _, name := range names {
		meta, body := readFixture(t, name)
		target := srv.URL + meta.Path
		if meta.Query != "" {
			target += "?" + meta.Query
		}
		req, err := http.NewRequest(meta.Method, target, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range meta.Headers {
			if !strings.EqualFold(k, "Host") && !strings.EqualFold(k, "Content-Length") {
				req.Header.Set(k, v)
			}
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d", name, res.StatusCode)
		}
	}
	for _, ev := range FixtureVariants(t, names, 3) {
		if _, err := ing.Ingest(ctx, ev.ProjectID, &ev.Event); err != nil {
			t.Fatal(err)
		}
	}

	// Un evento importado en un issue que ya existe.
	var issue int64
	if err := db.Pool().QueryRow(ctx, `SELECT min(id) FROM issues WHERE project_id = 1`).Scan(&issue); err != nil {
		t.Fatal(err)
	}
	imported := fixtureEvent(t, "api-excepcion-1")
	imported.ID = variantID("importado", 0)
	imported.Timestamp = time.Date(2026, 11, 21, 6, 0, 0, 0, time.UTC)
	imported.User = &event.User{Email: "Importada@Example.com"}
	if ok, err := ing.ImportEvent(ctx, 1, issue, &imported); err != nil || !ok {
		t.Fatalf("importar = %v, %v", ok, err)
	}

	// Lo que depende del reloj de la ingesta, fijo para comparar.
	for _, q := range []string{
		`UPDATE events SET received_at = occurred_at`,
		`UPDATE releases SET first_seen = '2026-11-19T00:00:00Z'`,
	} {
		if _, err := db.Pool().Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

func fixtureNames(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(fixturesDir, "*.envelope"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range files {
		names = append(names, strings.TrimSuffix(filepath.Base(f), ".envelope"))
	}
	slices.Sort(names)
	return names
}

type fixtureMeta struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Query   string            `json:"query"`
	Headers map[string]string `json:"headers"`
}

func readFixture(t testing.TB, name string) (fixtureMeta, []byte) {
	t.Helper()
	var m fixtureMeta
	b, err := os.ReadFile(filepath.Join(fixturesDir, name+".meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(fixturesDir, name+".envelope"))
	if err != nil {
		t.Fatal(err)
	}
	return m, body
}

// fixtureEvent es el evento del envío, o uno vacío si no trae ninguno.
func fixtureEvent(t testing.TB, name string) event.Event {
	t.Helper()
	m, body := readFixture(t, name)
	enc := ""
	for k, v := range m.Headers {
		if strings.EqualFold(k, "Content-Encoding") {
			enc = v
		}
	}
	data, err := envelope.Decompress(bytes.NewReader(body), enc, ingest.MaxDecompressed)
	if err != nil {
		t.Fatal(err)
	}
	env, err := envelope.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	items := env.Events()
	if len(items) == 0 {
		return event.Event{}
	}
	ev, err := event.Parse(items[0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	return *ev
}

func variantID(name string, i int) string {
	sum := md5.Sum(fmt.Appendf(nil, "%s/%d", name, i))
	return hex.EncodeToString(sum[:])
}

// VariantEvent es un evento sacado de un fixture para mandarlo a la ingesta.
type VariantEvent struct {
	ProjectID int64
	Event     event.Event
}

var variantUsers = []*event.User{
	{ID: "u-1", Email: "ana@example.com"},
	{Email: "  Ana@Example.com "},
	{Email: "ana@example.com"},
	{Username: "luis"},
	{IPAddress: "{{auto}}"},
	{IPAddress: "10.0.0.1"},
	{Email: " \t", Username: "pepe"},
	{ID: "luis"},
	nil,
}

var variantContexts = []string{
	`{"zz": {"b": "\u00e9 \u003c tab\t", "a": 1e2, "a": 2.50}, "a": [1, -0, 0.10, 1E-2, 12345678901234567890], "os": {"name": "Linux"}}`,
	`{"runtime": {"name": "CPython", "version": "3.14.0"}, "escape": "comillas \" barra \\ \/ \b\f\n\r línea\u2028 \ud83d\ude00", "vacío": {}, "": null}`,
	`[1, "dos", {"tres": true, "dos": false}]`,
	`"solo texto"`,
}

// FixtureVariants da n variantes de cada evento de los fixtures, con fechas,
// usuarios, organization_id y contexts que van cambiando.
func FixtureVariants(t testing.TB, names []string, n int) []VariantEvent {
	t.Helper()
	var out []VariantEvent
	k := 0
	for _, name := range names {
		base := fixtureEvent(t, name)
		if base.ID == "" {
			continue
		}
		m, _ := readFixture(t, name)
		var project int64
		if _, err := fmt.Sscanf(m.Path, "/api/%d/", &project); err != nil {
			t.Fatal(err)
		}
		for i := range n {
			b, err := json.Marshal(base)
			if err != nil {
				t.Fatal(err)
			}
			var ev event.Event
			if err := json.Unmarshal(b, &ev); err != nil {
				t.Fatal(err)
			}
			ev.ID = variantID(name, i)
			ev.Timestamp = time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC).Add(time.Duration(k*97) * time.Minute)
			ev.User = variantUsers[k%len(variantUsers)]
			if k%3 != 2 {
				if ev.Tags == nil {
					ev.Tags = map[string]string{}
				}
				ev.Tags["organization_id"] = fmt.Sprintf("org-%d", k%4)
			}
			if k%2 == 0 {
				ev.Contexts = json.RawMessage(variantContexts[(k/2)%len(variantContexts)])
			}
			if k%5 == 4 && len(ev.Exceptions) > 0 {
				ev.Exceptions[len(ev.Exceptions)-1].Value = ""
			}
			if k%7 == 6 {
				ev.Environment = "staging"
			}
			out = append(out, VariantEvent{ProjectID: project, Event: ev})
			k++
		}
	}
	return out
}

var dailyRE = regexp.MustCompile(`(?s)"daily":\s*\[.*?\]`)

// outputs junta lo que dan la API, el MCP y el resumen de todo lo guardado.
func (e *goldenEnv) outputs(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	var b strings.Builder
	section := func(name string, body []byte) {
		s := strings.ReplaceAll(string(body), e.srv.URL, "http://test")
		s = dailyRE.ReplaceAllString(s, `"daily":"…"`)
		fmt.Fprintf(&b, "=== %s\n%s\n", name, strings.TrimRight(s, "\n"))
	}
	get := func(path string) {
		code, body := e.do(t, http.MethodGet, path, "")
		if code != http.StatusOK {
			t.Fatalf("GET %s = %d: %s", path, code, body)
		}
		section("GET "+path, body)
	}
	cs := connect(t, e.srv.URL, e.token)
	tool := func(name string, args map[string]any) {
		out, isErr := call(t, cs, name, args)
		if isErr {
			t.Fatalf("%s %v: %s", name, args, out)
		}
		a, _ := json.Marshal(args)
		section("MCP "+name+" "+string(a), []byte(out))
	}

	var issues, events []int64
	rows, err := e.db.Pool().Query(ctx, `SELECT id FROM issues ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		issues = append(issues, id)
	}
	if err := e.db.Pool().QueryRow(ctx, `SELECT array_agg(id ORDER BY id) FROM events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if len(issues) == 0 || len(events) == 0 {
		t.Fatalf("%d issues y %d eventos", len(issues), len(events))
	}

	get("/api/0/issues/?status=all&limit=100")
	get("/api/0/tags/")
	for _, key := range []string{"organization_id", "browser", "environment", "level", "release"} {
		get("/api/0/tags/" + key + "/values/")
	}
	for _, id := range issues {
		p := fmt.Sprintf("/api/0/issues/%d/", id)
		get(p)
		get(p + "?environment=production")
		get(p + "events/?limit=100")
		get(p + "events/?limit=2")
		get(p + "events/?environment=staging")
		get(p + "tags/")
		get(fmt.Sprintf("/api/0/issues/%d/stacktrace.md", id))
		get(fmt.Sprintf("/api/0/issues/%d/context.md", id))
		tool("get_issue", map[string]any{"issue_id": id})
		tool("get_issue_tags", map[string]any{"issue_id": id})
		tool("get_stacktrace", map[string]any{"issue_id": id})
		tool("list_events", map[string]any{"issue_id": id, "limit": 100})
		tool("get_issue_context", map[string]any{"issue_id": id})
	}
	for _, id := range events {
		get(fmt.Sprintf("/api/0/events/%d/", id))
		tool("get_event", map[string]any{"event_id": fmt.Sprint(id)})
	}
	var eventIDs []string
	if err := e.db.Pool().QueryRow(ctx, `SELECT array_agg(replace(event_id::text, '-', '') ORDER BY id) FROM events`).Scan(&eventIDs); err != nil {
		t.Fatal(err)
	}
	for _, id := range eventIDs {
		get("/api/0/events/" + id + "/")
	}

	end := time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC)
	for _, p := range fixtureProjects {
		for _, freq := range []string{store.DigestDaily, store.DigestWeekly} {
			for _, lang := range []string{digest.Spanish, digest.English} {
				c := store.DefaultDigest(p.id)
				c.Frequency, c.Language = freq, lang
				length := 24 * time.Hour
				if freq == store.DigestWeekly {
					length = 7 * 24 * time.Hour
				}
				d, err := digest.Build(ctx, e.db.Store, digest.Options{Config: c, Start: end.Add(-length), End: end, Base: "http://test"})
				if err != nil {
					t.Fatal(err)
				}
				j, err := json.MarshalIndent(d, "", "  ")
				if err != nil {
					t.Fatal(err)
				}
				name := fmt.Sprintf("resumen %s %s %s", p.name, freq, lang)
				section(name+" json", j)
				section(name+" markdown", []byte(d.Markdown()))
				section(name+" texto", []byte(d.Text()))
			}
		}
		period := map[string]string{store.DigestDaily: "day", store.DigestWeekly: "week"}
		for _, freq := range []string{store.DigestDaily, store.DigestWeekly} {
			tool("get_digest", map[string]any{"project": p.name, "period": period[freq], "end": end.Format(time.RFC3339)})
		}
	}
	return b.String()
}

func checkGolden(t *testing.T, got string) {
	t.Helper()
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(goldenFile, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(goldenFile)
	if err != nil {
		t.Fatal(err)
	}
	if got == string(want) {
		return
	}
	gl, wl := strings.Split(got, "\n"), strings.Split(string(want), "\n")
	for i := range min(len(gl), len(wl)) {
		if gl[i] != wl[i] {
			t.Fatalf("difiere de %s en la línea %d:\n quiero %s\n tengo  %s", goldenFile, i+1, wl[i], gl[i])
		}
	}
	t.Fatalf("difiere de %s: %d líneas, quiero %d", goldenFile, len(gl), len(wl))
}

func TestFixturesOutputsMatchGolden(t *testing.T) {
	e := setupFixtures(t)
	checkGolden(t, e.outputs(t))
}

func (e *goldenEnv) eventIDs(t *testing.T, where string) []int64 {
	t.Helper()
	var ids []int64
	if err := e.db.Pool().QueryRow(context.Background(),
		`SELECT COALESCE(array_agg(id ORDER BY id), '{}') FROM events WHERE `+where).Scan(&ids); err != nil {
		t.Fatal(err)
	}
	return ids
}

// Con los eventos guardados como antes (en jsonb), con la mitad de cada
// formato y una vez convertidos, todo se lee igual que antes del cambio.
func TestFixturesOutputsWithLegacyEvents(t *testing.T) {
	e := setupFixtures(t)
	ctx := context.Background()
	all := e.eventIDs(t, "true")
	e.db.MakeLegacy(t, all...)
	if legacy := e.eventIDs(t, "data_zstd IS NULL"); len(legacy) != len(all) {
		t.Fatalf("%d de %d sin convertir", len(legacy), len(all))
	}
	t.Run("viejos", func(t *testing.T) { checkGolden(t, e.outputs(t)) })

	n, err := e.db.ConvertLegacyEvents(ctx, store.ConvertOptions{Batch: 7})
	if err != nil || n != int64(len(all)) {
		t.Fatalf("convertidos %d de %d: %v", n, len(all), err)
	}
	var even []int64
	for i, id := range all {
		if i%2 == 0 {
			even = append(even, id)
		}
	}
	e.db.MakeLegacy(t, even...)
	t.Run("mezclados", func(t *testing.T) { checkGolden(t, e.outputs(t)) })

	if n, err := e.db.ConvertLegacyEvents(ctx, store.DefaultConvertOptions(nil)); err != nil || n != int64(len(even)) {
		t.Fatalf("convertidos %d de %d: %v", n, len(even), err)
	}
	if left := e.eventIDs(t, "data_zstd IS NULL OR data IS NOT NULL"); len(left) != 0 {
		t.Fatalf("quedan %d sin convertir", len(left))
	}
	t.Run("convertidos", func(t *testing.T) { checkGolden(t, e.outputs(t)) })
}
