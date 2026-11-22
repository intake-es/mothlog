package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/intake-es/intake-errors/internal/store"
)

type listMonitorsIn struct {
	Project string `json:"project,omitempty" jsonschema:"id o nombre del proyecto; vacío, todos"`
	Status  string `json:"status,omitempty" jsonschema:"ok, error, missed, timeout o pending; vacío, todos"`
}

type monitorIn struct {
	Project string `json:"project" jsonschema:"id o nombre del proyecto (sale en list_monitors)"`
	Slug    string `json:"slug" jsonschema:"slug del monitor, el monitor_slug del SDK"`
	Cursor  string `json:"cursor,omitempty" jsonschema:"next_cursor de la respuesta anterior, para ver ejecuciones más antiguas"`
	Limit   int    `json:"limit,omitempty" jsonschema:"ejecuciones por página, de 1 a 200; 50 por defecto"`
}

type monitorOut struct {
	store.Monitor
	CheckIns   []store.CheckIn `json:"checkins"`
	NextCursor string          `json:"next_cursor,omitempty"`
}

func addMonitorTools(s *mcp.Server, st *store.Store) {
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_monitors",
		Annotations: readOnly,
		Description: "Lista los monitores de cron (tareas programadas que mandan check-ins) con su horario, su estado (ok, error, missed: no llegó a su hora; timeout: empezó y no acabó a tiempo; pending: aún sin check-ins), " +
			"si está silenciado (muted), la última ejecución (last_checkin_at), la próxima esperada (next_checkin_at) y sus últimas ejecuciones (recent). " +
			"Los que fallan salen primero. Úsala para saber qué crons han dejado de ir o fallan; luego get_monitor para el historial de uno.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listMonitorsIn) (*mcp.CallToolResult, any, error) {
		list, err := st.ListMonitors(ctx, store.MonitorFilter(in))
		if err != nil {
			return nil, nil, toolErrorFor(err, "no existe ese proyecto")
		}
		return result(map[string]any{"data": list}, nil)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_monitor",
		Annotations: readOnly,
		Description: "Devuelve un monitor de cron con su configuración y su historial de ejecuciones, de la más nueva a la más vieja: estado, duración en ms, entorno, release, " +
			"cuándo tocaba (expected_at), cuándo empezó y cuándo acabó. Úsala para ver desde cuándo falla un cron, cuánto suele tardar o en qué release empezó a fallar.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in monitorIn) (*mcp.CallToolResult, any, error) {
		m, err := st.MonitorByRef(ctx, in.Project, in.Slug)
		if err != nil {
			return nil, nil, toolErrorFor(err, "no existe ese monitor en el proyecto")
		}
		page, err := st.CheckIns(ctx, m.ID, in.Cursor, in.Limit)
		if err != nil {
			return nil, nil, toolError(err)
		}
		return result(monitorOut{Monitor: m, CheckIns: page.CheckIns, NextCursor: page.NextCursor}, nil)
	})
}
