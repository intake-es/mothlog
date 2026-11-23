package envelope

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andybalholm/brotli"
)

const sample = `{"event_id":"9ec79c33ec9942ab8353589fcb2e04dc","dsn":"https://k@o0.ingest.sentry.io/1"}
{"type":"event","length":34}
{"message":"hola","level":"error"}

{"type":"sessions"}
{"attrs":{"release":"r"},"aggregates":[]}
{"type":"client_report","length":2}
{}
`

func TestParse(t *testing.T) {
	env, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	if env.Header.EventID != "9ec79c33ec9942ab8353589fcb2e04dc" {
		t.Errorf("event_id = %q", env.Header.EventID)
	}
	var types []string
	for _, it := range env.Items {
		types = append(types, it.Header.Type)
	}
	if got := strings.Join(types, ","); got != "event,sessions,client_report" {
		t.Fatalf("items = %s", got)
	}
	if got := string(env.Items[0].Payload); got != `{"message":"hola","level":"error"}` {
		t.Errorf("payload event = %q", got)
	}
	if got := string(env.Items[1].Payload); got != `{"attrs":{"release":"r"},"aggregates":[]}` {
		t.Errorf("payload sessions = %q", got)
	}
	if ev := env.Events(); len(ev) != 1 || ev[0].Header.Type != TypeEvent {
		t.Errorf("Events() = %+v", ev)
	}
}

func TestParseWithoutLengthAtEOF(t *testing.T) {
	env, err := Parse([]byte("{}\n{\"type\":\"event\"}\n{\"message\":\"x\"}"))
	if err != nil {
		t.Fatal(err)
	}
	if len(env.Items) != 1 || string(env.Items[0].Payload) != `{"message":"x"}` {
		t.Fatalf("items = %+v", env.Items)
	}
}

func TestParseEmptyPayloadWithoutLength(t *testing.T) {
	env, err := Parse([]byte("{}\n{\"type\":\"client_report\"}\n\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(env.Items) != 1 || len(env.Items[0].Payload) != 0 {
		t.Fatalf("items = %+v", env.Items)
	}
}

func TestParseCRLF(t *testing.T) {
	env, err := Parse([]byte("{}\r\n{\"type\":\"event\"}\r\n{\"message\":\"x\"}\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(env.Items) != 1 || string(env.Items[0].Payload) != `{"message":"x"}` {
		t.Fatalf("items = %+v", env.Items)
	}
}

func TestParseOnlyHeader(t *testing.T) {
	env, err := Parse([]byte(`{"event_id":"abc"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(env.Items) != 0 || len(env.Events()) != 0 {
		t.Fatalf("items = %+v", env.Items)
	}
}

func TestParseMalformed(t *testing.T) {
	cases := map[string]string{
		"vacío":                    "",
		"solo saltos":              "\n\n",
		"cabecera no es json":      "hola\n",
		"cabecera truncada":        `{"event_id":"ab`,
		"cabecera es una lista":    "[]\n",
		"item no es json":          "{}\n{type:event}\n{}\n",
		"item sin type":            "{}\n{\"length\":2}\n{}\n",
		"length negativo":          "{}\n{\"type\":\"event\",\"length\":-1}\n{}\n",
		"length no numérico":       "{}\n{\"type\":\"event\",\"length\":\"2\"}\n{}\n",
		"payload truncado":         "{}\n{\"type\":\"event\",\"length\":100}\n{\"message\":\"x\"}\n",
		"sin payload ni length":    "{}\n{\"type\":\"event\"}\n",
		"sin payload ni salto":     "{}\n{\"type\":\"event\"}",
		"length más corto":         "{}\n{\"type\":\"event\",\"length\":3}\n{\"message\":\"x\"}\n",
		"truncado a mitad de item": sample[:len(sample)-30],
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			env, err := Parse([]byte(in))
			if !errors.Is(err, ErrMalformed) {
				t.Fatalf("err = %v, env = %+v; quiero ErrMalformed", err, env)
			}
		})
	}
}

func compress(t *testing.T, enc string, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	var w io.WriteCloser
	switch enc {
	case "gzip":
		w = gzip.NewWriter(&buf)
	case "deflate":
		w = zlib.NewWriter(&buf)
	case "deflate-raw":
		w, _ = flate.NewWriter(&buf, flate.DefaultCompression)
	case "br":
		w = brotli.NewWriter(&buf)
	default:
		return data
	}
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestDecompress(t *testing.T) {
	for _, tc := range []struct{ name, header string }{
		{"identity", ""},
		{"identity", "identity"},
		{"gzip", "gzip"},
		{"gzip", " GZIP "},
		{"deflate", "deflate"},
		{"deflate-raw", "deflate"},
		{"br", "br"},
	} {
		t.Run(tc.name+"/"+tc.header, func(t *testing.T) {
			body := compress(t, tc.name, []byte(sample))
			got, err := Decompress(bytes.NewReader(body), tc.header, 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != sample {
				t.Fatalf("got %q", got)
			}
		})
	}
}

func TestDecompressBroken(t *testing.T) {
	gz := compress(t, "gzip", []byte(sample))
	zl := compress(t, "deflate", []byte(sample))
	br := compress(t, "br", []byte(sample))
	for _, tc := range []struct {
		name, enc string
		body      []byte
	}{
		{"gzip sin comprimir", "gzip", []byte(sample)},
		{"gzip truncado", "gzip", gz[:len(gz)/2]},
		{"gzip vacío", "gzip", nil},
		{"deflate truncado", "deflate", zl[:len(zl)/2]},
		{"deflate vacío", "deflate", nil},
		{"br truncado", "br", br[:len(br)/2]},
		{"br basura", "br", []byte("esto no es brotli")},
		{"encoding desconocido", "zstd", []byte(sample)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Decompress(bytes.NewReader(tc.body), tc.enc, 1<<20)
			if !errors.Is(err, ErrMalformed) {
				t.Fatalf("err = %v; quiero ErrMalformed", err)
			}
		})
	}
}

func TestParseMaxItems(t *testing.T) {
	item := "{\"type\":\"client_report\"}\n{}\n"
	env, err := Parse([]byte("{}\n" + strings.Repeat(item, MaxItems)))
	if err != nil || len(env.Items) != MaxItems {
		t.Fatalf("con %d items: %v", MaxItems, err)
	}
	if _, err := Parse([]byte("{}\n" + strings.Repeat(item, MaxItems+1))); !errors.Is(err, ErrTooManyItems) {
		t.Fatalf("con %d items: err = %v; quiero ErrTooManyItems", MaxItems+1, err)
	}
}

func TestDecompressLimit(t *testing.T) {
	big := bytes.Repeat([]byte("a"), 10_000)
	_, err := Decompress(bytes.NewReader(compress(t, "gzip", big)), "gzip", 9_999)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v; quiero ErrTooLarge", err)
	}
	if _, err := Decompress(bytes.NewReader(big), "", 10_000); err != nil {
		t.Fatalf("en el límite: %v", err)
	}
}

func TestPublicKey(t *testing.T) {
	for _, tc := range []struct {
		name, header, query, want string
	}{
		{"x-sentry-auth", "Sentry sentry_key=b7d6, sentry_version=7, sentry_client=sentry.python/2.57.0", "", "b7d6"},
		{"sin espacios", "Sentry sentry_version=7,sentry_key=abc", "", "abc"},
		{"query", "", "sentry_version=7&sentry_key=d480&sentry_client=x", "d480"},
		{"cabecera antes que query", "Sentry sentry_key=uno", "sentry_key=dos", "uno"},
		{"cabecera sin clave", "Sentry sentry_version=7", "sentry_key=dos", "dos"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			if tc.header != "" {
				h.Set("X-Sentry-Auth", tc.header)
			}
			q, _ := url.ParseQuery(tc.query)
			got, err := PublicKey(h, q)
			if err != nil || got != tc.want {
				t.Fatalf("= %q, %v; quiero %q", got, err, tc.want)
			}
		})
	}

	h := http.Header{}
	h.Set("X-Sentry-Auth", "Bearer sentry_key=x")
	if _, err := PublicKey(h, url.Values{}); !errors.Is(err, ErrNoPublicKey) {
		t.Fatalf("err = %v; quiero ErrNoPublicKey", err)
	}
}

func FuzzParse(f *testing.F) {
	f.Add([]byte(sample))
	files, _ := filepath.Glob("../../testdata/envelopes/web-*.envelope")
	for _, p := range files {
		if b, err := os.ReadFile(p); err == nil {
			f.Add(b)
		}
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		env, err := Parse(data)
		if err == nil && env == nil {
			t.Fatal("sin error y sin envelope")
		}
		if err != nil && !errors.Is(err, ErrMalformed) {
			t.Fatalf("error sin ErrMalformed: %v", err)
		}
	})
}
