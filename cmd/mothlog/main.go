package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/intake-es/mothlog/internal/alert"
	"github.com/intake-es/mothlog/internal/api"
	"github.com/intake-es/mothlog/internal/digest"
	"github.com/intake-es/mothlog/internal/ingest"
	"github.com/intake-es/mothlog/internal/lang"
	"github.com/intake-es/mothlog/internal/metrics"
	"github.com/intake-es/mothlog/internal/monitor"
	"github.com/intake-es/mothlog/internal/oauth"
	"github.com/intake-es/mothlog/internal/sourcemap"
	"github.com/intake-es/mothlog/internal/store"
	"github.com/intake-es/mothlog/internal/ui"
	"github.com/intake-es/mothlog/internal/uptime"
	"github.com/intake-es/mothlog/internal/webauth"
	"github.com/intake-es/mothlog/internal/webhooks"
)

type config struct {
	DatabaseURL        string
	Addr               string
	LogLevel           slog.Level
	PublicURL          string
	RateLimitPerMinute int
	RetentionDays      int
	// SourceMapUploadToken protege la subida de source maps; sin él se rechaza.
	SourceMapUploadToken string
	Alerts               alert.Options
	// UptimeAllowPrivate deja a los monitores de disponibilidad pedir a redes privadas.
	UptimeAllowPrivate  bool
	LinearWebhookSecret string
	GitHubWebhookSecret string
	// MetricsToken protege /metrics; sin él, la ruta no existe.
	MetricsToken string
	Batch        ingest.BatchOptions
	// Lang es el idioma de los avisos, del Markdown, del MCP y de llms.txt.
	Lang lang.Lang
}

func loadConfig() (config, error) {
	c := config{
		DatabaseURL: os.Getenv("DATABASE_URL"),
		Addr:        os.Getenv("LISTEN_ADDR"),
		PublicURL:   os.Getenv("PUBLIC_URL"),

		SourceMapUploadToken: os.Getenv("SOURCEMAP_UPLOAD_TOKEN"),
		Alerts: alert.Options{
			SlackWebhookURL: os.Getenv("SLACK_WEBHOOK_URL"),
			LinearAPIKey:    os.Getenv("LINEAR_API_KEY"),
			LinearAPIURL:    os.Getenv("LINEAR_API_URL"),
			LinearTeamID:    os.Getenv("LINEAR_TEAM_ID"),
			LinearLabelIDs:  splitList(os.Getenv("LINEAR_LABEL_IDS")),
			SMTPURL:         os.Getenv("SMTP_URL"),
			SMTPFrom:        os.Getenv("SMTP_FROM"),
			GitHubToken:     os.Getenv("GITHUB_TOKEN"),
			GitHubAPIURL:    os.Getenv("GITHUB_API_URL"),
			TelegramAPIURL:  os.Getenv("TELEGRAM_API_URL"),
		},
		LinearWebhookSecret: os.Getenv("LINEAR_WEBHOOK_SECRET"),
		GitHubWebhookSecret: os.Getenv("GITHUB_WEBHOOK_SECRET"),
		MetricsToken:        os.Getenv("METRICS_TOKEN"),
	}
	c.Alerts.PublicURL = c.PublicURL
	c.UptimeAllowPrivate = os.Getenv("UPTIME_ALLOW_PRIVATE") == "1"
	c.Alerts.AllowPrivate = os.Getenv(alert.AllowPrivateEnv) == "1"
	if c.DatabaseURL == "" {
		return c, errors.New("DATABASE_URL is required")
	}
	if c.Addr == "" {
		c.Addr = ":8080"
	}
	if lvl := os.Getenv("LOG_LEVEL"); lvl != "" {
		if err := c.LogLevel.UnmarshalText([]byte(lvl)); err != nil {
			return c, fmt.Errorf("LOG_LEVEL: %w", err)
		}
	}
	var err error
	if c.Lang, err = lang.Parse(os.Getenv(lang.Env)); err != nil {
		return c, err
	}
	c.Alerts.Lang = c.Lang
	if c.RateLimitPerMinute, err = positiveEnv("RATE_LIMIT_PER_MINUTE", 60); err != nil {
		return c, err
	}
	if c.RetentionDays, err = positiveEnv("RETENTION_DAYS", 90); err != nil {
		return c, err
	}
	c.Batch = ingest.DefaultBatchOptions()
	if c.Batch.MaxEvents, err = positiveEnv("INGEST_BATCH_MAX", c.Batch.MaxEvents); err != nil {
		return c, err
	}
	if v := os.Getenv("INGEST_BATCH_WAIT"); v != "" {
		if c.Batch.MaxWait, err = time.ParseDuration(v); err != nil || c.Batch.MaxWait < 0 {
			return c, fmt.Errorf("INGEST_BATCH_WAIT must be a duration like 2ms: %q", v)
		}
	}
	return c, nil
}

func splitList(s string) []string {
	var out []string
	for v := range strings.SplitSeq(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func positiveEnv(name string, def int) (int, error) {
	v := os.Getenv(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer: %q", name, v)
	}
	return n, nil
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		slog.Error("mothlog", "err", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 1 && (args[0] == "version" || args[0] == "--version") {
		return printVersion(os.Stdout)
	}
	if len(args) == 1 && (args[0] == "help" || args[0] == "--help" || args[0] == "-h") {
		_, err := fmt.Fprintln(os.Stdout, usage)
		return err
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if len(args) > 0 {
		switch args[0] {
		case "projects":
			return projects(cfg, args[1:], os.Stdout)
		case "tokens", "releases", "people":
			return runCommand(args, os.Stdout)
		case "import":
			return importCommand(cfg, args[1:], os.Stdout, os.Stderr)
		}
		return fmt.Errorf("unknown command %q\n%s", args[0], usage)
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(log)
	setMemoryLimit(log, "/sys/fs/cgroup")
	if addr := os.Getenv("PPROF_ADDR"); addr != "" {
		if !webauth.Loopback(addr) {
			return fmt.Errorf("PPROF_ADDR only works on loopback (127.0.0.1, [::1] or localhost), not on %q", addr)
		}
		go servePprof(addr, log)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return err
	}
	return serve(ctx, cfg, log, ln)
}

// serve atiende en ln hasta que se cancela ctx.
func serve(ctx context.Context, cfg config, log *slog.Logger, ln net.Listener) error {
	defer func() { _ = ln.Close() }()
	auth, err := webauth.New(webauth.LoadConfig(os.Getenv, cfg.Addr, cfg.PublicURL), log)
	if err != nil {
		return err
	}

	db, err := store.Open(ctx, cfg.DatabaseURL, log)
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetDefaultLanguage(string(cfg.Lang))
	auth.SavePeople(db)

	if cfg.SourceMapUploadToken == "" {
		log.Warn("no SOURCEMAP_UPLOAD_TOKEN: source map uploads only accept API tokens")
	}
	maps := sourcemap.New(db, log, sourcemap.Options{UploadToken: cfg.SourceMapUploadToken})
	go maps.RunRetention(ctx, time.Duration(cfg.RetentionDays)*24*time.Hour, time.Hour)

	go ingest.RunRetention(ctx, db.Pool(), cfg.RetentionDays, log)
	go db.RunEventConversion(ctx, log, time.Minute, time.Hour)
	alerts, err := alert.New(db, log, cfg.Alerts)
	if err != nil {
		return err
	}
	if len(alerts.Channels()) > 0 && cfg.PublicURL == "" {
		log.Warn("no PUBLIC_URL: alerts go without a link to the UI")
	}
	go alerts.Run(ctx)
	go digest.NewScheduler(db, log, cfg.PublicURL).Run(ctx, digest.TickInterval)
	ingester := ingest.NewIngester(db.Pool(), maps, alerts, log)
	ingester.SetBatching(cfg.Batch)
	drops := ingest.NewDrops(db)
	go drops.Run(ctx, 10*time.Second, log)
	monitors := monitor.New(db, alerts, log)
	go monitors.Run(ctx, monitor.SweepInterval, time.Duration(cfg.RetentionDays)*24*time.Hour)
	go uptime.New(db, uptime.NewChecker(cfg.UptimeAllowPrivate, nil), alerts, log).Run(ctx, uptime.TickInterval)
	var met *metrics.Metrics
	if cfg.MetricsToken != "" {
		met = metrics.New(db, log)
	}
	envelopes := ingest.NewHandler(db, ingester, ingest.NewRateLimiter(cfg.RateLimitPerMinute), drops, log).
		WithCheckIns(monitors).WithMetrics(met)

	hooks := webhooks.New(db, webhooks.Options{
		LinearSecret: cfg.LinearWebhookSecret, GitHubSecret: cfg.GitHubWebhookSecret, PublicURL: cfg.PublicURL,
		LinearAPIKey: cfg.Alerts.LinearAPIKey != "", GitHubToken: cfg.Alerts.GitHubToken != "", Log: log,
	})
	register := []func(*http.ServeMux){maps.Register, auth.Register, hooks.Register}
	if met != nil {
		register = append(register, met.Register(cfg.MetricsToken))
	}
	mux := routes(db, envelopes, register...)
	apiAuth := api.RequireAuth(api.BearerToken(db), auth.Authenticator())
	mcpAuth := apiAuth
	// OAuth en el MCP solo con PUBLIC_URL y un login en el panel con el que consentir.
	if auth.Enabled() {
		oa, err := oauth.New(db, oauth.Config{PublicURL: cfg.PublicURL, Log: log})
		if err != nil {
			return err
		}
		if oa != nil {
			oa.Register(mux, api.RequireAuth(auth.Authenticator()))
			mcpAuth = api.RequireAuthChallenge(oa.Challenge, oa.Authenticator(), api.BearerToken(db), auth.Authenticator())
			go oa.Run(ctx)
		}
	}
	if err := api.Register(mux, db, api.Options{PublicURL: cfg.PublicURL, Lang: cfg.Lang, Auth: apiAuth, MCPAuth: mcpAuth, Alerts: alerts, Integrations: hooks, Digests: alerts, Channels: alerts}); err != nil {
		return err
	}
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           ui.Handler(met.Routes(mux)),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", ln.Addr().String())
		errc <- srv.Serve(ln)
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = srv.Shutdown(shutdown)
	// Lo descartado mientras se cerraban las peticiones.
	if ferr := drops.Flush(shutdown); ferr != nil {
		log.Error("save dropped", "err", ferr)
	}
	return err
}

type pinger interface {
	Ping(context.Context) error
}

func routes(db pinger, envelopes http.Handler, register ...func(*http.ServeMux)) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle(ingest.Pattern, envelopes)
	for _, r := range register {
		r(mux)
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if err := db.Ping(ctx); err != nil {
			slog.WarnContext(ctx, "healthz: database not responding", "err", err)
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("database not responding\n"))
			return
		}
		_, _ = w.Write([]byte("ok\n"))
	})
	return mux
}
