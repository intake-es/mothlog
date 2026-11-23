package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/intake-es/intake-errors/internal/db"
)

// proceso lee la memoria y la CPU del servicio desde el anfitrión: el /proc
// del proceso y el cgroup del contenedor.
type proceso struct {
	pid    int
	cgroup string
}

func nuevoProceso(ctx context.Context, contenedor string) (*proceso, error) {
	out, err := docker(ctx, "inspect", "-f", "{{.State.Pid}}", contenedor)
	if err != nil {
		return nil, err
	}
	pid, err := strconv.Atoi(out)
	if err != nil || pid <= 0 {
		return nil, fmt.Errorf("pid del servicio %q", out)
	}
	p := &proceso{pid: pid}
	if b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid)); err == nil {
		for l := range strings.SplitSeq(strings.TrimSpace(string(b)), "\n") {
			if rest, ok := strings.CutPrefix(l, "0::"); ok {
				p.cgroup = filepath.Join("/sys/fs/cgroup", rest)
			}
		}
	}
	if _, ok := p.rss(); !ok {
		return nil, fmt.Errorf("no se puede leer /proc/%d/status: la prueba necesita docker en esta misma máquina", pid)
	}
	return p, nil
}

func (p *proceso) status(campo string) (float64, bool) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", p.pid))
	if err != nil {
		return 0, false
	}
	for l := range strings.SplitSeq(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, campo+":"); ok {
			f := strings.Fields(v)
			if len(f) == 0 {
				return 0, false
			}
			kb, err := strconv.ParseFloat(f[0], 64)
			return kb * 1024, err == nil
		}
	}
	return 0, false
}

func (p *proceso) rss() (float64, bool)  { return p.status("VmRSS") }
func (p *proceso) pico() (float64, bool) { return p.status("VmHWM") }

// cpu da los microsegundos de CPU del contenedor y los que ha pasado
// frenado por el límite de --cpus.
func (p *proceso) cpu() (uso, frenado float64, ok bool) {
	if p.cgroup == "" {
		return 0, 0, false
	}
	b, err := os.ReadFile(filepath.Join(p.cgroup, "cpu.stat"))
	if err != nil {
		return 0, 0, false
	}
	for l := range strings.SplitSeq(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) != 2 {
			continue
		}
		v, _ := strconv.ParseFloat(f[1], 64)
		switch f[0] {
		case "usage_usec":
			uso, ok = v, true
		case "throttled_usec":
			frenado = v
		}
	}
	return uso, frenado, ok
}

// monitor muestrea el servicio y la base mientras dura un paso.
type monitor struct {
	proc *proceso
	pool db.DB
	base string
	cl   *http.Client

	cancel     context.CancelFunc
	wg         sync.WaitGroup
	t0         time.Time
	cpu0, fr0  float64
	met0       metricas
	rss        []float64
	poolEnUso  float64
	esperas    map[string]int
	muestrasDB int
}

func (m *monitor) empezar(ctx context.Context) {
	ctx, m.cancel = context.WithCancel(ctx)
	m.rss, m.poolEnUso, m.esperas, m.muestrasDB = nil, 0, map[string]int{}, 0
	m.t0 = time.Now()
	m.cpu0, m.fr0, _ = m.proc.cpu()
	m.met0 = leerMetricas(ctx, m.cl, m.base)
	m.wg.Go(func() {
		t := time.NewTicker(200 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			if v, ok := m.proc.rss(); ok {
				m.rss = append(m.rss, v)
			}
			m.muestrearBase(ctx)
		}
	})
	m.wg.Go(func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			if v, ok := leerMetricas(ctx, m.cl, m.base)["intake_db_pool_connections{state=\"in_use\"}"]; ok {
				m.poolEnUso = max(m.poolEnUso, v)
			}
		}
	})
}

// muestrearBase apunta en qué está cada conexión del servicio con Postgres.
func (m *monitor) muestrearBase(ctx context.Context) {
	if m.pool.Dialect().Name() != "postgres" {
		return
	}
	rows, err := m.pool.Query(ctx, `
		SELECT COALESCE(state, ''), COALESCE(wait_event_type, ''), COALESCE(wait_event, '')
		FROM pg_stat_activity
		WHERE backend_type = 'client backend' AND datname = current_database()
			AND pid <> pg_backend_pid() AND application_name <> 'carga'`)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var state, typ, ev string
		if rows.Scan(&state, &typ, &ev) != nil {
			return
		}
		if state == "idle" {
			continue
		}
		k := state
		if typ != "" {
			k += " · " + typ + ":" + ev
		}
		m.esperas[k]++
	}
	m.muestrasDB++
}

func (m *monitor) acabar(p *paso) {
	cpu1, fr1, ok := m.proc.cpu()
	secs := time.Since(m.t0).Seconds()
	m.cancel()
	m.wg.Wait()
	met1 := leerMetricas(context.Background(), m.cl, m.base)
	if ok {
		p.CPUSegundos = (cpu1 - m.cpu0) / 1e6
		p.CPUPorcentaje = 100 * p.CPUSegundos / secs
		p.CPUFrenadoSegundos = (fr1 - m.fr0) / 1e6
	}
	var sum float64
	for _, v := range m.rss {
		sum += v
		p.RSSMaxMB = max(p.RSSMaxMB, v/1e6)
	}
	if len(m.rss) > 0 {
		p.RSSMedioMB = sum / float64(len(m.rss)) / 1e6
	}
	p.PoolEnUsoMax = m.poolEnUso
	p.PoolMax = met1["intake_db_pool_max_connections"]
	p.PoolEsperas = met1["intake_db_pool_waits_total"] - m.met0["intake_db_pool_waits_total"]
	p.PoolEsperaSegundos = met1["intake_db_pool_wait_seconds_total"] - m.met0["intake_db_pool_wait_seconds_total"]
	p.ConexionesBase = map[string]float64{}
	if m.muestrasDB > 0 {
		for k, n := range m.esperas {
			p.ConexionesBase[k] = float64(n) / float64(m.muestrasDB)
		}
	}
}

type metricas map[string]float64

// leerMetricas lee /metrics: nombre con etiquetas y valor.
func leerMetricas(ctx context.Context, cl *http.Client, base string) metricas {
	out := metricas{}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/metrics", nil)
	req.Header.Set("Authorization", "Bearer "+tokenMetricas)
	resp, err := cl.Do(req)
	if err != nil {
		return out
	}
	defer func() { _ = resp.Body.Close() }()
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		l := sc.Text()
		if l == "" || l[0] == '#' {
			continue
		}
		i := strings.LastIndexByte(l, ' ')
		if i < 0 {
			continue
		}
		v, err := strconv.ParseFloat(l[i+1:], 64)
		if err == nil {
			out[l[:i]] = v
		}
	}
	return out
}

// sumar suma las series de nombre que llevan todas las etiquetas dadas.
func (m metricas) sumar(nombre string, etiquetas ...string) float64 {
	var s float64
	for k, v := range m {
		if k != nombre && !strings.HasPrefix(k, nombre+"{") {
			continue
		}
		ok := true
		for _, e := range etiquetas {
			if !strings.Contains(k, e) {
				ok = false
			}
		}
		if ok {
			s += v
		}
	}
	return s
}

// sacarPerfil pide a pprof un perfil de CPU de dur y deja el resumen de
// go tool pprof -top al lado. Devuelve el nombre del resumen, o vacío.
func sacarPerfil(ctx context.Context, base string, dur time.Duration, dir, nombre string) string {
	secs := int(dur.Seconds())
	ctx, cancel := context.WithTimeout(ctx, dur+time.Minute)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/debug/pprof/profile?seconds=%d", base, secs), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		logf("%s: sin perfil de CPU: %v", nombre, err)
		return ""
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		logf("%s: sin perfil de CPU: código %d (¿el servicio no tiene PPROF_ADDR?)", nombre, resp.StatusCode)
		return ""
	}
	fichero := filepath.Join(dir, "cpu-"+nombre+".pprof")
	f, err := os.Create(fichero)
	if err != nil {
		return ""
	}
	_, err = io.Copy(f, resp.Body)
	_ = f.Close()
	if err != nil {
		return ""
	}
	top, err := exec.CommandContext(ctx, "go", "tool", "pprof", "-top", "-nodecount=40", fichero).CombinedOutput()
	if err != nil {
		logf("%s: go tool pprof: %v", nombre, err)
		return ""
	}
	resumen := filepath.Join(dir, "cpu-"+nombre+".top.txt")
	if err := os.WriteFile(resumen, top, 0o644); err != nil {
		return ""
	}
	return filepath.Base(resumen)
}
