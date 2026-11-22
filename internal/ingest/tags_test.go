package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/intake-es/intake-errors/internal/event"
	"github.com/intake-es/intake-errors/internal/store"
	"github.com/intake-es/intake-errors/internal/store/storetest"
)

const (
	uaChromeMac  = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
	uaSafariMac  = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.1 Safari/605.1.15"
	uaSafariIOS  = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.1 Mobile/15E148 Safari/604.1"
	uaFirefoxWin = "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:132.0) Gecko/20100101 Firefox/132.0"
	uaEdgeWin    = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36 Edg/131.0.0.0"
	uaPixel      = "Mozilla/5.0 (Linux; Android 14; Pixel 8 Build/AP1A.240305.019) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Mobile Safari/537.36"
	uaReduced    = "Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Mobile Safari/537.36"
	uaHeadless   = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) HeadlessChrome/156.0.8078.4 Safari/537.36"
)

func TestParseUserAgent(t *testing.T) {
	for ua, want := range map[string]UserAgent{
		uaChromeMac:  {"Chrome", "Mac OS", "Mac"},
		uaSafariMac:  {"Safari", "Mac OS", "Mac"},
		uaSafariIOS:  {"Mobile Safari", "iOS", "iPhone"},
		uaFirefoxWin: {"Firefox", "Windows", ""},
		uaEdgeWin:    {"Edge", "Windows", ""},
		uaPixel:      {"Chrome", "Android", "Pixel 8"},
		uaReduced:    {"Chrome", "Android", ""},
		uaHeadless:   {"Chrome Headless", "Linux", ""},
		"Mozilla/5.0 (X11; CrOS x86_64 14541.0.0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36":                {"Chrome", "Chrome OS", ""},
		"Mozilla/5.0 (iPad; CPU OS 17_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/131.0 Mobile/15E148 Safari/604.1": {"Chrome", "iOS", "iPad"},
		"testclient":           {},
		"python-requests/2.32": {},
		"":                     {},
	} {
		if got := ParseUserAgent(ua); got != want {
			t.Errorf("ParseUserAgent(%q) = %+v; quiero %+v", ua, got, want)
		}
	}
}

func tagString(tags []Tag) string {
	var b []string
	for _, t := range tags {
		b = append(b, t.Key+"="+t.Value)
	}
	return strings.Join(b, " ")
}

func TestTags(t *testing.T) {
	web := storetest.Fixture(t, "web-typeerror-1")
	if got, want := tagString(Tags(&web)), "browser=Chrome Headless environment=production level=error os=Linux release=fixture-release-1"; got != want {
		t.Errorf("web-typeerror-1 = %s; quiero %s", got, want)
	}
	api := storetest.Fixture(t, "api-excepcion-1")
	if got, want := tagString(Tags(&api)), "component=api environment=production level=error release=fixture-release-1 runtime=CPython"; got != want {
		t.Errorf("api-excepcion-1 = %s; quiero %s", got, want)
	}

	// contexts gana al user agent; las tags no pisan lo derivado; claves raras fuera.
	ev := event.Event{
		Level:    "warning",
		Request:  &event.Request{UserAgent: uaChromeMac},
		Contexts: json.RawMessage(`{"browser":{"name":"Brave"},"device":{"family":"Desktop"}}`),
		Tags: map[string]string{
			"release": "de-tag", "user": "ana", "Is": "x", "con espacio": "x", "1numero": "x",
			"vacía": "", "org": "  7  ", "largo": strings.Repeat("ñ", 300),
		},
	}
	got := Tags(&ev)
	want := "browser=Brave device=Desktop largo=" + strings.Repeat("ñ", 200) + " level=warning org=7 os=Mac OS"
	if tagString(got) != want {
		t.Errorf("= %s", tagString(got))
	}

	many := event.Event{Tags: map[string]string{}}
	for i := range 80 {
		many.Tags[fmt.Sprintf("t%02d", i)] = "v"
	}
	if n := len(Tags(&many)); n != maxEventTags {
		t.Errorf("%d etiquetas; quiero %d", n, maxEventTags)
	}
}

func TestUserIdent(t *testing.T) {
	for _, tc := range []struct {
		u    *event.User
		want string
	}{
		{nil, ""},
		{&event.User{}, ""},
		{&event.User{ID: "42", Email: "ana@x.es"}, "id:42"},
		{&event.User{Email: " Ana@X.es ", IPAddress: "1.2.3.4"}, "email:ana@x.es"},
		{&event.User{Username: "ana"}, "username:ana"},
		{&event.User{IPAddress: "1.2.3.4"}, "ip:1.2.3.4"},
		{&event.User{IPAddress: "{{auto}}"}, ""},
	} {
		if got := store.UserIdent(tc.u); got != tc.want {
			t.Errorf("UserIdent(%+v) = %q; quiero %q", tc.u, got, tc.want)
		}
	}
}

// Un evento repetido no cuenta dos veces, un usuario repetido tampoco, y los
// eventos que pasan del tope por hora sí cuentan, como en event_count.
func TestTagCountsAndUsers(t *testing.T) {
	e := newTestEnv(t, 1000)
	ctx := context.Background()
	base := storetest.Fixture(t, "web-typeerror-1")
	send := func(id, ua string, user *event.User, lim Limits) Result {
		t.Helper()
		ev := base
		ev.ID = id
		ev.Request = &event.Request{URL: "https://app.example.com/", UserAgent: ua}
		ev.User = user
		res, err := e.ing.IngestWith(ctx, 2, &ev, lim)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	ana := &event.User{ID: "u-ana", Email: "ana@example.com"}
	luis := &event.User{Email: "Luis@Example.com"}
	ip := &event.User{IPAddress: "203.0.113.9"}

	first := newHexID()
	res := send(first, uaChromeMac, ana, DefaultLimits())
	send(newHexID(), uaChromeMac, ana, DefaultLimits())
	send(newHexID(), uaSafariMac, luis, DefaultLimits())
	send(newHexID(), uaSafariIOS, &event.User{Email: "luis@example.com"}, DefaultLimits())
	send(newHexID(), uaFirefoxWin, ip, DefaultLimits())
	send(newHexID(), uaFirefoxWin, nil, DefaultLimits())
	// Reenvío del primero: ni el issue ni las etiquetas lo cuentan.
	if r := send(first, uaEdgeWin, &event.User{ID: "otro"}, DefaultLimits()); !r.Duplicate {
		t.Fatalf("reenvío = %+v", r)
	}
	// Pasado el tope: no se guarda, pero cuenta.
	if r := send(newHexID(), uaSafariMac, &event.User{ID: "u-pedro"}, Limits{PerIssuePerHour: 1, SampleRate: 1}); r.Dropped != store.DropIssueLimit {
		t.Fatalf("con tope = %+v", r)
	}

	tags, err := e.s.IssueTags(ctx, res.IssueID)
	if err != nil {
		t.Fatal(err)
	}
	if tags.Events != 7 || tags.UserCount != 4 {
		t.Errorf("eventos %d, usuarios %d; quiero 7 y 4", tags.Events, tags.UserCount)
	}
	got := map[string]string{}
	for _, d := range tags.Tags {
		var parts []string
		var sum float64
		for _, v := range d.Values {
			parts = append(parts, fmt.Sprintf("%s=%d/%.1f%%", v.Value, v.Count, v.Percent))
			sum += v.Percent
		}
		if d.Distinct == len(d.Values) && (sum < 99.99 || sum > 100.01) {
			t.Errorf("%s suma %.2f%%", d.Key, sum)
		}
		got[d.Key] = fmt.Sprintf("%d: %s", d.Total, strings.Join(parts, " "))
	}
	want := map[string]string{
		"browser":     "7: Chrome=2/28.6% Firefox=2/28.6% Safari=2/28.6% Mobile Safari=1/14.3%",
		"os":          "7: Mac OS=4/57.1% Windows=2/28.6% iOS=1/14.3%",
		"device":      "5: Mac=4/80.0% iPhone=1/20.0%",
		"level":       "7: error=7/100.0%",
		"release":     "7: fixture-release-1=7/100.0%",
		"environment": "7: production=7/100.0%",
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %s; quiero %s", k, got[k], w)
		}
	}
	if len(tags.Tags) != len(want) {
		keys := []string{}
		for _, d := range tags.Tags {
			keys = append(keys, d.Key)
		}
		t.Errorf("claves = %v", keys)
	}

	// Ni el email ni la IP se guardan en claro.
	if n := e.count(t, `SELECT count(*) FROM issue_users WHERE position($1::bytea IN user_hash) > 0 OR length(user_hash) <> 16`, []byte("example.com")); n != 0 {
		t.Errorf("%d usuarios en claro", n)
	}
	if n := e.count(t, `SELECT user_count FROM issues WHERE id = $1`, res.IssueID); n != 4 {
		t.Errorf("user_count = %d", n)
	}
}

func TestTagsLastSeen(t *testing.T) {
	e := newTestEnv(t, 1000)
	ctx := context.Background()
	base := storetest.Fixture(t, "api-excepcion-1")
	t0 := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	var issue int64
	for i, at := range []time.Time{t0.Add(10 * time.Minute), t0, t0.Add(5 * time.Minute)} {
		ev := base
		ev.ID = newHexID()
		ev.Timestamp = at
		if i == 2 {
			ev.Release = "r2"
		}
		res, err := e.ing.Ingest(ctx, 1, &ev)
		if err != nil {
			t.Fatal(err)
		}
		issue = res.IssueID
	}
	tags, err := e.s.IssueTags(ctx, issue)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range tags.Tags {
		for _, v := range d.Values {
			want := t0.Add(10 * time.Minute)
			if v.Value == "r2" {
				want = t0.Add(5 * time.Minute)
			}
			if !v.LastSeen.Equal(want) {
				t.Errorf("%s=%s última vez %s; quiero %s", d.Key, v.Value, v.LastSeen, want)
			}
		}
	}
	keys := []string{}
	for _, d := range tags.Tags {
		keys = append(keys, d.Key)
	}
	if !slices.Equal(keys, []string{"component", "environment", "level", "release", "runtime"}) {
		t.Errorf("claves = %v", keys)
	}
}
