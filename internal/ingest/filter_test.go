package ingest

import (
	"bytes"
	"context"
	"maps"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/intake-es/mothlog/internal/event"
	"github.com/intake-es/mothlog/internal/store"
)

// webVariant es web-typeerror-1 con algo cambiado en el cuerpo, que va sin comprimir.
func webVariant(t *testing.T, replace ...string) fixture {
	t.Helper()
	f := loadFixture(t, "web-typeerror-1")
	for i := 0; i < len(replace); i += 2 {
		if !bytes.Contains(f.body, []byte(replace[i])) {
			t.Fatalf("web-typeerror-1 no contiene %q", replace[i])
		}
		f.body = bytes.ReplaceAll(f.body, []byte(replace[i]), []byte(replace[i+1]))
	}
	return f
}

// filterCases son los fixtures de error y variantes del de navegador, cada uno
// con algo que solo él tiene.
func filterCases(t *testing.T) map[string]fixture {
	t.Helper()
	cases := map[string]fixture{}
	for name := range errorFixtures {
		cases[name] = loadFixture(t, name)
	}
	cases["web-extension"] = webVariant(t, "https://app.example.com/chunk-Z554BZQ6.js", "chrome-extension://abcdefghijklmnop/content.js")
	cases["web-crawler"] = webVariant(t,
		"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) HeadlessChrome/156.0.8078.4 Safari/537.36",
		"Mozilla/5.0 (compatible; ExampleBot/2.1; +https://example.com/bot.html)")
	cases["web-localhost"] = webVariant(t, `"request":{"url":"https://app.example.com/"`, `"request":{"url":"http://localhost:4200/"`)
	cases["web-staging"] = webVariant(t, `"environment":"production"`, `"environment":"staging"`)
	cases["web-beta"] = webVariant(t, `fixture-release-1`, `2.0.0-beta.3`)
	return cases
}

func TestFiltersDropOnlyTheirOwn(t *testing.T) {
	for _, tc := range []struct {
		name    string
		set     func(*store.Filters)
		reason  string
		dropped []string
	}{
		{"sin filtros", func(*store.Filters) {}, "", nil},
		{"messages", func(f *store.Filters) { f.Messages = []string{`division by zero$`, `^pagos\.reembolso`} },
			store.DropMessage, []string{"api-excepcion-1", "api-log-error-1"}},
		{"exception_types", func(f *store.Filters) {
			f.ExceptionTypes = []string{"KeyError", "pydantic_core._pydantic_core.ValidationError"}
		}, store.DropExceptionType, []string{"api-log-exception-1", "api-sesion-crash-1", "api-validacion-1"}},
		{"browser_extensions", func(f *store.Filters) { f.BrowserExtensions = true },
			store.DropBrowserExtension, []string{"web-extension"}},
		{"web_crawlers", func(f *store.Filters) { f.WebCrawlers = true },
			store.DropWebCrawler, []string{"web-crawler"}},
		{"localhost", func(f *store.Filters) { f.Localhost = true },
			store.DropLocalhost, []string{"web-localhost"}},
		{"environments", func(f *store.Filters) { f.Environments = []string{"staging", "development"} },
			store.DropEnvironment, []string{"web-staging"}},
		{"releases", func(f *store.Filters) { f.Releases = []string{"*-beta.*"} },
			store.DropRelease, []string{"web-beta"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEnv(t, 1000)
			ctx := context.Background()
			for _, p := range fixtureProjects {
				f := store.DefaultFilters()
				tc.set(&f)
				if _, err := e.s.SetFilters(ctx, p.id, f); err != nil {
					t.Fatal(err)
				}
			}
			cases := filterCases(t)
			ids := map[string]string{}
			for _, name := range slices.Sorted(maps.Keys(cases)) {
				f := cases[name]
				ids[name] = newHexID()
				e.sendOK(t, f, f.withEventID(t, ids[name]))
			}

			var dropped []string
			for name, id := range ids {
				if e.count(t, `SELECT count(*) FROM events WHERE event_id = $1`, id) == 0 {
					dropped = append(dropped, name)
				}
			}
			sort.Strings(dropped)
			if !slices.Equal(dropped, tc.dropped) {
				t.Fatalf("descartados %v, quiero %v", dropped, tc.dropped)
			}
			if err := e.drops.Flush(ctx); err != nil {
				t.Fatal(err)
			}
			got := map[string]int64{}
			for _, p := range fixtureProjects {
				st, err := e.s.DroppedSince(ctx, p.id, 1, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				for r, n := range st.Reasons {
					if n > 0 {
						got[r] += n
					}
				}
			}
			want := map[string]int64{}
			if tc.reason != "" {
				want[tc.reason] = int64(len(tc.dropped))
			}
			if !maps.Equal(got, want) {
				t.Errorf("cuentas %v, quiero %v", got, want)
			}
		})
	}
}

// Un bucle de 10.000 eventos iguales guarda el tope, cuenta los 10.000 y avisa una vez.
func TestLoopIsCappedPerIssue(t *testing.T) {
	const n = 10_000
	e := newTestEnv(t, 1_000_000)
	now := time.Now().UTC()
	e.ing.now = func() time.Time { return now }
	f := loadFixture(t, "api-excepcion-1")

	bodies := make(chan []byte, n)
	for range n {
		bodies <- f.withEventID(t, newHexID())
	}
	close(bodies)
	var wg sync.WaitGroup
	var mu sync.Mutex
	codes := map[int]int{}
	for range 8 {
		wg.Go(func() {
			for b := range bodies {
				c := e.send(f.request(b)).Code
				mu.Lock()
				codes[c]++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if codes[http.StatusOK] != n {
		t.Fatalf("códigos %v", codes)
	}

	is := e.issues(t)
	if len(is) != 1 || is[0].count != n {
		t.Fatalf("issues = %+v", is)
	}
	stored := e.count(t, `SELECT count(*) FROM events WHERE issue_id = $1`, is[0].id)
	if stored != store.DefaultEventsPerIssuePerHour {
		t.Errorf("%d eventos guardados, quiero el tope, %d", stored, store.DefaultEventsPerIssuePerHour)
	}
	outcomes := map[Outcome]int{}
	for _, c := range e.notes.all() {
		outcomes[c.outcome]++
	}
	if outcomes[New] != 1 || outcomes[Regressed] != 0 || outcomes[Unchanged] != n-1 {
		t.Errorf("avisos %v; quiero un solo New", outcomes)
	}
	if err := e.drops.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	st, err := e.s.DroppedSince(context.Background(), 1, 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if st.Reasons[store.DropIssueLimit] != n-stored || st.Total != n-stored {
		t.Errorf("descartados %+v, quiero %d por el tope", st.Reasons, n-stored)
	}
	t.Logf("%d eventos: count %d, guardados %d, avisos %v, descartados por el tope %d",
		n, is[0].count, stored, outcomes, st.Reasons[store.DropIssueLimit])
}

func TestIssueLimitPerProjectAndHour(t *testing.T) {
	e := newTestEnv(t, 1000)
	ctx := context.Background()
	three := 3
	f := store.DefaultFilters()
	f.EventsPerIssuePerHour = &three
	if _, err := e.s.SetFilters(ctx, 1, f); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 21, 10, 59, 0, 0, time.UTC)
	e.ing.now = func() time.Time { return now }
	api := loadFixture(t, "api-excepcion-1")
	web := loadFixture(t, "web-typeerror-1")
	for range 5 {
		e.sendOK(t, api, api.withEventID(t, newHexID()))
		e.sendOK(t, web, web.withEventID(t, newHexID()))
	}
	stored := func(title string) int64 {
		return e.count(t, `SELECT count(*) FROM events e JOIN issues i ON i.id = e.issue_id WHERE i.title LIKE $1`, title+"%")
	}
	if a, w := stored("ZeroDivisionError"), stored("TypeError"); a != 3 || w != 5 {
		t.Fatalf("guardados: api %d (tope 3), web %d (tope por defecto)", a, w)
	}

	// En la hora siguiente vuelve a guardar, hasta el tope.
	now = now.Add(2 * time.Minute)
	var last string
	for range 3 {
		last = newHexID()
		e.sendOK(t, api, api.withEventID(t, last))
	}
	if a := stored("ZeroDivisionError"); a != 6 {
		t.Errorf("en la hora siguiente, %d guardados; quiero 6", a)
	}

	// Pasado el tope, el reenvío de un evento guardado no suma.
	e.sendOK(t, api, api.withEventID(t, last))
	for _, is := range e.issues(t) {
		want := int64(5)
		if strings.HasPrefix(is.title, "ZeroDivisionError") {
			want = 8
		}
		if is.count != want {
			t.Errorf("%s: count %d, quiero %d", is.title, is.count, want)
		}
	}

	// Una regresión se guarda aunque haya tope.
	if _, err := e.s.Pool().Exec(ctx, `UPDATE issues SET status = 'resolved' WHERE title LIKE 'ZeroDivisionError%'`); err != nil {
		t.Fatal(err)
	}
	reg := newHexID()
	e.sendOK(t, api, api.withEventID(t, reg))
	if e.count(t, `SELECT count(*) FROM events WHERE event_id = $1`, reg) != 1 {
		t.Error("la regresión no se guardó")
	}
	calls := e.notes.all()
	if last := calls[len(calls)-1]; last.outcome != Regressed || last.eventID != reg {
		t.Errorf("último aviso %+v", last)
	}
}

func TestSamplingNeverLosesANewIssue(t *testing.T) {
	for _, rate := range []float64{0, 0.5} {
		e := newTestEnv(t, 1000)
		ctx := context.Background()
		// Todo el mismo día: el primero de cada día también salta el muestreo.
		now := time.Now().UTC().Truncate(24 * time.Hour).Add(12 * time.Hour)
		e.ing.now = func() time.Time { return now }
		for _, p := range fixtureProjects {
			f := store.DefaultFilters()
			f.SampleRate = rate
			if _, err := e.s.SetFilters(ctx, p.id, f); err != nil {
				t.Fatal(err)
			}
		}
		// Con 0,5, un sorteo de cada dos sale fuera.
		var draws int
		e.ing.random = func() float64 {
			draws++
			if draws%2 == 1 {
				return 0.9
			}
			return 0.1
		}
		names := slices.Sorted(maps.Keys(errorFixtures))
		for _, name := range names {
			f := loadFixture(t, name)
			for range 4 {
				e.sendOK(t, f, f.withEventID(t, newHexID()))
			}
		}
		// Las dos tareas son el mismo issue.
		is := e.issues(t)
		if len(is) != len(names)-1 {
			t.Fatalf("muestreo %v: %d issues, quiero %d", rate, len(is), len(names)-1)
		}
		var sampled int64
		for _, i := range is {
			n := e.count(t, `SELECT count(*) FROM events WHERE issue_id = $1`, i.id)
			if i.count != 4 && i.count != 8 {
				t.Errorf("muestreo %v: %s count %d", rate, i.title, i.count)
			}
			sampled += i.count - n
		}
		if rate == 0 {
			if n := e.count(t, `SELECT count(*) FROM events`); n != int64(len(is)) {
				t.Errorf("muestreo 0: %d eventos guardados; quiero solo el primero de cada issue, %d", n, len(is))
			}
		}
		news := 0
		for _, c := range e.notes.all() {
			if c.outcome != New {
				continue
			}
			news++
			if e.count(t, `SELECT count(*) FROM events WHERE event_id = $1 AND issue_id = $2`, c.eventID, c.issueID) != 1 {
				t.Errorf("muestreo %v: el primer evento del issue %d no se guardó", rate, c.issueID)
			}
		}
		if news != len(is) {
			t.Errorf("muestreo %v: %d avisos de nuevo, quiero %d", rate, news, len(is))
		}
		if err := e.drops.Flush(ctx); err != nil {
			t.Fatal(err)
		}
		var counted int64
		for _, p := range fixtureProjects {
			st, err := e.s.DroppedSince(ctx, p.id, 1, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			counted += st.Reasons[store.DropSampled]
		}
		if counted != sampled || sampled == 0 {
			t.Errorf("muestreo %v: %d fuera de la muestra y %d contados", rate, sampled, counted)
		}
	}
}

func TestRateLimitPerProject(t *testing.T) {
	e := newTestEnv(t, 1000)
	ctx := context.Background()
	two := 2
	f := store.DefaultFilters()
	f.RateLimitPerMinute = &two
	if _, err := e.s.SetFilters(ctx, 1, f); err != nil {
		t.Fatal(err)
	}
	api := loadFixture(t, "api-excepcion-1")
	for range 2 {
		e.sendOK(t, api, api.withEventID(t, newHexID()))
	}
	if rec := e.send(api.request(api.withEventID(t, newHexID()))); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("código %d, quiero 429", rec.Code)
	}
	web := loadFixture(t, "web-typeerror-1")
	for range 3 {
		e.sendOK(t, web, web.withEventID(t, newHexID()))
	}

	// Lo filtrado no gasta el límite.
	f.Messages = []string{"division by zero"}
	if _, err := e.s.SetFilters(ctx, 1, f); err != nil {
		t.Fatal(err)
	}
	e.sendOK(t, api, api.withEventID(t, newHexID()))

	if err := e.drops.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	st, err := e.s.DroppedSince(ctx, 1, 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if st.Reasons[store.DropRateLimit] != 1 || st.Reasons[store.DropMessage] != 1 || st.Total != 2 {
		t.Errorf("descartados %+v", st.Reasons)
	}
}

func TestFilterRules(t *testing.T) {
	all := store.DefaultFilters()
	all.BrowserExtensions, all.WebCrawlers, all.Localhost = true, true, true
	fl := NewFilter(all)
	for _, tc := range []struct {
		name string
		ev   event.Event
		want string
	}{
		{"navegador", event.Event{Request: &event.Request{URL: "https://app.example.com/", UserAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/150.0 Safari/537.36"}}, ""},
		{"móvil", event.Event{Request: &event.Request{UserAgent: "Mozilla/5.0 (iPhone; CPU iPhone OS 19_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/19.0 Mobile/15E148 Safari/604.1"}}, ""},
		{"rastreador", event.Event{Request: &event.Request{UserAgent: "Mozilla/5.0 (compatible; SomeCrawler/1.0)"}}, store.DropWebCrawler},
		{"móvil que acaba en bot", event.Event{Request: &event.Request{UserAgent: "Mozilla/5.0 (Linux; Android 9; CUBOT X19 Build/PPR1.180610.011) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/150.0 Mobile Safari/537.36"}}, ""},
		{"otro móvil", event.Event{Request: &event.Request{UserAgent: "Mozilla/5.0 (Linux; Android 10; CUBOT_X30) AppleWebKit/537.36 Chrome/150.0 Mobile Safari/537.36"}}, ""},
		{"bot con versión", event.Event{Request: &event.Request{UserAgent: "Mozilla/5.0 (compatible; ExampleBot/2.1; +https://example.com/bot.html)"}}, store.DropWebCrawler},
		{"bot con guion", event.Event{Request: &event.Request{UserAgent: "ExampleBot-Image/1.0"}}, store.DropWebCrawler},
		{"bot con guion y sin versión", event.Event{Request: &event.Request{UserAgent: "Mozilla/5.0 (Linux; Android 14) Chrome/150.0 Mobile Safari/537.36 (compatible; AdsBot-Example-Mobile; +https://example.com/adsbot.html)"}}, store.DropWebCrawler},
		{"bot entre ;", event.Event{Request: &event.Request{UserAgent: "Mozilla/5.0 (compatible; SearchBot; +https://example.com)"}}, store.DropWebCrawler},
		{"araña", event.Event{Request: &event.Request{UserAgent: "examplespider/3.0"}}, store.DropWebCrawler},
		{"bot al final", event.Event{Request: &event.Request{UserAgent: "status-bot"}}, store.DropWebCrawler},
		{"disponibilidad", event.Event{Request: &event.Request{UserAgent: "Mozilla/5.0 (compatible; ExampleUptime/1.0)"}}, store.DropWebCrawler},
		{"extensión en el mensaje", event.Event{Message: "Error en moz-extension://1234/inject.js"}, store.DropBrowserExtension},
		{"extensión en la excepción", event.Event{Exceptions: []event.Exception{{Type: "Error", Value: "at safari-web-extension://x/y.js:1"}}}, store.DropBrowserExtension},
		{"extensión en abs_path", event.Event{Exceptions: []event.Exception{{Frames: []event.Frame{{AbsPath: "chrome-extension://abc/a.js"}}}}}, store.DropBrowserExtension},
		{"127.0.0.1", event.Event{Request: &event.Request{URL: "http://127.0.0.1:8000/x"}}, store.DropLocalhost},
		{"::1", event.Event{Request: &event.Request{URL: "http://[::1]:3000/"}}, store.DropLocalhost},
		{"*.localhost", event.Event{Request: &event.Request{URL: "https://app.localhost/"}}, store.DropLocalhost},
		{"ip del usuario", event.Event{User: &event.User{IPAddress: "127.0.0.1"}}, store.DropLocalhost},
		{"localhost en la ruta", event.Event{Request: &event.Request{URL: "https://app.example.com/localhost"}}, ""},
		{"ip pública", event.Event{User: &event.User{IPAddress: "203.0.113.5"}, Request: &event.Request{URL: "https://203.0.113.5/"}}, ""},
	} {
		if got := fl.Drop(&tc.ev); got != tc.want {
			t.Errorf("%s: %q, quiero %q", tc.name, got, tc.want)
		}
	}
	none := NewFilter(store.DefaultFilters())
	if got := none.Drop(&event.Event{Message: "chrome-extension://x", User: &event.User{IPAddress: "::1"}}); got != "" {
		t.Errorf("sin filtros activos: %q", got)
	}

	rel := store.DefaultFilters()
	rel.Releases = []string{"1.2.*", "exacto"}
	fl = NewFilter(rel)
	for release, want := range map[string]bool{"1.2.0": true, "1.2.10-rc": true, "1.20": false, "exacto": true, "exacto2": false, "": false} {
		if got := fl.Drop(&event.Event{Release: release}) == store.DropRelease; got != want {
			t.Errorf("release %q: descartado %v", release, got)
		}
	}
}

// Con muestreo a 0, un issue que sigue recibiendo eventos guarda el primero de
// cada día, así que la purga no lo borra ni el siguiente evento lo abre de nuevo.
func TestSamplingKeepsIssueAliveThroughRetention(t *testing.T) {
	e := newTestEnv(t, 1000)
	ctx := context.Background()
	f := store.DefaultFilters()
	f.SampleRate = 0
	if _, err := e.s.SetFilters(ctx, 1, f); err != nil {
		t.Fatal(err)
	}
	api := loadFixture(t, "api-excepcion-1")
	today := time.Now().UTC()
	now := today.AddDate(0, 0, -100)
	e.ing.now = func() time.Time { return now }
	for range 3 {
		e.sendOK(t, api, api.withEventID(t, newHexID()))
	}
	now = today
	var firstToday string
	for i := range 3 {
		id := newHexID()
		if i == 0 {
			firstToday = id
		}
		e.sendOK(t, api, api.withEventID(t, id))
	}
	if n := e.count(t, `SELECT count(*) FROM events WHERE event_id = $1`, firstToday); n != 1 {
		t.Fatal("el primer evento del día no se guardó con el muestreo a 0")
	}
	if n := e.count(t, `SELECT count(*) FROM events`); n != 2 {
		t.Errorf("%d eventos guardados; quiero el primero de cada uno de los dos días", n)
	}

	evs, iss, err := Purge(ctx, e.s.Pool(), today.AddDate(0, 0, -90))
	if err != nil || evs != 1 || iss != 0 {
		t.Fatalf("purga: %d eventos y %d issues borrados, %v; quiero 1 y 0", evs, iss, err)
	}
	e.sendOK(t, api, api.withEventID(t, newHexID()))
	is := e.issues(t)
	if len(is) != 1 || is[0].count != 7 {
		t.Fatalf("issues tras la purga = %+v; quiero uno con count 7", is)
	}
	news := 0
	for _, c := range e.notes.all() {
		if c.outcome != Unchanged {
			news++
		}
	}
	if news != 1 {
		t.Errorf("%d avisos; quiero solo el del error nuevo", news)
	}
}
