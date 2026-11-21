package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/intake-es/intake-errors/internal/store"
)

const filtersUsage = `claves de projects filters <nombre> set:
  messages <regex>…             descarta los eventos cuyo mensaje o excepción casa con alguna
  exception_types <tipo>…       descarta las excepciones de estos tipos
  environments <entorno>…       descarta los eventos de estos entornos
  releases <release>…           descarta los de estos releases; * es un comodín
  browser_extensions on|off     errores de extensiones del navegador
  web_crawlers on|off           user agents de bots y rastreadores
  localhost on|off              localhost y direcciones locales
  events_per_issue_per_hour <n>|default
  sample_rate <0..1>
  rate_limit_per_minute <n>|default
sin valores, una lista se vacía`

func projectByName(ctx context.Context, db *store.Store, name string) (store.Project, error) {
	p, err := db.ProjectByName(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		return p, fmt.Errorf("no hay ningún proyecto llamado %q", name)
	}
	return p, err
}

// filtersCommand enseña los filtros de un proyecto o cambia uno: args es
// <nombre> [set <clave> [<valor>…]].
func filtersCommand(ctx context.Context, db *store.Store, args []string, out io.Writer) error {
	p, err := projectByName(ctx, db, args[0])
	if err != nil {
		return err
	}
	f, err := db.Filters(ctx, p.ID)
	if err != nil {
		return err
	}
	if len(args) > 1 {
		if err := setFilter(&f, args[2], args[3:]); err != nil {
			return err
		}
		if f, err = db.SetFilters(ctx, p.ID, f); err != nil {
			return err
		}
	}
	return printFilters(out, f)
}

func setFilter(f *store.Filters, key string, vals []string) error {
	one := func() (string, error) {
		if len(vals) != 1 {
			return "", fmt.Errorf("%s lleva un solo valor\n%s", key, filtersUsage)
		}
		return vals[0], nil
	}
	onOff := func(dst *bool) error {
		v, err := one()
		if err != nil {
			return err
		}
		switch v {
		case "on":
			*dst = true
		case "off":
			*dst = false
		default:
			return fmt.Errorf("%s es on u off, no %q", key, v)
		}
		return nil
	}
	optInt := func(dst **int) error {
		v, err := one()
		if err != nil {
			return err
		}
		if v == "default" {
			*dst = nil
			return nil
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("%s es un número o default, no %q", key, v)
		}
		*dst = &n
		return nil
	}
	list := func() []string { return append([]string{}, vals...) }
	switch key {
	case "messages":
		f.Messages = list()
	case "exception_types":
		f.ExceptionTypes = list()
	case "environments":
		f.Environments = list()
	case "releases":
		f.Releases = list()
	case "browser_extensions":
		return onOff(&f.BrowserExtensions)
	case "web_crawlers":
		return onOff(&f.WebCrawlers)
	case "localhost":
		return onOff(&f.Localhost)
	case "events_per_issue_per_hour":
		return optInt(&f.EventsPerIssuePerHour)
	case "rate_limit_per_minute":
		return optInt(&f.RateLimitPerMinute)
	case "sample_rate":
		v, err := one()
		if err != nil {
			return err
		}
		if f.SampleRate, err = strconv.ParseFloat(v, 64); err != nil {
			return fmt.Errorf("sample_rate es un número de 0 a 1, no %q", v)
		}
	default:
		return fmt.Errorf("clave desconocida %q\n%s", key, filtersUsage)
	}
	return nil
}

func printFilters(out io.Writer, f store.Filters) error {
	onOff := func(b bool) string {
		if b {
			return "on"
		}
		return "off"
	}
	list := func(l []string) string {
		if len(l) == 0 {
			return "-"
		}
		return strings.Join(l, "\n\t")
	}
	perIssue := strconv.Itoa(f.PerIssuePerHour())
	if f.EventsPerIssuePerHour == nil {
		perIssue += " (por defecto)"
	}
	perMinute := "RATE_LIMIT_PER_MINUTE"
	if f.RateLimitPerMinute != nil {
		perMinute = strconv.Itoa(*f.RateLimitPerMinute)
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, row := range [][2]string{
		{"messages", list(f.Messages)},
		{"exception_types", list(f.ExceptionTypes)},
		{"environments", list(f.Environments)},
		{"releases", list(f.Releases)},
		{"browser_extensions", onOff(f.BrowserExtensions)},
		{"web_crawlers", onOff(f.WebCrawlers)},
		{"localhost", onOff(f.Localhost)},
		{"events_per_issue_per_hour", perIssue},
		{"sample_rate", strconv.FormatFloat(f.SampleRate, 'f', -1, 64)},
		{"rate_limit_per_minute", perMinute},
	} {
		_, _ = fmt.Fprintf(tw, "%s\t%s\n", row[0], row[1])
	}
	return tw.Flush()
}

// droppedCommand cuenta lo descartado de un proyecto por motivo: args es <nombre> [<días>].
func droppedCommand(ctx context.Context, db *store.Store, args []string, out io.Writer) error {
	p, err := projectByName(ctx, db, args[0])
	if err != nil {
		return err
	}
	days := 30
	if len(args) == 2 {
		if days, err = strconv.Atoi(args[1]); err != nil {
			return fmt.Errorf("los días son un número, no %q", args[1])
		}
	}
	st, err := db.DroppedSince(ctx, p.ID, days, time.Now())
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintf(out, "descartados desde el %s (UTC)\n", st.Since)
	for _, r := range store.DropReasons {
		_, _ = fmt.Fprintf(tw, "%s\t%d\n", r, st.Reasons[r])
	}
	_, _ = fmt.Fprintf(tw, "total\t%d\n", st.Total)
	return tw.Flush()
}
