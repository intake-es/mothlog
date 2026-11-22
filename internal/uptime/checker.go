// Package uptime comprueba por HTTP las URLs de los monitores de
// disponibilidad, en un bucle dentro del propio servicio.
package uptime

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"syscall"
	"time"

	"github.com/intake-es/intake-errors/internal/netguard"
	"github.com/intake-es/intake-errors/internal/store"
)

// MaxRedirects es lo más que sigue una comprobación.
const MaxRedirects = 5

// Resolver da las direcciones de un nombre.
type Resolver func(ctx context.Context, host string) ([]netip.Addr, error)

// Checker hace las peticiones. Resuelve él mismo los nombres y solo conecta a
// direcciones que netguard permite, también tras una redirección.
type Checker struct {
	allowPrivate bool
	resolve      Resolver
	client       *http.Client
}

// NewChecker acepta resolve nil: entonces usa el DNS del sistema.
func NewChecker(allowPrivate bool, resolve Resolver) *Checker {
	c := &Checker{allowPrivate: allowPrivate, resolve: resolve}
	if c.resolve == nil {
		c.resolve = func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		}
	}
	t := &http.Transport{
		Proxy:                  nil,
		DialContext:            c.dial,
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:    10 * time.Second,
		DisableKeepAlives:      true,
		DisableCompression:     true,
		MaxResponseHeaderBytes: 64 << 10,
		ForceAttemptHTTP2:      false,
	}
	c.client = &http.Client{
		Transport: t,
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) > MaxRedirects {
				return fmt.Errorf("más de %d redirecciones", MaxRedirects)
			}
			return nil
		},
	}
	return c
}

func (c *Checker) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	var addrs []netip.Addr
	if a, err := netip.ParseAddr(host); err == nil {
		addrs = []netip.Addr{a}
	} else if addrs, err = c.resolve(ctx, host); err != nil {
		return nil, err
	}
	var firstErr error
	d := net.Dialer{Control: c.control}
	for _, a := range addrs {
		if err := netguard.Check(a, c.allowPrivate); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		conn, err := d.DialContext(ctx, network, net.JoinHostPort(a.Unmap().String(), port))
		if err == nil {
			return conn, nil
		}
		if firstErr == nil || errors.Is(firstErr, netguard.ErrBlocked) {
			firstErr = err
		}
	}
	if firstErr == nil {
		firstErr = fmt.Errorf("%s no tiene direcciones", host)
	}
	return nil, firstErr
}

// control vuelve a mirar la dirección justo antes de conectar.
func (c *Checker) control(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return err
	}
	return netguard.Check(ap.Addr(), c.allowPrivate)
}

// Check pide la URL y cierra la respuesta sin leer el cuerpo.
func (c *Checker) Check(ctx context.Context, t store.UptimeTarget, now time.Time) store.UptimeCheck {
	res := store.UptimeCheck{CheckedAt: now}
	ctx, cancel := context.WithTimeout(ctx, t.Timeout)
	defer cancel()
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, t.Method, t.URL, nil)
	if err != nil {
		res.Error = "petición no válida: " + err.Error()
		return res
	}
	req.Header.Set("User-Agent", "intake-errors-uptime")
	resp, err := c.client.Do(req)
	res.DurationMs = int(time.Since(start).Milliseconds())
	if err != nil {
		res.Error = errorText(err, t.Timeout)
		return res
	}
	_ = resp.Body.Close()
	code := resp.StatusCode
	res.StatusCode = &code
	res.OK = store.StatusMatches(t.ExpectedStatus, code)
	if !res.OK {
		res.Error = "código " + strconv.Itoa(code)
	}
	return res
}

// errorText es el error de la petición, sin la URL, para guardarlo y avisar.
func errorText(err error, timeout time.Duration) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	var dnsErr *net.DNSError
	switch {
	case errors.Is(err, netguard.ErrBlocked):
		var op *net.OpError
		if errors.As(err, &op) && op.Err != nil {
			return op.Err.Error()
		}
		return err.Error()
	case errors.Is(err, context.DeadlineExceeded), isTimeout(err):
		return "sin respuesta en " + timeout.String()
	case errors.As(err, &dnsErr):
		if dnsErr.IsNotFound {
			return "el nombre " + dnsErr.Name + " no existe"
		}
		return "DNS: " + dnsErr.Err
	case errors.Is(err, syscall.ECONNREFUSED):
		return "conexión rechazada"
	case errors.Is(err, syscall.ECONNRESET):
		return "conexión cortada"
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "conexión cerrada sin respuesta"
	}
	var op *net.OpError
	if errors.As(err, &op) && op.Err != nil {
		return op.Op + ": " + op.Err.Error()
	}
	return err.Error()
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
