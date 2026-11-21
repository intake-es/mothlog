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

// Allow es AllowUpTo con el límite por defecto.
func (l *RateLimiter) Allow(projectID int64, n int) (bool, time.Duration) {
	return l.AllowUpTo(projectID, n, 0)
}

// AllowUpTo reserva n eventos para el proyecto con perMinute por minuto, o el
// límite por defecto si es 0. Si no caben, no reserva ninguno y devuelve
// cuánto falta para la siguiente ventana.
func (l *RateLimiter) AllowUpTo(projectID int64, n, perMinute int) (bool, time.Duration) {
	if perMinute <= 0 {
		perMinute = l.perMinute
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	w := l.windows[projectID]
	if w == nil || now.Sub(w.start) >= time.Minute {
		w = &window{start: now.Truncate(time.Minute)}
		l.windows[projectID] = w
	}
	if w.count+n > perMinute {
		return false, w.start.Add(time.Minute).Sub(now)
	}
	w.count += n
	return true, 0
}
