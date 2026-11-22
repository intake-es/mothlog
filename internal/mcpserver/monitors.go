package mcpserver

import (
	"context"
	"errors"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/intake-es/intake-errors/internal/store"
)

type listMonitorsIn struct {
	Project string `json:"project,omitempty" jsonschema:"id o nombre del proyecto; vacío, todos"`
	Status  string `json:"status,omitempty" jsonschema:"en los crons ok, error, missed, timeout o pending; en los de disponibilidad up, down, pending o paused; vacío, todos"`
}

type monitorIn struct {
	Project string `json:"project" jsonschema:"id o nombre del proyecto (sale en list_monitors)"`
	Slug    string `json:"slug" jsonschema:"slug del monitor, el monitor_slug del SDK o el de un monitor de disponibilidad"`
	Type    string `json:"type,omitempty" jsonschema:"cron o uptime; vacío, busca primero un cron y luego uno de disponibilidad"`
	Cursor  string `json:"cursor,omitempty" jsonschema:"next_cursor de la respuesta anterior, para ver ejecuciones más antiguas"`
	Limit   int    `json:"limit,omitempty" jsonschema:"ejecuciones por página, de 1 a 200; 50 por defecto"`
}

type uptimeOut struct {
	Type string `json:"type"`
	store.UptimeDetail
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
		Description: "Lista los monitores. En data, los de cron (tareas programadas que mandan check-ins) con su horario, su estado (ok, error, missed: no llegó a su hora; timeout: empezó y no acabó a tiempo; pending: aún sin check-ins), " +
			"si está silenciado (muted), la última ejecución (last_checkin_at), la próxima esperada (next_checkin_at) y sus últimas ejecuciones (recent). " +
			"En uptime, los de disponibilidad (una URL que el servicio pide cada interval_seconds) con su estado (up, down, pending), si está pausado, desde cuándo está caído (down_since), " +
			"el porcentaje de disponibilidad de las últimas 24 h y 30 días (uptime_24h, uptime_30d) y sus últimas comprobaciones (recent). " +
			"Los que fallan salen primero. Úsala para saber qué crons han dejado de ir o qué webs no responden; luego get_monitor para el historial de uno.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listMonitorsIn) (*mcp.CallToolResult, any, error) {
		out := map[string]any{"data": []store.Monitor{}, "uptime": []store.UptimeMonitor{}}
		var cronErr, uptimeErr error
		var list []store.Monitor
		if list, cronErr = st.ListMonitors(ctx, store.MonitorFilter(in)); cronErr == nil {
			out["data"] = list
		}
		var up []store.UptimeMonitor
		if up, uptimeErr = st.ListUptimeMonitors(ctx, store.UptimeFilter(in), time.Now()); uptimeErr == nil {
			out["uptime"] = up
		}
		// El status de un tipo no vale en el otro: basta con que valga en uno.
		if cronErr != nil && uptimeErr != nil {
			return nil, nil, toolErrorFor(cronErr, "no existe ese proyecto")
		}
		if errors.Is(cronErr, store.ErrNotFound) || errors.Is(uptimeErr, store.ErrNotFound) {
			return nil, nil, toolErrorFor(store.ErrNotFound, "no existe ese proyecto")
		}
		return result(out, nil)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_monitor",
		Annotations: readOnly,
		Description: "Devuelve un monitor. Uno de cron, con su configuración y su historial de ejecuciones, de la más nueva a la más vieja: estado, duración en ms, entorno, release, " +
			"cuándo tocaba (expected_at), cuándo empezó y cuándo acabó. Uno de disponibilidad (type uptime), con su URL, sus incidentes (cuándo empezó y acabó cada caída, cuánto duró y el primer error), " +
			"sus últimas comprobaciones (código, duración y error) y los tiempos de respuesta de las últimas 24 h en tramos de 15 min. " +
			"Úsala para ver desde cuándo falla un cron o una web, cuánto suele tardar o en qué release empezó a fallar.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in monitorIn) (*mcp.CallToolResult, any, error) {
		switch in.Type {
		case "", "cron", "uptime":
		default:
			return nil, nil, toolError(errors.New("type es cron o uptime"))
		}
		var m store.Monitor
		err := store.ErrNotFound
		if in.Type != "uptime" {
			m, err = st.MonitorByRef(ctx, in.Project, in.Slug)
		}
		if errors.Is(err, store.ErrNotFound) && in.Type != "cron" {
			u, uerr := st.UptimeMonitorByRef(ctx, in.Project, in.Slug)
			if uerr == nil {
				d, derr := st.UptimeDetail(ctx, u, time.Now())
				if derr != nil {
					return nil, nil, toolError(derr)
				}
				return result(uptimeOut{Type: "uptime", UptimeDetail: d}, nil)
			}
			err = uerr
		}
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
