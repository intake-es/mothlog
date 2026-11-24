package mcpserver

import (
	"context"
	"errors"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/intake-es/mothlog/internal/store"
)

type listMonitorsIn struct {
	Project string `json:"project,omitempty" jsonschema:"project id or name; empty, all"`
	Status  string `json:"status,omitempty" jsonschema:"for crons ok, error, missed, timeout or pending; for uptime monitors up, down, pending or paused; empty, all"`
}

type monitorIn struct {
	Project string `json:"project" jsonschema:"project id or name (shown in list_monitors)"`
	Slug    string `json:"slug" jsonschema:"monitor slug, the SDK monitor_slug or an uptime monitor's slug"`
	Type    string `json:"type,omitempty" jsonschema:"cron or uptime; empty, looks for a cron first and then an uptime monitor"`
	Cursor  string `json:"cursor,omitempty" jsonschema:"next_cursor from the previous response, to see older runs"`
	Limit   int    `json:"limit,omitempty" jsonschema:"runs per page, 1 to 200; 50 by default"`
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

func addMonitorTools(s *mcp.Server, st *store.Store, errs toolErrors) {
	l := errs.l
	tr := l.T
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true}
	addTool(s, l, &mcp.Tool{
		Name:        "list_monitors",
		Annotations: readOnly,
		Description: tr("Lists the monitors. In data, cron monitors (scheduled jobs that send check-ins) with their schedule, their status (ok, error, missed: didn't check in on time; timeout: started and didn't finish in time; pending: no check-ins yet), "+
			"whether it is muted, the last run (last_checkin_at), the next expected one (next_checkin_at) and its latest runs (recent). "+
			"In uptime, the uptime monitors (a URL the service requests every interval_seconds) with their status (up, down, pending), whether paused, since when it has been down (down_since), "+
			"the availability percentage over the last 24 h and 30 days (uptime_24h, uptime_30d) and their latest checks (recent). "+
			"Failing ones come first. Use it to find out which crons stopped working or which sites don't respond; then get_monitor for the history of one.",
			"Lista los monitores. En data, los de cron (tareas programadas que mandan check-ins) con su horario, su estado (ok, error, missed: no llegó a su hora; timeout: empezó y no acabó a tiempo; pending: aún sin check-ins), "+
				"si está silenciado (muted), la última ejecución (last_checkin_at), la próxima esperada (next_checkin_at) y sus últimas ejecuciones (recent). "+
				"En uptime, los de disponibilidad (una URL que el servicio pide cada interval_seconds) con su estado (up, down, pending), si está pausado, desde cuándo está caído (down_since), "+
				"el porcentaje de disponibilidad de las últimas 24 h y 30 días (uptime_24h, uptime_30d) y sus últimas comprobaciones (recent). "+
				"Los que fallan salen primero. Úsala para saber qué crons han dejado de ir o qué webs no responden; luego get_monitor para el historial de uno."),
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
			return nil, nil, errs.notFound(cronErr, errs.noProject())
		}
		if errors.Is(cronErr, store.ErrNotFound) || errors.Is(uptimeErr, store.ErrNotFound) {
			return nil, nil, errs.notFound(store.ErrNotFound, errs.noProject())
		}
		return errs.result(out, nil)
	})

	addTool(s, l, &mcp.Tool{
		Name:        "get_monitor",
		Annotations: readOnly,
		Description: tr("Returns a monitor. A cron one, with its configuration and its run history, newest first: status, duration in ms, environment, release, "+
			"when it was due (expected_at), when it started and when it finished. An uptime one (type uptime), with its URL, its incidents (when each outage started and ended, how long it lasted and the first error), "+
			"its latest checks (status code, duration and error) and the response times over the last 24 h in 15-minute buckets. "+
			"Use it to see since when a cron or a site has been failing, how long it usually takes or in which release it started failing.",
			"Devuelve un monitor. Uno de cron, con su configuración y su historial de ejecuciones, de la más nueva a la más vieja: estado, duración en ms, entorno, release, "+
				"cuándo tocaba (expected_at), cuándo empezó y cuándo acabó. Uno de disponibilidad (type uptime), con su URL, sus incidentes (cuándo empezó y acabó cada caída, cuánto duró y el primer error), "+
				"sus últimas comprobaciones (código, duración y error) y los tiempos de respuesta de las últimas 24 h en tramos de 15 min. "+
				"Úsala para ver desde cuándo falla un cron o una web, cuánto suele tardar o en qué release empezó a fallar."),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in monitorIn) (*mcp.CallToolResult, any, error) {
		switch in.Type {
		case "", "cron", "uptime":
		default:
			return nil, nil, errors.New(tr("type is cron or uptime", "type es cron o uptime"))
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
					return nil, nil, errs.toolError(derr)
				}
				return errs.result(uptimeOut{Type: "uptime", UptimeDetail: d}, nil)
			}
			err = uerr
		}
		if err != nil {
			return nil, nil, errs.notFound(err, tr("no such monitor in the project", "no existe ese monitor en el proyecto"))
		}
		page, err := st.CheckIns(ctx, m.ID, in.Cursor, in.Limit)
		if err != nil {
			return nil, nil, errs.toolError(err)
		}
		return errs.result(monitorOut{Monitor: m, CheckIns: page.CheckIns, NextCursor: page.NextCursor}, nil)
	})
}
