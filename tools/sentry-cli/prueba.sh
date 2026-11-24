#!/usr/bin/env bash
# Prueba de punta a punta con sentry-cli de verdad: arranca mothlog
# contra DATABASE_URL (una base vacía), sube el build de la página de
# ../fixtures/navegador de tres formas y comprueba que el error del navegador
# sale traducido a src/carrito.ts:7 en cada una.
#
#   upload:  sourcemaps upload --release, con SOURCEMAP_UPLOAD_TOKEN
#   inject:  sourcemaps inject y upload sin release, con un token de la API
#   antiguo: sentry-cli 1.77.3, releases files upload-sourcemaps
#
# Después marca el error para la siguiente versión y crea, finaliza y
# despliega otra release con releases new, releases finalize y deploys new:
# el error tiene que quedar resuelto con ella.
#
# Lo que manda sentry-cli queda en trabajo/subidas/<caso>/ y los envíos del
# navegador en trabajo/envios-<caso>/, para copiarlos a testdata/.
#
# uso: DATABASE_URL=postgres://... ./prueba.sh
set -euo pipefail

: "${DATABASE_URL:?falta DATABASE_URL}"
cd "$(dirname "$0")"
aqui=$PWD
raiz=$(cd ../.. && pwd)
navegador=$raiz/tools/fixtures/navegador
trabajo=$aqui/trabajo
puerto=${PUERTO:-18480}
intake=http://127.0.0.1:$puerto
cli=$aqui/node_modules/.bin/sentry-cli

rm -rf "$trabajo"
mkdir -p "$trabajo"
[ -d node_modules ] || npm ci --no-fund --no-audit
[ -d "$navegador/node_modules" ] || (cd "$navegador" && npm ci --no-fund --no-audit)

go build -o "$trabajo/mothlog" "$raiz/cmd/mothlog"
export PUBLIC_URL=$intake LISTEN_ADDR=127.0.0.1:$puerto
SOURCEMAP_UPLOAD_TOKEN=$(head -c 24 /dev/urandom | base64 | tr -d '/+=')
export SOURCEMAP_UPLOAD_TOKEN

# api y web son el 1 y el 2, como en testdata/envelopes; la prueba usa front, el 3.
"$trabajo/mothlog" projects create api >/dev/null
"$trabajo/mothlog" projects create web >/dev/null
clave=$("$trabajo/mothlog" projects create front | sed -E 's#^https?://([^@]+)@.*/([0-9]+)$#\1 \2#')
id=${clave#* }
clave=${clave% *}
api_token=$("$trabajo/mothlog" tokens create prueba-sentry-cli | head -n 1)

"$trabajo/mothlog" >"$trabajo/servidor.log" 2>&1 &
servidor=$!
trap 'kill $servidor 2>/dev/null || true' EXIT
for _ in $(seq 50); do
  curl -sf "$intake/healthz" >/dev/null && break
  sleep 0.2
done

export SENTRY_ORG=cualquiera SENTRY_PROJECT=front
grabar() { # <caso> <orden...>: la lanza con SENTRY_URL en un proxy que graba
  local caso=$1 proxy estado=0
  shift
  node proxy.mjs $((puerto + 1)) "$intake" "$trabajo/subidas/$caso" &
  proxy=$!
  for _ in $(seq 50); do
    curl -s "http://127.0.0.1:$((puerto + 1))/healthz" >/dev/null && break
    sleep 0.1
  done
  rm -f "$trabajo/subidas/$caso"/*
  SENTRY_URL=http://127.0.0.1:$((puerto + 1)) "$@" || estado=$?
  kill $proxy
  wait $proxy 2>/dev/null || true
  return $estado
}

build() { # <caso>: deja en trabajo/<caso> el build con release prueba-<caso>
  (cd "$navegador" && node build.mjs "https://$clave@errors.example.com/$id" "prueba-$1" >/dev/null)
  mv "$navegador/dist" "$trabajo/$1"
}

comprobar() { # <caso>: manda el error y mira el último fotograma del evento guardado
  node navegador.mjs "$trabajo/$1" "$intake" "$trabajo/envios-$1"
  local env evento
  env=$(grep -l '"type":"event"' "$trabajo/envios-$1"/*.envelope | head -n 1)
  evento=$(head -n 1 "$env" | python3 -c 'import json,sys; print(json.load(sys.stdin)["event_id"])')
  curl -sf -H "Authorization: Bearer $api_token" "$intake/api/0/events/$evento/" >"$trabajo/evento-$1.json"
  python3 - "$1" "$trabajo/evento-$1.json" <<'EOF'
import json, sys
caso, fichero = sys.argv[1], sys.argv[2]
ev = json.load(open(fichero))["event"]
f = ev["exceptions"][-1]["frames"][-1]
min_ = f.get("minified") or {}
print(f"{caso}: release={ev.get('release')} debug_meta={'sí' if ev.get('debug_meta') else 'no'} "
      f"{min_.get('filename', '?')}:{min_.get('lineno', '?')}:{min_.get('colno', '?')} -> "
      f"{f['filename']}:{f['lineno']}:{f['colno']}  {f.get('context_line', '').strip()}")
ok = f["filename"] == "src/carrito.ts" and f["lineno"] == 7
print(f"{caso}: {'OK' if ok else 'MAL'}")
sys.exit(0 if ok else 1)
EOF
}

echo "== upload"
build upload
if SENTRY_AUTH_TOKEN=no-vale grabar token-malo "$cli" sourcemaps upload --release prueba-upload "$trabajo/upload" >"$trabajo/token-malo.log" 2>&1; then
  echo "con un token malo se aceptó la subida"; exit 1
fi
echo "token malo rechazado:"
grep -i "error" "$trabajo/token-malo.log" | head -n 3
SENTRY_AUTH_TOKEN=$SOURCEMAP_UPLOAD_TOKEN grabar upload "$cli" sourcemaps upload --release prueba-upload "$trabajo/upload"
comprobar upload

echo "== inject"
build inject
"$cli" sourcemaps inject "$trabajo/inject"
SENTRY_AUTH_TOKEN=$api_token grabar inject "$cli" sourcemaps upload "$trabajo/inject"
comprobar inject

echo "== antiguo (sentry-cli 1.77.3)"
build antiguo
SENTRY_AUTH_TOKEN=$SOURCEMAP_UPLOAD_TOKEN grabar antiguo npm exec --yes --package=@sentry/cli@1.77.3 -- \
  sentry-cli releases files prueba-antiguo upload-sourcemaps "$trabajo/antiguo"
comprobar antiguo

echo "== releases y despliegues"
issue=$(curl -sf -H "Authorization: Bearer $api_token" "$intake/api/0/issues/?project=front" |
  python3 -c 'import json,sys; d=json.load(sys.stdin)["data"]; assert len(d)==1, d; print(d[0]["id"])')
curl -sf -H "Authorization: Bearer $api_token" -H 'Content-Type: application/json' \
  -d '{"status":"resolved_next"}' "$intake/api/0/issues/$issue/status" >/dev/null
SENTRY_AUTH_TOKEN=$api_token grabar releases sh -c \
  '"$0" releases new prueba-releases && "$0" releases finalize prueba-releases && "$0" deploys new -r prueba-releases -e production' "$cli"
curl -sf -H "Authorization: Bearer $api_token" "$intake/api/0/projects/$id/releases/prueba-releases/" >"$trabajo/release.json"
curl -sf -H "Authorization: Bearer $api_token" "$intake/api/0/issues/$issue/" >"$trabajo/issue-resuelto.json"
python3 - "$trabajo/release.json" "$trabajo/issue-resuelto.json" <<'EOF'
import json, sys
rel, issue = json.load(open(sys.argv[1])), json.load(open(sys.argv[2]))
deploys = [d["environment"] for d in rel["deploys"]]
print(f"release {rel['version']}: finalizada={rel['finalized_at'] is not None} despliegues={deploys} "
      f"dejaron de salir={[i['id'] for i in rel['stopped']]}")
print(f"issue {issue['id']}: {issue['status']}, resuelto con {issue['fixed_in_release']}")
ok = (rel["finalized_at"] and deploys == ["production"] and issue["status"] == "resolved"
      and issue["fixed_in_release"] == "prueba-releases" and [i["id"] for i in rel["stopped"]] == [issue["id"]])
print(f"releases: {'OK' if ok else 'MAL'}")
sys.exit(0 if ok else 1)
EOF

echo "== todo OK"
