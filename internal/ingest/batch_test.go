package ingest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/intake-es/intake-errors/internal/db"
	"github.com/intake-es/intake-errors/internal/event"
	"github.com/intake-es/intake-errors/internal/store"
	"github.com/intake-es/intake-errors/internal/store/storetest"
)

type noted struct {
	eventID string
	res     Result
}

// notes apunta cada aviso con su resultado entero; failOn hace fallar el
// aviso de ese evento.
type notes struct {
	mu      sync.Mutex
	calls   []noted
	failOn  string
	panicOn string
}

func (n *notes) IssueChanged(_ context.Context, _ db.Tx, ev *event.Event, res Result) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if ev.ID == n.failOn {
		return errors.New("aviso roto")
	}
	if ev.ID == n.panicOn {
		panic("aviso que revienta")
	}
	n.calls = append(n.calls, noted{ev.ID, res})
	return nil
}

func (n *notes) reset() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.calls = nil
}

type batchEnv struct {
	s     *store.Store
	in    *Ingester
	notes *notes
}

func newBatchEnv(t *testing.T, now time.Time) *batchEnv {
	t.Helper()
	s := storetest.New(t)
	if _, err := s.Pool().Exec(context.Background(), storetest.ProjectWithIDSQL(s.Pool().Dialect()), 1, "api", "clave"); err != nil {
		t.Fatal(err)
	}
	n := &notes{}
	in := NewIngester(s.Pool(), nil, n, slog.New(slog.NewTextHandler(io.Discard, nil)))
	in.now = func() time.Time { return now }
	return &batchEnv{s: s, in: in, notes: n}
}

// together mete los eventos en un solo lote, en este orden, y devuelve el
// resultado de cada uno.
func (e *batchEnv) together(t *testing.T, evs []*event.Event, lim Limits) ([]Result, []error) {
	t.Helper()
	b := e.in.batch
	b.mu.Lock()
	b.running = true
	b.opts.MaxEvents = len(evs)
	b.mu.Unlock()
	res, errs := make([]Result, len(evs)), make([]error, len(evs))
	var wg sync.WaitGroup
	for i, ev := range evs {
		wg.Go(func() { res[i], errs[i] = e.in.IngestWith(context.Background(), 1, ev, lim) })
		for {
			b.mu.Lock()
			n := b.queued()
			b.mu.Unlock()
			if n == i+1 {
				break
			}
			time.Sleep(time.Millisecond)
		}
	}
	go b.run()
	wg.Wait()
	return res, errs
}

// oneByOne guarda los eventos como antes de los lotes: cada uno en su
// transacción, con save.
func (e *batchEnv) oneByOne(t *testing.T, evs []*event.Event, lim Limits) ([]Result, []error) {
	t.Helper()
	res, errs := make([]Result, len(evs)), make([]error, len(evs))
	for i, ev := range evs {
		it, err := e.in.prepare(context.Background(), 1, ev, lim)
		if err != nil {
			t.Fatal(err)
		}
		res[i], errs[i] = e.in.ingestOne(context.Background(), it)
	}
	return res, errs
}

// dump es todo lo que la ingesta deja en la base, con los issues por título.
func (e *batchEnv) dump(t *testing.T) string {
	t.Helper()
	var out []string
	for _, q := range []string{
		`SELECT 'issue', title, status, event_count, user_count, first_seen, last_seen, stored_hour, stored_in_hour,
			last_stored_at, last_regressed_at, resolved_in_release, resolved_at, first_release FROM issues`,
		`SELECT 'event', i.title, e.event_id, e.occurred_at, e.received_at, e.release, e.environment, e.user_ident
			FROM events e JOIN issues i ON i.id = e.issue_id`,
		`SELECT 'tag', i.title, t.key, t.value, t.count, t.last_seen FROM issue_tag_values t JOIN issues i ON i.id = t.issue_id`,
		// El hash de cada usuario lleva la clave de la instalación, distinta en cada base.
		`SELECT 'users', i.title, count(*) FROM issue_users u JOIN issues i ON i.id = u.issue_id GROUP BY i.title`,
		`SELECT 'stat', i.title, s.release, s.environment, s.event_count, s.first_seen, s.last_seen, s.regressed_at IS NOT NULL
			FROM issue_stats s JOIN issues i ON i.id = s.issue_id`,
		`SELECT 'release', version, first_seen FROM releases`,
		`SELECT 'history', i.title, h.from_status, h.to_status, h.release, h.source
			FROM issue_status_changes h JOIN issues i ON i.id = h.issue_id`,
	} {
		rows, err := e.s.Pool().Query(context.Background(), q)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			vals, err := rows.Values()
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, fmt.Sprint(vals...))
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
	}
	slices.Sort(out)
	return strings.Join(out, "\n")
}

func (e *batchEnv) titles(t *testing.T) map[int64]string {
	t.Helper()
	rows, err := e.s.Pool().Query(context.Background(), `SELECT id, title FROM issues`)
	if err != nil {
		t.Fatal(err)
	}
	m := map[int64]string{}
	var id int64
	var title string
	if err := db.ForEachRow(rows, []any{&id, &title}, func() error { m[id] = title; return nil }); err != nil {
		t.Fatal(err)
	}
	return m
}

// outcomes da cada resultado como texto, con el título del issue en vez del id.
func (e *batchEnv) outcomes(t *testing.T, ids []string, res []Result) []string {
	t.Helper()
	titles := e.titles(t)
	out := make([]string, len(res))
	for i, r := range res {
		out[i] = fmt.Sprintf("%s %s %s dup=%v dropped=%q resolved=%v", ids[i], titles[r.IssueID], r.Outcome, r.Duplicate, r.Dropped, r.ResolvedAt != nil)
	}
	return out
}

func (e *batchEnv) noted(t *testing.T) []string {
	t.Helper()
	titles := e.titles(t)
	var out []string
	for _, c := range e.notes.calls {
		out = append(out, fmt.Sprintf("%s %s %s dropped=%q", c.eventID, titles[c.res.IssueID], c.res.Outcome, c.res.Dropped))
	}
	return out
}

func diff(t *testing.T, what, got, want string) {
	t.Helper()
	if got == want {
		return
	}
	g, w := strings.Split(got, "\n"), strings.Split(want, "\n")
	for i := range max(len(g), len(w)) {
		var a, b string
		if i < len(g) {
			a = g[i]
		}
		if i < len(w) {
			b = w[i]
		}
		if a != b {
			t.Errorf("%s: línea %d\n lote:       %s\n uno a uno:  %s", what, i, a, b)
			return
		}
	}
}

// batchScenario es lo mismo para los dos: un issue resuelto que vuelve, uno
// nuevo con más eventos que el tope por hora, una cola de errores, usuarios,
// etiquetas, una release nueva y repetidos dentro y fuera del lote.
type batchScenario struct {
	setup, batch []*event.Event
}

func newBatchScenario(now time.Time) batchScenario {
	at := func(s int) time.Time { return now.Add(-time.Duration(s) * time.Second) }
	id := func(n int) string { return fmt.Sprintf("%032x", n) }
	ev := func(n int, msg, release, env string, ts int, user string, tags map[string]string) *event.Event {
		e := &event.Event{ID: id(n), Message: msg, Level: "error", Release: release, Environment: env,
			Timestamp: at(ts), Tags: tags, Fingerprint: []string{msg}}
		if user != "" {
			e.User = &event.User{ID: user}
		}
		return e
	}
	sc := batchScenario{setup: []*event.Event{
		ev(1, "vuelve", "1.0", "production", 100, "u1", nil),
		ev(2, "repetido", "1.0", "production", 100, "", nil),
	}}
	n := 100
	for i := range 10 {
		n++
		sc.batch = append(sc.batch, ev(n, "vuelve", "1.1", []string{"production", "staging"}[i%2], i*3, fmt.Sprintf("u%d", i%3),
			map[string]string{"modo": []string{"x", "y"}[i%2]}))
	}
	for i := range 15 {
		n++
		sc.batch = append(sc.batch, ev(n, "nuevo", "1.1", "production", 50-i, fmt.Sprintf("u%d", i%4),
			map[string]string{"modo": "x", "zona": fmt.Sprint(i % 3)}))
		if i == 2 {
			// El mismo evento dos veces dentro del lote, cuando aún se guarda.
			sc.batch = append(sc.batch, ev(n, "nuevo", "1.1", "production", 50-i, fmt.Sprintf("u%d", i%4),
				map[string]string{"modo": "x", "zona": fmt.Sprint(i % 3)}))
		}
	}
	for i := range 8 {
		n++
		sc.batch = append(sc.batch, ev(n, fmt.Sprintf("cola %d", i), "2.0", "production", i, "", nil))
	}
	// Ya guardado antes del lote.
	sc.batch = append(sc.batch, ev(2, "repetido", "1.0", "production", 100, "", nil))
	// Entre medias, otro del issue que vuelve, para que el orden cuente.
	sc.batch = slices.Insert(sc.batch, 12, ev(999, "vuelve", "2.0", "production", 1, "u9", nil))
	return sc
}

func eventIDs(evs []*event.Event) []string {
	ids := make([]string, len(evs))
	for i, ev := range evs {
		ids[i] = ev.ID
	}
	return ids
}

func copyEvents(evs []*event.Event) []*event.Event {
	out := make([]*event.Event, len(evs))
	for i, ev := range evs {
		c := *ev
		out[i] = &c
	}
	return out
}

// prepareScenario guarda los eventos previos y resuelve el issue que vuelve.
func (e *batchEnv) prepareScenario(t *testing.T, sc batchScenario) {
	t.Helper()
	for _, ev := range copyEvents(sc.setup) {
		if _, err := e.in.Ingest(context.Background(), 1, ev); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.s.Pool().Exec(context.Background(),
		`UPDATE issues SET status = 'resolved', resolved_at = `+e.s.Pool().Dialect().SubSeconds("now()", "$1")+` WHERE title = 'vuelve'`,
		3600); err != nil {
		t.Fatal(err)
	}
	e.notes.reset()
}

func TestBatchMatchesOneByOne(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	sc := newBatchScenario(now)
	lim := Limits{PerIssuePerHour: 5, SampleRate: 1}

	batch := newBatchEnv(t, now)
	batch.prepareScenario(t, sc)
	bres, berrs := batch.together(t, copyEvents(sc.batch), lim)

	one := newBatchEnv(t, now)
	one.prepareScenario(t, sc)
	ores, oerrs := one.oneByOne(t, copyEvents(sc.batch), lim)

	for i := range sc.batch {
		if berrs[i] != nil || oerrs[i] != nil {
			t.Fatalf("evento %d: lote %v, uno a uno %v", i, berrs[i], oerrs[i])
		}
	}
	ids := eventIDs(sc.batch)
	diff(t, "resultados", strings.Join(batch.outcomes(t, ids, bres), "\n"), strings.Join(one.outcomes(t, ids, ores), "\n"))
	diff(t, "base", batch.dump(t), one.dump(t))
	diff(t, "avisos", strings.Join(batch.noted(t), "\n"), strings.Join(one.noted(t), "\n"))

	// Lo que se espera del escenario, para que no cuadre por estar mal los dos.
	var news, regressions, dups, dropped int
	for _, r := range bres {
		switch {
		case r.Duplicate:
			dups++
		case r.Outcome == New:
			news++
		case r.Outcome == Regressed:
			regressions++
		}
		if r.Dropped != "" {
			dropped++
		}
	}
	if news != 9 || regressions != 1 || dups != 2 || dropped != 17 {
		t.Errorf("nuevos %d, regresiones %d, repetidos %d, descartados %d; quiero 9, 1, 2 y 17", news, regressions, dups, dropped)
	}
	var count, users int64
	if err := batch.s.Pool().QueryRow(context.Background(),
		`SELECT event_count, user_count FROM issues WHERE title = 'nuevo'`).Scan(&count, &users); err != nil || count != 15 || users != 4 {
		t.Errorf("issue nuevo: %d eventos y %d usuarios, %v; quiero 15 y 4", count, users, err)
	}
}

func TestBatchBadEventDoesNotSinkOthers(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	for _, tc := range []struct {
		name string
		bad  func(t *testing.T, evs []*event.Event, e *batchEnv) string
	}{
		{"aviso que falla", func(_ *testing.T, evs []*event.Event, e *batchEnv) string {
			e.notes.failOn = evs[5].ID
			return evs[5].ID
		}},
		{"etiqueta que Postgres no acepta", func(_ *testing.T, evs []*event.Event, _ *batchEnv) string {
			evs[5].Tags = map[string]string{"modo": "a\x00b"}
			return evs[5].ID
		}},
		// Como la etiqueta con NUL, pero con los dos motores: la base rechaza
		// a mitad del lote lo que guarda un evento.
		{"etiqueta que la base rechaza", func(t *testing.T, evs []*event.Event, e *batchEnv) string {
			evs[5].Tags = map[string]string{"modo": "rechazado"}
			if _, err := e.s.Pool().Exec(context.Background(), db.ByEngine(e.s.Pool().Dialect(), `
				CREATE FUNCTION rechaza() RETURNS trigger LANGUAGE plpgsql AS $$
				BEGIN IF NEW.value = 'rechazado' THEN RAISE EXCEPTION 'rechazado'; END IF; RETURN NEW; END $$;
				CREATE TRIGGER rechaza BEFORE INSERT ON issue_tag_values FOR EACH ROW EXECUTE FUNCTION rechaza();`,
				`CREATE TRIGGER rechaza BEFORE INSERT ON issue_tag_values WHEN NEW.value = 'rechazado'
				BEGIN SELECT RAISE(ABORT, 'rechazado'); END`)); err != nil {
				t.Fatal(err)
			}
			return evs[5].ID
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if strings.Contains(tc.name, "Postgres") {
				storetest.OnlyPostgres(t, "SQLite sí guarda un texto con el carácter NUL, así que ese evento no falla")
			}
			sc := newBatchScenario(now)
			lim := Limits{PerIssuePerHour: 5, SampleRate: 1}

			batch := newBatchEnv(t, now)
			batch.prepareScenario(t, sc)
			bevs := copyEvents(sc.batch)
			bad := tc.bad(t, bevs, batch)
			_, berrs := batch.together(t, bevs, lim)

			one := newBatchEnv(t, now)
			one.prepareScenario(t, sc)
			oevs := copyEvents(sc.batch)
			tc.bad(t, oevs, one)
			_, oerrs := one.oneByOne(t, oevs, lim)

			for i, ev := range bevs {
				if (berrs[i] != nil) != (ev.ID == bad) || (oerrs[i] != nil) != (ev.ID == bad) {
					t.Errorf("evento %d (%s): lote %v, uno a uno %v", i, ev.ID, berrs[i], oerrs[i])
				}
			}
			diff(t, "base", batch.dump(t), one.dump(t))
			var n int
			if err := batch.s.Pool().QueryRow(context.Background(),
				`SELECT count(*) FROM events WHERE event_id = $1`, bad).Scan(&n); err != nil || n != 0 {
				t.Errorf("el evento malo se ha guardado: %d, %v", n, err)
			}
		})
	}
}

// Una regresión y un error nuevo, con muchos eventos a la vez, avisan una
// vez cada uno, mandados por el handler.
func TestConcurrentEnvelopesMatchOneByOne(t *testing.T) {
	names := []string{"api-excepcion-1", "api-validacion-1", "api-sesion-crash-1", "web-typeerror-1", "api-tarea-a-1"}
	const perFixture = 12
	type send struct {
		f    fixture
		body []byte
	}
	var sends []send
	for _, name := range names {
		f := loadFixture(t, name)
		for range perFixture {
			sends = append(sends, send{f, f.withEventID(t, newHexID())})
		}
	}
	setup := loadFixture(t, "api-tarea-a-1")
	run := func(concurrent bool) (*testEnv, string) {
		e := newTestEnv(t, 100000)
		// Un issue resuelto que vuelve en mitad de los envíos.
		e.sendOK(t, setup, setup.body)
		if _, err := e.s.Pool().Exec(context.Background(), `UPDATE issues SET status = 'resolved', resolved_at = now()`); err != nil {
			t.Fatal(err)
		}
		e.notes.mu.Lock()
		e.notes.calls = nil
		e.notes.mu.Unlock()
		if concurrent {
			var wg sync.WaitGroup
			codes := make([]int, len(sends))
			for i, s := range sends {
				wg.Go(func() { codes[i] = e.send(s.f.request(s.body)).Code })
			}
			wg.Wait()
			for i, c := range codes {
				if c != http.StatusOK {
					t.Fatalf("envío %d: código %d", i, c)
				}
			}
		} else {
			for _, s := range sends {
				e.sendOK(t, s.f, s.body)
			}
		}
		var out []string
		for _, q := range []string{
			`SELECT 'issue', title, status, event_count, user_count, last_regressed_at IS NOT NULL FROM issues`,
			`SELECT 'events', i.title, count(*) FROM events e JOIN issues i ON i.id = e.issue_id GROUP BY i.title`,
			`SELECT 'tag', i.title, t.key, t.value, t.count FROM issue_tag_values t JOIN issues i ON i.id = t.issue_id`,
			`SELECT 'users', i.title, count(*) FROM issue_users u JOIN issues i ON i.id = u.issue_id GROUP BY i.title`,
			`SELECT 'stat', i.title, s.release, s.environment, s.event_count FROM issue_stats s JOIN issues i ON i.id = s.issue_id`,
			`SELECT 'history', i.title, h.from_status, h.to_status FROM issue_status_changes h JOIN issues i ON i.id = h.issue_id`,
		} {
			rows, err := e.s.Pool().Query(context.Background(), q)
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
				vals, _ := rows.Values()
				out = append(out, fmt.Sprint(vals...))
			}
			rows.Close()
		}
		news, regressions := map[int64]int{}, map[int64]int{}
		for _, c := range e.notes.all() {
			switch c.outcome {
			case New:
				news[c.issueID]++
			case Regressed:
				regressions[c.issueID]++
			}
		}
		out = append(out, fmt.Sprintf("avisos: %d issues nuevos, %d regresiones, %d avisos", len(news), len(regressions), len(e.notes.all())))
		for id, n := range news {
			if n != 1 {
				t.Errorf("issue %d: %d avisos de nuevo", id, n)
			}
		}
		for id, n := range regressions {
			if n != 1 {
				t.Errorf("issue %d: %d avisos de regresión", id, n)
			}
		}
		slices.Sort(out)
		return e, strings.Join(out, "\n")
	}
	_, seq := run(false)
	_, conc := run(true)
	diff(t, "envíos a la vez", conc, seq)
	if !strings.Contains(seq, "avisos: 4 issues nuevos, 1 regresiones, 60 avisos") {
		t.Errorf("avisos de uno en uno:\n%s", seq)
	}
}

func TestBatchWaitsOnlyForAnnouncedEnvelopes(t *testing.T) {
	e := newBatchEnv(t, time.Now().UTC())
	e.in.SetBatching(BatchOptions{MaxEvents: 100, MaxWait: 300 * time.Millisecond})
	ingest := func() time.Duration {
		start := time.Now()
		ev := &event.Event{Message: "solo", Level: "error"}
		if _, err := e.in.IngestWith(context.Background(), 1, ev, DefaultLimits()); err != nil {
			t.Fatal(err)
		}
		return time.Since(start)
	}
	if d := ingest(); d >= 300*time.Millisecond {
		t.Errorf("sin nadie más, el lote ha esperado %s", d)
	}

	// Con otro envío a medias, espera hasta MaxWait por él.
	arrived := e.in.batch.announce()
	if d := ingest(); d < 300*time.Millisecond || d > 3*time.Second {
		t.Errorf("con otro envío a medias ha tardado %s; quiero unos 300 ms", d)
	}

	// Y deja de esperar en cuanto ese envío llega o se retira.
	go func() {
		time.Sleep(20 * time.Millisecond)
		arrived()
	}()
	arrived2 := e.in.batch.announce()
	go func() {
		time.Sleep(20 * time.Millisecond)
		arrived2()
	}()
	if d := ingest(); d >= 300*time.Millisecond {
		t.Errorf("el envío esperado se retiró y aun así ha tardado %s", d)
	}
}

// Si otro guarda a la vez un evento del lote, la escritura lo nota y el lote
// se repite evento a evento: aquí, que el error del callback llega tal cual.
func TestBatchWriteConflictIsDetected(t *testing.T) {
	e := newBatchEnv(t, time.Now().UTC())
	err := db.BeginFunc(context.Background(), e.s.Pool(), func(tx db.Tx) error {
		b := &db.Batch{}
		b.Queue(`SELECT 1`).Exec(func(db.Result) error { return errConflict })
		b.Queue(`SELECT 2`)
		return tx.SendBatch(context.Background(), b)
	})
	if !errors.Is(err, errConflict) {
		t.Fatalf("err = %v, quiero errConflict", err)
	}
}

// Un pánico al guardar un lote falla sus eventos sin tumbar el proceso, y el
// escritor sigue guardando los siguientes.
func TestBatchPanicFailsItsEventsAndKeepsWriting(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	e := newBatchEnv(t, now)
	ctx := context.Background()
	ev := func(n int, msg string) *event.Event {
		return &event.Event{ID: fmt.Sprintf("%032x", n), Message: msg, Level: "error", Timestamp: now, Fingerprint: []string{msg}}
	}
	boom := ev(1, "revienta")
	e.notes.panicOn = boom.ID
	if _, err := e.in.IngestWith(ctx, 1, boom, DefaultLimits()); err == nil {
		t.Fatal("el evento cuyo aviso revienta se dio por guardado")
	}

	done := make(chan error, 1)
	go func() {
		_, err := e.in.IngestWith(ctx, 1, ev(2, "sigue"), DefaultLimits())
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("el siguiente evento falla: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("el escritor de lotes no sigue tras el pánico")
	}
	var n int
	if err := e.s.Pool().QueryRow(ctx, `SELECT count(*) FROM events`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("eventos guardados = %d, %v; quiero solo el segundo", n, err)
	}
}
