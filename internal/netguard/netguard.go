// Package netguard decide a qué direcciones puede pedir el servicio cuando la
// URL la pone otra persona: nunca a las de metadatos de la nube ni a las
// link-local, y a las de redes privadas solo si se permite.
package netguard

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"
)

// Class es lo que es una dirección para el servicio.
type Class int

const (
	Public Class = iota
	// Private son loopback, redes privadas y CGNAT: solo si se permiten.
	Private
	// Blocked son las de metadatos, link-local y las que no son de un equipo.
	Blocked
)

var (
	// metadata son direcciones de metadatos de nubes fuera de 169.254.0.0/16.
	metadata = []netip.Prefix{
		netip.MustParsePrefix("100.100.100.200/32"),
		netip.MustParsePrefix("fd00:ec2::254/128"),
	}
	blocked4 = []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/8"),
		netip.MustParsePrefix("169.254.0.0/16"),
		netip.MustParsePrefix("192.0.0.0/24"),
		netip.MustParsePrefix("240.0.0.0/4"),
	}
	private4 = []netip.Prefix{
		netip.MustParsePrefix("100.64.0.0/10"),
		netip.MustParsePrefix("198.18.0.0/15"),
	}
	blocked6 = []netip.Prefix{
		// NAT64 de uso local: la IPv4 va en una posición que depende del prefijo.
		netip.MustParsePrefix("64:ff9b:1::/48"),
	}
	private6 = []netip.Prefix{
		// Site-local, obsoleta pero aún enrutable dentro de una red.
		netip.MustParsePrefix("fec0::/10"),
	}
	nat64  = netip.MustParsePrefix("64:ff9b::/96")
	siit   = netip.MustParsePrefix("::ffff:0:0:0/96")
	compat = netip.MustParsePrefix("::/96")
	sixTo4 = netip.MustParsePrefix("2002::/16")
)

// embedded4 es la IPv4 que lleva dentro una IPv6 de las formas que la
// traducen o la encapsulan: mapeada, compatible, SIIT, NAT64 y 6to4.
func embedded4(a netip.Addr) (netip.Addr, bool) {
	if a.Is4In6() {
		return a.Unmap(), true
	}
	if !a.Is6() {
		return a, false
	}
	b := a.As16()
	switch {
	case nat64.Contains(a), siit.Contains(a):
		return netip.AddrFrom4([4]byte(b[12:])), true
	case compat.Contains(a) && !a.IsLoopback() && !a.IsUnspecified():
		return netip.AddrFrom4([4]byte(b[12:])), true
	case sixTo4.Contains(a):
		return netip.AddrFrom4([4]byte(b[2:6])), true
	}
	return a, false
}

// Classify clasifica la dirección; las IPv4 dentro de IPv6 cuentan como la IPv4.
func Classify(a netip.Addr) Class {
	if v4, ok := embedded4(a); ok {
		a = v4
	}
	for _, p := range metadata {
		if p.Contains(a) {
			return Blocked
		}
	}
	if !a.IsValid() || a.IsUnspecified() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsMulticast() || a.IsInterfaceLocalMulticast() {
		return Blocked
	}
	if a.Is4() {
		for _, p := range blocked4 {
			if p.Contains(a) {
				return Blocked
			}
		}
		for _, p := range private4 {
			if p.Contains(a) {
				return Private
			}
		}
	}
	if a.Is6() {
		for _, p := range blocked6 {
			if p.Contains(a) {
				return Blocked
			}
		}
		for _, p := range private6 {
			if p.Contains(a) {
				return Private
			}
		}
	}
	if a.IsLoopback() || a.IsPrivate() {
		return Private
	}
	return Public
}

// ErrBlocked es una dirección a la que no se pide.
var ErrBlocked = errors.New("dirección bloqueada")

// Check dice por qué el monitor de disponibilidad no puede pedir a la
// dirección, o nil si puede.
func Check(a netip.Addr, allowPrivate bool) error {
	return Policy{AllowPrivate: allowPrivate, Env: "UPTIME_ALLOW_PRIVATE"}.Check(a)
}

// Policy es a qué direcciones se deja pedir; Env es la variable que permite
// las privadas, para nombrarla en el error.
type Policy struct {
	AllowPrivate bool
	Env          string
}

// Check dice por qué no se puede pedir a la dirección, o nil si se puede.
func (p Policy) Check(a netip.Addr) error {
	switch Classify(a) {
	case Blocked:
		return &BlockedError{Addr: a.Unmap()}
	case Private:
		if !p.AllowPrivate {
			return &BlockedError{Addr: a.Unmap(), Private: true, Env: p.Env}
		}
	}
	return nil
}

// BlockedError es una dirección a la que no se pide; es ErrBlocked.
type BlockedError struct {
	Addr    netip.Addr
	Private bool
	Env     string
}

func (e *BlockedError) Error() string {
	if e.Private {
		return fmt.Sprintf("%s: %s es de una red privada (%s=1 las permite)", ErrBlocked, e.Addr, e.Env)
	}
	return fmt.Sprintf("%s: %s es de metadatos, link-local o reservada", ErrBlocked, e.Addr)
}

func (e *BlockedError) Is(target error) bool { return target == ErrBlocked }

// Reason es el error sin la dirección, que puede ser la de un nombre interno.
func (e *BlockedError) Reason() string {
	if e.Private {
		return fmt.Sprintf("%s: es de una red privada (%s=1 las permite)", ErrBlocked, e.Env)
	}
	return fmt.Sprintf("%s: es de metadatos, link-local o reservada", ErrBlocked)
}

// Transport solo conecta a las direcciones que permite p, mirándolas justo
// antes de conectar (después de resolver el nombre), y no usa el proxy del
// entorno, que conectaría por su cuenta.
func Transport(p Policy) *http.Transport {
	d := &net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil {
				return err
			}
			return p.Check(ap.Addr())
		},
	}
	return &http.Transport{
		Proxy:                  nil,
		DialContext:            d.DialContext,
		TLSHandshakeTimeout:    10 * time.Second,
		MaxResponseHeaderBytes: 64 << 10,
		ForceAttemptHTTP2:      true,
		IdleConnTimeout:        90 * time.Second,
	}
}
