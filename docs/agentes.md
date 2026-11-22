# Conectar un agente

intake-errors tiene un servidor MCP en `/mcp` con el que un agente puede llevar un error de principio a fin: encontrarlo, leer todo su contexto, arreglarlo, dejar constancia de lo que ha hecho y marcar el estado. Todo lo que hace queda en el historial del error con el nombre de su token, y se ve en la interfaz.

`GET /llms.txt`, público y sin token, resume el servicio, la API y el MCP para un modelo.

## 1. Crear un token

Un token por agente, con un nombre que diga quién es: es el autor de sus comentarios y cambios de estado.

```sh
intake-errors tokens create agente-backend   # imprime el token (ie_…) una sola vez
intake-errors tokens list
intake-errors tokens revoke agente-backend   # si se filtra o deja de hacer falta
```

En la base solo queda el sha256 del token. Los tokens no tienen permisos distintos: con uno, el agente lee todos los errores de todos los proyectos, con lo que traen sus eventos (usuarios, peticiones, breadcrumbs), comenta y cambia estados por el MCP, y por la API puede además editar filtros y reglas de aviso y apuntar releases. Guárdalo en el gestor de secretos del cliente, no en un fichero del repositorio, y revócalo cuando el agente deje de usarlo.

## 2. Conectar el cliente

El servidor habla Streamable HTTP sin sesión: cada petición es un `POST` a `https://<host>/mcp` con la cabecera `Authorization: Bearer <token>`. Cualquier réplica atiende cualquier petición.

### Claude Code

```sh
claude mcp add --transport http intake-errors https://errors.example.com/mcp \
  --header "Authorization: Bearer $INTAKE_ERRORS_TOKEN"
```

Con `--scope project` queda en el `.mcp.json` del repositorio para todo el equipo; en ese caso, deja la cabecera con la variable (`"Authorization": "Bearer ${INTAKE_ERRORS_TOKEN}"`) y que cada uno ponga la suya en su entorno.

### Un cliente genérico

Los clientes que leen la configuración en JSON (la forma habitual de `mcpServers`) aceptan algo así:

```json
{
  "mcpServers": {
    "intake-errors": {
      "type": "http",
      "url": "https://errors.example.com/mcp",
      "headers": { "Authorization": "Bearer ${INTAKE_ERRORS_TOKEN}" }
    }
  }
}
```

Y desde código, con el SDK oficial de MCP para Go:

```go
type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

client := mcp.NewClient(&mcp.Implementation{Name: "mi-agente", Version: "1.0"}, nil)
cs, err := client.Connect(ctx, &mcp.StreamableClientTransport{
	Endpoint:   "https://errors.example.com/mcp",
	HTTPClient: &http.Client{Transport: bearer{os.Getenv("INTAKE_ERRORS_TOKEN")}},
}, nil)
// cs.CallTool(ctx, &mcp.CallToolParams{Name: "list_issues", Arguments: map[string]any{"query": "is:open"}})
```

Para probar a mano sin cliente, el mismo protocolo con `curl`:

```sh
curl -s https://errors.example.com/mcp \
  -H "Authorization: Bearer $INTAKE_ERRORS_TOKEN" \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'
```

## 3. Las herramientas

| Herramienta | Para qué | Solo lectura |
|---|---|---|
| `list_issues` | buscar errores con el [lenguaje de búsqueda](../README.md#búsqueda): `is:open environment:production`, `is:regressed release:1.4.0`, `TypeError browser:Safari` | sí |
| `get_issue_context` | el `context.md`: todo lo que hace falta para arreglar un error, en un Markdown | sí |
| `get_issue`, `get_issue_tags`, `get_stacktrace` | el issue en JSON, sus etiquetas y el `stacktrace.md` | sí |
| `list_events`, `get_event` | los eventos de un issue y un evento entero | sí |
| `list_comments` | el historial: comentarios y cambios de estado | sí |
| `list_releases`, `get_release` | las releases y lo que trajo una (nuevos, regresiones, los que dejaron de salir) y su salud (sesiones y usuarios sin crash, adopción) | sí |
| `list_monitors`, `get_monitor` | los crons con su estado y la próxima ejecución esperada, y el historial de uno | sí |
| `add_comment` | comentar en Markdown | no |
| `set_issue_status` | `resolved_next`, `resolved`, `silenced` u `open`, con un `comment` opcional unido al cambio; idempotente | no |

Las de leer aceptan `environment`. El servidor manda en sus instrucciones el flujo de abajo, así que un cliente que las lee no necesita más.

## 4. El flujo

1. **Encontrar el error**: `list_issues` con `{"query": "is:open project:backend \"division by zero\""}`.
2. **Leer su contexto**: `get_issue_context` con `{"issue_id": 42}`. Trae el resumen, las releases, los entornos, las etiquetas más frecuentes, la traza con el código de los fotogramas propios, los breadcrumbs, la petición, el usuario y el historial, y acaba con lo que el agente puede hacer. Si pasa de 64 KB, quita breadcrumbs, fotogramas de librerías e historial viejo, y lo dice al principio.
3. **Arreglarlo** en el código.
4. **Dejar constancia**: `add_comment` con `{"issue_id": 42, "body": "Causa: media() divide entre envios, que es 0 en los pedidos sin enviar."}`.
5. **Marcar el estado**: `set_issue_status` con `{"issue_id": 42, "status": "resolved_next", "comment": "Arreglado en abc123: devuelve 0 sin envíos."}`. En `resolved_next`, el error pasa a resuelto solo cuando se despliega una release posterior a la de su último evento, y si vuelve en ella se reabre como regresión; usa `resolved` si el arreglo ya está desplegado.
6. **Comprobarlo**: `list_comments` con `{"issue_id": 42}` enseña el comentario, el cambio de estado con su comentario y, tras el despliegue, el paso a resuelto.

Sin MCP, lo mismo con la API: `GET /api/0/issues/42/context.md`, `POST /api/0/issues/42/comments/` y `POST /api/0/issues/42/status`.

En la interfaz, «Copiar para un agente» en el detalle de un error copia el mismo `context.md`, para pegárselo a un agente que no tiene el MCP.
