#!/usr/bin/env bash
#
# local-bus.sh — recorrido end-to-end de jiku-docs POR EL BUS, con el stack local levantado.
#
# Habla con el módulo como lo hacen personas y agentes: el CLI `jiku` de jiku-go, con tres
# identidades: `docs` (el machine user del servicio jiku-docs, clase `service`: lee y escribe todo;
# hace el bootstrap y lo que necesita `manage`), `user` (una persona con rol user) y `external`
# (una persona con rol external-user). Las personas se autentican con el device flow (`jiku login`)
# en un JIKU_CONFIG_DIR propio, una vez; ver test/e2e/README.md. No usa la api HTTP salvo la ruta
# pública /p/docs/* de los links compartidos. Todo lo que crea vive bajo una carpeta nueva por
# corrida (`/e2e-<timestamp>/`), así que se puede repetir sin limpiar.
#
# Requisitos: el stack de Jiku (`deploy/local.sh up`) y jiku-docs (`docker compose -f
# docker-compose.local.yml up -d`) levantados, `bin/jiku` de jiku-go compilado, python3, curl.
#
# Uso:
#   test/e2e/local-bus.sh                 # recorrido completo, sin cortar el servicio
#   E2E_DISRUPTIVE=1 test/e2e/local-bus.sh   # además: docker stop/start y deregister del módulo
#
# Variables (todas con default local; ninguna lleva secretos, solo rutas a ellos):
#   JIKU_REPO          raíz del monorepo de jiku              (default: ../../jiku desde este repo)
#   JIKU_GO_REPO       raíz de jiku-go                       (default: ../../jiku-go)
#   JIKU_BIN           binario del CLI                       (default: $JIKU_GO_REPO/bin/jiku)
#   JIKU_ENV_FILE      .env del deploy de jiku, para el issuer y el project id de Zitadel
#                      (default: $JIKU_REPO/deploy/.env; JIKU_ISSUER y JIKU_PROJECT_ID lo pisan)
#   JIKU_SERVERS       NATS                                  (default: nats://localhost:4222)
#   JIKU_INSTANCE      instancia                             (default: dev)
#   JIKU_CREDS         creds del sentinel   (default: $JIKU_REPO/deploy/nats/creds/sentinel-client.creds)
#   E2E_DOCS_KEY       key del machine user del servicio (rol docs; default: .local/jiku_docs.service_user.json)
#   E2E_USER_CONFIG_DIR / E2E_EXTERNAL_CONFIG_DIR
#                      JIKU_CONFIG_DIR de cada persona, con el token del device flow ya obtenido
#                      (`jiku login`; default: .local/cli/human-user y .local/cli/human-external)
#   E2E_USER_KEY / E2E_EXTERNAL_KEY
#                      alternativa: key file de un machine user con ese rol (pisa el config dir)
#   JIKU_CLIENT_ID     app nativa del device flow, para refrescar el token de las personas
#                      (default: zitadel.client_id de ~/.config/jiku/config.<instancia>.yaml)
#   E2E_USER_ID        user-id del rol user, para la regla de ACL por usuario
#   E2E_EXTERNAL_ID    user-id del external-user, para darle project-permissions
#                      (defaults: el `subject` de `jiku whoami` de cada identidad)
#   E2E_PUBLIC_URL     base de los links compartidos         (default: http://127.0.0.1:3100)
#   E2E_JIKU_DB        contenedor Postgres de jiku           (default: jiku-local-database)
#   E2E_DOCS_DB        contenedor Postgres de jiku-docs      (default: jiku-docs-local-database)
#   E2E_DOCS_CONTAINER contenedor del servicio               (default: jiku-docs-local)
#   E2E_DISRUPTIVE     1 = probar módulo caído y deregister (default: 0)
#   E2E_KEEP           1 = no borrar el directorio temporal al terminar
#   E2E_KEEP_VISIBLE   1 = no quitar al final las reglas que dan acceso a la carpeta de la corrida
#   E2E_CLEAN_GRANT    1 = al terminar quita el project-permissions del externo si no lo tenía
#                      antes (default: 1; si ya lo tenía, siempre se le devuelve)
#
# Sale con 0 si todo pasó, 1 si algo falló. El resumen final lista cada falla.

set -uo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT=$(cd "$HERE/../.." && pwd)
JIKU_REPO=${JIKU_REPO:-$ROOT/../../jiku}
JIKU_GO_REPO=${JIKU_GO_REPO:-$ROOT/../../jiku-go}
JIKU_BIN=${JIKU_BIN:-$JIKU_GO_REPO/bin/jiku}
JIKU_ENV_FILE=${JIKU_ENV_FILE:-$JIKU_REPO/deploy/.env}
E2E_DOCS_KEY=${E2E_DOCS_KEY:-$ROOT/.local/jiku_docs.service_user.json}
E2E_USER_CONFIG_DIR=${E2E_USER_CONFIG_DIR:-$ROOT/.local/cli/human-user}
E2E_EXTERNAL_CONFIG_DIR=${E2E_EXTERNAL_CONFIG_DIR:-$ROOT/.local/cli/human-external}
E2E_USER_KEY=${E2E_USER_KEY:-}
E2E_EXTERNAL_KEY=${E2E_EXTERNAL_KEY:-}
E2E_PUBLIC_URL=${E2E_PUBLIC_URL:-http://127.0.0.1:3100}
E2E_JIKU_DB=${E2E_JIKU_DB:-jiku-local-database}
E2E_DOCS_DB=${E2E_DOCS_DB:-jiku-docs-local-database}
E2E_DOCS_CONTAINER=${E2E_DOCS_CONTAINER:-jiku-docs-local}
E2E_DISRUPTIVE=${E2E_DISRUPTIVE:-0}
E2E_STORAGE_CONTAINER=${E2E_STORAGE_CONTAINER:-jiku-local-storage}
E2E_DOCS_BUCKET=${E2E_DOCS_BUCKET:-jiku-docs-content}

if [[ -z "${JIKU_ISSUER:-}" || -z "${JIKU_PROJECT_ID:-}" ]]; then
  [[ -f "$JIKU_ENV_FILE" ]] || { echo "falta $JIKU_ENV_FILE (o JIKU_ISSUER y JIKU_PROJECT_ID)" >&2; exit 2; }
  JIKU_ISSUER=${JIKU_ISSUER:-$(grep -E '^GESTION_ZITADEL_ISSUER_URL=' "$JIKU_ENV_FILE" | cut -d= -f2- | tr -d '"')}
  JIKU_PROJECT_ID=${JIKU_PROJECT_ID:-$(grep -E '^GESTION_ZITADEL_PROJECT_ID=' "$JIKU_ENV_FILE" | cut -d= -f2- | tr -d '"')}
fi
export JIKU_ISSUER JIKU_PROJECT_ID
export JIKU_SERVERS=${JIKU_SERVERS:-nats://localhost:4222}
export JIKU_INSTANCE=${JIKU_INSTANCE:-dev}
export JIKU_CREDS=${JIKU_CREDS:-$JIKU_REPO/deploy/nats/creds/sentinel-client.creds}
unset JIKU_VIA

if [[ -z "${JIKU_CLIENT_ID:-}" && -f "$HOME/.config/jiku/config.$JIKU_INSTANCE.yaml" ]]; then
  JIKU_CLIENT_ID=$(sed -n 's/^[[:space:]]*client_id:[[:space:]]*"\{0,1\}\([^"#]*\)"\{0,1\}.*/\1/p' "$HOME/.config/jiku/config.$JIKU_INSTANCE.yaml" | head -1)
fi
export JIKU_CLIENT_ID=${JIKU_CLIENT_ID:-}
unset JIKU_KEY_FILE JIKU_CONFIG_DIR

need=("$JIKU_BIN" "$JIKU_CREDS" "$E2E_DOCS_KEY")
for who in USER EXTERNAL; do
  k=E2E_${who}_KEY; d=E2E_${who}_CONFIG_DIR
  if [[ -n "${!k}" ]]; then need+=("${!k}")
  else
    need+=("${!d}/tokens-$JIKU_INSTANCE.json")
    [[ -n "$JIKU_CLIENT_ID" ]] || { echo "falta JIKU_CLIENT_ID (app nativa del device flow)" >&2; exit 2; }
  fi
done
for f in "${need[@]}"; do
  [[ -e "$f" ]] || { echo "no existe: $f (las personas: JIKU_CONFIG_DIR=<dir> jiku login; ver test/e2e/README.md)" >&2; exit 2; }
done
for bin in python3 curl docker; do command -v "$bin" >/dev/null || { echo "falta $bin" >&2; exit 2; }; done

TMP=$(mktemp -d "${TMPDIR:-/tmp}/jiku-docs-e2e.XXXXXX")
SYNC_PID=""
GRANT_PRE=""; GRANT_REMOVED=0
cleanup() {
  [[ -n "$SYNC_PID" ]] && kill "$SYNC_PID" 2>/dev/null
  # Si el externo tenía project-permissions antes de la corrida y se le quitó, devolvérselo.
  [[ "$GRANT_REMOVED" == 1 ]] && jikudb "INSERT INTO user_project_permissions(user_id, project_id) VALUES ('$E2E_EXTERNAL_ID', $PROJECT_ID) ON CONFLICT DO NOTHING" >/dev/null 2>&1
  if [[ "${E2E_KEEP:-0}" == 1 ]]; then echo "directorio temporal: $TMP"; else rm -rf "$TMP"; fi
}
trap cleanup EXIT

# ---- helpers ---------------------------------------------------------------------------------

PASS=0; FAILS=(); SKIPS=()
ok()   { PASS=$((PASS + 1)); printf '  \033[32mok\033[0m    %s\n' "$1"; }
bad()  { FAILS+=("$1"); printf '  \033[31mFALLA\033[0m %s\n' "$1"; [[ -n "${2:-}" ]] && printf '        %s\n' "$2" | head -5; }
skip() { SKIPS+=("$1"); printf '  \033[33mskip\033[0m  %s\n' "$1"; }
section() { printf '\n\033[1m== %s\033[0m\n' "$1"; }

# idenv <docs|user|external>: argumentos de `env` que ponen el CLI en esa identidad. `docs` usa la
# key del servicio; las personas, su JIKU_CONFIG_DIR con el token del device flow (o una key).
idenv() {
  case "$1" in
    docs) ID_ENV=(JIKU_KEY_FILE="$E2E_DOCS_KEY" JIKU_CONFIG_DIR="$TMP/cfg-docs") ;;
    user|external)
      local k=E2E_${1^^}_KEY d=E2E_${1^^}_CONFIG_DIR
      if [[ -n "${!k}" ]]; then ID_ENV=(JIKU_KEY_FILE="${!k}" JIKU_CONFIG_DIR="$TMP/cfg-$1")
      else ID_ENV=(-u JIKU_KEY_FILE JIKU_CONFIG_DIR="${!d}"); fi ;;
    *) echo "identidad desconocida: $1" >&2; return 2 ;;
  esac
}

# as <docs|user|external> <args de jiku...>: corre el CLI con esa identidad. stdout y stderr
# quedan en $OUT y $ERR; el código de salida en $RC.
as() {
  local who=$1; shift
  idenv "$who" || return 2
  OUT=$(env "${ID_ENV[@]}" "$JIKU_BIN" "$@" 2>"$TMP/stderr")
  RC=$?
  ERR=$(cat "$TMP/stderr")
}

# expect <descripción> <rc esperado> [texto que debe aparecer en stdout+stderr]
expect() {
  local what=$1 want=$2 needle=${3:-}
  if [[ "$RC" != "$want" ]]; then bad "$what" "rc=$RC (esperado $want): $(echo "$ERR" | grep -v '^$' | head -2)"; return 1; fi
  if [[ -n "$needle" ]] && ! grep -qF -- "$needle" <<<"$OUT$ERR"; then bad "$what" "no aparece \"$needle\": $(echo "$OUT$ERR" | head -3)"; return 1; fi
  ok "$what"
}

ulid() {
  python3 -c '
import os,time
A="0123456789ABCDEFGHJKMNPQRSTVWXYZ"
n=(int(time.time()*1000)<<80)|int.from_bytes(os.urandom(10),"big")
print("".join(A[(n>>(5*i))&31] for i in reversed(range(26))))'
}
json() { python3 -c "import json,sys; d=json.load(sys.stdin); print(eval(sys.argv[1]))" "$1"; }
docsdb() { docker exec "$E2E_DOCS_DB" psql -U jiku_docs -d jiku_docs -tAc "$1"; }
jikudb() { docker exec "$E2E_JIKU_DB" sh -c 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -tAc "$1"' _ "$1"; }
# until_ok <segundos> <comando...>: reintenta hasta que el comando salga 0.
until_ok() { local t=$1 s; shift; s=$(date +%s); until "$@"; do (( $(date +%s) - s > t )) && return 1; sleep 1; done; }
ms() { python3 -c 'import time; print(int(time.time()*1000))'; }

# ---- 0. registro --------------------------------------------------------------------------------

section "0. registro y estado"
# La primera request de una persona puede perder la carrera con su propio evento de autenticación
# (core crea o actualiza su fila de `users` con él): doctor hasta que autorice.
for who in docs user external; do
  doc_ok() { as "$1" doctor; [[ $RC == 0 ]]; }
  if until_ok 20 doc_ok "$who"; then ok "jiku doctor como $who"; else bad "jiku doctor como $who" "$(grep -E '✗|FAIL' <<<"$OUT$ERR" | head -2)"; fi
done
subject() { as "$1" whoami; awk '$1=="subject" && NF==2 {print $2}' <<<"$OUT"; }
E2E_USER_ID=${E2E_USER_ID:-$(subject user)}
E2E_EXTERNAL_ID=${E2E_EXTERNAL_ID:-$(subject external)}
[[ -n "$E2E_USER_ID" && -n "$E2E_EXTERNAL_ID" ]] || { echo "no se pudo leer el subject de user/external (jiku whoami)" >&2; exit 2; }
as docs docs status
expect "docs status: available" 0 "available"
as docs doctor
expect "jiku doctor: línea del módulo" 0 "docs       available"

# ---- datos reales de Jiku ------------------------------------------------------------------------

section "datos de Jiku (por el bus, como el servicio)"
# Un proyecto CON cliente y una tarea de un requisito de ese proyecto.
as docs query projects.list --all -o json
PROJECTS=$OUT
as docs query tasks.list --all -o json
read -r TASK_ID REQ_ID PROJECT_ID CLIENT_ID < <(python3 -c '
import json,sys
ps={p["id"]:p for p in json.loads(sys.argv[1]) if p.get("clientId")}
for t in json.load(sys.stdin):
    if t.get("requirementId") and t.get("projectId") in ps:
        print(t["id"], t["requirementId"], t["projectId"], ps[t["projectId"]]["clientId"]); break' "$PROJECTS" <<<"$OUT" 2>/dev/null)
if [[ -z "${TASK_ID:-}" ]]; then echo "no hay una tarea de un requisito de un proyecto con cliente en la base de Jiku" >&2; exit 2; fi
PROJECT_NAME=$(python3 -c 'import json,sys; print(next(p["name"] for p in json.loads(sys.argv[1]) if p["id"]==int(sys.argv[2])))' "$PROJECTS" "$PROJECT_ID")
as docs query clients.list --filter "id=$CLIENT_ID" -o json
CLIENT_NAME=$(json 'd[0]["name"]' <<<"$OUT" 2>/dev/null)
ok "cliente $CLIENT_ID ($CLIENT_NAME), proyecto $PROJECT_ID ($PROJECT_NAME), requisito $REQ_ID, tarea $TASK_ID"
slug() { python3 -c 'import re,sys,unicodedata as u; s=u.normalize("NFKD",sys.argv[1]).encode("ascii","ignore").decode().lower(); print(re.sub(r"[^a-z0-9]+","-",s).strip("-") or "x")' "$1"; }
CSLUG=$(slug "$CLIENT_NAME")
GRANT_PRE=$(jikudb "SELECT count(*) FROM user_project_permissions WHERE user_id = '$E2E_EXTERNAL_ID' AND project_id = $PROJECT_ID" 2>/dev/null)
if [[ "${GRANT_PRE:-0}" -ge 1 ]]; then
  # Se quita ahora para que la caché de permisos de jiku-docs (60 s) ya esté vencida en la sección 3.
  jikudb "DELETE FROM user_project_permissions WHERE user_id = '$E2E_EXTERNAL_ID' AND project_id = $PROJECT_ID" >/dev/null && GRANT_REMOVED=1
  ok "el externo ya tenía project-permissions sobre $PROJECT_ID: se le quitan durante la corrida"
fi
PSLUG=$(slug "$PROJECT_NAME")

# ---- 1. bootstrap --------------------------------------------------------------------------------

section "1. bootstrap: árbol, ACL, herencia"
RUN=e2e-$(date +%Y%m%d%H%M%S)
# documents.changes se lee desde aquí: la base puede tener miles de cambios previos (importaciones).
START_SEQ=$(docsdb "SELECT coalesce(max(seq),0) FROM changelog" 2>/dev/null || echo 0)
R=00000000000000000000000000
mkfolder() { # mkfolder <var> <parentId> <name> [json extra]
  local id; id=$(ulid)
  as docs docs cmd folders.new "{\"id\":\"$id\",\"parentId\":\"$2\",\"name\":\"$3\"${4:+,$4}}"
  if [[ $RC == 0 ]]; then printf -v "$1" '%s' "$id"; else bad "folders.new $3" "$ERR"; fi
}
mkfolder BASE "$R" "$RUN"
mkfolder INTERNO "$BASE" interno
mkfolder INFRA "$BASE" infra '"sharingBlocked":true,"externalBlocked":true'
mkfolder SRV "$INFRA" servidores '"sharingBlocked":false'
mkfolder CLIENTES "$BASE" clientes
mkfolder CLI "$CLIENTES" "$CSLUG"
mkfolder PRJ "$CLI" "$PSLUG"
mkfolder PRIV "$PRJ" privado '"inheritAcl":false'
P="/$RUN"; PP="$P/clientes/$CSLUG/$PSLUG"
[[ ${#FAILS[@]} == 0 ]] && ok "árbol $P/{interno,infra/servidores,clientes/.../privado}"
as docs docs cmd "folders.$BASE.acl.replace" '{"rules":[{"principalType":"role","principal":"admin","permission":"manage"}]}'
expect "ACL de la base: admin manage" 0
as docs docs cmd "folders.$CLIENTES.acl.replace" '{"rules":[{"principalType":"role","principal":"user","permission":"write"}]}'
expect "ACL /clientes/: user write" 0
as docs docs cmd "folders.$INFRA.acl.replace" '{"rules":[{"principalType":"role","principal":"user","permission":"read"}]}'
expect "ACL /infra/: user read" 0
as docs docs cmd "folders.$INTERNO.acl.replace" "{\"rules\":[{\"principalType\":\"user\",\"principal\":\"$E2E_USER_ID\",\"permission\":\"read\"}]}"
expect "ACL /interno/: read por usuario" 0
as docs docs cmd "folders.$PRIV.acl.replace" '{"rules":[{"principalType":"role","principal":"admin","permission":"manage"}]}'
expect "ACL privado (inheritAcl=false): solo rol admin (nadie de la corrida; el servicio lo ve igual)" 0

as user docs query folders.list --filter "pathPrefix=$P/" --include effectivePermission -o json
perm() { json "{f['path']:f.get('effectivePermission') for f in d}.get('$1','-')" <<<"$OUT"; }
[[ $(perm "$PP/") == write ]] && ok "herencia: user write en el proyecto" || bad "herencia write" "$(perm "$PP/")"
[[ $(perm "$P/infra/servidores/") == read ]] && ok "herencia: user read en /infra/servidores/" || bad "herencia read"
[[ $(perm "$P/interno/") == read ]] && ok "regla por usuario: read en /interno/" || bad "regla por usuario"
[[ $(perm "$PP/privado/") == - ]] && ok "inheritAcl=false: privado invisible para user" || bad "inheritAcl=false" "$(perm "$PP/privado/")"
as docs docs query folders.get --id "$SRV" -o json
[[ $(json 'd["sharingBlocked"]' <<<"$OUT") == True ]] && ok "sharingBlocked heredado (no se desactiva en una subcarpeta)" || bad "sharingBlocked heredado"
as user docs ls /
expect "user: ls / muestra las carpetas legibles de arriba" 0 "$P/"
as external docs query folders.list -o json
[[ "$OUT" == "[]" ]] && ok "external no ve carpetas" || bad "external ve carpetas" "$OUT"

# ---- 2. ciclo de documento -------------------------------------------------------------------

section "2. ciclo de documento como user"
cd "$TMP" || exit 2
cat > arq.md <<EOF
---
title: Arquitectura e2e
topics: [Arquitectura, Integración]
jiku:
  clients: [$CLIENT_ID]
  projects: [$PROJECT_ID]
  requirements: [$REQ_ID]
  tasks: [$TASK_ID]
---

# Arquitectura

La integración con aduana no aplica. Las grabaciones van al bucket de pruebas.
EOF
sed "s/requirements: \[$REQ_ID\]/requirements: [999999999]/" arq.md > bad-rel.md
as user docs new bad-rel.md --folder "$PP/"
expect "new con relación inexistente → invalid_relation" 1 "invalid_relation"
t0=$(ms); as user docs new arq.md --folder "$PP/" --message "Primera versión"; t1=$(ms)
expect "new con frontmatter real" 0 "version 1"
DOC=$(json 'd["id"]' <<<"$OUT")
as user docs new arq.md --folder "$PP/" --id "$DOC"
expect "new repetido con el mismo id → mismo documento" 0 "$DOC"
as user docs new arq.md --folder "$PP/" --name ARQ.md
expect "nombre repetido sin distinguir mayúsculas → already_exists" 1 "already_exists"
as user docs get "$DOC"
[[ $RC == 0 && "$OUT"$'\n' == "$(cat arq.md)"$'\n' ]] && ok "get devuelve el markdown tal cual" || bad "get markdown" "$ERR"
as user docs get "$PP/arq.md" -o json
[[ $(json 'd["version"]' <<<"$OUT") == 1 ]] && ok "get -o json" || bad "get -o json"
as user docs query documents.get --id "$DOC" --include relations --include topics -o json
[[ $(json 'sorted(d["topics"])' <<<"$OUT") == "['arquitectura', 'integracion']" ]] && ok "topics normalizados" || bad "topics" "$OUT"
[[ $(json 'd["relations"]["tasks"]' <<<"$OUT") == "[$TASK_ID]" ]] && ok "relaciones guardadas" || bad "relaciones" "$OUT"
cp arq.md v2.md; echo "Segunda versión." >> v2.md
as user docs put "$DOC" v2.md --base-version 1 --message "v2"
expect "put con --base-version" 0 "version 2"
cp v2.md v3.md; echo "Otra." >> v3.md
as user docs put "$DOC" v3.md --base-version 1
expect "put con base vieja → version_conflict" 1 "version_conflict"
as user docs put "$DOC" v2.md --base-version 1
expect "put idempotente (mismo contenido, base vieja) → misma versión" 0 "version 2"
as user docs history "$DOC" -o json
[[ $(json 'len(d)' <<<"$OUT") == 2 ]] && ok "history: 2 versiones" || bad "history" "$OUT"
printf '# Nota\n' > nota.md
as user docs new nota.md --folder "$PP/"; NOTA=$(json 'd["id"]' <<<"$OUT")
as user docs mv "$NOTA" "$PP/nota-renombrada.md" --base-version 1
expect "mv (renombrar)" 0 "nota-renombrada.md"
as user docs mv "$NOTA" "$P/infra/" --base-version 1
expect "mv a /infra/ (solo read) → permission_denied" 1 "permission_denied"
as user docs rm "$NOTA" --base-version 1
expect "rm" 0
as user docs rm "$NOTA" --base-version 1
expect "rm repetido → success" 0
as user docs get "$NOTA"
expect "get de un borrado lo marca" 0 "DELETED"
as user docs restore "$NOTA" --from-version 1 --base-version 1
expect "restore" 0 "version 2"
as user docs new nota.md --folder "$PP/" --name nota.txt
expect "nombre inválido → file_name_invalid" 1 "file_name_invalid"
printf -- '---\nvisibility: secreto\n---\nhola\n' > fm.md
as user docs new fm.md --folder "$PP/"
expect "frontmatter inválido → frontmatter_invalid" 1 "frontmatter_invalid"
printf -- '---\njiku:\n  foo: [1]\n---\nhola\n' > fm2.md
as user docs new fm2.md --folder "$PP/"
expect "clave desconocida en jiku: → frontmatter_invalid" 1 "jiku.foo"
{ echo "# Grande"; for i in $(seq 1 6000); do echo "Línea $i de relleno para superar el límite de contenido inline."; done; } > grande.md
as user docs new grande.md --folder "$PP/"
expect "contenido > 256 KiB por URL prefirmada" 0 "version 1"
as user docs get "$PP/grande.md"
[[ $RC == 0 && "$OUT"$'\n' == "$(cat grande.md)"$'\n' ]] && ok "get de > 256 KiB (contentUrl + checksum)" || bad "get grande"
{ echo "# Enorme"; head -c 6000000 /dev/zero | tr '\0' a; } > enorme.md
as user docs new enorme.md --folder "$PP/"
expect "contenido > 5 MiB → content_too_large" 1 "content_too_large"

# ---- 3. permisos ------------------------------------------------------------------------------

section "3. permisos"
cat > staging.md <<EOF
---
title: Servidor de staging e2e
visibility: public
topics: [infra]
jiku:
  projects: [$PROJECT_ID]
---

# Staging

Servidores de pruebas, puerto 2222.
EOF
as docs docs new staging.md --folder "$P/infra/servidores/"; STG=$(json 'd["id"]' <<<"$OUT")
expect "docs (servicio) crea un public en /infra/ (externalBlocked)" 0
printf '# Privado\n\nrenegociación del contrato\n' > priv.md
as docs docs new priv.md --folder "$PP/privado/"; PRIVDOC=$(json 'd["id"]' <<<"$OUT")
as user docs new nota.md --folder "$P/infra/"
expect "user escribe en /infra/ → permission_denied" 1 "permission_denied"
as user docs put "$STG" staging.md --base-version 1
expect "user put en /infra/ → permission_denied" 1 "permission_denied"
as user docs get "$PRIVDOC"
expect "user lee privado → document_not_found" 1 "document_not_found"
as user docs put "$PRIVDOC" priv.md --base-version 1
expect "user escribe privado → document_not_found" 1 "document_not_found"
sed 's/^title: \(.*\)/title: \1\nvisibility: public/' v2.md > pub.md
as user docs put "$DOC" pub.md --base-version 2
expect "user (write) marca public → permission_denied" 1 "permission_denied"
as docs docs put "$DOC" pub.md --base-version 2
expect "docs (servicio, manage) marca public" 0 "version 3"
# El externo de la corrida es una persona real que puede tener ya project-permissions sobre el
# proyecto (se le quitó al principio y se le devuelve al terminar). jiku-docs cachea los
# permisos por proyecto 60 s, así que se espera a que el cambio se vea.
ext_blind() { as external docs get "$DOC"; [[ $RC == 1 ]] && grep -q document_not_found <<<"$ERR"; }
if until_ok 90 ext_blind; then ok "external sin project-permissions no lo ve"; else bad "external sin project-permissions no lo ve" "rc=$RC $(head -1 <<<"$ERR")"; fi
if jikudb "INSERT INTO user_project_permissions(user_id, project_id) VALUES ('$E2E_EXTERNAL_ID', $PROJECT_ID) ON CONFLICT DO NOTHING" >/dev/null; then
  ext_sees() { as external docs get "$DOC"; [[ $RC == 0 ]]; }
  if until_ok 90 ext_sees; then ok "external con project-permissions ve el public (caché de 60 s)"; else bad "external no ve el public"; fi
  as external docs get "$DOC" --version 1
  expect "external no lee versiones anteriores (de cuando era internal)" 1 "document_not_found"
  as external docs get "$STG"
  expect "external no ve un public en /infra/ (externalBlocked)" 1 "document_not_found"
  as external docs query documents.list --filter "pathPrefix=$P/" -o json
  [[ $(json 'len(d)' <<<"$OUT") == 1 ]] && ok "external: documents.list solo el public vinculado" || bad "external list" "$OUT"
  as external docs search integración --filter "pathPrefix=$P/" -o json
  [[ $(json '[h["documentId"] for h in d]' <<<"$OUT") == "['$DOC']" ]] && ok "external: search respeta permisos" || bad "external search" "$OUT"
  as external docs put "$DOC" pub.md --base-version 3
  [[ $RC != 0 ]] && ok "external nunca escribe (el bus rechaza el plano de comandos)" || bad "external escribió"
else
  skip "project-permissions del externo (no se pudo escribir en $E2E_JIKU_DB)"
fi

# ---- 4. locks -----------------------------------------------------------------------------------

section "4. locks"
as user docs lock "$DOC" --ttl 60
expect "lock de la sesión A" 0
SESS=$(json 'd["sessionId"]' <<<"$OUT")
cp pub.md l.md; echo "con lock" >> l.md
as docs docs put "$DOC" l.md --base-version 3
expect "put de otra sesión → document_locked con holder" 1 "$E2E_USER_ID is editing it"
as user docs lock "$DOC" --session "$SESS" --ttl 120
expect "renovación con la misma sesión" 0 "$SESS"
as user docs put "$DOC" l.md --base-version 3 --session "$SESS"
expect "put con la sesión que tiene el lock" 0 "version 4"
as user docs unlock "$DOC" --session "$SESS" --force
expect "force sin manage → permission_denied" 1 "permission_denied"
as docs docs unlock "$DOC" --session "$SESS" --force
expect "force con manage" 0
as user docs lock "$DOC" --ttl 10
expect "ttl < 30 → invalid_fields" 1 "ttlSeconds"
as user docs lock "$DOC" --ttl 30
expect "lock de 30 s" 0
LOCKSEQ=$(docsdb "SELECT coalesce(max(seq),0) FROM changelog")
expired() { [[ $(docsdb "SELECT count(*) FROM changelog WHERE seq > $LOCKSEQ AND type = 'document.unlocked' AND entity_id = '$DOC' AND payload->'changes'->>'reason' = 'expired'") -ge 1 ]]; }
if until_ok 75 expired; then ok "vencimiento → document.unlocked (reason expired)"; else bad "el lock no venció"; fi

# ---- 5. búsqueda ---------------------------------------------------------------------------------

section "5. búsqueda y documents.list"
printf -- "---\ntitle: Integración con aduana\ntopics: [aduana]\njiku:\n  requirements: [$REQ_ID]\n---\n\n# Aduana\n\nLa integración con aduana usa el webservice. Prueba de carga pendiente.\n" > aduana.md
printf -- "---\ntitle: Reunión semanal\ntopics: [reuniones]\njiku:\n  tasks: [$TASK_ID]\n---\n\n# Reunión\n\nSe habló de la aduana y de los servidores.\n" > reunion.md
printf -- "---\ntitle: Transversal\n---\n\n# Transversal\n\nServidor compartido sin relación.\n" > trans.md
for f in aduana reunion trans; do as user docs new $f.md --folder "$PP/" >/dev/null; done
hits() { as user docs search "$@" --filter "pathPrefix=$P/" -o json; json 'sorted(h["title"] for h in d)' <<<"$OUT"; }
[[ $(hits '"integración con aduana"') == "['Arquitectura e2e', 'Integración con aduana']" ]] && ok "frase entre comillas" || bad "frase" "$(hits '"integración con aduana"')"
[[ $(hits 'aduana -prueba') == "['Reunión semanal']" ]] && ok "-palabra excluye (con stemming: pruebas)" || bad "-palabra" "$(hits 'aduana -prueba')"
[[ $(hits 'reunion') == "['Reunión semanal']" ]] && ok "sin acento encuentra con acento" || bad "acentos"
[[ $(hits 'servidor') == "['Reunión semanal', 'Servidor de staging e2e', 'Transversal']" ]] && ok "singular encuentra plural" || bad "plurales" "$(hits servidor)"
as user docs search aduana --filter topic=reuniones --filter "pathPrefix=$P/" -o json
[[ $(json '[h["title"] for h in d]' <<<"$OUT") == "['Reunión semanal']" ]] && ok "filtro topic" || bad "filtro topic"
as user docs search servidor --filter "projectId=$PROJECT_ID" --filter "pathPrefix=$P/" -o json
[[ $(json 'sorted(h["title"] for h in d)' <<<"$OUT") == "['Reunión semanal', 'Servidor de staging e2e']" ]] && ok "filtro projectId en search" || bad "projectId search" "$OUT"
as docs docs search renegociación --filter "pathPrefix=$P/" -o json; A=$(json 'len(d)' <<<"$OUT")
as user docs search renegociación --filter "pathPrefix=$P/" -o json; U=$(json 'len(d)' <<<"$OUT")
[[ "$A/$U" == 1/0 ]] && ok "search no devuelve lo que no se ve" || bad "search permisos" "$A/$U"
as user docs query documents.list --filter "projectId=$PROJECT_ID" --filter "pathPrefix=$P/" --fields title -o json
[[ $(json 'sorted(x["title"] for x in d)' <<<"$OUT") == "['Arquitectura e2e', 'Integración con aduana', 'Reunión semanal', 'Servidor de staging e2e']" ]] \
  && ok "documents.list projectId: directos, por requisito, por tarea y transversal de /infra/" || bad "documents.list projectId" "$OUT"

# ---- 6. adjuntos ---------------------------------------------------------------------------------

section "6. adjuntos (delegados en files.* de core)"
python3 -c "
import zlib,struct
def c(t,d): return struct.pack('>I',len(d))+t+d+struct.pack('>I',zlib.crc32(t+d)&0xffffffff)
raw=b''.join(b'\x00'+b'\xff\x00\x00'*8 for _ in range(8))
open('diagrama.png','wb').write(b'\x89PNG\r\n\x1a\n'+c(b'IHDR',struct.pack('>IIBBBBB',8,8,8,2,0,0,0))+c(b'IDAT',zlib.compress(raw))+c(b'IEND',b''))"
as user docs attach "$DOC" diagrama.png
expect "attach → link a la web" 0 "/docs/attachments/"
ATTLINK=$(tail -1 <<<"$OUT"); ATT=${ATTLINK##*/}
as user docs query attachments.list --filter "documentId=$DOC" -o json
[[ $(json '[a["id"] for a in d]' <<<"$OUT") == "['$ATT']" ]] && ok "attachments.list" || bad "attachments.list" "$OUT"
as user docs cmd "attachments.$ATT.request-download" '{}'
URL=$(json 'd["downloadUrl"]' <<<"$OUT")
curl -sf -o dl.png "$URL" && cmp -s dl.png diagrama.png && ok "request-download baja el archivo real" || bad "descarga del adjunto"
printf '#!/bin/sh\n' > x.sh
as user docs attach "$DOC" x.sh
expect "tipo no permitido → attachment_type_not_allowed" 1 "attachment_type_not_allowed"
as user docs attach "$STG" diagrama.png
expect "adjuntar sin write → permission_denied" 1 "permission_denied"

# ---- 7. links compartidos -----------------------------------------------------------------------

section "7. links compartidos"
as user docs share "$DOC"
expect "share sin manage → permission_denied" 1 "permission_denied"
as docs docs get "$DOC" -o json; V=$(json 'd["version"]' <<<"$OUT")
cp l.md s.md; printf '\n![diagrama](%s)\n\n<script>alert(1)</script>\n' "$ATTLINK" >> s.md
as docs docs put "$DOC" s.md --base-version "$V" >/dev/null
as docs docs share "$DOC" --expires 2d --note e2e
expect "share como docs (servicio)" 0
SHARE_URL=$(head -1 <<<"$OUT"); SHARE_ID=$(grep -o 'shared link [0-9A-Z]*' <<<"$ERR" | awk '{print $3}')
[[ "$SHARE_URL" == "$E2E_PUBLIC_URL/p/docs/jds_"* ]] && ok "url {JIKU_PUBLIC_URL}/p/docs/jds_…" || bad "url del link" "${SHARE_URL%%jds_*}"
H=$(curl -s -D - -o page.html "$SHARE_URL")
grep -q '^HTTP/1.1 200' <<<"$H" && ok "GET del link → 200" || bad "GET del link" "$(head -1 <<<"$H")"
for h in "Content-Security-Policy: default-src 'none'" "X-Robots-Tag: noindex" "Referrer-Policy: no-referrer" "Cache-Control: private, no-store"; do
  grep -qiF "$h" <<<"$H" && ok "cabecera $h" || bad "falta cabecera $h"
done
grep -q "script-src\|'unsafe-eval'" <<<"$(grep -i '^content-security-policy' <<<"$H")" && bad "la CSP permite scripts" || ok "CSP sin scripts"
! grep -q 'jiku:\|<script' page.html && ok "sin frontmatter ni <script> en el HTML" || bad "frontmatter o script en el HTML"
IMG=$(grep -o '/p/docs/jds_[^"]*/attachments/[0-9A-Z]*' page.html | head -1)
[[ -n "$IMG" ]] && [[ $(curl -s -o /dev/null -w '%{http_code}' "$E2E_PUBLIC_URL$IMG") == 302 ]] && ok "adjunto del link → 302 a URL prefirmada" || bad "adjunto por el link"
as docs docs shares "$DOC" -o json
grep -q 'jds_' <<<"$OUT" && bad "shares devuelve el token" || ok "shares lista sin token"
as docs docs revoke "$SHARE_ID"
expect "revoke" 0
[[ $(curl -s -o /dev/null -w '%{http_code}' "$SHARE_URL") == 404 ]] && ok "link revocado → 404" || bad "revocado no da 404"
as docs docs share "$STG"
expect "share en /infra/ → sharing_blocked" 1 "sharing_blocked"
as docs docs share "$DOC" --expires 200d
expect "vencimiento > 180 días → share_expiry_invalid" 1 "share_expiry_invalid"
printf '# Accesos\n\npassword: Sup3rS3creta!\n' > sec.md
JIKU_VIA=agent as docs docs new sec.md --folder "$PP/"
expect "posible secreto → aviso, no bloquea" 0 "possible_secret_detected"
as docs docs share "$PP/sec.md"
expect "share de un doc con secreto exige --ack-secret-warning" 1 "secret_warning_not_acknowledged"
as docs docs share "$PP/trans.md" --expires 1d; TURL=$(head -1 <<<"$OUT")
as docs docs get "$PP/trans.md" -o json; TV=$(json 'd["version"]' <<<"$OUT")
as docs docs rm "$PP/trans.md" --base-version "$TV" >/dev/null
[[ $(curl -s -o /dev/null -w '%{http_code}' "$TURL") == 404 ]] && ok "borrar el documento revoca sus links" || bad "link sobrevive al borrado"

# ---- 8. eventos y sync ---------------------------------------------------------------------------

section "8. eventos, feed y sync"
if UNPUB=$(docsdb "SELECT count(*) FROM changelog WHERE published_at IS NULL AND occurred_at < now() - interval '10 seconds'"); then
  [[ "$UNPUB" == 0 ]] && ok "outbox: todo publicado (published_at)" || bad "outbox: $UNPUB eventos sin publicar"
  LEAK=$(docsdb "SELECT count(*) FROM changelog WHERE payload::text LIKE '%grabaciones van al bucket%' OR payload::text LIKE '%jds\_%'")
  [[ "$LEAK" == 0 ]] && ok "ningún evento lleva contenido ni tokens" || bad "eventos con contenido o tokens: $LEAK"
else
  skip "changelog (sin acceso a $E2E_DOCS_DB)"
fi
as user docs query documents.changes --payload "{\"sinceSeq\":${START_SEQ:-0},\"limit\":500}" -o json
grep -q "$DOC" <<<"$OUT" && ! grep -q "$PRIVDOC" <<<"$OUT" && ok "documents.changes filtrado por permisos" || bad "documents.changes"
SYNC=$TMP/sync
as user docs sync "$SYNC" --once --only "$P/"
expect "sync --once" 0
[[ -f "$SYNC$PP/arq.md" && ! -w "$SYNC$P/infra/servidores/staging.md" ]] && ok "sync baja los documentos; /infra/ queda de solo lectura" || bad "sync --once: archivos"
grep -q 'deleted in Jiku' <<<"$ERR" && bad "sync aplicó un borrado ya restaurado" || ok "sync no reaplica borrados restaurados"
idenv user; env "${ID_ENV[@]}" "$JIKU_BIN" docs sync "$SYNC" --only "$P/" 2>"$TMP/sync.log" &
SYNC_PID=$!
until_ok 20 grep -q 'online' "$TMP/sync.log" || bad "el daemon no quedó online"
as docs docs get "$PP/reunion.md" -o json; RV=$(json 'd["version"]' <<<"$OUT")
cp reunion.md r.md; echo "Cambio remoto del servicio." >> r.md
as docs docs put "$PP/reunion.md" r.md --base-version "$RV" >/dev/null; t0=$(ms)
if until_ok 15 grep -q 'Cambio remoto del servicio' "$SYNC$PP/reunion.md"; then ok "cambio remoto → archivo local en $(( $(ms) - t0 )) ms"; else bad "el cambio remoto no llegó"; fi
echo "Cambio local." >> "$SYNC$PP/aduana.md"; t0=$(ms)
remote_has() { as user docs get "$PP/aduana.md"; grep -q 'Cambio local' <<<"$OUT"; }
if until_ok 20 remote_has; then ok "cambio local → subido en $(( $(ms) - t0 )) ms"; else bad "el cambio local no subió"; fi
kill "$SYNC_PID"; wait "$SYNC_PID" 2>/dev/null; SYNC_PID=""
echo "Edición local offline." >> "$SYNC$PP/aduana.md"
as docs docs get "$PP/aduana.md" -o json; AV=$(json 'd["version"]' <<<"$OUT"); json 'd["content"]' <<<"$OUT" > ad.md
echo "Edición remota concurrente." >> ad.md
as docs docs put "$PP/aduana.md" ad.md --base-version "$AV" >/dev/null
as user docs sync "$SYNC" --once --only "$P/"
ls "$SYNC$PP/" | grep -q 'aduana.remoto-v' && ok "conflicto → .remoto-vN.md" || bad "conflicto sin copia remota" "$ERR"

# ---- 8b. compactación del historial ---------------------------------------------------------------

# Documento con siete guardados seguidos (uno con mensaje); se envejecen en la base todos menos el
# actual y se compacta a demanda dentro del contenedor (docs/compactacion-historial-v1.md).
section "8b. compactación del historial"
if docsdb "SELECT 1" >/dev/null 2>&1; then
  cp arq.md h.md
  as user docs new h.md --folder "$PP/" --name historial.md
  expect "documento para compactar" 0 "version 1"
  HDOC=$(json 'd["id"]' <<<"$OUT")
  for i in 1 2 3 4 5 6 7; do
    echo "Guardado $i." >> h.md
    if [[ $i == 3 ]]; then as user docs put "$HDOC" h.md --base-version "$i" --message "Hito e2e"
    else as user docs put "$HDOC" h.md --base-version "$i"; fi
    [[ $RC == 0 ]] || bad "put $i del historial" "$ERR"
  done
  docsdb "UPDATE document_versions SET created_at = created_at - interval '3 days' WHERE document_id = '$HDOC' AND version < 8" >/dev/null
  [[ $(docsdb "SELECT string_agg(kind || ':' || protected, ',' ORDER BY version) FROM document_versions WHERE document_id = '$HDOC'") == \
    "create:true,update:false,update:false,update:true,update:false,update:false,update:false,update:false" ]] \
    && ok "kind y protected al escribir (create, mensaje)" || bad "kind/protected al escribir"
  DRY=$(docker exec "$E2E_DOCS_CONTAINER" jiku-docs history compact --document "$HDOC" --dry-run 2>/dev/null)
  grep -q 'versionsPruned=4 ' <<<"$DRY" && [[ $(docsdb "SELECT count(*) FROM document_versions WHERE document_id = '$HDOC'") == 8 ]] \
    && ok "history compact --dry-run informa 4 y no borra" || bad "dry-run" "$DRY"
  HKEY3=$(docsdb "SELECT storage_key FROM document_versions WHERE document_id = '$HDOC' AND version = 3")
  RUN=$(docker exec "$E2E_DOCS_CONTAINER" jiku-docs history compact --document "$HDOC" 2>/dev/null)
  [[ $(docsdb "SELECT string_agg(version::text, ',' ORDER BY version) FROM document_versions WHERE document_id = '$HDOC'") == "1,4,7,8" ]] \
    && ok "compactación: quedan la creación, la del mensaje, la última del bloque y la actual" || bad "compactación" "$RUN"
  [[ $(docsdb "SELECT count(*) FROM pruned_objects WHERE document_id = '$HDOC'") == 0 ]] && grep -q 'objectsTrashed=4 ' <<<"$RUN" \
    && ok "objetos movidos a la papelera (pruned_objects vacío, objectsTrashed=4)" || bad "pruned_objects con filas" "$RUN"
  # Los cuatro objetos compactados quedaron en papelera/ y nada de ellos bajo docs/ (con el mc del
  # MinIO de Jiku y sus credenciales de root, dentro del contenedor).
  if docker exec "$E2E_STORAGE_CONTAINER" sh -c 'mc alias set e2e http://127.0.0.1:9000 "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD"' >/dev/null 2>&1; then
    s3count() { docker exec "$E2E_STORAGE_CONTAINER" mc ls --recursive "e2e/$E2E_DOCS_BUCKET/$1" 2>/dev/null | grep -c '\.md$'; }
    [[ $(s3count "papelera/docs/$HDOC/") == 4 && $(s3count "docs/$HDOC/") == 4 ]] \
      && ok "papelera/: los 4 objetos compactados están en papelera/docs/<id>/ y bajo docs/<id>/ quedan solo los 4 conservados" \
      || bad "papelera/" "papelera=$(s3count "papelera/docs/$HDOC/") docs=$(s3count "docs/$HDOC/")"
  else
    skip "papelera/ por S3 (sin mc en $E2E_STORAGE_CONTAINER)"
  fi
  as user docs query documents.get --id "$HDOC" -o json
  [[ $(json 'd["prunedVersionsCount"]' <<<"$OUT") == 4 ]] && ok "documents.get: prunedVersionsCount" || bad "prunedVersionsCount" "$OUT"
  as user docs get "$HDOC" --version 3
  expect "get de una versión compactada → version_pruned (sale con 5)" 5 "version_pruned"
  as user docs restore "$HDOC" --from-version 2 --base-version 8
  expect "restore desde una versión compactada → version_pruned (sale con 5)" 5 "version_pruned"
  as docs docs cmd shares.new "{\"id\":\"$(ulid)\",\"documentId\":\"$HDOC\",\"mode\":\"pinned\",\"version\":5,\"expiresAt\":\"$(date -u -d '+1 day' +%Y-%m-%dT%H:%M:%S.000Z)\",\"includeAttachments\":false}"
  [[ $RC != 0 ]] && grep -q version_pruned <<<"$OUT$ERR" && ok "shares.new pinned sobre una versión compactada → version_pruned" \
    || bad "shares.new pinned sobre una versión compactada" "$ERR"
  as user docs history "$HDOC" -o json
  grep -q '"protected"' <<<"$OUT" && grep -q '"kind"' <<<"$OUT" && ok "history muestra kind y protected" || bad "history sin kind/protected" "$OUT"
  RUN2=$(docker exec "$E2E_DOCS_CONTAINER" jiku-docs history compact --document "$HDOC" 2>/dev/null)
  grep -q 'versionsPruned=0 ' <<<"$RUN2" && ok "segunda corrida: no borra nada" || bad "segunda corrida" "$RUN2"
  # Recuperar la versión 3 desde la papelera: objeto y fila del historial (queda protegida).
  REST=$(docker exec "$E2E_DOCS_CONTAINER" jiku-docs storage restore-from-trash --key "$HKEY3" --version-row 2>/dev/null)
  grep -q '"objectRestored":true' <<<"$REST" && grep -q '"rowRestored":true' <<<"$REST" \
    && [[ $(docsdb "SELECT string_agg(version::text || ':' || protected, ',' ORDER BY version) FROM document_versions WHERE document_id = '$HDOC'") == \
      "1:true,3:true,4:true,7:false,8:false" ]] \
    && ok "storage restore-from-trash --version-row: vuelven el objeto y la versión 3, protegida" || bad "restore-from-trash" "$REST"
  as user docs get "$HDOC" --version 3
  expect "la versión recuperada se lee de nuevo" 0
  if M=$(curl -fsS "${E2E_DOCS_METRICS:-http://127.0.0.1:8090/metrics}" 2>/dev/null); then
    grep -q '^docs_history_pruned_objects_pending' <<<"$M" && ok "métricas docs_history_*" || bad "faltan métricas docs_history_*"
    grep -q '^# TYPE docs_trash_objects_moved_total' <<<"$M" && ok "métrica docs_trash_objects_moved_total" || bad "falta docs_trash_objects_moved_total"
  else
    skip "métricas (sin acceso a /metrics)"
  fi
else
  skip "compactación del historial (sin acceso a $E2E_DOCS_DB)"
fi

# ---- 9. módulo caído y deregister --------------------------------------------------------------

section "9. módulo caído y deregister"
if [[ "$E2E_DISRUPTIVE" == 1 ]]; then
  docker stop "$E2E_DOCS_CONTAINER" >/dev/null
  as user docs get "$DOC"
  expect "módulo caído: jiku docs get sale con 4" 4
  docker start "$E2E_DOCS_CONTAINER" >/dev/null
  healthy() { [[ $(docker inspect -f '{{.State.Health.Status}}' "$E2E_DOCS_CONTAINER") == healthy ]]; }
  until_ok 60 healthy
  as user docs get "$DOC"
  expect "vuelve a available" 0
  docker exec "$E2E_DOCS_CONTAINER" jiku-docs module deregister >/dev/null 2>&1
  as user docs status
  expect "deregister → not_installed (sale con 3)" 3 "not_installed"
  docker restart "$E2E_DOCS_CONTAINER" >/dev/null; until_ok 60 healthy
  as user docs status
  expect "reiniciar lo vuelve a registrar" 0 "available"
else
  skip "módulo caído y deregister (E2E_DISRUPTIVE=1 para correrlo)"
fi

# ---- 10. logs ------------------------------------------------------------------------------------

section "10. logs"
for c in "$E2E_DOCS_CONTAINER" jiku-local-core jiku-local-api jiku-local-auth-callout-1 jiku-local-nats; do
  docker inspect "$c" >/dev/null 2>&1 || { skip "logs de $c (no existe)"; continue; }
  L=$(docker logs --since 30m "$c" 2>&1)
  n=$(grep -cE 'grabaciones van al bucket|renegociaci|Sup3rS3creta|jds_[A-Za-z0-9_-]{20}|X-Amz-Signature=' <<<"$L")
  [[ "$n" == 0 ]] && ok "$c: sin contenido, tokens ni URLs prefirmadas" || bad "$c: $n líneas con contenido o secretos"
done
n=$(docker logs --since 30m "$E2E_DOCS_CONTAINER" 2>&1 | grep -c '"level":"ERROR"')
[[ "$n" == 0 ]] && ok "jiku-docs sin errores" || bad "jiku-docs: $n errores en el log"

# ---- limpieza --------------------------------------------------------------------------------------

# jiku-docs no borra carpetas: lo que crea la corrida queda. Para que no se mezcle con las carpetas
# reales en la web de las personas (la raíz muestra las carpetas legibles de más arriba, y cada
# corrida agregaría otro "clientes", "infra" e "interno"), se le quitan las reglas que daban acceso.
section "limpieza"
if [[ "${E2E_KEEP_VISIBLE:-0}" != 1 ]]; then
  for f in "${CLIENTES:-}" "${INFRA:-}" "${INTERNO:-}"; do
    [[ -n "$f" ]] && as docs docs cmd "folders.$f.acl.replace" '{"rules":[]}' && [[ $RC == 0 ]] || bad "quitar las reglas de $f" "$ERR"
  done
  as user docs query folders.list --filter "pathPrefix=$P/" -o json
  [[ "$OUT" == "[]" ]] && ok "la corrida $P/ quedó invisible para user (E2E_KEEP_VISIBLE=1 la deja)" || bad "user sigue viendo $P/" "$OUT"
else
  skip "limpieza (E2E_KEEP_VISIBLE=1)"
fi

# ---- resumen -------------------------------------------------------------------------------------

if [[ "$GRANT_REMOVED" == 1 ]]; then
  jikudb "INSERT INTO user_project_permissions(user_id, project_id) VALUES ('$E2E_EXTERNAL_ID', $PROJECT_ID) ON CONFLICT DO NOTHING" >/dev/null 2>&1 && GRANT_REMOVED=0
elif [[ "${E2E_CLEAN_GRANT:-1}" == 1 ]]; then
  jikudb "DELETE FROM user_project_permissions WHERE user_id = '$E2E_EXTERNAL_ID' AND project_id = $PROJECT_ID" >/dev/null 2>&1
fi
printf '\n\033[1m%d ok, %d fallas, %d salteados\033[0m  (todo quedó bajo %s/)\n' "$PASS" "${#FAILS[@]}" "${#SKIPS[@]}" "$P"
for f in "${FAILS[@]}"; do echo "  FALLA: $f"; done
[[ ${#FAILS[@]} == 0 ]]
