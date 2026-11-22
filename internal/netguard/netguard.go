// Package netguard decide a qué direcciones puede pedir el servicio cuando la
// URL la pone otra persona: nunca a las de metadatos de la nube ni a las
// link-local, y a las de redes privadas solo si se permite.
package netguard

import (
	"errors"
	"fmt"
	"net/netip"
)

// Class es lo que es una dirección para el servicio.
type Class int

const (
	Public Class = iota
	// Private son loopback, redes privadas y CGNAT: solo con UPTIME_ALLOW_PRIVATE.
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
	nat64 = netip.MustParsePrefix("64:ff9b::/96")
)

// Classify clasifica la dirección; las IPv4 dentro de IPv6 cuentan como la IPv4.
func Classify(a netip.Addr) Class {
	a = a.Unmap()
	if a.Is6() && nat64.Contains(a) {
		b := a.As16()
		a = netip.AddrFrom4([4]byte(b[12:]))
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
	if a.IsLoopback() || a.IsPrivate() {
		return Private
	}
	return Public
}

// ErrBlocked es una dirección a la que no se pide.
var ErrBlocked = errors.New("dirección bloqueada")

// Check dice por qué no se puede pedir a la dirección, o nil si se puede.
func Check(a netip.Addr, allowPrivate bool) error {
	switch Classify(a) {
	case Blocked:
		return fmt.Errorf("%w: %s es de metadatos, link-local o reservada", ErrBlocked, a.Unmap())
	case Private:
		if !allowPrivate {
			return fmt.Errorf("%w: %s es de una red privada (UPTIME_ALLOW_PRIVATE=1 las permite)", ErrBlocked, a.Unmap())
		}
	}
	return nil
}
