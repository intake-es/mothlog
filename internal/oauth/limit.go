package oauth

import (
	"sync"
	"time"
)

// Cuántos registros y autorizaciones puede pedir una dirección por ventana:
// de sobra para quien conecta sus clientes, y así nadie agota el tope global.
const (
	limitWindow       = 10 * time.Minute
	maxRegistersPerIP = 30
	maxAuthorizePerIP = 120
	maxTrackedIPs     = 100000
)

// limiter cuenta las peticiones de cada dirección en ventanas fijas.
type limiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	start  time.Time
	counts map[string]int
}

func newLimiter(max int) *limiter {
	return &limiter{max: max, window: limitWindow, counts: map[string]int{}}
}

// allow apunta una petición de ip y dice si cabe en la ventana.
func (l *limiter) allow(ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.start) >= l.window || len(l.counts) >= maxTrackedIPs {
		l.start, l.counts = now, map[string]int{}
	}
	if l.counts[ip] >= l.max {
		return false
	}
	l.counts[ip]++
	return true
}
