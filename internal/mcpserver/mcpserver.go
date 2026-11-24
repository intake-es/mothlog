// Package mcpserver expone los errores como herramientas MCP, con las mismas
// consultas que la API REST.
package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/intake-es/mothlog/internal/lang"
	"github.com/intake-es/mothlog/internal/markdown"
	"github.com/intake-es/mothlog/internal/store"
)

type listIssuesIn struct {
	Project     string `json:"project,omitempty" jsonschema:"project id or name; empty, all"`
	Status      string `json:"status,omitempty" jsonschema:"open, resolved, resolved_next, silenced or all; empty is open, or all if query has is:"`
	Query       string `json:"query,omitempty" jsonschema:"search, e.g. is:open browser:Safari !environment:staging timeout; the syntax is in the tool description"`
	Environment string `json:"environment,omitempty" jsonschema:"only issues with events in this environment (production, staging…), with their counts in it; empty, all"`
	Sort        string `json:"sort,omitempty" jsonschema:"last_seen (default), first_seen or count; always descending"`
	Cursor      string `json:"cursor,omitempty" jsonschema:"next_cursor from the previous response, for the next page"`
	Limit       int    `json:"limit,omitempty" jsonschema:"issues per page, 1 to 100; 25 by default"`
}

type issueIn struct {
	IssueID     int64  `json:"issue_id" jsonschema:"issue id"`
	Environment string `json:"environment,omitempty" jsonschema:"only events from this environment (production, staging…); empty, all"`
}

type listEventsIn struct {
	IssueID     int64  `json:"issue_id" jsonschema:"issue id"`
	Environment string `json:"environment,omitempty" jsonschema:"only events from this environment (production, staging…); empty, all"`
	Cursor      string `json:"cursor,omitempty" jsonschema:"next_cursor from the previous response"`
	Limit       int    `json:"limit,omitempty" jsonschema:"events per page, 1 to 100; 25 by default"`
}

type setStatusIn struct {
	IssueID int64  `json:"issue_id" jsonschema:"issue id"`
	Status  string `json:"status" jsonschema:"open, resolved, resolved_next or silenced"`
	Comment string `json:"comment,omitempty" jsonschema:"Markdown comment attached to the change in the history, e.g. what was fixed and in which commit"`
}

type contextIn struct {
	IssueID     int64  `json:"issue_id" jsonschema:"issue id"`
	Environment string `json:"environment,omitempty" jsonschema:"only events from this environment (production, staging…): counts and latest event; empty, all"`
	Event       string `json:"event,omitempty" jsonschema:"internal id or event_id of an event of the issue (from list_events), to read that one instead of the latest"`
}

type eventIn struct {
	EventID string `json:"event_id" jsonschema:"internal id (from list_events) or the SDK event_id, as 32 hex or a UUID"`
}

type issueOnlyIn struct {
	IssueID int64 `json:"issue_id" jsonschema:"issue id"`
}

type addCommentIn struct {
	IssueID int64  `json:"issue_id" jsonschema:"issue id"`
	Body    string `json:"body" jsonschema:"the comment, in Markdown"`
}

type assignIn struct {
	IssueID  int64  `json:"issue_id" jsonschema:"issue id"`
	Assignee string `json:"assignee" jsonschema:"me to assign it to yourself (your token), a person's login or a token name (from list_assignees); empty unassigns it"`
	Kind     string `json:"kind,omitempty" jsonschema:"user or token; only needed if the name is both a person's and a token's"`
}

type listReleasesIn struct {
	Project     string `json:"project,omitempty" jsonschema:"project id or name; empty, all"`
	Environment string `json:"environment,omitempty" jsonschema:"only releases with events, deploys or sessions in this environment, counting only those; empty, all"`
	Cursor      string `json:"cursor,omitempty" jsonschema:"next_cursor from the previous response"`
	Limit       int    `json:"limit,omitempty" jsonschema:"releases per page, 1 to 100; 25 by default"`
}

type releaseIn struct {
	Project     string `json:"project" jsonschema:"project id or name (shown in list_issues and list_releases)"`
	Version     string `json:"version" jsonschema:"the version, like web@1.4.0"`
	Environment string `json:"environment,omitempty" jsonschema:"only what belongs to this environment; empty, all"`
}

// esFields son las descripciones en español de los campos de entrada, por
// nombre en JSON; las inglesas van en las etiquetas jsonschema.
var esFields = map[string]string{
	"project":     "id o nombre del proyecto; vacío, todos",
	"status":      "open, resolved, resolved_next, silenced o all; vacío es open, o all si query trae is:",
	"query":       "búsqueda, p. ej. is:open browser:Safari !environment:staging timeout; la sintaxis, en la descripción de la herramienta",
	"environment": "solo los eventos de este entorno (production, staging…); vacío, todos",
	"sort":        "last_seen (por defecto), first_seen o count; siempre de mayor a menor",
	"cursor":      "next_cursor de la respuesta anterior, para la página siguiente",
	"issue_id":    "id del issue",
	"event":       "id interno o event_id de un evento del issue (de list_events), para leer ese en vez del último",
	"event_id":    "id interno (de list_events) o event_id del SDK, en 32 hex o UUID",
	"body":        "el comentario, en Markdown",
	"assignee":    "me para asignártelo a ti (a tu token), el login de una persona o el nombre de un token (de list_assignees); vacío lo desasigna",
	"kind":        "user o token; solo hace falta si el nombre es a la vez de una persona y de un token",
	"version":     "la versión, como web@1.4.0",
	"slug":        "slug del monitor, el monitor_slug del SDK o el de un monitor de disponibilidad",
	"type":        "cron o uptime; vacío, busca primero un cron y luego uno de disponibilidad",
	"period":      "day (24 horas) o week (7 días); vacío, el del resumen del proyecto, o day si no tiene",
	"end":         "fin del periodo en RFC 3339, como 2026-11-22T09:00:00+01:00; vacío, ahora",
	"language":    "es o en; vacío, el del resumen del proyecto",
}

// esToolFields son las descripciones en español que cambian de una herramienta a otra.
var esToolFields = map[string]map[string]string{
	"list_issues": {
		"environment": "solo los issues con eventos en este entorno (production, staging…), con sus recuentos en él; vacío, todos",
		"limit":       "issues por página, de 1 a 100; 25 por defecto",
	},
	"list_events":       {"limit": "eventos por página, de 1 a 100; 25 por defecto", "cursor": "next_cursor de la respuesta anterior"},
	"set_issue_status":  {"status": "open, resolved, resolved_next o silenced", "comment": "comentario en Markdown que queda unido al cambio en el historial, p. ej. qué se arregló y en qué commit"},
	"get_issue_context": {"environment": "solo los eventos de este entorno (production, staging…): recuentos y último evento; vacío, todos"},
	"list_releases": {
		"environment": "solo las releases con eventos, despliegues o sesiones en este entorno, contando solo lo suyo; vacío, todos",
		"cursor":      "next_cursor de la respuesta anterior",
		"limit":       "releases por página, de 1 a 100; 25 por defecto",
	},
	"get_release":   {"project": "id o nombre del proyecto (sale en list_issues y list_releases)", "environment": "solo lo de este entorno; vacío, todos"},
	"list_monitors": {"status": "en los crons ok, error, missed, timeout o pending; en los de disponibilidad up, down, pending o paused; vacío, todos"},
	"get_monitor": {
		"project": "id o nombre del proyecto (sale en list_monitors)",
		"cursor":  "next_cursor de la respuesta anterior, para ver ejecuciones más antiguas",
		"limit":   "ejecuciones por página, de 1 a 200; 50 por defecto",
	},
	"get_digest": {"project": "id o nombre del proyecto"},
}

// inputSchema es el esquema de entrada de la herramienta con las
// descripciones del idioma; en inglés, nil, y el SDK lo saca de las etiquetas.
func inputSchema[T any](l lang.Lang, tool string) *jsonschema.Schema {
	if l != lang.ES {
		return nil
	}
	sc, err := jsonschema.For[T](nil)
	if err != nil {
		panic(err)
	}
	for name, p := range sc.Properties {
		if d, ok := esToolFields[tool][name]; ok {
			p.Description = d
		} else if d, ok := esFields[name]; ok {
			p.Description = d
		} else {
			panic("mcpserver: falta la descripción en español de " + tool + "." + name)
		}
	}
	return sc
}

// addTool es mcp.AddTool con el esquema de entrada en el idioma del servidor.
func addTool[In, Out any](s *mcp.Server, l lang.Lang, t *mcp.Tool, h mcp.ToolHandlerFor[In, Out]) {
	if sc := inputSchema[In](l, t.Name); sc != nil {
		t.InputSchema = sc
	}
	mcp.AddTool(s, t, h)
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
	// Lang es el idioma de las instrucciones, las descripciones y lo que devuelve.
	Lang lang.Lang
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

const instructionsEN = `Errors from the applications that send to Mothlog, grouped into issues. To take an error from start to finish:

1. Find it with list_issues. Without a query, the open ones that happened most recently. Query examples: "is:open environment:production", "is:regressed release:1.4.0", "TypeError browser:Safari", "user:ana@example.com is:open", "is:unassigned is:open".
2. Assign it to yourself with assign_issue and assignee "me", so the team knows you are on it; yours come up with list_issues and query "assigned:me".
3. Read get_issue_context: a single Markdown with the summary, releases, environments, tags, the stack trace with the code of the application frames, breadcrumbs, request, user and history (earlier comments and status changes, worth reading before you start).
4. Fix it in the code.
5. Record it with add_comment: what caused the error and what was changed (commit, PR, file).
6. Set the status with set_issue_status: resolved_next if the fix is not deployed yet (it becomes resolved only when a later release is deployed), resolved if it already is, silenced if there is nothing to fix. It takes an optional comment that is attached to the change in the history.

If you need more: list_events and get_event for other events, get_issue_tags for all tags, list_releases and get_release for what a release brought, list_comments for the history, list_monitors and get_monitor for crons that fail or don't check in and sites that don't respond, get_digest for a summary of how a project is doing over the last 24 hours or 7 days. The read tools accept environment (production, staging…) to keep only the events from that environment.`

const instructionsES = `Errores de las aplicaciones que envían a Mothlog, agrupados en issues. Para llevar un error de principio a fin:

1. Encontrarlo con list_issues. Sin query, los abiertos que han pasado más recientemente. Ejemplos de query: "is:open environment:production", "is:regressed release:1.4.0", "TypeError browser:Safari", "user:ana@example.com is:open", "is:unassigned is:open".
2. Asignártelo con assign_issue y assignee "me", para que el equipo sepa que estás con él; los tuyos salen con list_issues y query "assigned:me".
3. Leer get_issue_context: un único Markdown con el resumen, las releases, los entornos, las etiquetas, la traza con el código de los fotogramas propios, los breadcrumbs, la petición, el usuario y el historial (comentarios y cambios de estado anteriores, que conviene leer antes de ponerse).
4. Arreglarlo en el código.
5. Dejar constancia con add_comment: qué causaba el error y qué se ha cambiado (commit, PR, fichero).
6. Marcar el estado con set_issue_status: resolved_next si el arreglo aún no está desplegado (pasa a resolved solo al desplegarse una release posterior), resolved si ya lo está, silenced si no hay nada que arreglar. Lleva un comment opcional que queda unido al cambio en el historial.

Si hace falta más: list_events y get_event para otros eventos, get_issue_tags para todas las etiquetas, list_releases y get_release para lo que trajo una release, list_comments para el historial, list_monitors y get_monitor para los crons que fallan o no llegan y las webs que no responden, get_digest para el resumen de cómo va un proyecto en las últimas 24 horas o 7 días. Las herramientas de leer aceptan environment (production, staging…) para quedarse con los eventos de ese entorno.`

// Name es el nombre del servidor MCP.
const Name = "mothlog"

func New(st *store.Store, opts Options) *mcp.Server {
	l := opts.Lang
	tr := l.T
	s := mcp.NewServer(&mcp.Implementation{Name: Name, Version: "0.2.0"}, &mcp.ServerOptions{
		Instructions: tr(instructionsEN, instructionsES),
	})
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true}
	actor := func(ctx context.Context) *store.Actor {
		if opts.Actor == nil {
			return nil
		}
		return opts.Actor(ctx)
	}
	errs := toolErrors{l}

	addTool(s, l, &mcp.Tool{
		Name:        "list_issues",
		Annotations: readOnly,
		Description: tr("Lists grouped errors (issues) with their title, culprit, status, event count, affected users (user_count), first and last seen, latest release and who owns them (assignee). "+
			"Use it to see what is failing right now (by default, open ones ordered by last seen) or to find errors by their text or tags. "+
			"Query syntax: space-separated terms that must all match. "+
			"is:open|resolved|resolved_next|silenced|regressed (regressed: open ones that came back after being resolved); "+
			"release:1.2.0, environment:production, level:error, project:web (id or name); "+
			"user:<id, email, username or IP> (users are stored hashed: give the exact value); "+
			"any key:value tag, like browser:Safari, os:\"Mac OS\", device:iPhone, runtime:CPython or the SDK's own tags; "+
			"assigned:me (yours), assigned:<login or token name>, is:assigned, is:unassigned; "+
			"! in front negates (!browser:Chrome, !timeout, !assigned:me); quotes group spaces (\"division by zero\"); "+
			"the rest is free text searched in the title and culprit, case-insensitive. "+
			"Tag values are exact, with their case; get_issue_tags tells the ones an issue has. "+
			"A malformed query returns an error saying what is wrong and at which character. "+
			"If the response has next_cursor, there are more pages.",
			"Lista los errores agrupados (issues) con su título, culprit, estado, número de eventos, usuarios afectados (user_count), primera y última vez, último release y quién lo lleva (assignee). "+
				"Úsala para ver qué está fallando ahora (por defecto, los abiertos ordenados por la última vez) o para encontrar errores por su texto o sus etiquetas. "+
				"Sintaxis de query: términos separados por espacios que se cumplen todos. "+
				"is:open|resolved|resolved_next|silenced|regressed (regressed: abiertos que volvieron tras resolverse); "+
				"release:1.2.0, environment:production, level:error, project:web (id o nombre); "+
				"user:<id, email, username o IP> (los usuarios se guardan con hash: hay que dar el valor exacto); "+
				"cualquier etiqueta clave:valor, como browser:Safari, os:\"Mac OS\", device:iPhone, runtime:CPython o las tags propias del SDK; "+
				"assigned:me (los tuyos), assigned:<login o nombre de token>, is:assigned, is:unassigned; "+
				"! delante niega (!browser:Chrome, !timeout, !assigned:me); las comillas agrupan espacios (\"division by zero\"); "+
				"el resto es texto libre que se busca en el título y el culprit sin distinguir mayúsculas. "+
				"Los valores de etiqueta van exactos, con sus mayúsculas; get_issue_tags dice los que tiene un issue. "+
				"Una query mal escrita devuelve un error que dice qué falla y en qué carácter. "+
				"Si la respuesta trae next_cursor, hay más páginas."),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listIssuesIn) (*mcp.CallToolResult, any, error) {
		page, err := st.ListIssues(ctx, store.IssueFilter{
			Project: in.Project, Status: in.Status, Query: in.Query, Environment: in.Environment,
			Sort: in.Sort, Cursor: in.Cursor, Limit: in.Limit, Viewer: actor(ctx),
		})
		return errs.result(page, err)
	})

	addTool(s, l, &mcp.Tool{
		Name:        "get_issue",
		Annotations: readOnly,
		Description: tr("Returns an issue with who owns it (assignee), its latest event in full as JSON, events per day for the last 14 days, the most frequent values of environment, release, user and organization_id, and its links: Linear issues and GitHub issues and PRs, with their state there. "+
			"Use it to know how many users or organizations it affects, since which release, or whether it is growing. To read the stack trace, get_stacktrace is better.",
			"Devuelve un issue con quién lo lleva (assignee), su último evento entero en JSON, los eventos por día de los últimos 14 días y los valores más frecuentes de entorno, release, usuario y organization_id, y sus enlaces (links): las issues de Linear y las issues y PRs de GitHub, con su estado allí. "+
				"Úsala para saber a cuántos usuarios u organizaciones afecta, desde qué release o si va a más. Para leer la traza, mejor get_stacktrace."),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in issueIn) (*mcp.CallToolResult, any, error) {
		d, err := st.IssueDetailIn(ctx, in.IssueID, in.Environment, time.Now())
		return errs.result(d, err)
	})

	addTool(s, l, &mcp.Tool{
		Name:        "get_issue_tags",
		Annotations: readOnly,
		Description: tr("Returns the tags of an issue (browser, OS, device, runtime, release, environment, level and the SDK tags) with the most frequent values of each, their count and percentage, and how many distinct users it affects. "+
			"Use it to find out whether an error belongs to a specific browser, release or environment; then you can search by that value with list_issues.",
			"Devuelve las etiquetas de un issue (navegador, sistema, dispositivo, runtime, release, entorno, nivel y las tags del SDK) con los valores más frecuentes de cada una, su recuento y su porcentaje, y cuántos usuarios distintos afecta. "+
				"Úsala para saber si un error es de un navegador, un release o un entorno concretos; luego puedes buscar por ese valor con list_issues."),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in issueIn) (*mcp.CallToolResult, any, error) {
		tags, err := st.IssueTags(ctx, in.IssueID)
		return errs.result(tags, err)
	})

	addTool(s, l, &mcp.Tool{
		Name:        "get_stacktrace",
		Annotations: readOnly,
		Description: tr("Returns the latest event of an issue in Markdown: release, environment, tags, user, request, the exception chain with the code of the application frames and the latest breadcrumbs. "+
			"Use it before touching code to fix an error.",
			"Devuelve en Markdown el último evento de un issue: release, entorno, etiquetas, usuario, request, la cadena de excepciones con el código de los fotogramas de la aplicación y los últimos breadcrumbs. "+
				"Úsala antes de tocar código para arreglar un error."),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in issueIn) (*mcp.CallToolResult, any, error) {
		d, err := st.IssueDetailIn(ctx, in.IssueID, in.Environment, time.Now())
		if err != nil {
			return nil, nil, errs.toolError(err)
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: markdown.Issue(d, l)}}}, nil, nil
	})

	addTool(s, l, &mcp.Tool{
		Name:        "list_events",
		Annotations: readOnly,
		Description: tr("Lists the events (each time it happened) of an issue, newest first, with date, release, environment, user and message. "+
			"Use it to see whether an error keeps happening after a deploy or which users it happened to.",
			"Lista los eventos (cada vez que ocurrió) de un issue, del más nuevo al más viejo, con fecha, release, entorno, usuario y mensaje. "+
				"Úsala para ver si un error sigue pasando tras un despliegue o a qué usuarios les pasó."),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listEventsIn) (*mcp.CallToolResult, any, error) {
		page, err := st.IssueEventsIn(ctx, in.IssueID, in.Environment, in.Cursor, in.Limit)
		return errs.result(page, err)
	})

	addTool(s, l, &mcp.Tool{
		Name:        "get_issue_context",
		Annotations: readOnly,
		Description: tr("Returns in a single Markdown document everything needed to understand and fix an issue: the summary (status, level, project, events, users, first and last seen and the link to the UI), "+
			"the releases (first, last and the one it was resolved in), the environments with their events, the most frequent tags with their percentage, "+
			"the event with the stack trace and the code of the application frames, the breadcrumbs, the request and the user, the history of comments and status changes, "+
			"and which tools to use to comment on it or resolve it. "+
			"Use it after finding the issue with list_issues and before touching code; it is more complete than get_stacktrace. "+
			"With event, that event's (id or event_id, from list_events) instead of the latest. If the document goes over 64 KB, it trims breadcrumbs, library frames and old history, and says so at the top.",
			"Devuelve en un único documento Markdown todo lo que hace falta para entender y arreglar un issue: el resumen (estado, nivel, proyecto, eventos, usuarios, primera y última vez y el enlace a la interfaz), "+
				"las releases (primera, última y con la que se resolvió), los entornos con sus eventos, las etiquetas más frecuentes con su porcentaje, "+
				"el evento con la traza y el código de los fotogramas de la aplicación, los breadcrumbs, la petición y el usuario, el historial de comentarios y cambios de estado, "+
				"y qué herramientas usar para comentarlo o resolverlo. "+
				"Úsala después de encontrar el issue con list_issues y antes de tocar código; es más completa que get_stacktrace. "+
				"Con event, el de ese evento (id o event_id, de list_events) en vez del último. Si el documento pasa de 64 KB, recorta breadcrumbs, fotogramas de librerías e historial viejo, y lo dice al principio."),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in contextIn) (*mcp.CallToolResult, any, error) {
		c, err := st.IssueContextIn(ctx, in.IssueID, in.Environment, in.Event, actor(ctx), time.Now())
		if err != nil {
			return nil, nil, errs.notFound(err, tr("no such issue, or that event isn't one of its events", "no existe ese issue, o ese evento no es suyo"))
		}
		url := ""
		if opts.IssueURL != nil {
			url = opts.IssueURL(ctx, in.IssueID)
		}
		return textResult(markdown.Context(c, url, markdown.MaxContextBytes, l)), nil, nil
	})

	addTool(s, l, &mcp.Tool{
		Name:        "get_event",
		Annotations: readOnly,
		Description: tr("Returns a whole event as JSON: exceptions with all their frames, breadcrumbs, contexts, request, user, tags, release, environment and SDK. "+
			"Use it when get_issue_context is not enough: to see an event that is not the latest (the ids come from list_events) or the data the Markdown summarizes, like contexts or library frames.",
			"Devuelve un evento entero en JSON: excepciones con todos sus fotogramas, breadcrumbs, contexts, request, usuario, etiquetas, release, entorno y SDK. "+
				"Úsala cuando get_issue_context no basta: para ver un evento que no es el último (los ids salen en list_events) o los datos que el Markdown resume, como contexts o los fotogramas de librerías."),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in eventIn) (*mcp.CallToolResult, any, error) {
		e, err := st.Event(ctx, in.EventID)
		if err != nil {
			return nil, nil, errs.notFound(err, tr("no such event", "no existe ese evento"))
		}
		return errs.result(e, nil)
	})

	addTool(s, l, &mcp.Tool{
		Name:        "list_comments",
		Annotations: readOnly,
		Description: tr("Returns the history of an issue, oldest first: the comments (Markdown, author and whether it is a person or a token) and the status changes (who, from where: UI, API, MCP, ingestion when it came back as a regression, a deploy, or Linear or GitHub when their issue was closed or reopened there or a PR was merged; from which status to which, the release and the comment left with the change). "+
			"Use it to learn what has already been tried with an error before you start, or to check that your comment or status change was recorded.",
			"Devuelve el historial de un issue, del más viejo al más nuevo: los comentarios (Markdown, autor y si es una persona o un token) y los cambios de estado (quién, desde dónde: interfaz, API, MCP, la ingesta al volver como regresión, un despliegue, o Linear o GitHub al cerrarse o reabrirse allí su issue o mergearse un PR; de qué estado a cuál, la release y el comentario que se dejó con el cambio). "+
				"Úsala para saber qué se ha intentado ya con un error antes de ponerte, o para comprobar que tu comentario o tu cambio de estado han quedado."),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in issueOnlyIn) (*mcp.CallToolResult, any, error) {
		h, err := st.History(ctx, in.IssueID, actor(ctx))
		if err != nil {
			return nil, nil, errs.notFound(err, errs.noIssue())
		}
		return errs.result(map[string]any{"data": h}, nil)
	})

	addTool(s, l, &mcp.Tool{
		Name: "add_comment",
		Annotations: &mcp.ToolAnnotations{
			DestructiveHint: new(false),
			OpenWorldHint:   new(false),
		},
		Description: tr("Adds a Markdown comment to the history of an issue, signed with your token's name. "+
			"Use it to record what you found out or did: the cause, the fix (commit, PR, file) or why there is nothing to fix. "+
			"If you change the status at the same time, the comment of set_issue_status is better, as it stays attached to the change. Each call adds a new comment.",
			"Añade un comentario en Markdown al historial de un issue, firmado con el nombre de tu token. "+
				"Úsala para dejar constancia de lo que has averiguado o hecho: la causa, el arreglo (commit, PR, fichero) o por qué no hay nada que arreglar. "+
				"Si a la vez cambias el estado, mejor el comment de set_issue_status, que queda unido al cambio. Cada llamada añade un comentario nuevo."),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in addCommentIn) (*mcp.CallToolResult, any, error) {
		a := actor(ctx)
		if a == nil {
			return nil, nil, errors.New(tr("unknown commenter: a token is needed", "no se sabe quién comenta: hace falta un token"))
		}
		c, err := st.AddComment(ctx, in.IssueID, *a, in.Body)
		if err != nil {
			return nil, nil, errs.notFound(err, errs.noIssue())
		}
		return errs.result(c, nil)
	})

	addTool(s, l, &mcp.Tool{
		Name: "assign_issue",
		Annotations: &mcp.ToolAnnotations{
			IdempotentHint:  true,
			DestructiveHint: new(false),
			OpenWorldHint:   new(false),
		},
		Description: tr("Assigns an issue to whoever is going to own it, so the team knows someone is on it. "+
			"Use it when you start on an error: with assignee \"me\" you assign it to yourself (your token), and then you find it with list_issues and query assigned:me. "+
			"A person's login or another token's name also work (list_assignees tells who); an empty assignee unassigns it. "+
			"It is recorded in the history with your token's name; assigning it to whoever already owns it records nothing. Returns the issue with its assignee.",
			"Asigna un issue a quien lo va a llevar, para que el equipo sepa que alguien está con él. "+
				"Úsala al ponerte con un error: con assignee \"me\" te lo asignas a ti (a tu token), y luego lo encuentras con list_issues y query assigned:me. "+
				"También vale el login de una persona o el nombre de otro token (list_assignees dice quiénes); assignee vacío lo desasigna. "+
				"Queda en el historial con el nombre de tu token; asignarlo a quien ya lo lleva no apunta nada. Devuelve el issue con su assignee."),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in assignIn) (*mcp.CallToolResult, any, error) {
		a := actor(ctx)
		to, err := store.AssigneeRef(in.Assignee, in.Kind, a)
		if err != nil {
			return nil, nil, errs.toolError(err)
		}
		issue, err := st.AssignIssue(ctx, in.IssueID, to, store.AssignChange{Actor: a, Source: store.SourceMCP})
		return errs.result(issue, err)
	})

	addTool(s, l, &mcp.Tool{
		Name:        "list_assignees",
		Annotations: readOnly,
		Description: tr("Lists who an issue can be assigned to: the people who have signed in to the UI (kind user, by login, with their name) and the active tokens (kind token, the agents). "+
			"Use it to give the assignee of assign_issue or the value of assigned: in list_issues.",
			"Lista a quién se puede asignar un issue: las personas que han entrado en la interfaz (kind user, por su login, con su nombre) y los tokens activos (kind token, los agentes). "+
				"Úsala para dar el assignee de assign_issue o el valor de assigned: en list_issues."),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		out, err := st.Assignees(ctx)
		return errs.result(map[string]any{"data": out}, err)
	})

	addTool(s, l, &mcp.Tool{
		Name:        "list_releases",
		Annotations: readOnly,
		Description: tr("Lists releases, newest first, with their deploys and how many new errors (new_issues), errors with events (issues), regressions and events each one brought. "+
			"With SDK sessions it includes their health in health (and per environment in health_by_environment): crash_free_sessions and crash_free_users from 0 to 1 (null if there are no sessions or users), the totals and adoption, the share of the project's sessions in the last 24 h that belong to that release. "+
			"Use it to know what was deployed and where, or which release made things worse; then get_release for the details of one. If the response has next_cursor, there are more pages.",
			"Lista las releases, de la más nueva a la más vieja, con sus despliegues y cuántos errores nuevos (new_issues), con eventos (issues), regresiones y eventos trajo cada una. "+
				"Con las sesiones de los SDK trae su salud en health (y por entorno en health_by_environment): crash_free_sessions y crash_free_users de 0 a 1 (null si no hay sesiones o usuarios), los totales y adoption, la parte de las sesiones de las últimas 24 h del proyecto que es de esa release. "+
				"Úsala para saber qué se ha desplegado y dónde, o qué release empeoró las cosas; luego get_release para el detalle de una. Si la respuesta trae next_cursor, hay más páginas."),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listReleasesIn) (*mcp.CallToolResult, any, error) {
		page, err := st.ListReleases(ctx, store.ReleaseFilter(in))
		return errs.result(page, err)
	})

	addTool(s, l, &mcp.Tool{
		Name:        "get_release",
		Annotations: readOnly,
		Description: tr("Returns a release of a project with its deploys, the previous and next ones, and what it brought: the issues that appeared with it (new), the regressions (regressed), the ones that already existed and kept coming (continued) and the ones that stopped or were resolved when it was deployed (stopped). "+
			"It also includes its health (health, health_by_environment) and the sessions per hour over the last 72 h (health_series). "+
			"Use it after a deploy to see whether it broke something or fixed what it should; to find the issues of a release, list_issues with release:<version>.",
			"Devuelve una release de un proyecto con sus despliegues, la anterior y la siguiente, y lo nuevo que trajo: los issues que aparecieron con ella (new), las regresiones (regressed), los que ya existían y siguieron saliendo (continued) y los que dejaron de salir o se resolvieron al desplegarla (stopped). "+
				"Trae también su salud (health, health_by_environment) y las sesiones por hora de las últimas 72 h (health_series). "+
				"Úsala tras un despliegue para ver si rompió algo o si arregló lo que debía; para buscar los issues de una release, list_issues con release:<versión>."),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in releaseIn) (*mcp.CallToolResult, any, error) {
		p, err := project(ctx, st, in.Project)
		if err != nil {
			return nil, nil, errs.notFound(err, errs.noProject())
		}
		d, err := st.ReleaseDetail(ctx, p.ID, in.Version, in.Environment)
		if err != nil {
			return nil, nil, errs.notFound(err, tr("no such release in the project", "no existe esa release en el proyecto"))
		}
		return errs.result(d, nil)
	})

	addTool(s, l, &mcp.Tool{
		Name: "set_issue_status",
		Annotations: &mcp.ToolAnnotations{
			IdempotentHint:  true,
			DestructiveHint: new(false),
			OpenWorldHint:   new(false),
		},
		Description: tr("Changes the status of an issue. resolved: fixed; if it happens again, it reopens as a regression. "+
			"resolved_next: fixed in the next release (use it when the fix is not deployed yet); it becomes resolved when a release later than its latest event's is deployed, and reopens as a regression if it happens in a later release. "+
			"silenced: stop alerting even if it keeps happening. open: reopen it. Use it only when the user asks or after fixing the error. "+
			"With comment (Markdown), the comment is attached to the change in the history: say what was fixed and where. The change is recorded in the history with your token's name; setting the same status again records nothing new.",
			"Cambia el estado de un issue. resolved: arreglado; si vuelve a ocurrir, se reabre como regresión. "+
				"resolved_next: arreglado en el próximo release (úsalo cuando el arreglo aún no está desplegado); pasa a resolved al desplegarse una release posterior a la de su último evento, y se reabre como regresión si ocurre en una release posterior. "+
				"silenced: no avisar más aunque siga ocurriendo. open: reabrirlo. Úsala solo cuando el usuario lo pida o tras arreglar el error. "+
				"Con comment (Markdown), el comentario queda unido al cambio en el historial: di qué se arregló y dónde. El cambio queda en el historial con el nombre de tu token; repetir el mismo estado no apunta nada nuevo."),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in setStatusIn) (*mcp.CallToolResult, any, error) {
		issue, err := st.ChangeIssueStatus(ctx, in.IssueID, in.Status, store.StatusChange{
			Actor: actor(ctx), Source: store.SourceMCP, Comment: in.Comment,
		})
		return errs.result(issue, err)
	})
	addMonitorTools(s, st, errs)
	addDigestTool(s, st, opts.BaseURL, errs)
	if opts.Scopes != nil {
		s.AddReceivingMiddleware(scopeMiddleware(opts.Scopes, l))
	}
	return s
}

// scopeMiddleware deja ver y usar solo las herramientas que permiten los
// permisos de quien llama.
func scopeMiddleware(scopes func(context.Context) []string, l lang.Lang) mcp.Middleware {
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
							Text: l.F("%s needs the %q scope and this connection only has %q: connect again and grant it on the consent screen",
								"%s necesita el permiso %q y esta conexión solo tiene %q: vuelve a conectar y concédelo en la pantalla de consentimiento",
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

// toolErrors da los errores de las herramientas en el idioma del servidor.
type toolErrors struct{ l lang.Lang }

func (e toolErrors) noIssue() string   { return e.l.T("no such issue", "no existe ese issue") }
func (e toolErrors) noProject() string { return e.l.T("no such project", "no existe ese proyecto") }

// notFound es toolError con su propio mensaje para lo que no existe.
func (e toolErrors) notFound(err error, msg string) error {
	if errors.Is(err, store.ErrNotFound) {
		return errors.New(msg)
	}
	return e.toolError(err)
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

func (e toolErrors) result(v any, err error) (*mcp.CallToolResult, any, error) {
	if err != nil {
		return nil, nil, e.toolError(err)
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}, nil, nil
}

func (e toolErrors) toolError(err error) error {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return errors.New(e.noIssue())
	case errors.Is(err, store.ErrInvalid), errors.Is(err, store.ErrNoRelease):
		return err
	}
	slog.Error("mcp", "err", err)
	return errors.New(e.l.T("internal error", "error interno"))
}
