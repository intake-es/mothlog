package netguard

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
)

func TestClassify(t *testing.T) {
	for _, c := range []struct {
		addr string
		want Class
	}{
		{"169.254.169.254", Blocked},
		{"169.254.0.1", Blocked},
		{"::ffff:169.254.169.254", Blocked},
		{"64:ff9b::a9fe:a9fe", Blocked},
		{"fe80::1", Blocked},
		{"fd00:ec2::254", Blocked},
		{"100.100.100.200", Blocked},
		{"0.0.0.0", Blocked},
		{"::", Blocked},
		{"224.0.0.1", Blocked},
		{"255.255.255.255", Blocked},
		{"127.0.0.1", Private},
		{"::1", Private},
		{"10.1.2.3", Private},
		{"172.16.0.1", Private},
		{"192.168.1.1", Private},
		{"100.64.0.1", Private},
		{"fd12::1", Private},
		{"::ffff:10.0.0.1", Private},
		{"::7f00:1", Private},
		{"::a9fe:a9fe", Blocked},
		{"::ffff:0:a9fe:a9fe", Blocked},
		{"::ffff:0:7f00:1", Private},
		{"64:ff9b:1::a9fe:a9fe", Blocked},
		{"64:ff9b:1::808:808", Blocked},
		{"2002:a9fe:a9fe::1", Blocked},
		{"2002:7f00:1::1", Private},
		{"2002:808:808::1", Public},
		{"fec0::1", Private},
		{"::808:808", Public},
		{"8.8.8.8", Public},
		{"2606:4700::1111", Public},
	} {
		if got := Classify(netip.MustParseAddr(c.addr)); got != c.want {
			t.Errorf("Classify(%s) = %d; quería %d", c.addr, got, c.want)
		}
	}
}

func TestCheck(t *testing.T) {
	meta := netip.MustParseAddr("169.254.169.254")
	if err := Check(meta, true); !errors.Is(err, ErrBlocked) {
		t.Errorf("metadatos con redes privadas permitidas: %v", err)
	}
	local := netip.MustParseAddr("127.0.0.1")
	if err := Check(local, false); !errors.Is(err, ErrBlocked) {
		t.Errorf("loopback sin permitir: %v", err)
	}
	if err := Check(local, true); err != nil {
		t.Errorf("loopback permitido: %v", err)
	}
	if err := Check(netip.MustParseAddr("1.1.1.1"), false); err != nil {
		t.Errorf("pública: %v", err)
	}
}

func TestTransport(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1) }))
	defer srv.Close()
	// Un proxy del entorno que conectaría por su cuenta no se usa.
	t.Setenv("HTTP_PROXY", srv.URL)
	t.Setenv("NO_PROXY", "")

	get := func(p Policy, url string) error {
		c := &http.Client{Transport: Transport(p)}
		resp, err := c.Get(url)
		if err == nil {
			_ = resp.Body.Close()
		}
		return err
	}
	err := get(Policy{Env: "PRUEBA_ALLOW_PRIVATE"}, srv.URL)
	if !errors.Is(err, ErrBlocked) || !strings.Contains(err.Error(), "PRUEBA_ALLOW_PRIVATE=1") {
		t.Fatalf("loopback sin permiso: %v", err)
	}
	var be *BlockedError
	if !errors.As(err, &be) || strings.Contains(be.Reason(), "127.0.0.1") {
		t.Fatalf("Reason no debe llevar la dirección: %v", err)
	}
	if err := get(Policy{Env: "PRUEBA_ALLOW_PRIVATE"}, "http://169.254.169.254/"); !errors.Is(err, ErrBlocked) {
		t.Fatalf("metadatos: %v", err)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("el servidor ha recibido %d peticiones", n)
	}
	if err := get(Policy{AllowPrivate: true}, srv.URL); err != nil || hits.Load() != 1 {
		t.Fatalf("loopback con permiso: %v, %d peticiones", err, hits.Load())
	}
	if err := get(Policy{AllowPrivate: true}, "http://[::ffff:169.254.169.254]/"); !errors.Is(err, ErrBlocked) {
		t.Fatalf("metadatos con permiso: %v", err)
	}
}
