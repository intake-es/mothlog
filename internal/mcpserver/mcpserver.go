// Package mcpserver expone los errores como herramientas MCP, con las mismas
// consultas que la API REST.
package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/intake-es/intake-errors/internal/markdown"
	"github.com/intake-es/intake-errors/internal/store"
)

type listIssuesIn struct {
	Project string `json:"project,omitempty" jsonschema:"id o nombre del proyecto; vacío, todos"`
	Status  string `json:"status,omitempty" jsonschema:"open (por defecto), resolved, resolved_next, silenced o all"`
	Query   string `json:"query,omitempty" jsonschema:"texto a buscar en el título y el culprit"`
	Sort    string `json:"sort,omitempty" jsonschema:"last_seen (por defecto), first_seen o count; siempre de mayor a menor"`
	Cursor  string `json:"cursor,omitempty" jsonschema:"next_cursor de la respuesta anterior, para la página siguiente"`
	Limit   int    `json:"limit,omitempty" jsonschema:"issues por página, de 1 a 100; 25 por defecto"`
}

type issueIn struct {
	IssueID int64 `json:"issue_id" jsonschema:"id del issue"`
}

type listEventsIn struct {
	IssueID int64  `json:"issue_id" jsonschema:"id del issue"`
	Cursor  string `json:"cursor,omitempty" jsonschema:"next_cursor de la respuesta anterior"`
	Limit   int    `json:"limit,omitempty" jsonschema:"eventos por página, de 1 a 100; 25 por defecto"`
}

type setStatusIn struct {
	IssueID int64  `json:"issue_id" jsonschema:"id del issue"`
	Status  string `json:"status" jsonschema:"open, resolved, resolved_next o silenced"`
}

func New(st *store.Store) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "intake-errors", Version: "0.1.0"}, &mcp.ServerOptions{
		Instructions: "intake-errors (backend, integrations y panel). Para investigar uno: list_issues para encontrarlo, get_stacktrace para leer la traza y set_issue_status cuando esté arreglado.",
	})
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_issues",
		Annotations: readOnly,
		Description: "Lista los errores agrupados (issues) con su título, culprit, estado, número de eventos, primera y última vez y último release. " +
			"Úsala para ver qué está fallando ahora (por defecto, los abiertos ordenados por la última vez) o para encontrar el id de un error por su texto. " +
			"Si la respuesta trae next_cursor, hay más páginas.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listIssuesIn) (*mcp.CallToolResult, any, error) {
		page, err := st.ListIssues(ctx, store.IssueFilter(in))
		return result(page, err)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_issue",
		Annotations: readOnly,
		Description: "Devuelve un issue con su último evento entero en JSON, los eventos por día de los últimos 14 días y los valores más frecuentes de entorno, release, usuario y organization_id. " +
			"Úsala para saber a cuántos usuarios u organizaciones afecta, desde qué release o si va a más. Para leer la traza, mejor get_stacktrace.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in issueIn) (*mcp.CallToolResult, any, error) {
		d, err := st.IssueDetail(ctx, in.IssueID, time.Now())
		return result(d, err)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_stacktrace",
		Annotations: readOnly,
		Description: "Devuelve en Markdown el último evento de un issue: release, entorno, etiquetas, usuario, request, la cadena de excepciones con el código de los fotogramas de la aplicación y los últimos breadcrumbs. " +
			"Úsala antes de tocar código para arreglar un error.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in issueIn) (*mcp.CallToolResult, any, error) {
		d, err := st.IssueDetail(ctx, in.IssueID, time.Now())
		if err != nil {
			return nil, nil, toolError(err)
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: markdown.Issue(d)}}}, nil, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_events",
		Annotations: readOnly,
		Description: "Lista los eventos (cada vez que ocurrió) de un issue, del más nuevo al más viejo, con fecha, release, entorno, usuario y mensaje. " +
			"Úsala para ver si un error sigue pasando tras un despliegue o a qué usuarios les pasó.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listEventsIn) (*mcp.CallToolResult, any, error) {
		page, err := st.IssueEvents(ctx, in.IssueID, in.Cursor, in.Limit)
		return result(page, err)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "set_issue_status",
		Annotations: &mcp.ToolAnnotations{
			IdempotentHint:  true,
			DestructiveHint: new(false),
			OpenWorldHint:   new(false),
		},
		Description: "Cambia el estado de un issue. resolved: arreglado; si vuelve a ocurrir, se reabre como regresión. " +
			"resolved_next: arreglado en el próximo release; solo se reabre si ocurre en un release distinto del de su último evento (úsalo cuando el arreglo aún no está desplegado). " +
			"silenced: no avisar más aunque siga ocurriendo. open: reabrirlo. Úsala solo cuando el usuario lo pida o tras arreglar el error.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in setStatusIn) (*mcp.CallToolResult, any, error) {
		issue, err := st.SetIssueStatus(ctx, in.IssueID, in.Status)
		return result(issue, err)
	})
	return s
}

// Handler sirve el MCP por Streamable HTTP. La autenticación va fuera.
func Handler(st *store.Store) http.Handler {
	s := New(st)
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, &mcp.StreamableHTTPOptions{
		// Sin sesiones en memoria: cualquier réplica atiende cualquier petición.
		Stateless:    true,
		JSONResponse: true,
		// Cada petición lleva su token, así que el DNS rebinding no da acceso;
		// y detrás de un proxy local el Host no es localhost.
		DisableLocalhostProtection: true,
	})
}

func result(v any, err error) (*mcp.CallToolResult, any, error) {
	if err != nil {
		return nil, nil, toolError(err)
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}, nil, nil
}

func toolError(err error) error {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return errors.New("no existe ese issue")
	case errors.Is(err, store.ErrInvalid), errors.Is(err, store.ErrNoRelease):
		return err
	}
	slog.Error("mcp", "err", err)
	return errors.New("error interno")
}
