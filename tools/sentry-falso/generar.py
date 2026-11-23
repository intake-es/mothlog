"""Genera testdata/sentry/: las respuestas de una API de Sentry falsa.

Los eventos salen de los envíos grabados de testdata/envelopes/, pasados a la
forma en que la API de Sentry los devuelve (entries, fotogramas en camelCase,
el código como pares [línea, texto], tags como lista). Cada issue lleva uno o
varios eventos: el grabado y copias con otro id y otra fecha.

    python3 -I tools/sentry-falso/generar.py
"""

import gzip
import hashlib
import json
import pathlib
from datetime import datetime, timedelta, timezone

RAIZ = pathlib.Path(__file__).resolve().parents[2]
ENVIOS = RAIZ / "testdata" / "envelopes"
SALIDA = RAIZ / "testdata" / "sentry" / "ejemplo"
ORG = "ejemplo"


def evento_grabado(nombre):
    datos = (ENVIOS / f"{nombre}.envelope").read_bytes()
    try:
        datos = gzip.decompress(datos)
    except OSError:
        pass
    lineas = datos.split(b"\n")
    for i in range(1, len(lineas) - 1, 2):
        if lineas[i].strip() and json.loads(lineas[i]).get("type") == "event":
            return json.loads(lineas[i + 1])
    raise SystemExit(f"{nombre}: sin evento")


def fecha(ts):
    if isinstance(ts, (int, float)):
        t = datetime.fromtimestamp(ts, timezone.utc)
    else:
        t = datetime.fromisoformat(ts.replace("Z", "+00:00"))
    return t


def iso(t):
    return t.astimezone(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%f")[:-3] + "Z"


def nuevo_id(*partes):
    return hashlib.md5("/".join(partes).encode()).hexdigest()


def fotograma(f):
    contexto = []
    if f.get("lineno"):
        pre = f.get("pre_context") or []
        n = f["lineno"] - len(pre)
        for linea in pre:
            contexto.append([n, linea])
            n += 1
        if "context_line" in f:
            contexto.append([f["lineno"], f["context_line"]])
        for i, linea in enumerate(f.get("post_context") or []):
            contexto.append([f["lineno"] + 1 + i, linea])
    return {
        "filename": f.get("filename"),
        "absPath": f.get("abs_path"),
        "module": f.get("module"),
        "package": None,
        "platform": None,
        "instructionAddr": None,
        "symbolAddr": None,
        "function": f.get("function"),
        "rawFunction": None,
        "symbol": None,
        "context": contexto,
        "lineNo": f.get("lineno"),
        "colNo": f.get("colno"),
        "inApp": bool(f.get("in_app")),
        "trust": None,
        "errors": None,
        "lock": None,
        "sourceLink": None,
        "vars": f.get("vars") or {},
    }


def a_la_api(ev, issue_id, proyecto, event_id, cuando, migas):
    entradas = []
    titulo, culprit, tipo, metadata = "", "", "default", {}
    exc = ev.get("exception")
    if exc:
        valores = exc["values"] if isinstance(exc, dict) else exc
        api = []
        for v in valores:
            frames = (v.get("stacktrace") or {}).get("frames") or []
            api.append({
                "type": v.get("type"),
                "value": v.get("value"),
                "mechanism": v.get("mechanism"),
                "threadId": None,
                "module": v.get("module"),
                "stacktrace": {
                    "frames": [fotograma(f) for f in frames],
                    "framesOmitted": None,
                    "registers": None,
                    "hasSystemFrames": any(not f.get("in_app") for f in frames),
                } if frames else None,
                "rawStacktrace": None,
            })
        entradas.append({"type": "exception", "data": {"values": api, "hasSystemFrames": True, "excOmitted": None}})
        ultima = valores[-1]
        titulo = f"{ultima.get('type')}: {ultima.get('value')}"
        tipo = "error"
        metadata = {"type": ultima.get("type"), "value": ultima.get("value"), "in_app_frame_mix": "mixed"}
        propios = [f for f in (ultima.get("stacktrace") or {}).get("frames") or [] if f.get("in_app")]
        if propios:
            culprit = f"{propios[-1].get('module') or propios[-1].get('filename')} in {propios[-1].get('function')}"
            metadata["filename"] = propios[-1].get("filename")
            metadata["function"] = propios[-1].get("function")
        else:
            culprit = ev.get("transaction") or ""
    mensaje = ""
    if ev.get("logentry"):
        le = ev["logentry"]
        mensaje = le.get("formatted") or le.get("message") or ""
        entradas.append({"type": "message", "data": {"formatted": mensaje, "message": le.get("message"), "params": le.get("params") or None}})
        if not titulo:
            titulo = mensaje
            metadata = {"title": mensaje}
            culprit = ev.get("transaction") or ""
    if migas:
        entradas.append({"type": "breadcrumbs", "data": {"values": migas}})
    req = ev.get("request")
    if req:
        cab = req.get("headers") or {}
        cab = sorted(([k.title(), v] for k, v in cab.items()) if isinstance(cab, dict) else cab)
        entradas.append({"type": "request", "data": {
            "apiTarget": None,
            "method": req.get("method"),
            "url": req.get("url"),
            "query": [],
            "fragment": None,
            "data": None,
            "headers": cab,
            "cookies": [],
            "env": req.get("env"),
            "inferredContentType": None,
        }})

    tags = dict(ev.get("tags") or {})
    for clave in ("environment", "level", "release", "server_name", "transaction", "logger"):
        if ev.get(clave):
            tags[clave] = ev[clave]
    tags.setdefault("level", "error")
    if exc:
        mech = (valores[-1].get("mechanism") or {})
        tags["handled"] = "yes" if mech.get("handled", True) else "no"
        if mech.get("type"):
            tags["mechanism"] = mech["type"]
    rt = (ev.get("contexts") or {}).get("runtime") or {}
    if rt.get("name"):
        tags["runtime"] = f"{rt['name']} {rt.get('version', '')}".strip()
        tags["runtime.name"] = rt["name"]
    if req and req.get("url"):
        tags["url"] = req["url"]
    usuario = ev.get("user")
    if usuario and usuario.get("id"):
        tags["user"] = f"id:{usuario['id']}"

    contextos = {}
    for k, v in (ev.get("contexts") or {}).items():
        v = dict(v)
        v.setdefault("type", k)
        contextos[k] = v

    release = None
    if ev.get("release"):
        release = {
            "id": 1,
            "version": ev["release"],
            "shortVersion": ev["release"],
            "status": "open",
            "dateCreated": "2026-11-01T09:00:00.000Z",
            "dateReleased": None,
            "firstEvent": "2026-11-01T09:12:00.000Z",
            "lastEvent": iso(cuando),
            "newGroups": 0,
            "projects": [{"id": int(proyecto["id"]), "slug": proyecto["slug"], "name": proyecto["name"]}],
        }

    return {
        "id": event_id,
        "groupID": issue_id,
        "eventID": event_id,
        "projectID": proyecto["id"],
        "size": 6543,
        "entries": entradas,
        "dist": ev.get("dist"),
        "message": mensaje,
        "title": titulo,
        "location": culprit,
        "user": {
            "id": usuario.get("id"),
            "email": usuario.get("email"),
            "username": usuario.get("username"),
            "ip_address": usuario.get("ip_address"),
            "name": None,
            "geo": None,
            "data": None,
        } if usuario else None,
        "contexts": contextos,
        "sdk": {"name": ev["sdk"]["name"], "version": ev["sdk"]["version"]},
        "context": ev.get("extra") or {},
        "packages": ev.get("modules") or {},
        "type": tipo,
        "metadata": metadata,
        "tags": [{"key": k, "value": tags[k]} for k in sorted(tags)],
        "platform": ev.get("platform"),
        "dateReceived": iso(cuando + timedelta(milliseconds=180)),
        "errors": [],
        "occurrence": None,
        "_meta": {},
        "crashFile": None,
        "culprit": culprit,
        "dateCreated": iso(cuando),
        "fingerprints": [hashlib.md5(titulo.encode()).hexdigest()],
        "groupingConfig": {"enhancements": "KLUv_SAYwQAAkwKRs25ld3N0eWxlOjIwMjMtMDEtMTGQ", "id": "newstyle:2023-01-11"},
        "release": release,
        "userReport": None,
        "sdkUpdates": [],
        "resolvedWith": [],
        "nextEventID": None,
        "previousEventID": None,
    }, titulo, culprit, metadata, tipo


PROYECTOS = [
    {"id": "4501001", "slug": "api", "name": "api", "platform": "python"},
    {"id": "4501002", "slug": "web", "name": "web", "platform": "javascript-browser"},
    {"id": "4501003", "slug": "sin-errores", "name": "sin-errores", "platform": "python"},
]

ISSUES = [
    # id, proyecto, envío grabado, copias, estado, detalle del estado, contador, usuarios
    ("7001", "api", "api-excepcion-1", 3, "unresolved", {}, 42, 0),
    ("7002", "api", "api-validacion-1", 2, "resolved", {"inRelease": "fixture-release-1"}, 9, 0),
    ("7003", "api", "api-tarea-a-1", 1, "ignored", {"ignoreCount": None, "ignoreUntil": None}, 12, 0),
    ("7004", "api", "api-log-error-1", 1, "resolved", {"inNextRelease": True}, 3, 0),
    ("7005", "api", "api-sesion-crash-1", 7, "unresolved", {}, 7, 1),
    ("7006", "api", None, 0, "unresolved", {}, 128, 4),
    ("7101", "web", "web-typeerror-1", 2, "unresolved", {}, 5, 2),
]

MIGAS = [
    {"type": "http", "timestamp": None, "level": "info", "message": None, "category": "httplib",
     "data": {"method": "GET", "url": "http://inventario.interno/stock/p_1042", "status_code": 200}, "event_id": None},
    {"type": "default", "timestamp": None, "level": "info", "message": "calculando la media del pedido", "category": "pedidos",
     "data": None, "event_id": None},
]


def main():
    SALIDA.mkdir(parents=True, exist_ok=True)
    proyecto = {p["slug"]: p for p in PROYECTOS}
    issues = {p["slug"]: [] for p in PROYECTOS}
    for issue_id, slug, envio, copias, estado, detalle, contador, usuarios in ISSUES:
        p = proyecto[slug]
        eventos = []
        titulo, culprit, metadata, tipo = "OperationalError: server closed the connection unexpectedly", "pedidos.db in conectar", {"type": "OperationalError", "value": "server closed the connection unexpectedly"}, "error"
        nivel = "error"
        primera = ultima = datetime(2026, 8, 2, 10, 0, tzinfo=timezone.utc)
        if envio:
            ev = evento_grabado(envio)
            base = fecha(ev["timestamp"])
            nivel = ev.get("level") or "error"
            for k in range(copias):
                cuando = base - timedelta(days=copias - 1 - k, minutes=7 * k)
                eid = ev["event_id"].replace("-", "") if k == copias - 1 else nuevo_id(envio, str(k))
                migas = []
                if slug == "api" and k % 2 == 0:
                    for j, m in enumerate(MIGAS):
                        m = dict(m)
                        m["timestamp"] = iso(cuando - timedelta(seconds=2 - j))
                        migas.append(m)
                api, titulo, culprit, metadata, tipo = a_la_api(ev, issue_id, p, eid, cuando, migas)
                eventos.append(api)
            eventos.sort(key=lambda e: e["dateCreated"], reverse=True)
            primera = fecha(eventos[-1]["dateCreated"]) - timedelta(days=20)
            ultima = fecha(eventos[0]["dateCreated"])
        (SALIDA / f"events-{issue_id}.json").write_text(json.dumps(eventos, indent=1, ensure_ascii=False) + "\n")
        issues[slug].append({
            "id": issue_id,
            "shareId": None,
            "shortId": f"{slug.upper()}-{int(issue_id) % 100}",
            "title": titulo,
            "culprit": culprit,
            "permalink": f"https://{ORG}.sentry.io/issues/{issue_id}/",
            "logger": None,
            "level": nivel,
            "status": estado,
            "statusDetails": detalle,
            "substatus": {"unresolved": "ongoing", "resolved": None, "ignored": "archived_until_escalating"}[estado],
            "isPublic": False,
            "platform": p["platform"],
            "project": {"id": p["id"], "name": p["name"], "slug": p["slug"], "platform": p["platform"]},
            "type": tipo,
            "metadata": metadata,
            "numComments": 0,
            "assignedTo": None,
            "isBookmarked": False,
            "isSubscribed": False,
            "subscriptionDetails": None,
            "hasSeen": True,
            "annotations": [],
            "issueType": "error",
            "issueCategory": "error",
            "priority": "high",
            "priorityLockedAt": None,
            "isUnhandled": True,
            "count": str(contador),
            "userCount": usuarios,
            "firstSeen": iso(primera),
            "lastSeen": iso(ultima),
            "stats": {"24h": [[int(ultima.timestamp()) // 3600 * 3600, 1]]},
        })
    for slug, lista in issues.items():
        lista.sort(key=lambda i: i["lastSeen"], reverse=True)
        (SALIDA / f"issues-{slug}.json").write_text(json.dumps(lista, indent=1, ensure_ascii=False) + "\n")

    (SALIDA / "projects.json").write_text(json.dumps([{
        "id": p["id"], "slug": p["slug"], "name": p["name"], "platform": p["platform"],
        "dateCreated": "2025-03-10T08:00:00.000000Z", "isBookmarked": False, "isMember": True,
        "features": ["releases", "minidump"], "firstEvent": "2025-03-10T08:30:00.000000Z", "firstTransactionEvent": False,
        "access": ["project:read", "event:read"], "hasAccess": True, "hasMinifiedStackTrace": p["slug"] == "web",
        "hasMonitors": False, "hasProfiles": False, "hasReplays": False, "hasSessions": True,
        "isInternal": False, "isPublic": False, "avatar": {"avatarType": "letter_avatar", "avatarUuid": None},
        "color": "#3f70bf", "status": "active",
    } for p in PROYECTOS], indent=1) + "\n")

    def proyectos(*slugs):
        return [{"id": int(proyecto[s]["id"]), "slug": s, "name": proyecto[s]["name"],
                 "platform": proyecto[s]["platform"], "platforms": [proyecto[s]["platform"]], "hasHealthData": True,
                 "newGroups": 0} for s in slugs]

    releases = [
        {"id": 301, "version": "fixture-release-1", "shortVersion": "fixture-release-1", "status": "open",
         "ref": None, "url": None, "dateCreated": "2026-11-01T09:00:00.000000Z", "dateReleased": "2026-11-01T09:10:00.000000Z",
         "data": {}, "newGroups": 4, "owner": None, "commitCount": 0, "lastCommit": None, "deployCount": 2,
         "lastDeploy": {"id": "53", "environment": "production", "dateStarted": None,
                        "dateFinished": "2026-11-01T09:20:00.000000Z", "name": None, "url": None},
         "authors": [], "projects": proyectos("api", "web"), "firstEvent": "2026-11-01T09:12:00.000000Z",
         "lastEvent": "2026-11-22T15:37:49.873000Z", "currentProjectMeta": {}, "userAgent": None},
        {"id": 290, "version": "api@0.9.0", "shortVersion": "0.9.0", "status": "open",
         "ref": None, "url": None, "dateCreated": "2026-09-15T12:00:00.000000Z", "dateReleased": None,
         "data": {}, "newGroups": 1, "owner": None, "commitCount": 0, "lastCommit": None, "deployCount": 0,
         "lastDeploy": None, "authors": [], "projects": proyectos("api"),
         "firstEvent": "2026-09-15T12:05:00.000000Z", "lastEvent": "2026-10-30T18:00:00.000000Z",
         "currentProjectMeta": {}, "userAgent": None},
    ]
    (SALIDA / "releases.json").write_text(json.dumps(releases, indent=1) + "\n")
    (SALIDA / "deploys.json").write_text(json.dumps({
        "fixture-release-1": [
            {"id": "52", "environment": "staging", "dateStarted": "2026-11-01T09:05:00.000000Z",
             "dateFinished": "2026-11-01T09:06:00.000000Z", "name": "fixture-release-1 a staging", "url": None},
            {"id": "53", "environment": "production", "dateStarted": None,
             "dateFinished": "2026-11-01T09:20:00.000000Z", "name": None, "url": None},
        ],
        "api@0.9.0": [],
    }, indent=1) + "\n")


if __name__ == "__main__":
    main()
