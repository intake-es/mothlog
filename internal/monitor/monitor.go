// Package monitor lee los check-ins de los crons y, en un bucle dentro del
// propio servicio, marca los que no llegan (missed) y los que no acaban a
// tiempo (timeout).
//
// Formato: https://develop.sentry.dev/sdk/telemetry/check-ins/
package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/intake-es/intake-errors/internal/cron"
	"github.com/intake-es/intake-errors/internal/store"
)

// ErrMalformed es un check-in que no se puede leer.
var ErrMalformed = errors.New("check-in mal formado")

type payload struct {
	CheckInID     string         `json:"check_in_id"`
	MonitorSlug   string         `json:"monitor_slug"`
	Status        string         `json:"status"`
	Duration      *float64       `json:"duration"`
	Release       string         `json:"release"`
	Environment   string         `json:"environment"`
	MonitorConfig *monitorConfig `json:"monitor_config"`
}

type monitorConfig struct {
	Schedule      *cron.Schedule `json:"schedule"`
	CheckinMargin *float64       `json:"checkin_margin"`
	MaxRuntime    *float64       `json:"max_runtime"`
	Timezone      string         `json:"timezone"`
}

// Parse lee el payload de un item check_in.
func Parse(data []byte) (store.CheckInInput, error) {
	var p payload
	if err := json.Unmarshal(data, &p); err != nil {
		return store.CheckInInput{}, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	if strings.TrimSpace(p.MonitorSlug) == "" {
		return store.CheckInInput{}, fmt.Errorf("%w: falta monitor_slug", ErrMalformed)
	}
	in := store.CheckInInput{
		CheckInID:   normalizeID(p.CheckInID),
		Slug:        p.MonitorSlug,
		Status:      strings.ToLower(strings.TrimSpace(p.Status)),
		Duration:    p.Duration,
		Release:     strings.TrimSpace(p.Release),
		Environment: strings.TrimSpace(p.Environment),
	}
	if in.Environment == "" {
		in.Environment = "production"
	}
	if c := p.MonitorConfig; c != nil && c.Schedule != nil {
		in.Config = &store.MonitorConfig{Schedule: *c.Schedule, Timezone: strings.TrimSpace(c.Timezone),
			CheckinMargin: minutes(c.CheckinMargin), MaxRuntime: minutes(c.MaxRuntime)}
	}
	return in, nil
}

func minutes(f *float64) *int {
	if f == nil || *f <= 0 {
		return nil
	}
	n := int(*f + 0.999)
	return &n
}

// normalizeID deja el id en 32 hex en minúsculas, o vacío si no lo es.
func normalizeID(s string) string {
	s = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), "-", ""))
	if len(s) != 32 {
		return ""
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return ""
		}
	}
	return s
}

// SweepInterval es cada cuánto se buscan los check-ins que faltan o se pasan.
const SweepInterval = 15 * time.Second

// Service guarda los check-ins y marca los que faltan.
type Service struct {
	st     *store.Store
	notify store.MonitorNotifier
	log    *slog.Logger
	// Now es el reloj; se cambia en los tests.
	Now func() time.Time
}

// New acepta notify nil: entonces no avisa.
func New(st *store.Store, notify store.MonitorNotifier, log *slog.Logger) *Service {
	return &Service{st: st, notify: notify, log: log, Now: time.Now}
}

// CheckIn guarda un check-in del proyecto.
func (s *Service) CheckIn(ctx context.Context, projectID int64, in store.CheckInInput) (store.CheckInResult, error) {
	res, err := s.st.ProcessCheckIn(ctx, projectID, in, s.Now(), s.notify)
	if err != nil {
		return res, err
	}
	if res.Outcome == store.CheckInIgnored {
		s.log.InfoContext(ctx, "check-in ignorado", "proyecto", projectID, "monitor", in.Slug, "motivo", res.Reason)
	} else if res.Reason != "" {
		s.log.WarnContext(ctx, "check-in", "proyecto", projectID, "monitor", in.Slug, "aviso", res.Reason)
	}
	if res.Created {
		s.log.InfoContext(ctx, "monitor creado", "proyecto", projectID, "monitor", res.Monitor.Slug)
	}
	return res, nil
}

// Sweep marca una vez los missed y los timeout que tocan ahora.
func (s *Service) Sweep(ctx context.Context) (missed, timeouts int, err error) {
	return s.st.SweepMonitors(ctx, s.Now(), s.notify)
}

// Run repasa los monitores cada interval y borra cada hora los check-ins de
// más de retention, hasta que se cancela ctx.
func (s *Service) Run(ctx context.Context, interval, retention time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	var purged time.Time
	for {
		missed, timeouts, err := s.Sweep(ctx)
		switch {
		case err != nil && ctx.Err() == nil:
			s.log.ErrorContext(ctx, "repasar monitores", "err", err)
		case missed > 0 || timeouts > 0:
			s.log.InfoContext(ctx, "monitores", "missed", missed, "timeout", timeouts)
		}
		if now := s.Now(); retention > 0 && now.Sub(purged) >= time.Hour {
			purged = now
			if n, err := s.st.PurgeCheckIns(ctx, now.Add(-retention)); err != nil && ctx.Err() == nil {
				s.log.ErrorContext(ctx, "retención de check-ins", "err", err)
			} else if n > 0 {
				s.log.InfoContext(ctx, "retención", "check-ins", n)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
