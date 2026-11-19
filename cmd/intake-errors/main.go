package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/intake-es/intake-errors/internal/store"
)

type config struct {
	DatabaseURL string
	Addr        string
	LogLevel    slog.Level
}

func loadConfig() (config, error) {
	c := config{
		DatabaseURL: os.Getenv("DATABASE_URL"),
		Addr:        os.Getenv("LISTEN_ADDR"),
	}
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
	return c, nil
}

func main() {
	if err := run(); err != nil {
		slog.Error("intake-errors", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := store.Open(ctx, cfg.DatabaseURL, log)
	if err != nil {
		return err
	}
	defer db.Close()

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           routes(db),
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
	return srv.Shutdown(shutdown)
}

type pinger interface {
	Ping(context.Context) error
}

func routes(db pinger) http.Handler {
	mux := http.NewServeMux()
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
