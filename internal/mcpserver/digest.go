package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/intake-es/intake-errors/internal/digest"
	"github.com/intake-es/intake-errors/internal/store"
)

type digestIn struct {
	Project  string `json:"project" jsonschema:"id o nombre del proyecto"`
	Period   string `json:"period,omitempty" jsonschema:"day (24 horas) o week (7 días); vacío, el del resumen del proyecto, o day si no tiene"`
	End      string `json:"end,omitempty" jsonschema:"fin del periodo en RFC 3339, como 2026-11-22T09:00:00+01:00; vacío, ahora"`
	Language string `json:"language,omitempty" jsonschema:"es o en; vacío, el del resumen del proyecto"`
}

func addDigestTool(s *mcp.Server, st *store.Store, base func(context.Context) string) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_digest",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		Description: "Devuelve en Markdown el resumen de un proyecto en un periodo (las 24 horas o los 7 días que acaban en end): los totales de eventos, errores con eventos, usuarios afectados y resueltos, con el cambio respecto al periodo anterior; " +
			"los errores nuevos y las regresiones con su enlace; los que más crecen respecto al periodo anterior; los crons que fallaron, no llegaron o se pasaron de tiempo; y la salud de la última release si tiene sesiones. " +
			"Las secciones sin nada no salen. Úsala para saber cómo va un proyecto sin recorrer los issues uno a uno, p. ej. al empezar el día o antes de decidir qué arreglar primero.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in digestIn) (*mcp.CallToolResult, any, error) {
		p, err := project(ctx, st, in.Project)
		if err != nil {
			return nil, nil, toolErrorFor(err, "no existe ese proyecto")
		}
		end := time.Now()
		if in.End != "" {
			if end, err = time.Parse(time.RFC3339, in.End); err != nil {
				return nil, nil, errors.New("end debe ir en RFC 3339, como 2026-11-22T09:00:00+01:00")
			}
		}
		c, err := st.Digest(ctx, p.ID, end)
		if err != nil {
			return nil, nil, toolError(err)
		}
		switch in.Period {
		case "":
		case "day":
			c.Frequency = store.DigestDaily
		case "week":
			c.Frequency = store.DigestWeekly
		default:
			return nil, nil, fmt.Errorf("period es day o week, no %q", in.Period)
		}
		if in.Language != "" {
			if in.Language != digest.Spanish && in.Language != digest.English {
				return nil, nil, fmt.Errorf("language es es o en, no %q", in.Language)
			}
			c.Language = in.Language
		}
		b := ""
		if base != nil {
			b = strings.TrimRight(base(ctx), "/")
		}
		d, err := digest.Build(ctx, st, digest.Options{Config: c, Start: end.Add(-c.Length()), End: end, Base: b})
		if err != nil {
			return nil, nil, toolError(err)
		}
		return textResult(d.Markdown()), nil, nil
	})
}
