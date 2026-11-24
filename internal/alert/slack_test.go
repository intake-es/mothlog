package alert

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/intake-es/mothlog/internal/event"
	"github.com/intake-es/mothlog/internal/lang"
)

var update = flag.Bool("update", false, "reescribe los ficheros dorados de testdata/")

func yes() *bool { b := true; return &b }
func no() *bool  { b := false; return &b }

// flatten junta los textos y las URL del mensaje para buscar en él.
func flatten(p slackPayload) string {
	var b strings.Builder
	b.WriteString(p.Text + "\n")
	for _, bl := range p.Blocks {
		if bl.Text != nil {
			b.WriteString(bl.Text.Text + "\n")
		}
		for _, f := range bl.Fields {
			b.WriteString(f.Text + "\n")
		}
		for _, el := range bl.Elements {
			raw, _ := json.Marshal(el)
			var x struct {
				Text any
				URL  string
			}
			_ = json.Unmarshal(raw, &x)
			switch t := x.Text.(type) {
			case string:
				b.WriteString(t + "\n")
			case map[string]any:
				b.WriteString(t["text"].(string) + " " + x.URL + "\n")
			}
		}
	}
	return b.String()
}

func golden(t *testing.T, name string, msg slackPayload) {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(msg); err != nil {
		t.Fatal(err)
	}
	got := buf.Bytes()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s no coincide (go test -run %s -update):\n%s", path, t.Name(), got)
	}
}

var at = time.Date(2026, 11, 18, 10, 30, 0, 0, time.UTC)

func TestSlackGoldenNewWithFrame(t *testing.T) {
	p := &pending{
		issueID: 42, kind: "new", project: "frontend", environment: "production",
		release: "3f9c2a1b7d4e5f60718293a4b5c6d7e8f9a0b1c2", eventAt: at, count: 1,
		title: "TypeError: Cannot read properties of undefined (reading 'lineas')",
	}
	ev := &event.Event{
		Release: p.release, Environment: p.environment,
		Exceptions: []event.Exception{{
			Type: "TypeError", Value: "Cannot read properties of undefined (reading 'lineas')",
			Frames: []event.Frame{
				{Filename: "src/app/pedidos/pedidos.component.ts", Function: "cargarPedidos", Line: 31, InApp: yes(),
					ContextLine: "    this.total = this.calcular(pedidos);"},
				{Filename: "src/app/pedidos/total.ts", Function: "leerTotal", Line: 12, InApp: yes(),
					ContextLine: "  return pedido.lineas.reduce((s, l) => s + l.importe, 0);",
					Minified:    &event.Position{Filename: "https://app.example.com/main-AB12CD.js", Line: 1, Column: 48211}},
				{Filename: "node_modules/zone.js/fesm2015/zone.js", Function: "invokeTask", Line: 400, InApp: no()},
			},
		}},
		Tags: map[string]string{"organization_id": "8a1c4e2f-0b3d-4c5e-9f6a-7b8c9d0e1f2a", "organization_name": "Clínica Sonrisa"},
		User: &event.User{ID: "u_1834", Email: "ana@example.com", IPAddress: "203.0.113.9"},
	}
	for _, l := range []lang.Lang{lang.EN, lang.ES} {
		p.l = l
		golden(t, goldenName("slack-nuevo", l), slackMessage(p, ev, "https://errors.test/issues/42"))
	}
}

// goldenName es el fichero de referencia de un idioma: el de español lleva .es.
func goldenName(base string, l lang.Lang) string {
	if l == lang.ES {
		return base + ".es.json"
	}
	return base + ".json"
}

func TestSlackGoldenRegressionWithLinear(t *testing.T) {
	resolved := time.Date(2026, 11, 12, 16, 5, 0, 0, time.UTC)
	p := &pending{
		issueID: 7, kind: "regressed", project: "backend", environment: "production",
		release: "backend-2026.11.18", eventAt: at, count: 128, resolvedAt: &resolved,
		title: "ZeroDivisionError: division by zero", culprit: "backend in division",
		linear: "lin-1", linearIdentifier: "ERR-123", linearURL: "https://linear.app/ejemplo/issue/ERR-123",
	}
	ev := &event.Event{
		Release: p.release, Environment: p.environment,
		Exceptions: []event.Exception{{
			Type: "ZeroDivisionError", Value: "division by zero",
			Frames: []event.Frame{
				{Filename: "app/core/http/middlewares.py", Function: "__call__", Line: 41, InApp: yes(),
					ContextLine: "            await self.app(scope, receive, _send)"},
				{Filename: "fastapi/routing.py", Function: "run_endpoint_function", Line: 328, InApp: no()},
				{Filename: "app/informes/medias.py", Module: "app.informes.medias", Function: "division", Line: 25, InApp: yes(),
					ContextLine: `    return {"media": 10 / total["mensajes"]}`},
			},
		}},
		Tags:    map[string]string{"organization_id": "8a1c4e2f-0b3d-4c5e-9f6a-7b8c9d0e1f2a"},
		Request: &event.Request{Method: "get", URL: "https://api.example.com/v1/informes/medias?token=secreto&x=1#a"},
	}
	for _, l := range []lang.Lang{lang.EN, lang.ES} {
		p.l = l
		golden(t, goldenName("slack-regresion", l), slackMessage(p, ev, "https://errors.test/issues/7"))
	}
}

func TestSlackWithoutOptionalData(t *testing.T) {
	p := &pending{issueID: 3, kind: "new", project: "backend", eventAt: at, count: 1, title: "fallo sin más"}
	for name, ev := range map[string]*event.Event{
		"sin evento":            nil,
		"sin fotograma propio":  {Message: "fallo sin más", Exceptions: []event.Exception{{Type: "", Frames: []event.Frame{{Filename: "lib.py", InApp: no()}}}}},
		"usuario y tags vacíos": {Message: "fallo sin más", User: &event.User{Email: "a@b.c"}, Tags: map[string]string{"organization_id": " "}},
	} {
		t.Run(name, func(t *testing.T) {
			msg := slackMessage(p, ev, "https://errors.test/issues/3")
			var types []string
			for _, b := range msg.Blocks {
				types = append(types, b.Type)
				if b.Text != nil && strings.TrimSpace(b.Text.Text) == "" {
					t.Errorf("bloque %s vacío", b.Type)
				}
				if b.Type == "section" && b.Text == nil && len(b.Fields) == 0 {
					t.Errorf("sección sin texto ni campos")
				}
			}
			if got := strings.Join(types, ","); got != "context,section,actions" {
				t.Fatalf("bloques = %s", got)
			}
			if len(msg.Blocks[2].Elements) != 1 {
				t.Errorf("botones = %+v", msg.Blocks[2].Elements)
			}
			if c := msg.Blocks[0].Elements[0].(slackText).Text; c != "*New error* · backend" {
				t.Errorf("contexto = %q", c)
			}
			if msg.Blocks[1].Text.Text != "*fallo sin más*" || msg.Text != "New error in backend: fallo sin más" {
				t.Errorf("titular = %q, text = %q", msg.Blocks[1].Text.Text, msg.Text)
			}
			if strings.Contains(flatten(msg), "a@b.c") {
				t.Error("el email no puede salir en Slack")
			}
		})
	}

	// Sin fotograma, pero con culprit.
	pc := *p
	pc.culprit = "app.tasks in run"
	msg := slackMessage(&pc, nil, "")
	if len(msg.Blocks) != 3 || msg.Blocks[2].Text.Text != "`app.tasks in run`" {
		t.Fatalf("bloques = %+v", msg.Blocks)
	}

	// Una regresión sin fecha de resolución no la menciona.
	pr := *p
	pr.kind, pr.count = "regressed", 4
	msg = slackMessage(&pr, nil, "")
	if c := msg.Blocks[0].Elements[0].(slackText).Text; c != "*Regression* · backend" {
		t.Errorf("contexto = %q", c)
	}
	if f := msg.Blocks[2].Fields; len(f) != 1 || f[0].Text != "*Events*\n4 in total" {
		t.Errorf("campos = %+v", f)
	}
}

func TestSlackEscapesAndTruncates(t *testing.T) {
	long := strings.Repeat("x", 5000)
	p := &pending{
		issueID: 1, kind: "new", project: "a&b", environment: "<prod>", release: "<!channel>", eventAt: at,
		linearIdentifier: "ERR-" + strings.Repeat("9", 100), linearURL: "https://linear.app/x",
	}
	ev := &event.Event{
		Exceptions: []event.Exception{{
			Type: "Error", Value: "<!here> & <@U123|alguien>\nsegunda línea " + long,
			Frames: []event.Frame{{Filename: "a<b>.ts", Function: "f`&`", Line: 1, InApp: yes(),
				ContextLine: "if (a < b && c > d) ```" + long}},
		}},
		Tags:    map[string]string{"organization_name": "<https://malo.test|Pulsa> & " + long},
		Request: &event.Request{Method: "POST", URL: "/x<y>?a=1"},
	}
	msg := slackMessage(p, ev, "https://errors.test/issues/1")
	all := flatten(msg)
	for _, bad := range []string{"<!", "<@", "<https", "<prod>", "a<b>", "a < b", "&b", "segunda línea", "a=1", "``````"} {
		if strings.Contains(all, bad) {
			t.Errorf("sin escapar o sin recortar: %q en\n%s", bad, all)
		}
	}
	for _, want := range []string{"a&amp;b", "&lt;prod&gt;", "&lt;!here&gt; &amp; &lt;@U123|alguien&gt;", "`a&lt;b&gt;.ts:1` in `f'&amp;'`",
		"if (a &lt; b &amp;&amp; c &gt; d)", "*Release*\n&lt;!channel&gt;", "*Request*\nPOST /x%3Cy%3E"} {
		if !strings.Contains(all, want) {
			t.Errorf("falta %q en\n%s", want, all)
		}
	}
	if strings.Contains(msg.Text, "\n") || strings.Contains(msg.Text, "<!") {
		t.Errorf("text = %q", msg.Text)
	}
	for _, b := range msg.Blocks {
		if b.Text != nil && utf8.RuneCountInString(b.Text.Text) > maxSectionText {
			t.Errorf("sección de %d caracteres", utf8.RuneCountInString(b.Text.Text))
		}
		if b.Text != nil && utf8.RuneCountInString(b.Text.Text) > 1000 {
			t.Errorf("sección sin recortar: %d caracteres", utf8.RuneCountInString(b.Text.Text))
		}
		for _, f := range b.Fields {
			if utf8.RuneCountInString(f.Text) > maxFieldText || utf8.RuneCountInString(f.Text) > 600 {
				t.Errorf("campo de %d caracteres", utf8.RuneCountInString(f.Text))
			}
		}
		for _, el := range b.Elements {
			if btn, ok := el.(slackButton); ok && utf8.RuneCountInString(btn.Text.Text) > maxButtonText {
				t.Errorf("botón de %d caracteres", utf8.RuneCountInString(btn.Text.Text))
			}
		}
	}
}

func TestReleaseSHA(t *testing.T) {
	for in, want := range map[string]string{
		"3f9c2a1b7d4e5f60718293a4b5c6d7e8f9a0b1c2": "3f9c2a1",
		"3f9c2a1b7d4e":                     "3f9c2a1",
		"3f9c2a1":                          "3f9c2a1",
		"backend@3f9c2a1b7d4e5f60718293a4": "backend@3f9c2a1b7d4e5f60718293a4",
		"2026.11.18":                       "2026.11.18",
		"3f9c2a1b":                         "3f9c2a1",
		"20261118103045":                   "20261118103045",
		"1234567890abcdef1234":             "1234567",
		"1234567":                          "1234567",
	} {
		f := slackFields(&pending{release: in}, nil)
		if len(f) != 1 || f[0].Text != "*Release*\n"+want {
			t.Errorf("%s: %+v", in, f)
		}
	}
}

func TestSpanishDate(t *testing.T) {
	ref := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	if got := spanishDate(time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC), ref); got != "3 de noviembre" {
		t.Errorf("got %q", got)
	}
	if got := spanishDate(time.Date(2025, 12, 31, 12, 0, 0, 0, time.UTC), ref); got != "31 de diciembre de 2025" {
		t.Errorf("got %q", got)
	}
}

func TestSlackHeadlineAsterisks(t *testing.T) {
	p := &pending{issueID: 1, kind: "new", project: "backend", eventAt: at}
	ev := &event.Event{Exceptions: []event.Exception{{Type: "TypeError", Value: "unsupported operand type(s) for *: 'int' and 'str'"}}}
	msg := slackMessage(p, ev, "")
	want := "*TypeError: unsupported operand type(s) for \u2217: 'int' and 'str'*"
	if got := msg.Blocks[1].Text.Text; got != want {
		t.Errorf("titular = %q, quiero %q", got, want)
	}
}
