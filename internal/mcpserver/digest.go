package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/intake-es/mothlog/internal/digest"
	"github.com/intake-es/mothlog/internal/store"
)

type digestIn struct {
	Project  string `json:"project" jsonschema:"project id or name"`
	Period   string `json:"period,omitempty" jsonschema:"day (24 hours) or week (7 days); empty, the project's digest setting, or day if it has none"`
	End      string `json:"end,omitempty" jsonschema:"end of the period in RFC 3339, like 2026-11-22T09:00:00+01:00; empty, now"`
	Language string `json:"language,omitempty" jsonschema:"es or en; empty, the project's digest language"`
}

func addDigestTool(s *mcp.Server, st *store.Store, base func(context.Context) string, errs toolErrors) {
	l := errs.l
	tr := l.T
	addTool(s, l, &mcp.Tool{
		Name:        "get_digest",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		Description: tr("Returns in Markdown the digest of a project over a period (the 24 hours or 7 days ending at end): the totals of events, errors with events, affected users and resolved, with the change against the previous period; "+
			"new errors and regressions with their link; the ones growing fastest against the previous period; the crons that failed, missed or timed out; and the health of the latest release if it has sessions. "+
			"Empty sections are left out. Use it to see how a project is doing without going through the issues one by one, e.g. at the start of the day or before deciding what to fix first.",
			"Devuelve en Markdown el resumen de un proyecto en un periodo (las 24 horas o los 7 días que acaban en end): los totales de eventos, errores con eventos, usuarios afectados y resueltos, con el cambio respecto al periodo anterior; "+
				"los errores nuevos y las regresiones con su enlace; los que más crecen respecto al periodo anterior; los crons que fallaron, no llegaron o se pasaron de tiempo; y la salud de la última release si tiene sesiones. "+
				"Las secciones sin nada no salen. Úsala para saber cómo va un proyecto sin recorrer los issues uno a uno, p. ej. al empezar el día o antes de decidir qué arreglar primero."),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in digestIn) (*mcp.CallToolResult, any, error) {
		p, err := project(ctx, st, in.Project)
		if err != nil {
			return nil, nil, errs.notFound(err, errs.noProject())
		}
		end := time.Now()
		if in.End != "" {
			if end, err = time.Parse(time.RFC3339, in.End); err != nil {
				return nil, nil, errors.New(tr("end must be RFC 3339, like 2026-11-22T09:00:00+01:00", "end debe ir en RFC 3339, como 2026-11-22T09:00:00+01:00"))
			}
		}
		c, err := st.Digest(ctx, p.ID, end)
		if err != nil {
			return nil, nil, errs.toolError(err)
		}
		switch in.Period {
		case "":
		case "day":
			c.Frequency = store.DigestDaily
		case "week":
			c.Frequency = store.DigestWeekly
		default:
			return nil, nil, fmt.Errorf(tr("period is day or week, not %q", "period es day o week, no %q"), in.Period)
		}
		if in.Language != "" {
			if in.Language != digest.Spanish && in.Language != digest.English {
				return nil, nil, fmt.Errorf(tr("language is es or en, not %q", "language es es o en, no %q"), in.Language)
			}
			c.Language = in.Language
		}
		b := ""
		if base != nil {
			b = strings.TrimRight(base(ctx), "/")
		}
		d, err := digest.Build(ctx, st, digest.Options{Config: c, Start: end.Add(-c.Length()), End: end, Base: b})
		if err != nil {
			return nil, nil, errs.toolError(err)
		}
		return textResult(d.Markdown()), nil, nil
	})
}
