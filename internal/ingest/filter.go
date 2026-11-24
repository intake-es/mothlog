package ingest

import (
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/intake-es/mothlog/internal/event"
	"github.com/intake-es/mothlog/internal/store"
)

var extensionSchemes = []string{
	"chrome-extension://", "moz-extension://", "safari-extension://", "safari-web-extension://",
	"ms-browser-extension://", "edge-extension://",
}

// crawlers reconoce los user agents que se declaran bots, rastreadores o
// comprobadores de disponibilidad, por palabras genéricas. Un nombre que acaba
// en bot solo cuenta con versión o separador detrás (NombreBot/2.1, NombreBot-Image), para no
// confundirlo con modelos de móvil que acaban en «bot».
var crawlers = regexp.MustCompile(`(?i)(\bbots?\b|[a-z]bots?[/;)-]|crawl|spider|slurp|scraper|archiver|externalhit|fetcher|uptime|healthcheck)`)

// Filter son los filtros de un proyecto listos para aplicar.
type Filter struct {
	f        store.Filters
	messages []*regexp.Regexp
	releases []*regexp.Regexp
}

// NewFilter compila los filtros; los que no compilan se ignoran, aunque el
// store no deja guardarlos.
func NewFilter(f store.Filters) *Filter {
	out := &Filter{f: f}
	for _, m := range f.Messages {
		if re, err := regexp.Compile(m); err == nil {
			out.messages = append(out.messages, re)
		}
	}
	for _, r := range f.Releases {
		out.releases = append(out.releases, glob(r))
	}
	return out
}

func glob(p string) *regexp.Regexp {
	parts := strings.Split(p, "*")
	for i, s := range parts {
		parts[i] = regexp.QuoteMeta(s)
	}
	return regexp.MustCompile(`^` + strings.Join(parts, `.*`) + `$`)
}

// Drop dice por qué se descarta el evento, o "" si pasa.
func (fl *Filter) Drop(ev *event.Event) string {
	f := fl.f
	switch {
	case len(f.Environments) > 0 && ev.Environment != "" && slices.Contains(f.Environments, ev.Environment):
		return store.DropEnvironment
	case ev.Release != "" && slices.ContainsFunc(fl.releases, func(re *regexp.Regexp) bool { return re.MatchString(ev.Release) }):
		return store.DropRelease
	case len(f.ExceptionTypes) > 0 && hasExceptionType(ev, f.ExceptionTypes):
		return store.DropExceptionType
	case len(fl.messages) > 0 && matchesMessage(ev, fl.messages):
		return store.DropMessage
	case f.BrowserExtensions && fromExtension(ev):
		return store.DropBrowserExtension
	case f.WebCrawlers && ev.Request != nil && crawlers.MatchString(ev.Request.UserAgent):
		return store.DropWebCrawler
	case f.Localhost && fromLocalhost(ev):
		return store.DropLocalhost
	}
	return ""
}

// DropSession dice si las sesiones de esa release y ese entorno se ignoran,
// con los mismos filtros de entorno y release que los eventos.
func (fl *Filter) DropSession(release, environment string) bool {
	if environment != "" && slices.Contains(fl.f.Environments, environment) {
		return true
	}
	return slices.ContainsFunc(fl.releases, func(re *regexp.Regexp) bool { return re.MatchString(release) })
}

func hasExceptionType(ev *event.Event, types []string) bool {
	for _, x := range ev.Exceptions {
		if x.Type == "" {
			continue
		}
		if slices.Contains(types, x.Type) || (x.Module != "" && slices.Contains(types, x.Module+"."+x.Type)) {
			return true
		}
	}
	return false
}

// messageTexts son el mensaje y, de cada excepción, «Tipo: valor» y el valor solo.
func messageTexts(ev *event.Event) []string {
	var out []string
	if ev.Message != "" {
		out = append(out, ev.Message)
	}
	for _, x := range ev.Exceptions {
		switch {
		case x.Type != "" && x.Value != "":
			out = append(out, x.Type+": "+x.Value, x.Value)
		case x.Type != "":
			out = append(out, x.Type)
		case x.Value != "":
			out = append(out, x.Value)
		}
	}
	return out
}

func matchesMessage(ev *event.Event, res []*regexp.Regexp) bool {
	for _, s := range messageTexts(ev) {
		for _, re := range res {
			if re.MatchString(s) {
				return true
			}
		}
	}
	return false
}

func isExtension(s string) bool {
	s = strings.ToLower(s)
	for _, p := range extensionSchemes {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}

func fromExtension(ev *event.Event) bool {
	if slices.ContainsFunc(messageTexts(ev), isExtension) {
		return true
	}
	for _, x := range ev.Exceptions {
		for _, fr := range x.Frames {
			if isExtension(fr.Filename) || isExtension(fr.AbsPath) {
				return true
			}
		}
	}
	return false
}

func fromLocalhost(ev *event.Event) bool {
	if ev.User != nil {
		if ip, err := netip.ParseAddr(ev.User.IPAddress); err == nil && (ip.IsLoopback() || ip.IsUnspecified()) {
			return true
		}
	}
	if ev.Request == nil || ev.Request.URL == "" {
		return false
	}
	u, err := url.Parse(ev.Request.URL)
	if err != nil {
		return false
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && (ip.IsLoopback() || ip.IsUnspecified())
}
