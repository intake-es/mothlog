// Package mcpserver expone los errores como herramientas MCP, con las mismas
// consultas que la API REST.
package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
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
	Comment string `json:"comment,omitempty" jsonschema:"comentario en Markdown que queda unido al cambio en el historial, p. ej. qué se arregló y en qué commit"`
}

type contextIn struct {
	IssueID     int64  `json:"issue_id" jsonschema:"id del issue"`
	Environment string `json:"environment,omitempty" jsonschema:"solo los eventos de este entorno (production, staging…): recuentos y último evento; vacío, todos"`
	Event       string `json:"event,omitempty" jsonschema:"id interno o event_id de un evento del issue (de list_events), para leer ese en vez del último"`
}

type eventIn struct {
	EventID string `json:"event_id" jsonschema:"id interno (de list_events) o event_id del SDK, en 32 hex o UUID"`
}

type issueOnlyIn struct {
	IssueID int64 `json:"issue_id" jsonschema:"id del issue"`
}

type addCommentIn struct {
	IssueID int64  `json:"issue_id" jsonschema:"id del issue"`
	Body    string `json:"body" jsonschema:"el comentario, en Markdown"`
}

type assignIn struct {
	IssueID  int64  `json:"issue_id" jsonschema:"id del issue"`
	Assignee string `json:"assignee" jsonschema:"me para asignártelo a ti (a tu token), el login de una persona o el nombre de un token (de list_assignees); vacío lo desasigna"`
	Kind     string `json:"kind,omitempty" jsonschema:"user o token; solo hace falta si el nombre es a la vez de una persona y de un token"`
}

type listReleasesIn struct {
	Project     string `json:"project,omitempty" jsonschema:"id o nombre del proyecto; vacío, todos"`
	Environment string `json:"environment,omitempty" jsonschema:"solo las releases con eventos, despliegues o sesiones en este entorno, contando solo lo suyo; vacío, todos"`
	Cursor      string `json:"cursor,omitempty" jsonschema:"next_cursor de la respuesta anterior"`
	Limit       int    `json:"limit,omitempty" jsonschema:"releases por página, de 1 a 100; 25 por defecto"`
}

type releaseIn struct {
	Project     string `json:"project" jsonschema:"id o nombre del proyecto (sale en list_issues y list_releases)"`
	Version     string `json:"version" jsonschema:"la versión, como web@1.4.0"`
	Environment string `json:"environment,omitempty" jsonschema:"solo lo de este entorno; vacío, todos"`
}

// Options dice al servidor quién llama y cómo enlazar a la interfaz.
type Options struct {
	// Actor es quien hace la petición (el nombre del token); sin él, no se puede comentar.
	Actor func(context.Context) *store.Actor
	// IssueURL es el enlace al issue en la interfaz; sin él, el contexto va sin enlace.
	IssueURL func(ctx context.Context, id int64) string
	// BaseURL es la base de la interfaz, para los enlaces del resumen.
	BaseURL func(ctx context.Context) string
	// Scopes son los permisos de quien llama (ScopeRead, ScopeWrite); nil, todos.
	Scopes func(ctx context.Context) []string
}

// Permisos de un token de OAuth: leer, o cambiar algo (comentar, asignar y
// cambiar el estado).
const (
	ScopeRead  = "read"
	ScopeWrite = "write"
)

// writeTools son las herramientas que cambian algo; las demás son de leer.
var writeTools = map[string]bool{"add_comment": true, "assign_issue": true, "set_issue_status": true}

// ToolScope es el permiso que hace falta para usar la herramienta.
func ToolScope(name string) string {
	if writeTools[name] {
		return ScopeWrite
	}
	return ScopeRead
}

const instructions = `Errores de las aplicaciones que envían a intake-errors, agrupados en issues. Para llevar un error de principio a fin:

1. Encontrarlo con list_issues. Sin query, los abiertos que han pasado más recientemente. Ejemplos de query: "is:open environment:production", "is:regressed release:1.4.0", "TypeError browser:Safari", "user:ana@example.com is:open", "is:unassigned is:open".
2. Asignártelo con assign_issue y assignee "me", para que el equipo sepa que estás con él; los tuyos salen con list_issues y query "assigned:me".
3. Leer get_issue_context: un único Markdown con el resumen, las releases, los entornos, las etiquetas, la traza con el código de los fotogramas propios, los breadcrumbs, la petición, el usuario y el historial (comentarios y cambios de estado anteriores, que conviene leer antes de ponerse).
4. Arreglarlo en el código.
5. Dejar constancia con add_comment: qué causaba el error y qué se ha cambiado (commit, PR, fichero).
6. Marcar el estado con set_issue_status: resolved_next si el arreglo aún no está desplegado (pasa a resolved solo al desplegarse una release posterior), resolved si ya lo está, silenced si no hay nada que arreglar. Lleva un comment opcional que queda unido al cambio en el historial.

Si hace falta más: list_events y get_event para otros eventos, get_issue_tags para todas las etiquetas, list_releases y get_release para lo que trajo una release, list_comments para el historial, list_monitors y get_monitor para los crons que fallan o no llegan y las webs que no responden, get_digest para el resumen de cómo va un proyecto en las últimas 24 horas o 7 días. Las herramientas de leer aceptan environment (production, staging…) para quedarse con los eventos de ese entorno.`

func New(st *store.Store, opts Options) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "intake-errors", Version: "0.2.0"}, &mcp.ServerOptions{
		Instructions: instructions,
	})
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true}
	actor := func(ctx context.Context) *store.Actor {
		if opts.Actor == nil {
			return nil
		}
		return opts.Actor(ctx)
	}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_issues",
		Annotations: readOnly,
		Description: "Lista los errores agrupados (issues) con su título, culprit, estado, número de eventos, usuarios afectados (user_count), primera y última vez, último release y quién lo lleva (assignee). " +
			"Úsala para ver qué está fallando ahora (por defecto, los abiertos ordenados por la última vez) o para encontrar errores por su texto o sus etiquetas. " +
			"Sintaxis de query: términos separados por espacios que se cumplen todos. " +
			"is:open|resolved|resolved_next|silenced|regressed (regressed: abiertos que volvieron tras resolverse); " +
			"release:1.2.0, environment:production, level:error, project:web (id o nombre); " +
			"user:<id, email, username o IP> (los usuarios se guardan con hash: hay que dar el valor exacto); " +
			"cualquier etiqueta clave:valor, como browser:Safari, os:\"Mac OS\", device:iPhone, runtime:CPython o las tags propias del SDK; " +
			"assigned:me (los tuyos), assigned:<login o nombre de token>, is:assigned, is:unassigned; " +
			"! delante niega (!browser:Chrome, !timeout, !assigned:me); las comillas agrupan espacios (\"division by zero\"); " +
			"el resto es texto libre que se busca en el título y el culprit sin distinguir mayúsculas. " +
			"Los valores de etiqueta van exactos, con sus mayúsculas; get_issue_tags dice los que tiene un issue. " +
			"Una query mal escrita devuelve un error que dice qué falla y en qué carácter. " +
			"Si la respuesta trae next_cursor, hay más páginas.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listIssuesIn) (*mcp.CallToolResult, any, error) {
		page, err := st.ListIssues(ctx, store.IssueFilter{
			Project: in.Project, Status: in.Status, Query: in.Query, Environment: in.Environment,
			Sort: in.Sort, Cursor: in.Cursor, Limit: in.Limit, Viewer: actor(ctx),
		})
		return result(page, err)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_issue",
		Annotations: readOnly,
		Description: "Devuelve un issue con quién lo lleva (assignee), su último evento entero en JSON, los eventos por día de los últimos 14 días y los valores más frecuentes de entorno, release, usuario y organization_id, y sus enlaces (links): las issues de Linear y las issues y PRs de GitHub, con su estado allí. " +
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
		Name:        "get_issue_context",
		Annotations: readOnly,
		Description: "Devuelve en un único documento Markdown todo lo que hace falta para entender y arreglar un issue: el resumen (estado, nivel, proyecto, eventos, usuarios, primera y última vez y el enlace a la interfaz), " +
			"las releases (primera, última y con la que se resolvió), los entornos con sus eventos, las etiquetas más frecuentes con su porcentaje, " +
			"el evento con la traza y el código de los fotogramas de la aplicación, los breadcrumbs, la petición y el usuario, el historial de comentarios y cambios de estado, " +
			"y qué herramientas usar para comentarlo o resolverlo. " +
			"Úsala después de encontrar el issue con list_issues y antes de tocar código; es más completa que get_stacktrace. " +
			"Con event, el de ese evento (id o event_id, de list_events) en vez del último. Si el documento pasa de 64 KB, recorta breadcrumbs, fotogramas de librerías e historial viejo, y lo dice al principio.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in contextIn) (*mcp.CallToolResult, any, error) {
		c, err := st.IssueContextIn(ctx, in.IssueID, in.Environment, in.Event, actor(ctx), time.Now())
		if err != nil {
			return nil, nil, toolErrorFor(err, "no existe ese issue, o ese evento no es suyo")
		}
		url := ""
		if opts.IssueURL != nil {
			url = opts.IssueURL(ctx, in.IssueID)
		}
		return textResult(markdown.Context(c, url, markdown.MaxContextBytes)), nil, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_event",
		Annotations: readOnly,
		Description: "Devuelve un evento entero en JSON: excepciones con todos sus fotogramas, breadcrumbs, contexts, request, usuario, etiquetas, release, entorno y SDK. " +
			"Úsala cuando get_issue_context no basta: para ver un evento que no es el último (los ids salen en list_events) o los datos que el Markdown resume, como contexts o los fotogramas de librerías.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in eventIn) (*mcp.CallToolResult, any, error) {
		e, err := st.Event(ctx, in.EventID)
		if err != nil {
			return nil, nil, toolErrorFor(err, "no existe ese evento")
		}
		return result(e, nil)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_comments",
		Annotations: readOnly,
		Description: "Devuelve el historial de un issue, del más viejo al más nuevo: los comentarios (Markdown, autor y si es una persona o un token) y los cambios de estado (quién, desde dónde: interfaz, API, MCP, la ingesta al volver como regresión, un despliegue, o Linear o GitHub al cerrarse o reabrirse allí su issue o mergearse un PR; de qué estado a cuál, la release y el comentario que se dejó con el cambio). " +
			"Úsala para saber qué se ha intentado ya con un error antes de ponerte, o para comprobar que tu comentario o tu cambio de estado han quedado.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in issueOnlyIn) (*mcp.CallToolResult, any, error) {
		h, err := st.History(ctx, in.IssueID, actor(ctx))
		if err != nil {
			return nil, nil, toolErrorFor(err, "no existe ese issue")
		}
		return result(map[string]any{"data": h}, nil)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "add_comment",
		Annotations: &mcp.ToolAnnotations{
			DestructiveHint: new(false),
			OpenWorldHint:   new(false),
		},
		Description: "Añade un comentario en Markdown al historial de un issue, firmado con el nombre de tu token. " +
			"Úsala para dejar constancia de lo que has averiguado o hecho: la causa, el arreglo (commit, PR, fichero) o por qué no hay nada que arreglar. " +
			"Si a la vez cambias el estado, mejor el comment de set_issue_status, que queda unido al cambio. Cada llamada añade un comentario nuevo.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in addCommentIn) (*mcp.CallToolResult, any, error) {
		a := actor(ctx)
		if a == nil {
			return nil, nil, errors.New("no se sabe quién comenta: hace falta un token")
		}
		c, err := st.AddComment(ctx, in.IssueID, *a, in.Body)
		if err != nil {
			return nil, nil, toolErrorFor(err, "no existe ese issue")
		}
		return result(c, nil)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "assign_issue",
		Annotations: &mcp.ToolAnnotations{
			IdempotentHint:  true,
			DestructiveHint: new(false),
			OpenWorldHint:   new(false),
		},
		Description: "Asigna un issue a quien lo va a llevar, para que el equipo sepa que alguien está con él. " +
			"Úsala al ponerte con un error: con assignee \"me\" te lo asignas a ti (a tu token), y luego lo encuentras con list_issues y query assigned:me. " +
			"También vale el login de una persona o el nombre de otro token (list_assignees dice quiénes); assignee vacío lo desasigna. " +
			"Queda en el historial con el nombre de tu token; asignarlo a quien ya lo lleva no apunta nada. Devuelve el issue con su assignee.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in assignIn) (*mcp.CallToolResult, any, error) {
		a := actor(ctx)
		to, err := store.AssigneeRef(in.Assignee, in.Kind, a)
		if err != nil {
			return nil, nil, toolError(err)
		}
		issue, err := st.AssignIssue(ctx, in.IssueID, to, store.AssignChange{Actor: a, Source: store.SourceMCP})
		return result(issue, err)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_assignees",
		Annotations: readOnly,
		Description: "Lista a quién se puede asignar un issue: las personas que han entrado en la interfaz (kind user, por su login, con su nombre) y los tokens activos (kind token, los agentes). " +
			"Úsala para dar el assignee de assign_issue o el valor de assigned: en list_issues.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		out, err := st.Assignees(ctx)
		return result(map[string]any{"data": out}, err)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_releases",
		Annotations: readOnly,
		Description: "Lista las releases, de la más nueva a la más vieja, con sus despliegues y cuántos errores nuevos (new_issues), con eventos (issues), regresiones y eventos trajo cada una. " +
			"Con las sesiones de los SDK trae su salud en health (y por entorno en health_by_environment): crash_free_sessions y crash_free_users de 0 a 1 (null si no hay sesiones o usuarios), los totales y adoption, la parte de las sesiones de las últimas 24 h del proyecto que es de esa release. " +
			"Úsala para saber qué se ha desplegado y dónde, o qué release empeoró las cosas; luego get_release para el detalle de una. Si la respuesta trae next_cursor, hay más páginas.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listReleasesIn) (*mcp.CallToolResult, any, error) {
		page, err := st.ListReleases(ctx, store.ReleaseFilter(in))
		return result(page, err)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_release",
		Annotations: readOnly,
		Description: "Devuelve una release de un proyecto con sus despliegues, la anterior y la siguiente, y lo nuevo que trajo: los issues que aparecieron con ella (new), las regresiones (regressed), los que ya existían y siguieron saliendo (continued) y los que dejaron de salir o se resolvieron al desplegarla (stopped). " +
			"Trae también su salud (health, health_by_environment) y las sesiones por hora de las últimas 72 h (health_series). " +
			"Úsala tras un despliegue para ver si rompió algo o si arregló lo que debía; para buscar los issues de una release, list_issues con release:<versión>.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in releaseIn) (*mcp.CallToolResult, any, error) {
		p, err := project(ctx, st, in.Project)
		if err != nil {
			return nil, nil, toolErrorFor(err, "no existe ese proyecto")
		}
		d, err := st.ReleaseDetail(ctx, p.ID, in.Version, in.Environment)
		if err != nil {
			return nil, nil, toolErrorFor(err, "no existe esa release en el proyecto")
		}
		return result(d, nil)
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
			"silenced: no avisar más aunque siga ocurriendo. open: reabrirlo. Úsala solo cuando el usuario lo pida o tras arreglar el error. " +
			"Con comment (Markdown), el comentario queda unido al cambio en el historial: di qué se arregló y dónde. El cambio queda en el historial con el nombre de tu token; repetir el mismo estado no apunta nada nuevo.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in setStatusIn) (*mcp.CallToolResult, any, error) {
		issue, err := st.ChangeIssueStatus(ctx, in.IssueID, in.Status, store.StatusChange{
			Actor: actor(ctx), Source: store.SourceMCP, Comment: in.Comment,
		})
		return result(issue, err)
	})
	addMonitorTools(s, st)
	addDigestTool(s, st, opts.BaseURL)
	if opts.Scopes != nil {
		s.AddReceivingMiddleware(scopeMiddleware(opts.Scopes))
	}
	return s
}

// scopeMiddleware deja ver y usar solo las herramientas que permiten los
// permisos de quien llama.
func scopeMiddleware(scopes func(context.Context) []string) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			have := scopes(ctx)
			if have == nil {
				return next(ctx, method, req)
			}
			switch method {
			case "tools/call":
				if r, ok := req.(*mcp.CallToolRequest); ok && r.Params != nil {
					need := ToolScope(r.Params.Name)
					if !slices.Contains(have, need) {
						return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{
							Text: fmt.Sprintf("%s necesita el permiso %q y esta conexión solo tiene %q: vuelve a conectar y concédelo en la pantalla de consentimiento",
								r.Params.Name, need, strings.Join(have, " ")),
						}}}, nil
					}
				}
			case "tools/list":
				res, err := next(ctx, method, req)
				if list, ok := res.(*mcp.ListToolsResult); ok && err == nil {
					kept := make([]*mcp.Tool, 0, len(list.Tools))
					for _, t := range list.Tools {
						if slices.Contains(have, ToolScope(t.Name)) {
							kept = append(kept, t)
						}
					}
					list.Tools = kept
				}
				return res, err
			}
			return next(ctx, method, req)
		}
	}
}

// project busca un proyecto por id o por nombre.
func project(ctx context.Context, st *store.Store, ref string) (store.Project, error) {
	if id, err := strconv.ParseInt(ref, 10, 64); err == nil {
		if p, err := st.ProjectByID(ctx, id); !errors.Is(err, store.ErrNotFound) {
			return p, err
		}
	}
	return st.ProjectByName(ctx, ref)
}

func textResult(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

// toolErrorFor es toolError con su propio mensaje para lo que no existe.
func toolErrorFor(err error, notFound string) error {
	if errors.Is(err, store.ErrNotFound) {
		return errors.New(notFound)
	}
	return toolError(err)
}

// Handler sirve el MCP por Streamable HTTP. La autenticación va fuera.
func Handler(st *store.Store, opts Options) http.Handler {
	s := New(st, opts)
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
