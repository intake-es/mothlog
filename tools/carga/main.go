// Command carga mide la ingesta: levanta el servicio con la imagen del
// Dockerfile en un contenedor con la CPU y la memoria limitadas y un Postgres
// 16 aparte (o, con -motor sqlite, un fichero de SQLite), le manda envíos de los fixtures por pasos de concurrencia o a un
// ritmo fijo, y saca un informe. Al acabar borra lo que ha creado.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/intake-es/mothlog/internal/db"
)

type opciones struct {
	imagen        string
	construir     bool
	conservar     bool
	ref           string
	concurrencias []int
	ritmo         float64
	duracion      time.Duration
	calentamiento time.Duration
	salida        string
	cpus, memoria string
	postgres      string
	motor         string
	env           listaFlag
	perfil        bool
	fixtures      string
	sourcemaps    string
	cola          int
	semilla       uint64
	etiqueta      string
}

type listaFlag []string

func (l *listaFlag) String() string     { return strings.Join(*l, ",") }
func (l *listaFlag) Set(v string) error { *l = append(*l, v); return nil }

const (
	tokenMetricas = "carga-metricas"
	tokenMapas    = "carga-sourcemaps"
	imagenProxy   = "caddy:2-alpine"
	// puertoPprof es donde el proxy publica el pprof del servicio.
	puertoPprof = "6061"
)

func main() {
	var o opciones
	var conc string
	flag.StringVar(&o.imagen, "imagen", "mothlog-carga:local", "imagen del servicio")
	flag.BoolVar(&o.construir, "construir", true, "construir la imagen con el Dockerfile antes de medir")
	flag.BoolVar(&o.conservar, "conservar-imagen", false, "no borrar al acabar la imagen construida, para repetir la medida con -construir=false")
	flag.StringVar(&o.ref, "ref", "", "construir la imagen con el código de esta referencia de git en vez del directorio de trabajo")
	flag.StringVar(&conc, "concurrencia", "1,4,16,64", "envíos a la vez de cada paso, separados por comas")
	flag.Float64Var(&o.ritmo, "ritmo", 0, "envíos por segundo; con 0, cada paso manda tan rápido como responde el servicio")
	flag.DurationVar(&o.duracion, "duracion", 20*time.Second, "duración de cada paso")
	flag.DurationVar(&o.calentamiento, "calentamiento", 5*time.Second, "envíos antes de medir, que no salen en el informe")
	flag.StringVar(&o.salida, "salida", "carga-salida", "directorio del informe")
	flag.StringVar(&o.cpus, "cpus", "0.25", "--cpus del contenedor del servicio")
	flag.StringVar(&o.memoria, "memoria", "256m", "--memory del contenedor del servicio")
	flag.StringVar(&o.postgres, "postgres", "postgres:16", "imagen de Postgres")
	flag.StringVar(&o.motor, "motor", "postgres", "base de datos del servicio: postgres o sqlite")
	flag.Var(&o.env, "e", "variable de entorno más para el servicio, CLAVE=valor (se puede repetir)")
	flag.BoolVar(&o.perfil, "perfil", true, "sacar un perfil de CPU de cada paso (hace falta PPROF_ADDR en el servicio)")
	flag.StringVar(&o.fixtures, "fixtures", "testdata/envelopes", "directorio de los envíos grabados")
	flag.StringVar(&o.sourcemaps, "sourcemaps", "testdata/sourcemaps", "source maps del envío del navegador")
	flag.IntVar(&o.cola, "cola", 5000, "cuántos errores distintos puede haber en la cola")
	flag.Uint64Var(&o.semilla, "semilla", 1, "semilla de la mezcla")
	flag.StringVar(&o.etiqueta, "etiqueta", "", "nombre de la medida en el informe (por defecto, la ref o el commit)")
	flag.Parse()
	if o.motor != "postgres" && o.motor != "sqlite" {
		fmt.Fprintf(os.Stderr, "carga: -motor es postgres o sqlite, no %q\n", o.motor)
		os.Exit(2)
	}
	for v := range strings.SplitSeq(conc, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || n <= 0 {
			fmt.Fprintf(os.Stderr, "carga: concurrencia %q\n", v)
			os.Exit(2)
		}
		o.concurrencias = append(o.concurrencias, n)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, o); err != nil {
		fmt.Fprintln(os.Stderr, "carga:", err)
		os.Exit(1)
	}
}

func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "%s "+format+"\n", append([]any{time.Now().Format("15:04:05")}, args...)...)
}

func run(ctx context.Context, o opciones) error {
	if err := os.MkdirAll(o.salida, 0o755); err != nil {
		return err
	}
	gen, err := nuevoGenerador(o.fixtures, o.cola, o.semilla)
	if err != nil {
		return err
	}
	if o.etiqueta == "" {
		o.etiqueta = o.ref
		if o.etiqueta == "" {
			o.etiqueta = "directorio de trabajo"
		}
	}
	commit := gitCommit(ctx, o.ref)
	env := &entorno{}
	defer env.limpiar()
	if o.construir {
		logf("construyendo %s (%s)", o.imagen, o.etiqueta)
		if err := construir(ctx, o.imagen, o.ref); err != nil {
			return err
		}
		if !o.conservar {
			env.imagen = o.imagen
		}
	}
	id := "ie-carga-" + hexID()[:8]
	if err := env.crearRed(ctx, id); err != nil {
		return err
	}
	var pool db.DB
	var sqlitePath string
	dbURL := "postgres://postgres:x@db:5432/postgres?sslmode=disable"
	var dbArgs []string
	switch o.motor {
	case "sqlite":
		// El fichero, en un directorio del anfitrión, y el servicio con el
		// usuario de aquí, para poder abrirlo también desde aquí y comprobarlo.
		dir, err := os.MkdirTemp("", id+"-")
		if err != nil {
			return err
		}
		env.dirs = append(env.dirs, dir)
		dbURL = "sqlite:///data/mothlog.db"
		dbArgs = []string{"-v", dir + ":/data", "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())}
		defer func() {
			if pool != nil {
				pool.Close()
			}
		}()
		sqlitePath = filepath.Join(dir, "mothlog.db")
	default:
		dbc := id + "-db"
		if err := env.arrancar(ctx, dbc, "--network", id, "--network-alias", "db", "-p", "127.0.0.1::5432",
			"-e", "POSTGRES_PASSWORD=x", o.postgres); err != nil {
			return err
		}
		dbAddr, err := puerto(ctx, dbc, "5432/tcp")
		if err != nil {
			return err
		}
		if pool, err = esperarPostgres(ctx, "postgres://postgres:x@"+dbAddr+"/postgres?sslmode=disable&application_name=carga"); err != nil {
			return err
		}
		defer pool.Close()
	}

	svc := id + "-svc"
	args := []string{"--network", id, "--cpus", o.cpus, "--memory", o.memoria,
		"-p", "127.0.0.1::8000", "-p", "127.0.0.1::" + puertoPprof,
		"-e", "DATABASE_URL=" + dbURL,
		"-e", "LISTEN_ADDR=:8000",
		"-e", "RATE_LIMIT_PER_MINUTE=1000000000",
		"-e", "METRICS_TOKEN=" + tokenMetricas,
		"-e", "SOURCEMAP_UPLOAD_TOKEN=" + tokenMapas,
		"-e", "PPROF_ADDR=127.0.0.1:6060",
		"-e", "LOG_LEVEL=warn",
	}
	args = append(args, dbArgs...)
	for _, e := range o.env {
		args = append(args, "-e", e)
	}
	args = append(args, o.imagen)
	logf("arrancando el servicio con --cpus=%s --memory=%s", o.cpus, o.memoria)
	if err := env.arrancar(ctx, svc, args...); err != nil {
		return err
	}
	defer guardarLogs(svc, filepath.Join(o.salida, "servicio.log"))
	svcAddr, err := puerto(ctx, svc, "8000/tcp")
	if err != nil {
		return err
	}
	// PPROF_ADDR solo vale en loopback: un proxy en la red del servicio lo
	// publica, como el de los compose.
	if err := env.arrancar(ctx, svc+"-pprof", "--network", "container:"+svc, imagenProxy,
		"caddy", "reverse-proxy", "--from", ":"+puertoPprof, "--to", "127.0.0.1:6060"); err != nil {
		return err
	}
	pprofAddr, err := puerto(ctx, svc, puertoPprof+"/tcp")
	if err != nil {
		return err
	}
	base := "http://" + svcAddr
	if err := esperarHTTP(ctx, base+"/healthz"); err != nil {
		return err
	}
	proc, err := nuevoProceso(ctx, svc)
	if err != nil {
		return err
	}
	if o.motor == "sqlite" {
		// Ya ha creado el esquema; desde aquí se abre como las órdenes, sin cerrojo.
		if pool, err = db.OpenSQLite(ctx, "sqlite://"+sqlitePath, db.SQLiteOptions{}); err != nil {
			return err
		}
	}
	if err := sembrar(ctx, pool, base, o.sourcemaps); err != nil {
		return err
	}

	cl := &http.Client{
		Timeout: time.Minute,
		Transport: &http.Transport{
			MaxIdleConns: 1024, MaxIdleConnsPerHost: 1024, DisableCompression: true,
		},
	}
	reg := newRegistro()
	inf := &informe{
		Etiqueta: o.etiqueta, Commit: commit, Motor: o.motor, Imagen: o.imagen, CPUs: o.cpus, Memoria: o.memoria,
		Duracion: o.duracion.Seconds(), Ritmo: o.ritmo, Maquina: maquina(ctx), Fecha: time.Now().Format(time.RFC3339),
		Env: o.env,
	}
	mon := &monitor{proc: proc, pool: pool, base: base, cl: cl}
	if rss, ok := proc.rss(); ok {
		inf.RSSReposoMB = rss / 1e6
	}

	if o.calentamiento > 0 {
		logf("calentando %s", o.calentamiento)
		cargar(ctx, cl, base, gen, reg, o.concurrencias[0], 0, o.calentamiento)
	}
	pasos := o.concurrencias
	if o.ritmo > 0 {
		pasos = []int{o.concurrencias[len(o.concurrencias)-1]}
	}
	for i, c := range pasos {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		nombre := fmt.Sprintf("paso-%d-c%d", i+1, c)
		logf("%s: concurrencia %d, ritmo %g, %s", nombre, c, o.ritmo, o.duracion)
		perfil := make(chan string, 1)
		if o.perfil {
			go func() { perfil <- sacarPerfil(ctx, "http://"+pprofAddr, o.duracion, o.salida, nombre) }()
		} else {
			perfil <- ""
		}
		mon.empezar(ctx)
		p := cargar(ctx, cl, base, gen, reg, c, o.ritmo, o.duracion)
		mon.acabar(&p)
		p.Nombre = nombre
		p.Perfil = <-perfil
		inf.Pasos = append(inf.Pasos, p)
		logf("%s: %.1f eventos/s aceptados, p50 %.0f ms, p99 %.0f ms, RSS máx %.1f MB, CPU %.0f%% de una vCPU",
			nombre, p.EventosPorSegundo, p.P50, p.P99, p.RSSMaxMB, p.CPUPorcentaje)
	}
	if hwm, ok := proc.pico(); ok {
		inf.RSSPicoMB = hwm / 1e6
	}

	// Los descartes se guardan en la base cada 10 s.
	logf("esperando a que el servicio guarde los descartes")
	select {
	case <-time.After(12 * time.Second):
	case <-ctx.Done():
		return ctx.Err()
	}
	inf.Metricas = leerMetricas(ctx, cl, base)
	inf.Comprobacion, err = comprobar(ctx, pool, reg, inf.Metricas)
	if err != nil {
		return err
	}
	inf.resumir()
	if err := inf.escribir(o.salida); err != nil {
		return err
	}
	logf("informe en %s", filepath.Join(o.salida, "informe.md"))
	if !inf.Comprobacion.Cuadra {
		return errors.New("lo guardado en la base no cuadra con lo aceptado")
	}
	return nil
}

func gitCommit(ctx context.Context, ref string) string {
	if ref == "" {
		ref = "HEAD"
	}
	out, err := exec.CommandContext(ctx, "git", "rev-parse", "--short", ref).Output()
	if err != nil {
		return ""
	}
	c := strings.TrimSpace(string(out))
	if ref == "HEAD" {
		if st, _ := exec.CommandContext(ctx, "git", "status", "--porcelain", "--untracked-files=no").Output(); len(st) > 0 {
			c += " con cambios sin commit"
		}
	}
	return c
}

func guardarLogs(contenedor, fichero string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	f, err := os.Create(fichero)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	cmd := exec.CommandContext(ctx, "docker", "logs", contenedor)
	cmd.Stdout, cmd.Stderr = f, f
	_ = cmd.Run()
}

func esperarPostgres(ctx context.Context, url string) (db.DB, error) {
	deadline := time.Now().Add(time.Minute)
	for {
		pool, err := db.OpenPostgres(ctx, url)
		if err == nil {
			if err = pool.Ping(ctx); err == nil {
				return pool, nil
			}
			pool.Close()
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("postgres no arranca: %w", err)
		}
		select {
		case <-time.After(300 * time.Millisecond):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func esperarHTTP(ctx context.Context, url string) error {
	deadline := time.Now().Add(2 * time.Minute)
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			err = fmt.Errorf("código %d", resp.StatusCode)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("el servicio no arranca: %w", err)
		}
		select {
		case <-time.After(300 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// maquina describe dónde se mide, para poder comparar informes.
func maquina(ctx context.Context) string {
	cpu := ""
	if b, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for l := range strings.SplitSeq(string(b), "\n") {
			if k, v, ok := strings.Cut(l, ":"); ok && strings.TrimSpace(k) == "model name" {
				cpu = strings.TrimSpace(v)
				break
			}
		}
	}
	kernel, _ := os.ReadFile("/proc/sys/kernel/osrelease")
	dv, _ := docker(ctx, "version", "-f", "{{.Server.Version}}")
	ncpu, _ := docker(ctx, "info", "-f", "{{.NCPU}} CPU, {{.MemTotal}} B")
	return fmt.Sprintf("%s; anfitrión de docker con %s; kernel %s; docker %s", cpu, ncpu, strings.TrimSpace(string(kernel)), dv)
}
