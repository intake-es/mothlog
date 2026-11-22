package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/intake-es/intake-errors/internal/alert"
	"github.com/intake-es/intake-errors/internal/api"
	"github.com/intake-es/intake-errors/internal/ingest"
	"github.com/intake-es/intake-errors/internal/sourcemap"
	"github.com/intake-es/intake-errors/internal/store"
	"github.com/intake-es/intake-errors/internal/ui"
	"github.com/intake-es/intake-errors/internal/webauth"
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
		},
	}
	c.Alerts.PublicURL = c.PublicURL
	if c.DatabaseURL == "" {
		return c, errors.New("falta DATABASE_URL")
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
	if c.RateLimitPerMinute, err = positiveEnv("RATE_LIMIT_PER_MINUTE", 60); err != nil {
		return c, err
	}
	if c.RetentionDays, err = positiveEnv("RETENTION_DAYS", 90); err != nil {
		return c, err
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
		return 0, fmt.Errorf("%s debe ser un entero positivo: %q", name, v)
	}
	return n, nil
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		slog.Error("intake-errors", "err", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if len(args) > 0 {
		switch args[0] {
		case "projects":
			return projects(cfg, args[1:], os.Stdout)
		case "tokens", "releases":
			return runCommand(args, os.Stdout)
		}
		return fmt.Errorf("comando desconocido %q\n%s", args[0], usage)
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(log)

	auth, err := webauth.New(webauth.LoadConfig(os.Getenv, cfg.Addr, cfg.PublicURL), log)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := store.Open(ctx, cfg.DatabaseURL, log)
	if err != nil {
		return err
	}
	defer db.Close()

	if cfg.SourceMapUploadToken == "" {
		log.Warn("sin SOURCEMAP_UPLOAD_TOKEN: la subida de source maps solo acepta tokens de la API")
	}
	maps := sourcemap.New(db, log, sourcemap.Options{UploadToken: cfg.SourceMapUploadToken})
	go maps.RunRetention(ctx, time.Duration(cfg.RetentionDays)*24*time.Hour, time.Hour)

	go ingest.RunRetention(ctx, db.Pool(), cfg.RetentionDays, log)
	alerts, err := alert.New(db, log, cfg.Alerts)
	if err != nil {
		return err
	}
	if len(alerts.Channels()) > 0 && cfg.PublicURL == "" {
		log.Warn("sin PUBLIC_URL: los avisos van sin enlace a la interfaz")
	}
	go alerts.Run(ctx)
	ingester := ingest.NewIngester(db.Pool(), maps, alerts, log)
	drops := ingest.NewDrops(db)
	go drops.Run(ctx, 10*time.Second, log)
	envelopes := ingest.NewHandler(db, ingester, ingest.NewRateLimiter(cfg.RateLimitPerMinute), drops, log)

	mux := routes(db, envelopes, maps.Register, auth.Register)
	apiAuth := api.RequireAuth(api.BearerToken(db), auth.Authenticator())
	if err := api.Register(mux, db, api.Options{PublicURL: cfg.PublicURL, Auth: apiAuth}); err != nil {
		return err
	}
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           ui.Handler(mux),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	errc := make(chan error, 1)
	go func() {
		log.Info("escuchando", "addr", cfg.Addr)
		errc <- srv.ListenAndServe()
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
		log.Error("guardar descartados", "err", ferr)
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
			slog.WarnContext(ctx, "healthz: postgres no responde", "err", err)
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("postgres no responde\n"))
			return
		}
		_, _ = w.Write([]byte("ok\n"))
	})
	return mux
}
