package ingest

import (
	"sync"
	"time"
)

// RateLimiter cuenta eventos por proyecto en ventanas fijas de un minuto, en
// memoria: con varias réplicas, cada una lleva su cuenta.
type RateLimiter struct {
	perMinute int
	now       func() time.Time

	mu      sync.Mutex
	windows map[int64]*window
}

type window struct {
	start time.Time
	count int
}

func NewRateLimiter(perMinute int) *RateLimiter {
	return &RateLimiter{perMinute: perMinute, now: time.Now, windows: map[int64]*window{}}
}

// Allow reserva n eventos para el proyecto. Si no caben, no reserva ninguno y
// devuelve cuánto falta para la siguiente ventana.
func (l *RateLimiter) Allow(projectID int64, n int) (bool, time.Duration) {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	w := l.windows[projectID]
	if w == nil || now.Sub(w.start) >= time.Minute {
		w = &window{start: now.Truncate(time.Minute)}
		l.windows[projectID] = w
	}
	if w.count+n > l.perMinute {
		return false, w.start.Add(time.Minute).Sub(now)
	}
	w.count += n
	return true, 0
}
