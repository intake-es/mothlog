package event

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestParseTimestamp(t *testing.T) {
	want := time.Date(2026, 11, 19, 12, 13, 49, 116000000, time.UTC)
	for _, in := range []string{
		`1795090429.116`,
		`"1795090429.116"`,
		`"2026-11-19T12:13:49.116Z"`,
		`"2026-11-19T13:13:49.116+01:00"`,
		`"2026-11-19T12:13:49.116"`,
	} {
		e, err := Parse([]byte(`{"timestamp":` + in + `}`))
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if !e.Timestamp.Equal(want) {
			t.Errorf("%s: %v", in, e.Timestamp)
		}
	}
	for _, in := range []string{`"ayer"`, `true`, `{}`} {
		if _, err := Parse([]byte(`{"timestamp":` + in + `}`)); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v", in, err)
		}
	}
}

func TestParseMessage(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`{"logentry":{"message":"m %s","formatted":"m x"},"message":"otro"}`, "m x"},
		{`{"logentry":{"message":"m %s","params":["x"]}}`, "m %s"},
		{`{"message":"suelto"}`, "suelto"},
		{`{"message":{"formatted":"con forma"}}`, "con forma"},
		{`{"logentry":{},"message":"suelto"}`, "suelto"},
		{`{}`, ""},
	} {
		e, err := Parse([]byte(tc.in))
		if err != nil {
			t.Fatal(err)
		}
		if e.Message != tc.want {
			t.Errorf("%s: %q, quiero %q", tc.in, e.Message, tc.want)
		}
	}
}

func TestParseExceptionShapes(t *testing.T) {
	for _, in := range []string{
		`{"exception":{"values":[{"type":"A"},{"type":"B","stacktrace":{"frames":[{"lineno":3}]}}]}}`,
		`{"exception":[{"type":"A"},{"type":"B","stacktrace":{"frames":[{"lineno":3}]}}]}`,
	} {
		e, err := Parse([]byte(in))
		if err != nil {
			t.Fatal(err)
		}
		if len(e.Exceptions) != 2 || e.Exceptions[1].Type != "B" || e.Exceptions[1].Frames[0].Line != 3 {
			t.Errorf("%s: %+v", in, e.Exceptions)
		}
	}
	e, err := Parse([]byte(`{"exception":{"type":"Solo","value":3}}`))
	if err != nil || len(e.Exceptions) != 1 || e.Exceptions[0].Value != "3" {
		t.Errorf("una sola: %+v, %v", e, err)
	}
	if _, err := Parse([]byte(`{"exception":"no"}`)); !errors.Is(err, ErrInvalid) {
		t.Errorf("err = %v", err)
	}
}

func TestParseLoose(t *testing.T) {
	e, err := Parse([]byte(`{
		"event_id":"9EC79C33-EC99-42AB-8353-589FCB2E04DC",
		"release":1.2,
		"tags":[["a","1"],["b",2]],
		"user":{"id":42,"email":"a@b.c"},
		"fingerprint":["{{ default }}",7],
		"breadcrumbs":null,
		"exception":{"values":[{"type":"E","stacktrace":{"frames":[{"lineno":"x","pre_context":["a",null]}]}}]}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if e.ID != "9ec79c33ec9942ab8353589fcb2e04dc" || e.Release != "1.2" || e.Level != "error" {
		t.Errorf("id %q, release %q, nivel %q", e.ID, e.Release, e.Level)
	}
	if e.Tags["a"] != "1" || e.Tags["b"] != "2" {
		t.Errorf("tags = %v", e.Tags)
	}
	if e.User == nil || e.User.ID != "42" || e.User.Email != "a@b.c" {
		t.Errorf("user = %+v", e.User)
	}
	if len(e.Fingerprint) != 2 || e.Fingerprint[1] != "7" {
		t.Errorf("fingerprint = %v", e.Fingerprint)
	}
	if e.Breadcrumbs != nil {
		t.Errorf("breadcrumbs = %s", e.Breadcrumbs)
	}
	f := e.Exceptions[0].Frames[0]
	if f.Line != 0 || len(f.PreContext) != 2 || f.PreContext[1] != "" {
		t.Errorf("frame = %+v", f)
	}
}

func TestParseUnexpectedTypes(t *testing.T) {
	e, err := Parse([]byte(`{
		"event_id":["x"],
		"platform":7,
		"level":{"a":1},
		"logger":true,
		"environment":3,
		"transaction":{},
		"server_name":[1],
		"sdk":"sentry.python",
		"logentry":"suelto",
		"message":"mensaje",
		"fingerprint":"no es lista",
		"request":{"method":1,"url":"https://a"},
		"user":{"email":["x"],"id":"u"},
		"exception":{"values":[{"type":1,"value":"v","mechanism":"m","stacktrace":{"frames":[3,{"filename":2,"in_app":"sí","lineno":4,"pre_context":[1,{}]}]}}]}
	}`))
	if err != nil {
		t.Fatalf("un tipo inesperado no debe tirar el evento: %v", err)
	}
	if e.Platform != "7" || e.Logger != "true" || e.Environment != "3" || e.Level != "error" {
		t.Errorf("platform %q, logger %q, environment %q, level %q", e.Platform, e.Logger, e.Environment, e.Level)
	}
	if e.ID != "" || e.Transaction != "" || e.ServerName != "" || e.SDK != (SDK{}) || e.Fingerprint != nil {
		t.Errorf("campos no escalares: %+v", e)
	}
	if e.Message != "mensaje" {
		t.Errorf("mensaje = %q", e.Message)
	}
	if e.Request == nil || e.Request.Method != "1" || e.User == nil || e.User.ID != "u" || e.User.Email != "" {
		t.Errorf("request %+v, user %+v", e.Request, e.User)
	}
	x := e.Exceptions[0]
	if x.Type != "1" || x.Mechanism != nil || len(x.Frames) != 1 {
		t.Fatalf("excepción = %+v", x)
	}
	if f := x.Frames[0]; f.Filename != "2" || f.InApp != nil || f.Line != 4 || len(f.PreContext) != 2 || f.PreContext[0] != "1" {
		t.Errorf("frame = %+v", f)
	}
}

func TestParseContextLinesNotList(t *testing.T) {
	e, err := Parse([]byte(`{"exception":{"values":[{"type":"E","stacktrace":{"frames":[
		{"filename":"a.py","lineno":3,"in_app":true,"pre_context":"una","context_line":"x = 1","post_context":{"a":1}}
	]}}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	frames := e.Exceptions[0].Frames
	if len(frames) != 1 {
		t.Fatalf("se ha perdido el fotograma: %+v", frames)
	}
	f := frames[0]
	if f.Filename != "a.py" || f.Line != 3 || f.InApp == nil || !*f.InApp || f.ContextLine != "x = 1" {
		t.Errorf("frame = %+v", f)
	}
	if f.PreContext != nil || f.PostContext != nil {
		t.Errorf("contexto = %q, %q", f.PreContext, f.PostContext)
	}
}

func TestParseInvalid(t *testing.T) {
	for _, in := range []string{``, `[]`, `{"level":`, `{"exception":{"values":"x"}}`, `null`} {
		e, err := Parse([]byte(in))
		if in == `null` {
			// null es JSON válido: queda un evento vacío, no un panic.
			if err != nil || e == nil {
				t.Errorf("null: %v", err)
			}
			continue
		}
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%q: err = %v", in, err)
		}
	}
}

func TestParseKeepsDebugMetaAndDist(t *testing.T) {
	dm := `{"images":[{"type":"sourcemap","code_file":"https://app.example.com/a.js","debug_id":"174ab4b5-75a1-56fb-be73-185871cd4267"}]}`
	e, err := Parse([]byte(`{"event_id":"a","dist":"d1","debug_meta":` + dm + `}`))
	if err != nil {
		t.Fatal(err)
	}
	if e.Dist != "d1" || string(e.DebugMeta) != dm {
		t.Fatalf("dist %q, debug_meta %s", e.Dist, e.DebugMeta)
	}
	out, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"dist":"d1"`) || !strings.Contains(string(out), `"debug_meta":`+dm) {
		t.Errorf("normalizado = %s", out)
	}

	for _, raw := range []string{`null`, `"x"`, `[1]`, `3`} {
		e, err := Parse([]byte(`{"event_id":"a","debug_meta":` + raw + `}`))
		if err != nil || e.DebugMeta != nil {
			t.Errorf("debug_meta %s: %s, %v", raw, e.DebugMeta, err)
		}
	}
}

func TestParseUserAgent(t *testing.T) {
	for _, body := range []string{
		`{"request":{"url":"https://a","headers":{"User-Agent":"ExampleBot/1.0"}}}`,
		`{"request":{"url":"https://a","headers":{"user-agent":"ExampleBot/1.0","accept":"*/*"}}}`,
		`{"request":{"url":"https://a","headers":[["Accept","*/*"],["USER-AGENT","ExampleBot/1.0"]]}}`,
	} {
		e, err := Parse([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		if e.Request == nil || e.Request.UserAgent != "ExampleBot/1.0" || e.Request.URL != "https://a" {
			t.Errorf("%s: request = %+v", body, e.Request)
		}
	}
	e, err := Parse([]byte(`{"request":{"headers":{"accept":"*/*"}}}`))
	if err != nil || e.Request != nil {
		t.Errorf("sin método, url ni user agent: %+v, %v", e.Request, err)
	}
}
