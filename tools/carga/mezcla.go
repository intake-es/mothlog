package main

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	mrand "math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/intake-es/intake-errors/internal/envelope"
)

// Los proyectos y claves con que se grabaron los fixtures.
var proyectos = []struct {
	id         int64
	nombre, pk string
}{
	{1, "api", "5b6d3a35fd6eb6e0f9d410fc5c0f6cbc"},
	{2, "web", "ce01335f125960aa6957a133d15b5f3a"},
}

// tipo es una clase de envío de la mezcla.
type tipo struct {
	nombre  string
	fixture string
	peso    int
	// cola: el evento lleva una huella al azar entre las de la cola.
	cola bool
}

// mezcla imita el tráfico de un servicio: muchos eventos de pocos errores
// calientes, una cola larga de errores distintos, errores del navegador con
// source maps, check-ins y sesiones.
var mezcla = []tipo{
	{nombre: "caliente-excepcion", fixture: "api-excepcion-1", peso: 20},
	{nombre: "caliente-validacion", fixture: "api-validacion-1", peso: 10},
	{nombre: "caliente-log", fixture: "api-log-exception-1", peso: 10},
	{nombre: "caliente-usuario", fixture: "api-sesion-crash-1", peso: 5},
	{nombre: "navegador-sourcemaps", fixture: "web-typeerror-1", peso: 15},
	{nombre: "cola", fixture: "api-log-error-1", peso: 20, cola: true},
	{nombre: "check-in", fixture: "api-cron-ok-2", peso: 5},
	{nombre: "sesion-navegador", fixture: "web-sesion-1", peso: 10},
	{nombre: "sesiones-agregadas", fixture: "api-sesiones-1", peso: 5},
}

type fixture struct {
	path, query string
	headers     map[string]string
	gzip        bool
	header      map[string]json.RawMessage
	items       []envelope.Item
}

func cargarFixture(dir, nombre string) (*fixture, error) {
	var meta struct {
		Path    string            `json:"path"`
		Query   string            `json:"query"`
		Headers map[string]string `json:"headers"`
	}
	b, err := os.ReadFile(filepath.Join(dir, nombre+".meta.json"))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &meta); err != nil {
		return nil, fmt.Errorf("%s: %w", nombre, err)
	}
	body, err := os.ReadFile(filepath.Join(dir, nombre+".envelope"))
	if err != nil {
		return nil, err
	}
	f := &fixture{path: meta.Path, query: meta.Query, headers: map[string]string{}}
	// Las cabeceras de los de Python vienen con mayúsculas y las del navegador, sin.
	for k, v := range meta.Headers {
		k = strings.ToLower(k)
		if k == "host" || k == "content-length" || k == "content-encoding" || k == "accept-encoding" {
			if k == "content-encoding" {
				f.gzip = v == "gzip"
			}
			continue
		}
		f.headers[k] = v
	}
	data, err := envelope.Decompress(bytes.NewReader(body), map[bool]string{true: "gzip"}[f.gzip], 10<<20)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", nombre, err)
	}
	cab, _, _ := bytes.Cut(data, []byte("\n"))
	if err := json.Unmarshal(cab, &f.header); err != nil {
		return nil, fmt.Errorf("%s: cabecera: %w", nombre, err)
	}
	env, err := envelope.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", nombre, err)
	}
	f.items = env.Items
	return f, nil
}

// envio es una petición lista para mandar y lo que lleva dentro.
type envio struct {
	tipo     string
	path     string
	headers  map[string]string
	body     []byte
	eventos  []idEvento
	checkIns []string
	sesiones int
	// issue identifica el issue al que va el evento, para saber cuántos debe haber.
	issue string
}

type idEvento struct {
	proyecto int64
	id       string
}

type generador struct {
	fixtures map[string]*fixture
	total    int
	cola     int
	mu       sync.Mutex
	rnd      *mrand.Rand
}

func nuevoGenerador(dir string, cola int, semilla uint64) (*generador, error) {
	g := &generador{fixtures: map[string]*fixture{}, cola: cola, rnd: mrand.New(mrand.NewPCG(semilla, semilla))}
	for _, t := range mezcla {
		if _, ok := g.fixtures[t.fixture]; ok {
			continue
		}
		f, err := cargarFixture(dir, t.fixture)
		if err != nil {
			return nil, err
		}
		g.fixtures[t.fixture] = f
	}
	for _, t := range mezcla {
		g.total += t.peso
	}
	return g, nil
}

// elegir saca el tipo del siguiente envío y, si es de la cola, su huella.
func (g *generador) elegir() (tipo, int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := g.rnd.IntN(g.total)
	for _, t := range mezcla {
		if n < t.peso {
			return t, g.rnd.IntN(g.cola)
		}
		n -= t.peso
	}
	panic("pesos")
}

func hexID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func uuid() string {
	h := hexID()
	return h[:8] + "-" + h[8:12] + "-4" + h[13:16] + "-a" + h[17:20] + "-" + h[20:]
}

// siguiente monta un envío de la mezcla con ids nuevos.
func (g *generador) siguiente() (*envio, error) {
	t, k := g.elegir()
	f := g.fixtures[t.fixture]
	e := &envio{tipo: t.nombre, headers: f.headers, issue: t.fixture}
	proyecto, err := strconv.ParseInt(strings.Split(strings.Trim(f.path, "/"), "/")[1], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("%s: proyecto de %q", t.fixture, f.path)
	}
	eventID := hexID()
	var buf bytes.Buffer
	cab := map[string]json.RawMessage{}
	for k, v := range f.header {
		cab[k] = v
	}
	if _, ok := cab["event_id"]; ok {
		cab["event_id"] = mustJSON(eventID)
	}
	cab["sent_at"] = mustJSON(time.Now().UTC().Format(time.RFC3339Nano))
	buf.Write(mustJSON(cab))
	buf.WriteByte('\n')
	for _, it := range f.items {
		payload := it.Payload
		switch it.Header.Type {
		case envelope.TypeEvent:
			p := map[string]json.RawMessage{}
			if err := json.Unmarshal(payload, &p); err != nil {
				return nil, err
			}
			p["event_id"] = mustJSON(eventID)
			p["timestamp"] = mustJSON(float64(time.Now().UnixMicro()) / 1e6)
			if t.cola {
				huella := "carga-cola-" + strconv.Itoa(k)
				p["fingerprint"] = mustJSON([]string{huella})
				e.issue = huella
			}
			payload = mustJSON(p)
			e.eventos = append(e.eventos, idEvento{proyecto, eventID})
		case envelope.TypeCheckIn:
			p := map[string]json.RawMessage{}
			if err := json.Unmarshal(payload, &p); err != nil {
				return nil, err
			}
			id := hexID()
			p["check_in_id"] = mustJSON(id)
			payload = mustJSON(p)
			e.checkIns = append(e.checkIns, id)
		case envelope.TypeSession:
			p := map[string]json.RawMessage{}
			if err := json.Unmarshal(payload, &p); err != nil {
				return nil, err
			}
			p["sid"] = mustJSON(uuid())
			payload = mustJSON(p)
			e.sesiones++
		case envelope.TypeSessions:
			e.sesiones++
		}
		ih := map[string]any{"type": it.Header.Type, "length": len(payload)}
		if it.Header.ContentType != "" {
			ih["content_type"] = it.Header.ContentType
		}
		buf.Write(mustJSON(ih))
		buf.WriteByte('\n')
		buf.Write(payload)
		buf.WriteByte('\n')
	}
	e.body = buf.Bytes()
	if f.gzip {
		var z bytes.Buffer
		zw := gzip.NewWriter(&z)
		_, _ = zw.Write(e.body)
		_ = zw.Close()
		e.body = z.Bytes()
	}
	e.path = f.path
	if f.query != "" {
		e.path += "?" + f.query
	}
	if len(e.eventos) == 0 {
		e.issue = ""
	}
	return e, nil
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
