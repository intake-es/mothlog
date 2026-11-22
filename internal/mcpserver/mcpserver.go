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
	Project     string `json:"project,omitempty" jsonschema:"id o nombre del proyecto; vacío, todos"`
	Status      string `json:"status,omitempty" jsonschema:"open, resolved, resolved_next, silenced o all; vacío es open, o all si query trae is:"`
	Query       string `json:"query,omitempty" jsonschema:"búsqueda, p. ej. is:open browser:Safari !environment:staging timeout; la sintaxis, en la descripción de la herramienta"`
	Environment string `json:"environment,omitempty" jsonschema:"solo los issues con eventos en este entorno (production, staging…), con sus recuentos en él; vacío, todos"`
	Sort        string `json:"sort,omitempty" jsonschema:"last_seen (por defecto), first_seen o count; siempre de mayor a menor"`
	Cursor      string `json:"cursor,omitempty" jsonschema:"next_cursor de la respuesta anterior, para la página siguiente"`
	Limit       int    `json:"limit,omitempty" jsonschema:"issues por página, de 1 a 100; 25 por defecto"`
}

type issueIn struct {
	IssueID     int64  `json:"issue_id" jsonschema:"id del issue"`
	Environment string `json:"environment,omitempty" jsonschema:"solo los eventos de este entorno (production, staging…); vacío, todos"`
}

type listEventsIn struct {
	IssueID     int64  `json:"issue_id" jsonschema:"id del issue"`
	Environment string `json:"environment,omitempty" jsonschema:"solo los eventos de este entorno (production, staging…); vacío, todos"`
	Cursor      string `json:"cursor,omitempty" jsonschema:"next_cursor de la respuesta anterior"`
	Limit       int    `json:"limit,omitempty" jsonschema:"eventos por página, de 1 a 100; 25 por defecto"`
}

type setStatusIn struct {
	IssueID int64  `json:"issue_id" jsonschema:"id del issue"`
	Status  string `json:"status" jsonschema:"open, resolved, resolved_next o silenced"`
}

func New(st *store.Store) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "intake-errors", Version: "0.1.0"}, &mcp.ServerOptions{
		Instructions: "Errores de las aplicaciones que envían a intake-errors. Para investigar uno: list_issues para encontrarlo, get_stacktrace para leer la traza y set_issue_status cuando esté arreglado.",
	})
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_issues",
		Annotations: readOnly,
		Description: "Lista los errores agrupados (issues) con su título, culprit, estado, número de eventos, usuarios afectados (user_count), primera y última vez y último release. " +
			"Úsala para ver qué está fallando ahora (por defecto, los abiertos ordenados por la última vez) o para encontrar errores por su texto o sus etiquetas. " +
			"Sintaxis de query: términos separados por espacios que se cumplen todos. " +
			"is:open|resolved|resolved_next|silenced|regressed (regressed: abiertos que volvieron tras resolverse); " +
			"release:1.2.0, environment:production, level:error, project:web (id o nombre); " +
			"user:<id, email, username o IP> (los usuarios se guardan con hash: hay que dar el valor exacto); " +
			"cualquier etiqueta clave:valor, como browser:Safari, os:\"Mac OS\", device:iPhone, runtime:CPython o las tags propias del SDK; " +
			"! delante niega (!browser:Chrome, !timeout); las comillas agrupan espacios (\"division by zero\"); " +
			"el resto es texto libre que se busca en el título y el culprit sin distinguir mayúsculas. " +
			"Los valores de etiqueta van exactos, con sus mayúsculas; get_issue_tags dice los que tiene un issue. " +
			"Una query mal escrita devuelve un error que dice qué falla y en qué carácter. " +
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
		d, err := st.IssueDetailIn(ctx, in.IssueID, in.Environment, time.Now())
		return result(d, err)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_issue_tags",
		Annotations: readOnly,
		Description: "Devuelve las etiquetas de un issue (navegador, sistema, dispositivo, runtime, release, entorno, nivel y las tags del SDK) con los valores más frecuentes de cada una, su recuento y su porcentaje, y cuántos usuarios distintos afecta. " +
			"Úsala para saber si un error es de un navegador, un release o un entorno concretos; luego puedes buscar por ese valor con list_issues.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in issueIn) (*mcp.CallToolResult, any, error) {
		tags, err := st.IssueTags(ctx, in.IssueID)
		return result(tags, err)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_stacktrace",
		Annotations: readOnly,
		Description: "Devuelve en Markdown el último evento de un issue: release, entorno, etiquetas, usuario, request, la cadena de excepciones con el código de los fotogramas de la aplicación y los últimos breadcrumbs. " +
			"Úsala antes de tocar código para arreglar un error.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in issueIn) (*mcp.CallToolResult, any, error) {
		d, err := st.IssueDetailIn(ctx, in.IssueID, in.Environment, time.Now())
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
		page, err := st.IssueEventsIn(ctx, in.IssueID, in.Environment, in.Cursor, in.Limit)
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
			"resolved_next: arreglado en el próximo release (úsalo cuando el arreglo aún no está desplegado); pasa a resolved al desplegarse una release posterior a la de su último evento, y se reabre como regresión si ocurre en una release posterior. " +
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
