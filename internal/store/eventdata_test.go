package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/intake-es/intake-errors/internal/db"
	"github.com/intake-es/intake-errors/internal/envelope"
	"github.com/intake-es/intake-errors/internal/event"
)

// fixtureEvents son los eventos de testdata/envelopes.
func fixtureEvents(t *testing.T) []event.Event {
	t.Helper()
	files, err := filepath.Glob("../../testdata/envelopes/*.envelope")
	if err != nil {
		t.Fatal(err)
	}
	var out []event.Event
	for _, f := range files {
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		meta, err := os.ReadFile(strings.TrimSuffix(f, ".envelope") + ".meta.json")
		if err != nil {
			t.Fatal(err)
		}
		enc := ""
		if bytes.Contains(bytes.ToLower(meta), []byte(`"content-encoding": "gzip"`)) {
			enc = "gzip"
		}
		data, err := envelope.Decompress(bytes.NewReader(body), enc, 10<<20)
		if err != nil {
			t.Fatal(err)
		}
		env, err := envelope.Parse(data)
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range env.Events() {
			ev, err := event.Parse(it.Payload)
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, *ev)
		}
	}
	if len(out) < 5 {
		t.Fatalf("solo %d eventos en los fixtures", len(out))
	}
	return out
}

// asAPI es como sale un JSON crudo en la respuesta de la API.
func asAPI(t *testing.T, b []byte) string {
	t.Helper()
	out, err := json.Marshal(json.RawMessage(b))
	if err != nil {
		t.Fatalf("%s: %v", b, err)
	}
	return string(out)
}

// randomJSON da un JSON con claves repetidas, de varias longitudes y con
// caracteres especiales, y números escritos de muchas formas.
func randomJSON(r *rand.Rand, depth int) string {
	keys := []string{"", "a", "b", "ab", "ba", "zz", "é", "ñu", "<x>", "a&b", "dos palabras", "\\u00e9", "Z", "aa", "línea\\n"}
	nums := []string{"0", "-0", "1", "-1", "1.0", "1.50", "0.10", "1e2", "1E+2", "1.5e-3", "-2.50E1", "12345678901234567890", "1e-7", "0.000", "3.14159", "100e-2"}
	strs := []string{`""`, `"hola"`, `"\u00e9\u00f1"`, `"\ud83d\ude00"`, `"<script>&amp;"`, `"\/ \b\f\n\r\t"`, `"\u2028\u2029"`, `"\u001f"`, `"comillas \" y \\"`, `"ñandú"`}
	switch k := r.IntN(10); {
	case depth > 0 && k < 3:
		n := r.IntN(5)
		parts := make([]string, n)
		for i := range parts {
			parts[i] = `"` + keys[r.IntN(len(keys))] + `": ` + randomJSON(r, depth-1)
		}
		return "{" + strings.Join(parts, ", ") + "}"
	case depth > 0 && k < 5:
		n := r.IntN(4)
		parts := make([]string, n)
		for i := range parts {
			parts[i] = randomJSON(r, depth-1)
		}
		return "[ " + strings.Join(parts, " , ") + " ]"
	case k < 7:
		return nums[r.IntN(len(nums))]
	case k < 9:
		return strs[r.IntN(len(strs))]
	default:
		return []string{"true", "false", "null"}[r.IntN(3)]
	}
}

// La forma canónica es la misma que da Postgres al leer el JSON de jsonb.
func TestCanonicalJSONMatchesJSONB(t *testing.T) {
	onlyPostgres(t, "compara con el jsonb de Postgres")
	s := testStore(t)
	ctx := context.Background()
	samples := []string{
		`{}`, `[]`, `"texto"`, `12`, `null`,
		`{"b": 1, "a": 2, "a": 3, "aa": {"y": [1, 2.0, -0.0], "x": null}}`,
		`{"zz": {"b": "\u00e9 \u003c tab\t", "a": 1e2, "a": 2.50}, "a": [1, -0, 0.10, 1E-2, 12345678901234567890]}`,
		`{"escape": "comillas \" barra \\ \/ \b\f\n\r línea\u2028 \ud83d\ude00", "vacío": {}, "": null}`,
		`[1, "dos", {"tres": true, "dos": false}]`,
	}
	for _, ev := range fixtureEvents(t) {
		for _, raw := range []json.RawMessage{ev.Contexts, ev.Breadcrumbs, ev.DebugMeta} {
			if len(raw) > 0 {
				samples = append(samples, string(raw))
			}
		}
	}
	r := rand.New(rand.NewPCG(1, 2))
	for range 500 {
		samples = append(samples, randomJSON(r, 4))
	}
	for _, sample := range samples {
		var pg string
		if err := s.pool.QueryRow(ctx, `SELECT $1::text::jsonb::text`, sample).Scan(&pg); err != nil {
			t.Fatalf("%s: %v", sample, err)
		}
		got, err := canonicalJSON([]byte(sample))
		if err != nil {
			t.Fatalf("%s: %v", sample, err)
		}
		if a, b := asAPI(t, got), asAPI(t, []byte(pg)); a != b {
			t.Errorf("%s:\n canónico %s\n jsonb    %s", sample, a, b)
		}
		// Lo que ya está en forma canónica no cambia.
		again, err := canonicalJSON(got)
		if err != nil || !bytes.Equal(again, got) {
			t.Errorf("%s: segunda pasada %s, %v", got, again, err)
		}
	}
}

func TestNumericTextMatchesPostgres(t *testing.T) {
	onlyPostgres(t, "compara con los numeric de Postgres")
	s := testStore(t)
	ctx := context.Background()
	lits := []string{"0", "-0", "0.0", "-0.00", "7", "-7", "10", "1.0", "1.50", "0.10", "0.001", "1e0", "1e2", "1E+2", "1e-2",
		"1.5e-3", "-2.50E1", "2.5e1", "25e-1", "100e-2", "1.234e2", "1.234e5", "12345678901234567890", "0.000",
		"123.456e-10", "9e30", "-1e-30", "0e5", "0.0e-3", "5E-0"}
	for _, l := range lits {
		var pg string
		if err := s.pool.QueryRow(ctx, `SELECT $1::text::numeric::text`, l).Scan(&pg); err != nil {
			t.Fatal(err)
		}
		if got := numericText(l); got != pg {
			t.Errorf("%s = %s; Postgres da %s", l, got, pg)
		}
	}
}

func TestEncodeDecodeEvent(t *testing.T) {
	for _, ev := range fixtureEvents(t) {
		enc, err := EncodeEvent(&ev)
		if err != nil {
			t.Fatal(err)
		}
		got, err := DecodeEvent(enc.Data)
		if err != nil {
			t.Fatal(err)
		}
		a, _ := json.Marshal(got)
		raw, _ := json.Marshal(ev)
		if len(enc.Data) >= len(raw) {
			t.Errorf("%s: %d bytes comprimido, %d sin comprimir", ev.ID, len(enc.Data), len(raw))
		}
		// Igual que el evento salvo el orden de las claves de los JSON crudos.
		var x, y any
		_ = json.Unmarshal(a, &x)
		_ = json.Unmarshal(raw, &y)
		xb, _ := json.Marshal(x)
		yb, _ := json.Marshal(y)
		if !bytes.Equal(xb, yb) {
			t.Errorf("%s: al leerlo\n %s\nquiero\n %s", ev.ID, a, raw)
		}
	}
	if _, err := DecodeEvent([]byte("no es zstd")); err == nil {
		t.Error("leer algo que no es zstd no falla")
	}
}

func addTestIssue(t *testing.T, s *Store, project int64) int64 {
	t.Helper()
	var id int64
	if err := s.pool.QueryRow(context.Background(), `
		INSERT INTO issues (project_id, fingerprint, title, first_seen, last_seen, event_count)
		VALUES ($1, $2, 'Error', now(), now(), 1) RETURNING id`, project, randomHex(t, 8)).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func insertLegacy(t *testing.T, s *Store, project, issue int64, ev event.Event) int64 {
	t.Helper()
	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := s.pool.QueryRow(context.Background(), `
		INSERT INTO events (issue_id, project_id, event_id, occurred_at, release, environment, data)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''), NULLIF($6, ''), $7) RETURNING id`,
		issue, project, ev.ID, ev.Timestamp, ev.Release, ev.Environment, data).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// variedEvents son los eventos de los fixtures con usuarios, organizaciones
// y excepciones de todas las formas que distinguen las consultas.
func variedEvents(t *testing.T, n int) []event.Event {
	t.Helper()
	users := []*event.User{nil, {ID: "u1", Email: "a@b.c"}, {Email: "  Ana@Example.com "}, {Email: " \t", Username: "pepe"},
		{Username: "luis"}, {IPAddress: "{{auto}}"}, {IPAddress: "10.0.0.1"}, {Email: "ana@example.com"}, {ID: "luis"}, {}}
	base := fixtureEvents(t)
	out := make([]event.Event, 0, n)
	for i := range n {
		b, _ := json.Marshal(base[i%len(base)])
		var ev event.Event
		if err := json.Unmarshal(b, &ev); err != nil {
			t.Fatal(err)
		}
		ev.ID = fmt.Sprintf("%032x", i+1)
		ev.Timestamp = time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Minute)
		ev.User = users[i%len(users)]
		switch i % 4 {
		case 0:
			ev.Tags = map[string]string{"organization_id": fmt.Sprint("org-", i%3)}
		case 1:
			ev.Tags = map[string]string{"organization_id": ""}
		case 2:
			ev.Tags = nil
		}
		if len(ev.Exceptions) > 0 {
			last := &ev.Exceptions[len(ev.Exceptions)-1]
			switch i % 5 {
			case 1:
				last.Value = ""
			case 2:
				last.Type = ""
			case 3:
				last.Type, last.Value = "", ""
			}
		} else if i%3 == 0 {
			ev.Message = ""
		}
		out = append(out, ev)
	}
	return out
}

// Las columnas que salen del evento dan lo mismo que el SQL que las sacaba de
// events.data.
func TestEncodedColumnsMatchLegacySQL(t *testing.T) {
	onlyPostgres(t, "compara con el SQL de los eventos guardados en jsonb, que en SQLite no existen")
	s := testStore(t)
	ctx := context.Background()
	p, err := s.CreateProject(ctx, "p", "clave")
	if err != nil {
		t.Fatal(err)
	}
	issue := addTestIssue(t, s, p.ID)
	evs := variedEvents(t, 60)
	for _, ev := range evs {
		insertLegacy(t, s, p.ID, issue, ev)
	}
	rows, err := s.pool.Query(ctx, `
		SELECT COALESCE(`+legacyUserLabel+`, ''), COALESCE(`+legacyUserIdent+`, ''),
			COALESCE(`+legacyOrganizationID+`, ''), COALESCE(`+legacySummary+`, '')
		FROM events e ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	i := 0
	var label, ident, org, summary string
	err = db.ForEachRow(rows, []any{&label, &ident, &org, &summary}, func() error {
		enc, err := EncodeEvent(&evs[i])
		if err != nil {
			return err
		}
		got := [4]string{enc.UserLabel, enc.UserIdent, enc.OrganizationID, enc.Summary}
		if want := [4]string{label, ident, org, summary}; got != want {
			t.Errorf("evento %d: columnas %q; el SQL de antes da %q", i, got, want)
		}
		i++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if i != len(evs) {
		t.Fatalf("%d filas de %d", i, len(evs))
	}
}

type eventSnapshot map[int64]string

func snapshot(t *testing.T, s *Store) eventSnapshot {
	t.Helper()
	ctx := context.Background()
	rows, err := s.pool.Query(ctx, `SELECT id FROM events ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := db.CollectRows(rows, db.RowTo[int64])
	if err != nil {
		t.Fatal(err)
	}
	out := eventSnapshot{}
	for _, id := range ids {
		e, err := s.Event(ctx, strconv.FormatInt(id, 10))
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(e)
		out[id] = string(b)
	}
	return out
}

func (s *Store) legacyCount(t *testing.T) (legacy, converted int64) {
	t.Helper()
	if err := s.pool.QueryRow(context.Background(), `
		SELECT count(*) FILTER (WHERE data_zstd IS NULL), count(*) FILTER (WHERE data_zstd IS NOT NULL AND data IS NULL) FROM events`).
		Scan(&legacy, &converted); err != nil {
		t.Fatal(err)
	}
	return legacy, converted
}

// La conversión se corta a medias, se retoma con dos procesos a la vez y con
// una fila bloqueada por otro, y al final todo está convertido una sola vez y
// se lee igual que antes.
func TestConvertLegacyEvents(t *testing.T) {
	onlyPostgres(t, "la conversión de eventos antiguos no hace nada con SQLite")
	s := testStore(t)
	ctx := context.Background()
	p, err := s.CreateProject(ctx, "p", "clave")
	if err != nil {
		t.Fatal(err)
	}
	issue := addTestIssue(t, s, p.ID)
	const total = 400
	for _, ev := range variedEvents(t, total) {
		insertLegacy(t, s, p.ID, issue, ev)
	}
	// Uno que ya está en el formato nuevo no se toca.
	fresh := variedEvents(t, total+1)[total]
	enc, err := EncodeEvent(&fresh)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := InsertEvent(ctx, s.pool, NewEvent{IssueID: issue, ProjectID: p.ID, Event: &fresh, ReceivedAt: time.Now(), Encoded: enc}); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, s)

	// Cortado tras unos lotes, a veces dentro de una transacción.
	cut, cancel := context.WithCancel(ctx)
	o := ConvertOptions{Batch: 25, Pause: 5 * time.Millisecond}
	done := make(chan struct{})
	var first int64
	go func() {
		defer close(done)
		var err error
		first, err = s.ConvertLegacyEvents(cut, o)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("cortado: err = %v", err)
		}
	}()
	for {
		_, converted := s.legacyCount(t)
		if converted >= 60 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	legacy, converted := s.legacyCount(t)
	if legacy == 0 || converted-1 != first || legacy+converted != total+1 {
		t.Fatalf("tras cortar: %d sin convertir, %d convertidos y el proceso dice %d", legacy, converted, first)
	}
	if got := snapshot(t, s); !maps.Equal(got, before) {
		t.Fatal("con la mitad convertida, los eventos no se leen igual")
	}

	// Una fila bloqueada por otro proceso que no acaba.
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var locked int64
	if err := tx.QueryRow(ctx, `SELECT id FROM events WHERE data_zstd IS NULL ORDER BY id DESC LIMIT 1 FOR UPDATE`).Scan(&locked); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	counts := make([]int64, 2)
	for i := range counts {
		wg.Go(func() {
			n, err := s.ConvertLegacyEvents(ctx, ConvertOptions{Batch: 10 + i*7})
			if err != nil {
				t.Error(err)
			}
			counts[i] = n
		})
	}
	wg.Wait()
	if legacy, _ := s.legacyCount(t); legacy != 1 {
		t.Fatalf("con una fila bloqueada quedan %d sin convertir", legacy)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	last, err := s.ConvertLegacyEvents(ctx, ConvertOptions{Batch: 10})
	if err != nil || last != 1 {
		t.Fatalf("la fila que estaba bloqueada: %d, %v", last, err)
	}
	if first+counts[0]+counts[1]+last != total {
		t.Errorf("convertidos %d + %v + %d; quiero %d en total, ninguno dos veces", first, counts, last, total)
	}
	if counts[0] == 0 || counts[1] == 0 {
		t.Logf("un proceso no convirtió nada: %v", counts)
	}
	if legacy, converted := s.legacyCount(t); legacy != 0 || converted != total+1 {
		t.Fatalf("al acabar: %d sin convertir, %d convertidos", legacy, converted)
	}
	if got := snapshot(t, s); !maps.Equal(got, before) {
		t.Fatal("convertidos, los eventos no se leen igual")
	}
	if n, err := s.ConvertLegacyEvents(ctx, DefaultConvertOptions(nil)); err != nil || n != 0 {
		t.Fatalf("sin nada que convertir: %d, %v", n, err)
	}
	if n, err := s.ConvertLegacyEvents(ctx, ConvertOptions{Batch: 10, MaxBatchBytes: 1}); err != nil || n != 0 {
		t.Fatalf("otra vez: %d, %v", n, err)
	}
}

// Con un tope de bytes, cada lote lleva al menos un evento y la conversión acaba.
func TestConvertLegacyEventsByteLimit(t *testing.T) {
	onlyPostgres(t, "la conversión de eventos antiguos no hace nada con SQLite")
	s := testStore(t)
	ctx := context.Background()
	p, err := s.CreateProject(ctx, "p", "clave")
	if err != nil {
		t.Fatal(err)
	}
	issue := addTestIssue(t, s, p.ID)
	for _, ev := range variedEvents(t, 30) {
		insertLegacy(t, s, p.ID, issue, ev)
	}
	// Uno que no se lee como evento se guarda tal cual y no para la conversión.
	if _, err := s.pool.Exec(ctx, `INSERT INTO events (issue_id, project_id, event_id, occurred_at, data)
		VALUES ($1, $2, gen_random_uuid(), now(), '{"user": [1, 2], "timestamp": "no es fecha"}')`, issue, p.ID); err != nil {
		t.Fatal(err)
	}
	n, err := s.ConvertLegacyEvents(ctx, ConvertOptions{Batch: 100, MaxBatchBytes: 1})
	if err != nil || n != 31 {
		t.Fatalf("convertidos %d, %v", n, err)
	}
	if legacy, _ := s.legacyCount(t); legacy != 0 {
		t.Fatalf("quedan %d", legacy)
	}
}

// Un diccionario ya usado no puede cambiar: los eventos guardados con él
// dejarían de leerse. Uno nuevo va con otro id.
func TestEventDictionariesNeverChange(t *testing.T) {
	pinned := map[uint32]string{1: "3e5282381741e58979b9b1f6a74798f8172d3b90395400520a2072c3394d4277"}
	for id, d := range eventDicts {
		if got := fmt.Sprintf("%x", sha256.Sum256(d)); got != pinned[id] {
			t.Errorf("el diccionario %d ha cambiado: sha256 %s", id, got)
		}
	}
	if _, ok := eventDicts[currentDict]; !ok {
		t.Fatalf("no existe el diccionario %d", currentDict)
	}
}

// Cada evento lleva en la cabecera el id de su diccionario, y se leen tanto
// los de cualquier diccionario conocido como los comprimidos sin diccionario.
func TestEventDictionaryVersion(t *testing.T) {
	ev := fixtureEvents(t)[0]
	enc, err := EncodeEvent(&ev)
	if err != nil {
		t.Fatal(err)
	}
	var h zstd.Header
	if err := h.Decode(enc.Data); err != nil || h.DictionaryID != currentDict {
		t.Fatalf("diccionario en la cabecera %d, %v", h.DictionaryID, err)
	}
	b, _ := json.Marshal(ev)
	plain, _ := zstd.NewWriter(nil)
	got, err := DecodeEvent(plain.EncodeAll(b, nil))
	if err != nil || got.ID != ev.ID {
		t.Fatalf("sin diccionario: %v", err)
	}
	other, _ := zstd.NewWriter(nil, zstd.WithEncoderDictRaw(99, []byte("otro")))
	if _, err := DecodeEvent(other.EncodeAll(b, nil)); err == nil {
		t.Fatal("con un diccionario desconocido no falla")
	}
}

// Mientras aparecen eventos en jsonb (una réplica de la versión anterior que
// sigue guardando así), la conversión vuelve a pasar; acaba con la primera
// pasada que no encuentra ninguno.
func TestRunEventConversionRepeats(t *testing.T) {
	onlyPostgres(t, "la conversión de eventos antiguos no hace nada con SQLite")
	s := testStore(t)
	ctx := context.Background()
	p, err := s.CreateProject(ctx, "p", "clave")
	if err != nil {
		t.Fatal(err)
	}
	issue := addTestIssue(t, s, p.ID)
	evs := variedEvents(t, 3)
	insertLegacy(t, s, p.ID, issue, evs[0])

	var buf bytes.Buffer
	var mu sync.Mutex
	log := slog.New(slog.NewTextHandler(writerFunc(func(b []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return buf.Write(b)
	}), nil))
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.RunEventConversion(ctx, log, time.Second, 300*time.Millisecond)
	}()
	waitConverted := func(want int64) {
		t.Helper()
		for range 200 {
			if legacy, converted := s.legacyCount(t); legacy == 0 && converted == want {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("no se convierten los %d", want)
	}
	waitConverted(1)
	logged := func() bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(buf.String(), "eventos convertidos")
	}
	for !logged() {
		time.Sleep(5 * time.Millisecond)
	}
	// Llegan más cuando la primera pasada ya ha acabado.
	insertLegacy(t, s, p.ID, issue, evs[1])
	insertLegacy(t, s, p.ID, issue, evs[2])
	select {
	case <-done:
		t.Fatal("acabó tras una pasada que había convertido eventos")
	case <-time.After(100 * time.Millisecond):
	}
	waitConverted(3)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("no acaba cuando ya no queda nada")
	}
	mu.Lock()
	defer mu.Unlock()
	total := 0
	for _, m := range regexp.MustCompile(`eventos=(\d+)`).FindAllStringSubmatch(buf.String(), -1) {
		n, _ := strconv.Atoi(m[1])
		total += n
	}
	if total != 3 {
		t.Errorf("convertidos %d según el log, quiero 3:\n%s", total, buf.String())
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(b []byte) (int, error) { return f(b) }

// Una pasada que no convierte nada porque otra réplica tiene bloqueadas las
// filas no da la conversión por acabada: si esa réplica muere, se convierten.
func TestRunEventConversionWaitsForLockedRows(t *testing.T) {
	onlyPostgres(t, "la conversión de eventos antiguos no hace nada con SQLite")
	s := testStore(t)
	ctx := context.Background()
	p, err := s.CreateProject(ctx, "p", "clave")
	if err != nil {
		t.Fatal(err)
	}
	issue := addTestIssue(t, s, p.ID)
	insertLegacy(t, s, p.ID, issue, variedEvents(t, 1)[0])

	other, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Rollback(ctx) }()
	if _, err := other.Exec(ctx, `SELECT id FROM events WHERE data IS NOT NULL FOR UPDATE`); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.RunEventConversion(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Second, 100*time.Millisecond)
	}()
	select {
	case <-done:
		t.Fatal("acabó con un evento sin convertir que tenía bloqueado otra réplica")
	case <-time.After(400 * time.Millisecond):
	}
	if legacy, _ := s.legacyCount(t); legacy != 1 {
		t.Fatalf("sin convertir = %d, quiero 1 mientras está bloqueado", legacy)
	}

	// La otra réplica muere: suelta la fila sin convertirla.
	if err := other.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("no acabó tras convertir el evento que quedaba")
	}
	if legacy, converted := s.legacyCount(t); legacy != 0 || converted != 1 {
		t.Fatalf("sin convertir = %d, convertidos = %d; quiero 0 y 1", legacy, converted)
	}
}
