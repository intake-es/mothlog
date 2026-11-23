package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/intake-es/intake-errors/internal/event"
)

// referenceCanonical es la forma canónica escrita del modo más directo, sin
// topes: cada valor en su búfer y los objetos ordenados al cerrarlos.
func referenceCanonical(t *testing.T, b []byte) []byte {
	t.Helper()
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var value func() []byte
	value = func() []byte {
		tok, err := d.Token()
		if err != nil {
			t.Fatalf("%s: %v", b, err)
		}
		switch v := tok.(type) {
		case json.Delim:
			var parts [][]byte
			if v == '[' {
				for d.More() {
					parts = append(parts, value())
				}
				_, _ = d.Token()
				return append(append([]byte{'['}, bytes.Join(parts, []byte{','})...), ']')
			}
			type m struct {
				k string
				v []byte
			}
			var ms []m
			for d.More() {
				k, _ := d.Token()
				key := k.(string)
				val := value()
				if i := slices.IndexFunc(ms, func(x m) bool { return x.k == key }); i >= 0 {
					ms[i].v = val
				} else {
					ms = append(ms, m{key, val})
				}
			}
			_, _ = d.Token()
			slices.SortFunc(ms, func(a, b m) int {
				if len(a.k) != len(b.k) {
					return len(a.k) - len(b.k)
				}
				return strings.Compare(a.k, b.k)
			})
			for _, x := range ms {
				kb, _ := json.Marshal(x.k)
				parts = append(parts, append(append(kb, ':'), x.v...))
			}
			return append(append([]byte{'{'}, bytes.Join(parts, []byte{','})...), '}')
		case string:
			s, _ := json.Marshal(v)
			return s
		case json.Number:
			return []byte(numericText(string(v)))
		case bool:
			return []byte(strconv.FormatBool(v))
		}
		return []byte("null")
	}
	return value()
}

// La forma canónica no cambia: la misma que la directa en los fixtures y en
// JSON al azar, con objetos y arrays anidados de todas las formas.
func TestCanonicalJSONMatchesReference(t *testing.T) {
	samples := []string{
		`{}`, `[]`, `"texto"`, `12`, `null`, `[[[]]]`, `[{}]`, `{"a":{}}`,
		`{"b": 1, "a": 2, "a": 3, "aa": {"y": [1, 2.0, -0.0], "x": null}}`,
		`{"b": {"d": 1, "c": 2}, "a": [{"z": 1, "y": {"q": 1, "p": 2}}, 3, 4, {"b": 1, "a": 2}, 5]}`,
		`[1, 2, {"b": {"y": 1, "x": 2}, "a": 1}, 3, [4, {"d": 1, "c": [5, {"f": 1, "e": 2}]}], 6]`,
		`{"a": 1, "a": {"c": 1, "b": 2}, "b": [], "b": [{"y": 1, "x": 1}]}`,
	}
	for _, ev := range fixtureEvents(t) {
		for _, raw := range []json.RawMessage{ev.Contexts, ev.Breadcrumbs, ev.DebugMeta} {
			if len(raw) > 0 {
				samples = append(samples, string(raw))
			}
		}
	}
	r := rand.New(rand.NewPCG(3, 4))
	for range 3000 {
		samples = append(samples, randomJSON(r, 6))
	}
	for _, s := range samples {
		got, err := canonicalJSON([]byte(s))
		if err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		if want := referenceCanonical(t, []byte(s)); !bytes.Equal(got, want) {
			t.Errorf("%s:\n canónico %s\n quiero   %s", s, got, want)
		}
	}
}

func allocated(f func()) uint64 {
	runtime.GC()
	var a, b runtime.MemStats
	runtime.ReadMemStats(&a)
	f()
	runtime.ReadMemStats(&b)
	return b.TotalAlloc - a.TotalAlloc
}

func repeatJSON(open, item, close string, n int) []byte {
	var b bytes.Buffer
	b.WriteString(open)
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		if strings.Contains(item, "%") {
			fmt.Fprintf(&b, item, i)
		} else {
			b.WriteString(item)
		}
	}
	b.WriteString(close)
	return b.Bytes()
}

// Las entradas de la auditoría: lo que cuesta guardar el evento queda en
// pocos MB y lo guardado no pasa del doble de lo recibido.
func TestEncodeEventCostBounded(t *testing.T) {
	nested := []byte(`"` + strings.Repeat("x", 64<<10) + `"`)
	for range 400 {
		nested = slices.Concat([]byte(`{"a":`), nested, []byte(`,"0":1}`))
	}
	cases := map[string][]byte{
		// 192 KB que antes asignaban más de 400 MB.
		"1e999":       repeatJSON(`{"a":[`, "1e999", `]}`, 32_000),
		"1e30":        repeatJSON(`{"a":[`, "1e30", `]}`, 40_000),
		"claves":      repeatJSON(`{`, `"k%07d":0`, `}`, 9_000),
		"demasiadas":  repeatJSON(`{`, `"k%07d":0`, `}`, 16_000),
		"repetidas":   repeatJSON(`{`, `"k":0`, `}`, 9_000),
		"anidado":     nested,
		"objetos":     repeatJSON(`[`, `{"b":[],"a":[]}`, `]`, 12_000),
		"desordenado": repeatJSON(`[`, `{"bb":{"d":1,"c":2},"a":{"y":1,"x":2}}`, `]`, 5_000),
	}
	for name, in := range cases {
		var c []byte
		allocC := allocated(func() {
			var err error
			c, err = canonicalJSON(in)
			if err != nil {
				c = in
			}
		})
		if len(c) > 2*len(in)+1024 {
			t.Errorf("%s: %d bytes de entrada dan %d", name, len(in), len(c))
		}
		ev := &event.Event{ID: strings.Repeat("a", 32), Level: "error", Message: "x", Contexts: in}
		// La media de varios, para no contar el compresor que se crea si el
		// GC ha vaciado su pool.
		allocE := allocated(func() {
			for range 5 {
				if _, err := EncodeEvent(ev); err != nil {
					t.Fatal(err)
				}
			}
		}) / 5
		t.Logf("%s: entrada %d B, salida %d B, canonicalJSON asigna %d B, EncodeEvent %d B", name, len(in), len(c), allocC, allocE)
		if allocE > 8<<20 {
			t.Errorf("%s: guardar un evento con %d bytes de contexts asigna %d MB", name, len(in), allocE>>20)
		}
	}
}

func TestCanonicalJSONLimits(t *testing.T) {
	if _, err := canonicalJSON(repeatJSON(`{"a":[`, "1e999", `]}`, 1000)); err != nil {
		t.Errorf("1e999 se queda como venía y no se pasa del tope: %v", err)
	}
	got, _ := canonicalJSON([]byte(`[1e999,1e3,-1e-30,9e30]`))
	if want := `[1e999,1000,-0.000000000000000000000000000001,9000000000000000000000000000000]`; string(got) != want {
		t.Errorf("números: %s; quiero %s", got, want)
	}
	if _, err := canonicalJSON(repeatJSON(`{"a":[`, "1e30", `]}`, 1000)); !errors.Is(err, errCanonicalTooBig) {
		t.Errorf("1e30 mil veces: %v; quiero que pase del tope", err)
	}
	if _, err := canonicalJSON(repeatJSON(`{`, `"k%07d":0`, `}`, maxObjectMembers+1)); !errors.Is(err, errCanonicalTooBig) {
		t.Errorf("objeto con %d miembros: %v", maxObjectMembers+1, err)
	}
	if _, err := canonicalJSON(repeatJSON(`{`, `"k%07d":0`, `}`, maxObjectMembers)); err != nil {
		t.Errorf("objeto con %d miembros: %v", maxObjectMembers, err)
	}
	// Si no se puede normalizar, se guarda como llegó.
	in := repeatJSON(`{"a":[`, "1e30", `]}`, 1000)
	if got := canonicalRaw(in); !bytes.Equal(got, in) {
		t.Error("canonicalRaw no devuelve el original cuando pasa del tope")
	}
}

// Normalizar un objeto cuesta lo mismo por clave tenga las claves que tenga.
func TestCanonicalJSONManyKeysLinear(t *testing.T) {
	timeOf := func(n int) time.Duration {
		in := repeatJSON(`{`, `"k%07d":0`, `}`, n)
		best := time.Duration(1 << 62)
		for range 5 {
			start := time.Now()
			if _, err := canonicalJSON(in); err != nil {
				t.Fatal(err)
			}
			best = min(best, time.Since(start))
		}
		return best
	}
	small, big := timeOf(1_250), timeOf(10_000)
	t.Logf("1250 claves: %v, 10000 claves: %v (%.1fx)", small, big, float64(big)/float64(small))
	// Con 8 veces más claves, lineal da unas 8 veces más y cuadrático 64.
	if big > 24*small {
		t.Errorf("8 veces más claves tardan %.1f veces más", float64(big)/float64(small))
	}
}

func BenchmarkCanonicalJSON(b *testing.B) {
	in := repeatJSON(`{"a":{"os":{"name":"Linux","version":"6.6"},"trace":{"span_id":"abc","trace_id":"def"},"list":[`, `{"b":%d,"a":"x"}`, `]}}`, 200)
	b.SetBytes(int64(len(in)))
	for b.Loop() {
		if _, err := canonicalJSON(in); err != nil {
			b.Fatal(err)
		}
	}
}
