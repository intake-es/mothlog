package uptime

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/intake-es/intake-errors/internal/store"
)

func target(u string) store.UptimeTarget {
	return store.UptimeTarget{ID: 1, URL: u, Method: http.MethodGet, Timeout: 2 * time.Second, ExpectedStatus: "200-299"}
}

// fakeDNS resuelve los nombres de names y falla con el resto.
func fakeDNS(names map[string]string) Resolver {
	return func(_ context.Context, host string) ([]netip.Addr, error) {
		if a, ok := names[host]; ok {
			return []netip.Addr{netip.MustParseAddr(a)}, nil
		}
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
}

func port(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Port()
}

func TestCheckOK(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path == "/falta" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(srv.Close)
	c := NewChecker(true, nil)
	now := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)
	res := c.Check(context.Background(), target(srv.URL+"/"), now)
	if !res.OK || res.StatusCode == nil || *res.StatusCode != 200 || res.Error != "" || !res.CheckedAt.Equal(now) {
		t.Errorf("bien = %+v", res)
	}
	res = c.Check(context.Background(), target(srv.URL+"/falta"), now)
	if res.OK || *res.StatusCode != 404 || res.Error != "código 404" {
		t.Errorf("404 = %+v", res)
	}
	tg := target(srv.URL + "/falta")
	tg.ExpectedStatus = "200-299,404"
	if res := c.Check(context.Background(), tg, now); !res.OK {
		t.Errorf("404 esperado = %+v", res)
	}
	tg = target(srv.URL + "/")
	tg.Method = http.MethodHead
	if res := c.Check(context.Background(), tg, now); !res.OK {
		t.Errorf("HEAD = %+v", res)
	}
}

// Las direcciones de metadatos y link-local no se piden nunca: ni en la URL,
// ni tras una redirección, ni si el DNS las da, aunque se permitan las privadas.
func TestCheckBlocksMetadata(t *testing.T) {
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, r.URL.Query().Get("to"), http.StatusFound)
	}))
	t.Cleanup(redirect.Close)
	c := NewChecker(true, fakeDNS(map[string]string{
		"metadata.prueba": "169.254.169.254",
		"v6.prueba":       "fe80::1",
		"mapeada.prueba":  "::ffff:169.254.169.254",
	}))
	p := port(t, redirect)
	for _, u := range []string{
		"http://169.254.169.254/latest/meta-data/",
		"http://[fe80::1]/",
		"http://[fd00:ec2::254]/latest/meta-data/",
		redirect.URL + "/?to=" + url.QueryEscape("http://169.254.169.254/latest/meta-data/iam/"),
		redirect.URL + "/?to=" + url.QueryEscape("http://[fe80::1%25eth0]/"),
		"http://metadata.prueba:" + p + "/",
		"http://v6.prueba:" + p + "/",
		"http://mapeada.prueba:" + p + "/",
		redirect.URL + "/?to=" + url.QueryEscape("http://metadata.prueba/computeMetadata/v1/"),
	} {
		res := c.Check(context.Background(), target(u), time.Now())
		if res.OK || res.StatusCode != nil || !strings.Contains(res.Error, "bloqueada") {
			t.Errorf("%s = %+v", u, res)
		}
	}
}

// Las redes privadas, solo con UPTIME_ALLOW_PRIVATE.
func TestCheckPrivateNetworks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	t.Cleanup(srv.Close)
	dns := fakeDNS(map[string]string{"interna.prueba": "127.0.0.1"})
	byName := "http://interna.prueba:" + port(t, srv) + "/"

	closed := NewChecker(false, dns)
	for _, u := range []string{srv.URL + "/", byName} {
		res := closed.Check(context.Background(), target(u), time.Now())
		if res.OK || !strings.Contains(res.Error, "red privada") {
			t.Errorf("sin permitir %s = %+v", u, res)
		}
	}
	open := NewChecker(true, dns)
	for _, u := range []string{srv.URL + "/", byName} {
		if res := open.Check(context.Background(), target(u), time.Now()); !res.OK {
			t.Errorf("permitida %s = %+v", u, res)
		}
	}
}

func TestCheckRedirectLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.URL.Query().Get("n"))
		if n > 0 {
			http.Redirect(w, r, fmt.Sprintf("/?n=%d", n-1), http.StatusFound)
		}
	}))
	t.Cleanup(srv.Close)
	c := NewChecker(true, nil)
	if res := c.Check(context.Background(), target(srv.URL+"/?n=5"), time.Now()); !res.OK {
		t.Errorf("5 redirecciones = %+v", res)
	}
	res := c.Check(context.Background(), target(srv.URL+"/?n=6"), time.Now())
	if res.OK || !strings.Contains(res.Error, "más de 5 redirecciones") {
		t.Errorf("6 redirecciones = %+v", res)
	}
}

// El cuerpo no se lee: una respuesta que no acaba no retrasa la comprobación.
func TestCheckSkipsBody(t *testing.T) {
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		chunk := make([]byte, 32<<10)
		for {
			select {
			case <-r.Context().Done():
				return
			case <-done:
				return
			default:
			}
			if _, err := w.Write(chunk); err != nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}))
	t.Cleanup(func() { close(done); srv.Close() })
	start := time.Now()
	res := NewChecker(true, nil).Check(context.Background(), target(srv.URL+"/"), time.Now())
	if !res.OK || time.Since(start) > time.Second {
		t.Errorf("cuerpo sin fin = %+v en %s", res, time.Since(start))
	}
}

func TestCheckTimeoutAndRefused(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	t.Cleanup(slow.Close)
	c := NewChecker(true, nil)
	tg := target(slow.URL + "/")
	tg.Timeout = 300 * time.Millisecond
	res := c.Check(context.Background(), tg, time.Now())
	if res.OK || res.Error != "sin respuesta en 300ms" || res.DurationMs < 300 {
		t.Errorf("lento = %+v", res)
	}

	gone := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	u := gone.URL
	gone.Close()
	res = c.Check(context.Background(), target(u+"/"), time.Now())
	if res.OK || res.Error != "conexión rechazada" {
		t.Errorf("caído = %+v", res)
	}
}
