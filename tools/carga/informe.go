package main

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/intake-es/mothlog/internal/db"
)

// Lo que se quiere aguantar: el servicio con 0,25 vCPU y menos de esto.
const objetivoRSSMB = 50

type paso struct {
	Nombre            string
	Concurrencia      int
	Ritmo             float64
	Duracion          float64
	Envios, EnviosOK  int
	Eventos           int
	EventosOK         int
	CheckInsOK        int
	EventosPorSegundo float64
	EnviosPorSegundo  float64
	// Latencias en milisegundos.
	P50, P95, P99, Max float64
	Codigos            map[string]int
	NoEnviadosATiempo  int
	CPUSegundos        float64
	// CPUPorcentaje es sobre una vCPU: con --cpus=0.25, el tope es 25.
	CPUPorcentaje      float64
	CPUFrenadoSegundos float64
	RSSMedioMB         float64
	RSSMaxMB           float64
	PoolEnUsoMax       float64
	PoolMax            float64
	PoolEsperas        float64
	PoolEsperaSegundos float64
	// ConexionesBase es, por estado y espera, cuántas conexiones del servicio
	// había de media en cada muestra de pg_stat_activity (sin las inactivas).
	ConexionesBase map[string]float64
	Perfil         string
}

type comprobacion struct {
	Cuadra bool
	Lineas []linea
}

type linea struct {
	Que      string
	Esperado float64
	Obtenido float64
}

type informe struct {
	Etiqueta, Commit, Imagen string
	// Motor es postgres o sqlite.
	Motor           string
	CPUs, Memoria   string
	Env             []string
	Duracion, Ritmo float64
	Maquina, Fecha  string
	RSSReposoMB     float64
	RSSPicoMB       float64
	Pasos           []paso
	Metricas        metricas `json:"-"`
	Comprobacion    comprobacion
	// Aguanta es el paso con más eventos por segundo sin errores y por debajo
	// del objetivo de memoria.
	Aguanta *paso
}

func (inf *informe) resumir() {
	for i := range inf.Pasos {
		p := &inf.Pasos[i]
		if p.EnviosOK != p.Envios || p.RSSMaxMB >= objetivoRSSMB {
			continue
		}
		if inf.Aguanta == nil || p.EventosPorSegundo > inf.Aguanta.EventosPorSegundo {
			inf.Aguanta = p
		}
	}
}

// comprobar mira que la base tiene lo que el servicio dijo que había aceptado.
func comprobar(ctx context.Context, pool db.DB, reg *registro, met metricas) (comprobacion, error) {
	var c comprobacion
	var aceptados, guardados int64
	for p, ids := range reg.eventos {
		aceptados += int64(len(ids))
		var n int64
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE project_id = $1 AND `+
			pool.Dialect().InList("event_id", "$2"), p, ids).Scan(&n); err != nil {
			return c, err
		}
		guardados += n
	}
	q := func(sql string, args ...any) (float64, error) {
		var v float64
		err := pool.QueryRow(ctx, sql, args...).Scan(&v)
		return v, err
	}
	var err error
	val := map[string]float64{}
	for k, sql := range map[string]string{
		"events":       `SELECT count(*) FROM events`,
		"contador":     `SELECT COALESCE(sum(event_count), 0) FROM issues`,
		"issues":       `SELECT count(*) FROM issues`,
		"stats":        `SELECT COALESCE(sum(event_count), 0) FROM issue_stats`,
		"descartados":  `SELECT COALESCE(sum(count), 0) FROM dropped_events WHERE reason IN ('issue_limit', 'sampled')`,
		"repetidos":    `SELECT COALESCE(sum(count), 0) FROM dropped_events WHERE reason = 'duplicate'`,
		"etiquetasMal": `SELECT count(*) FROM issues i WHERE i.event_count <> (SELECT COALESCE(sum(count), 0) FROM issue_tag_values t WHERE t.issue_id = i.id AND t.key = 'level')`,
		"usuariosMal":  `SELECT count(*) FROM issues i WHERE i.user_count <> (SELECT count(*) FROM issue_users u WHERE u.issue_id = i.id)`,
	} {
		if val[k], err = q(sql); err != nil {
			return c, fmt.Errorf("%s: %w", k, err)
		}
	}
	checkIns, err := q(`SELECT count(*) FROM monitor_checkins WHERE `+
		pool.Dialect().InList("replace(CAST(check_in_id AS text), '-', '')", "$1"), reg.checkIns)
	if err != nil {
		return c, err
	}
	n := float64(aceptados)
	g := float64(guardados)
	c.Lineas = []linea{
		{"eventos aceptados (200) contados en issues.event_count", n, val["contador"]},
		{"eventos aceptados contados por release y entorno (issue_stats)", n, val["stats"]},
		{"eventos aceptados guardados + descartados por tope o muestreo", n, g + val["descartados"]},
		{"filas de events = eventos aceptados guardados (nada de más)", g, val["events"]},
		{"eventos guardados según /metrics", g, met.sumar("mothlog_events_saved_total")},
		{"descartados por tope o muestreo según /metrics", val["descartados"],
			met.sumar("mothlog_events_dropped_total", `reason="issue_limit"`) + met.sumar("mothlog_events_dropped_total", `reason="sampled"`)},
		{"eventos repetidos (todos los ids son nuevos)", 0, val["repetidos"]},
		{"issues = errores distintos aceptados", float64(len(reg.issues)), val["issues"]},
		{"issues cuya etiqueta level no suma su contador", 0, val["etiquetasMal"]},
		{"issues cuyo user_count no es su número de usuarios", 0, val["usuariosMal"]},
		{"check-ins aceptados guardados", float64(len(reg.checkIns)), checkIns},
	}
	c.Cuadra = true
	for _, l := range c.Lineas {
		if l.Esperado != l.Obtenido {
			c.Cuadra = false
		}
	}
	return c, nil
}

func (inf *informe) escribir(dir string) error {
	b, err := json.MarshalIndent(inf, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "informe.json"), b, 0o644); err != nil {
		return err
	}
	var w strings.Builder
	f := func(format string, args ...any) { fmt.Fprintf(&w, format, args...) }
	f("# Prueba de carga: %s\n\n", inf.Etiqueta)
	base := "Postgres 16 en otro contenedor"
	if inf.Motor == "sqlite" {
		base = "SQLite en un fichero de un directorio montado en el contenedor"
	}
	f("- Commit: %s\n- Imagen: %s, con `--cpus=%s --memory=%s`; %s\n", inf.Commit, inf.Imagen, inf.CPUs, inf.Memoria, base)
	if len(inf.Env) > 0 {
		f("- Entorno del servicio: `%s`\n", strings.Join(inf.Env, " "))
	}
	f("- Máquina: %s\n- Fecha: %s\n", inf.Maquina, inf.Fecha)
	f("- Pasos de %.0f s", inf.Duracion)
	if inf.Ritmo > 0 {
		f(" a %.0f envíos/s", inf.Ritmo)
	}
	f("; mezcla: ")
	var mz []string
	for _, t := range mezcla {
		mz = append(mz, fmt.Sprintf("%s %d %%", t.nombre, t.peso))
	}
	f("%s\n\n", strings.Join(mz, ", "))

	if a := inf.Aguanta; a != nil {
		f("**Aguanta %.0f eventos/s aceptados** (%.0f envíos/s) con %s vCPU, con p99 de %.0f ms y RSS máximo de %.1f MB (objetivo: menos de %d MB), en el paso %s.\n\n",
			a.EventosPorSegundo, a.EnviosPorSegundo, inf.CPUs, a.P99, a.RSSMaxMB, objetivoRSSMB, a.Nombre)
	} else {
		f("**Ningún paso acaba sin errores y por debajo de %d MB.**\n\n", objetivoRSSMB)
	}
	f("RSS en reposo tras arrancar: %.1f MB; pico de RSS de toda la prueba (VmHWM): %.1f MB.\n\n", inf.RSSReposoMB, inf.RSSPicoMB)

	f("| Paso | Conc. | Eventos/s | Envíos/s | p50 ms | p95 ms | p99 ms | máx ms | Códigos | CPU (%% de 1 vCPU) | Frenado por cgroup s | RSS medio MB | RSS máx MB | Pool en uso máx/tope | Esperas del pool | Espera del pool s |\n")
	f("|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, p := range inf.Pasos {
		var cs []string
		for _, k := range slices.Sorted(maps.Keys(p.Codigos)) {
			cs = append(cs, fmt.Sprintf("%s: %d", k, p.Codigos[k]))
		}
		f("| %s | %d | %.1f | %.1f | %.1f | %.1f | %.1f | %.1f | %s | %.1f | %.1f | %.1f | %.1f | %.0f/%.0f | %.0f | %.2f |\n",
			p.Nombre, p.Concurrencia, p.EventosPorSegundo, p.EnviosPorSegundo, p.P50, p.P95, p.P99, p.Max,
			strings.Join(cs, ", "), p.CPUPorcentaje, p.CPUFrenadoSegundos, p.RSSMedioMB, p.RSSMaxMB,
			p.PoolEnUsoMax, p.PoolMax, p.PoolEsperas, p.PoolEsperaSegundos)
	}
	if inf.Motor != "sqlite" {
		inf.conexiones(f)
	}
	f("\n## Lo guardado cuadra con lo aceptado\n\n| Comprobación | Esperado | En la base | |\n|---|---|---|---|\n")
	for _, l := range inf.Comprobacion.Lineas {
		ok := "ok"
		if l.Esperado != l.Obtenido {
			ok = "**NO CUADRA**"
		}
		f("| %s | %.0f | %.0f | %s |\n", l.Que, l.Esperado, l.Obtenido, ok)
	}
	if inf.Comprobacion.Cuadra {
		f("\nTodo cuadra.\n")
	} else {
		f("\n**Hay diferencias.**\n")
	}
	var perfiles []string
	for _, p := range inf.Pasos {
		if p.Perfil != "" {
			perfiles = append(perfiles, p.Perfil)
		}
	}
	if len(perfiles) > 0 {
		f("\n## Perfiles de CPU\n\n`go tool pprof -top` de cada paso: %s. El de más carga:\n\n```\n", strings.Join(perfiles, ", "))
		if b, err := os.ReadFile(filepath.Join(dir, perfiles[len(perfiles)-1])); err == nil {
			lines := strings.Split(string(b), "\n")
			f("%s\n", strings.Join(lines[:min(len(lines), 35)], "\n"))
		}
		f("```\n")
	}
	return os.WriteFile(filepath.Join(dir, "informe.md"), []byte(w.String()), 0o644)
}

// conexiones escribe en qué esperan las conexiones con Postgres en cada paso.
func (inf *informe) conexiones(f func(string, ...any)) {
	f("\n## Conexiones del servicio con Postgres\n\nMedia de conexiones en cada estado y espera por muestra de `pg_stat_activity` (cada 200 ms, sin las inactivas). `idle in transaction · Client:ClientRead` es una transacción abierta esperando al servicio; `active · Lock:…`, una esperando el cerrojo de otra.\n\n")
	for _, p := range inf.Pasos {
		f("- %s: ", p.Nombre)
		keys := slices.SortedFunc(maps.Keys(p.ConexionesBase), func(a, b string) int {
			switch {
			case p.ConexionesBase[a] > p.ConexionesBase[b]:
				return -1
			case p.ConexionesBase[a] < p.ConexionesBase[b]:
				return 1
			}
			return strings.Compare(a, b)
		})
		var es []string
		for _, k := range keys {
			es = append(es, fmt.Sprintf("%s %.2f", k, p.ConexionesBase[k]))
		}
		if len(es) == 0 {
			es = []string{"ninguna activa"}
		}
		f("%s\n", strings.Join(es, "; "))
	}
}
