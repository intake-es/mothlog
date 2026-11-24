package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/intake-es/mothlog/internal/db"
)

// sembrar crea los proyectos de los fixtures con sus claves y sube los
// source maps del envío del navegador.
func sembrar(ctx context.Context, pool db.DB, base, dir string) error {
	for _, p := range proyectos {
		if _, err := pool.Exec(ctx, db.ByEngine(pool.Dialect(),
			`INSERT INTO projects (id, name, public_key) OVERRIDING SYSTEM VALUE VALUES ($1, $2, $3)`,
			`INSERT INTO projects (id, name, public_key) VALUES ($1, $2, $3)`),
			p.id, p.nombre, p.pk); err != nil {
			return fmt.Errorf("crear proyecto: %w", err)
		}
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, f := range []string{"main-3TO43VUM.js.map", "chunk-Z554BZQ6.js.map"} {
		b, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			return err
		}
		w, err := mw.CreateFormFile("file", f)
		if err != nil {
			return err
		}
		_, _ = w.Write(b)
	}
	_ = mw.Close()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/2/releases/fixture-release-1/sourcemaps/", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+tokenMapas)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("subir source maps: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("subir source maps: %d %s", resp.StatusCode, b)
	}
	return nil
}

// registro guarda lo que el servicio ha aceptado, para comprobar después la base.
type registro struct {
	mu       sync.Mutex
	eventos  map[int64][]string
	checkIns []string
	issues   map[string]bool
	sesiones int
}

func newRegistro() *registro {
	return &registro{eventos: map[int64][]string{}, issues: map[string]bool{}}
}

func (r *registro) aceptado(e *envio) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ev := range e.eventos {
		r.eventos[ev.proyecto] = append(r.eventos[ev.proyecto], ev.id)
	}
	r.checkIns = append(r.checkIns, e.checkIns...)
	if e.issue != "" {
		r.issues[e.issue] = true
	}
	r.sesiones += e.sesiones
}

type respuesta struct {
	lat    time.Duration
	codigo int
	e      *envio
}

func mandar(ctx context.Context, cl *http.Client, base string, e *envio) int {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+e.path, bytes.NewReader(e.body))
	if err != nil {
		return -1
	}
	for k, v := range e.headers {
		req.Header.Set(k, v)
	}
	if e.body[0] == 0x1f {
		req.Header.Set("Content-Encoding", "gzip")
	}
	resp, err := cl.Do(req)
	if err != nil {
		return -1
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode
}

// cargar manda envíos durante dur con conc a la vez. Con ritmo, los manda a
// ese ritmo y la latencia cuenta desde cuándo tocaba mandarlo, para que una
// cola en el cliente no esconda la lentitud del servicio.
func cargar(ctx context.Context, cl *http.Client, base string, gen *generador, reg *registro, conc int, ritmo float64, dur time.Duration) paso {
	ctx, cancel := context.WithTimeout(ctx, dur)
	defer cancel()
	out := make(chan respuesta, 4096)
	var tarde int
	var trabajos chan time.Time
	if ritmo > 0 {
		trabajos = make(chan time.Time, conc*4)
		go func() {
			defer close(trabajos)
			intervalo := time.Duration(float64(time.Second) / ritmo)
			next := time.Now()
			for {
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Until(next)):
				}
				select {
				case trabajos <- next:
				default:
					tarde++
				}
				next = next.Add(intervalo)
			}
		}()
	}
	start := time.Now()
	var wg sync.WaitGroup
	for range conc {
		wg.Go(func() {
			for {
				t0 := time.Now()
				if trabajos != nil {
					var ok bool
					if t0, ok = <-trabajos; !ok {
						return
					}
				} else if ctx.Err() != nil {
					return
				}
				e, err := gen.siguiente()
				if err != nil {
					panic(err)
				}
				// Lo que se manda se acaba aunque el paso termine, para que el
				// registro sepa si se aceptó.
				code := mandar(context.WithoutCancel(ctx), cl, base, e)
				out <- respuesta{time.Since(t0), code, e}
			}
		})
	}
	go func() { wg.Wait(); close(out) }()

	p := paso{Concurrencia: conc, Ritmo: ritmo, Codigos: map[string]int{}}
	var lats []float64
	for r := range out {
		p.Envios++
		p.Eventos += len(r.e.eventos)
		p.Codigos[codigoTexto(r.codigo)]++
		lats = append(lats, float64(r.lat.Microseconds())/1000)
		if r.codigo == http.StatusOK {
			p.EnviosOK++
			p.EventosOK += len(r.e.eventos)
			p.CheckInsOK += len(r.e.checkIns)
			reg.aceptado(r.e)
		}
	}
	p.Duracion = time.Since(start).Seconds()
	p.NoEnviadosATiempo = tarde
	p.EventosPorSegundo = float64(p.EventosOK) / p.Duracion
	p.EnviosPorSegundo = float64(p.EnviosOK) / p.Duracion
	slices.Sort(lats)
	p.P50, p.P95, p.P99 = percentil(lats, 50), percentil(lats, 95), percentil(lats, 99)
	if len(lats) > 0 {
		p.Max = lats[len(lats)-1]
	}
	return p
}

func codigoTexto(c int) string {
	if c < 0 {
		return "error de red"
	}
	return strconv.Itoa(c)
}

func percentil(s []float64, p float64) float64 {
	if len(s) == 0 {
		return 0
	}
	i := int(float64(len(s)-1) * p / 100)
	return s[i]
}
