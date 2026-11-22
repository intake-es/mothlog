package netguard

import (
	"errors"
	"net/netip"
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
