#!/usr/bin/env bash
#
# local-bus.sh — end-to-end run of jiku-docs OVER THE BUS, with the local stack up.
#
# Talks to the module the way persons and agents do: the `jiku` CLI from jiku-go, with three
# identities: `docs` (the machine user of the jiku-docs service, class `service`: reads and writes
# everything, restricted included; does the bootstrap and what is admin-only), `user` (a person
# with the user role) and `external` (a person with the external-user role). Checks the P-1 model
# (contract 1.3.0): permissions by visibility, restricted invisible to user and external through
# every path, generated location, relate, suggest and the new filters. The persons authenticate with the device flow (`jiku login`)
# in their own JIKU_CONFIG_DIR, once; see test/e2e/README.md. It does not use the HTTP api except the
# public path /p/docs/* of the share links. Everything it creates lives under a new folder per
# run (`/e2e-<timestamp>/`), so it can be repeated without cleaning up.
#
# Requirements: the Jiku stack (`deploy/local.sh up`) and jiku-docs (`docker compose -f
# docker-compose.local.yml up -d`) up, `bin/jiku` of jiku-go built, python3, curl.
#
# Usage:
#   test/e2e/local-bus.sh                 # full run, without stopping the service
#   E2E_DISRUPTIVE=1 test/e2e/local-bus.sh   # also: docker stop/start and deregister of the module
#
# Variables (all with a local default; none carries secrets, only paths to them):
#   JIKU_REPO          root of the jiku monorepo             (default: ../../jiku from this repo)
#   JIKU_GO_REPO       root of jiku-go                       (default: ../../jiku-go)
#   JIKU_BIN           CLI binary                            (default: $JIKU_GO_REPO/bin/jiku)
#   JIKU_ENV_FILE      .env of the jiku deploy, for the Zitadel issuer and project id
#                      (default: $JIKU_REPO/deploy/.env; JIKU_ISSUER and JIKU_PROJECT_ID override it)
#   JIKU_SERVERS       NATS                                  (default: nats://localhost:4222)
#   JIKU_INSTANCE      instance                              (default: dev)
#   JIKU_CREDS         sentinel creds       (default: $JIKU_REPO/deploy/nats/creds/sentinel-client.creds)
#   E2E_DOCS_KEY       key of the service machine user (docs role; default: .local/jiku_docs.service_user.json)
#   E2E_USER_CONFIG_DIR / E2E_EXTERNAL_CONFIG_DIR
#                      JIKU_CONFIG_DIR of each person, with the device-flow token already obtained
#                      (`jiku login`; default: .local/cli/human-user and .local/cli/human-external)
#   E2E_USER_KEY / E2E_EXTERNAL_KEY
#                      alternative: key file of a machine user with that role (overrides the config dir)
#   JIKU_CLIENT_ID     native app of the device flow, to refresh the persons' token
#                      (default: zitadel.client_id of ~/.config/jiku/config.<instance>.yaml)
#   E2E_USER_ID        user-id of the user role, for the per-user ACL rule
#   E2E_EXTERNAL_ID    user-id of the external-user, to grant it project-permissions
#                      (defaults: the `subject` of `jiku whoami` of each identity)
#   E2E_PUBLIC_URL     base of the share links               (default: http://127.0.0.1:3100)
#   E2E_JIKU_DB        Postgres container of jiku            (default: jiku-local-database)
#   E2E_DOCS_DB        Postgres container of jiku-docs       (default: jiku-docs-local-database)
#   E2E_DOCS_CONTAINER service container                     (default: jiku-docs-local)
#   E2E_DISRUPTIVE     1 = test the module down and deregister (default: 0)
#   E2E_KEEP           1 = do not delete the temporary directory at the end
#   E2E_KEEP_VISIBLE   1 = do not delete the documents of the run at the end
#   E2E_CLEAN_GRANT    1 = at the end remove the external person's project-permissions if it did not
#                      have them before (default: 1; if it already had them, they are always given back)
#
# Exits with 0 if everything passed, 1 if something failed. The final summary lists each failure.

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
  [[ -f "$JIKU_ENV_FILE" ]] || { echo "missing $JIKU_ENV_FILE (or JIKU_ISSUER and JIKU_PROJECT_ID)" >&2; exit 2; }
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
    [[ -n "$JIKU_CLIENT_ID" ]] || { echo "missing JIKU_CLIENT_ID (native app of the device flow)" >&2; exit 2; }
  fi
done
for f in "${need[@]}"; do
  [[ -e "$f" ]] || { echo "does not exist: $f (the persons: JIKU_CONFIG_DIR=<dir> jiku login; see test/e2e/README.md)" >&2; exit 2; }
done
for bin in python3 curl docker; do command -v "$bin" >/dev/null || { echo "missing $bin" >&2; exit 2; }; done

TMP=$(mktemp -d "${TMPDIR:-/tmp}/jiku-docs-e2e.XXXXXX")
SYNC_PID=""
GRANT_PRE=""; GRANT_REMOVED=0
cleanup() {
  [[ -n "$SYNC_PID" ]] && kill "$SYNC_PID" 2>/dev/null
  # If the external person had project-permissions before the run and they were removed, give them back.
  [[ "$GRANT_REMOVED" == 1 ]] && jikudb "INSERT INTO user_project_permissions(user_id, project_id) VALUES ('$E2E_EXTERNAL_ID', $PROJECT_ID) ON CONFLICT DO NOTHING" >/dev/null 2>&1
  if [[ "${E2E_KEEP:-0}" == 1 ]]; then echo "temporary directory: $TMP"; else rm -rf "$TMP"; fi
}
trap cleanup EXIT

# ---- helpers ---------------------------------------------------------------------------------

PASS=0; FAILS=(); SKIPS=()
ok()   { PASS=$((PASS + 1)); printf '  \033[32mok\033[0m    %s\n' "$1"; }
bad()  { FAILS+=("$1"); printf '  \033[31mFAIL\033[0m  %s\n' "$1"; [[ -n "${2:-}" ]] && printf '        %s\n' "$2" | head -5; }
skip() { SKIPS+=("$1"); printf '  \033[33mskip\033[0m  %s\n' "$1"; }
section() { printf '\n\033[1m== %s\033[0m\n' "$1"; }

# idenv <docs|user|external>: `env` arguments that put the CLI in that identity. `docs` uses the
# service key; the persons, their JIKU_CONFIG_DIR with the device-flow token (or a key).
idenv() {
  case "$1" in
    docs) ID_ENV=(JIKU_KEY_FILE="$E2E_DOCS_KEY" JIKU_CONFIG_DIR="$TMP/cfg-docs") ;;
    user|external)
      local k=E2E_${1^^}_KEY d=E2E_${1^^}_CONFIG_DIR
      if [[ -n "${!k}" ]]; then ID_ENV=(JIKU_KEY_FILE="${!k}" JIKU_CONFIG_DIR="$TMP/cfg-$1")
      else ID_ENV=(-u JIKU_KEY_FILE JIKU_CONFIG_DIR="${!d}"); fi ;;
    *) echo "unknown identity: $1" >&2; return 2 ;;
  esac
}

# as <docs|user|external> <jiku args...>: runs the CLI with that identity. stdout and stderr
# end up in $OUT and $ERR; the exit code in $RC.
as() {
  local who=$1; shift
  idenv "$who" || return 2
  OUT=$(env "${ID_ENV[@]}" "$JIKU_BIN" "$@" 2>"$TMP/stderr")
  RC=$?
  ERR=$(cat "$TMP/stderr")
}

# expect <description> <expected rc> [text that must appear in stdout+stderr]
expect() {
  local what=$1 want=$2 needle=${3:-}
  if [[ "$RC" != "$want" ]]; then bad "$what" "rc=$RC (expected $want): $(echo "$ERR" | grep -v '^$' | head -2)"; return 1; fi
  if [[ -n "$needle" ]] && ! grep -qF -- "$needle" <<<"$OUT$ERR"; then bad "$what" "\"$needle\" does not appear: $(echo "$OUT$ERR" | head -3)"; return 1; fi
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
# until_ok <seconds> <command...>: retries until the command exits 0.
until_ok() { local t=$1 s; shift; s=$(date +%s); until "$@"; do (( $(date +%s) - s > t )) && return 1; sleep 1; done; }
ms() { python3 -c 'import time; print(int(time.time()*1000))'; }

# ---- 0. registration ----------------------------------------------------------------------------

section "0. registration and status"
# The first request of a person may lose the race with its own authentication event
# (core creates or updates its `users` row with it): doctor until it authorizes.
for who in docs user external; do
  doc_ok() { as "$1" doctor; [[ $RC == 0 ]]; }
  if until_ok 20 doc_ok "$who"; then ok "jiku doctor as $who"; else bad "jiku doctor as $who" "$(grep -E '✗|FAIL' <<<"$OUT$ERR" | head -2)"; fi
done
subject() { as "$1" whoami; awk '$1=="subject" && NF==2 {print $2}' <<<"$OUT"; }
E2E_USER_ID=${E2E_USER_ID:-$(subject user)}
E2E_EXTERNAL_ID=${E2E_EXTERNAL_ID:-$(subject external)}
[[ -n "$E2E_USER_ID" && -n "$E2E_EXTERNAL_ID" ]] || { echo "could not read the subject of user/external (jiku whoami)" >&2; exit 2; }
as docs docs status
expect "docs status: available" 0 "available"
as docs doctor
expect "jiku doctor: module line" 0 "docs       available"

# ---- real Jiku data ------------------------------------------------------------------------------

section "Jiku data (over the bus, as the service)"
# A project WITH a client and a task of a requirement of that project.
as docs query projects.list --all -o json
PROJECTS=$OUT
as docs query tasks.list --all -o json
read -r TASK_ID REQ_ID PROJECT_ID CLIENT_ID < <(python3 -c '
import json,sys
ps={p["id"]:p for p in json.loads(sys.argv[1]) if p.get("clientId")}
for t in json.load(sys.stdin):
    if t.get("requirementId") and t.get("projectId") in ps:
        print(t["id"], t["requirementId"], t["projectId"], ps[t["projectId"]]["clientId"]); break' "$PROJECTS" <<<"$OUT" 2>/dev/null)
if [[ -z "${TASK_ID:-}" ]]; then echo "there is no task of a requirement of a project with a client in the Jiku database" >&2; exit 2; fi
PROJECT_NAME=$(python3 -c 'import json,sys; print(next(p["name"] for p in json.loads(sys.argv[1]) if p["id"]==int(sys.argv[2])))' "$PROJECTS" "$PROJECT_ID")
as docs query clients.list --filter "id=$CLIENT_ID" -o json
CLIENT_NAME=$(json 'd[0]["name"]' <<<"$OUT" 2>/dev/null)
ok "client $CLIENT_ID ($CLIENT_NAME), project $PROJECT_ID ($PROJECT_NAME), requirement $REQ_ID, task $TASK_ID"
slug() { python3 -c 'import re,sys,unicodedata as u; s=u.normalize("NFKD",sys.argv[1]).encode("ascii","ignore").decode().lower(); print(re.sub(r"[^a-z0-9]+","-",s).strip("-") or "x")' "$1"; }
CSLUG=$(slug "$CLIENT_NAME")
GRANT_PRE=$(jikudb "SELECT count(*) FROM user_project_permissions WHERE user_id = '$E2E_EXTERNAL_ID' AND project_id = $PROJECT_ID" 2>/dev/null)
if [[ "${GRANT_PRE:-0}" -ge 1 ]]; then
  # Removed now so that the jiku-docs permission cache (60 s) has already expired in section 3.
  jikudb "DELETE FROM user_project_permissions WHERE user_id = '$E2E_EXTERNAL_ID' AND project_id = $PROJECT_ID" >/dev/null && GRANT_REMOVED=1
  ok "the external person already had project-permissions on $PROJECT_ID: removed during the run"
fi
PSLUG=$(slug "$PROJECT_NAME")

# ---- 1. bootstrap --------------------------------------------------------------------------------

section "1. bootstrap: folders (they only organize, P-1)"
RUN=e2e-$(date +%Y%m%d%H%M%S)
# documents.changes is read from here: the database may hold thousands of earlier changes (imports).
START_SEQ=$(docsdb "SELECT coalesce(max(seq),0) FROM changelog" 2>/dev/null || echo 0)
R=00000000000000000000000000
mkfolder() { # mkfolder <var> <parentId> <name> [json extra]
  local id; id=$(ulid)
  as docs docs cmd folders.new "{\"id\":\"$id\",\"parentId\":\"$2\",\"name\":\"$3\"${4:+,$4}}"
  if [[ $RC == 0 ]]; then printf -v "$1" '%s' "$id"; else bad "folders.new $3" "$ERR"; fi
}
mkfolder BASE "$R" "$RUN"
mkfolder INTERNAL "$BASE" internal
mkfolder INFRA "$BASE" infra '"sharingBlocked":true,"externalBlocked":true'
mkfolder SRV "$INFRA" servers
mkfolder CLIENTS "$BASE" clients
mkfolder CLI "$CLIENTS" "$CSLUG"
mkfolder PRJ "$CLI" "$PSLUG"
mkfolder PRIV "$PRJ" private '"inheritAcl":false'
P="/$RUN"; PP="$P/clients/$CSLUG/$PSLUG"
[[ ${#FAILS[@]} == 0 ]] && ok "docs (service) creates the tree $P/{internal,infra/servers,clients/.../private}"
as docs docs cmd "folders.$PRIV.acl.replace" '{"rules":[{"principalType":"role","principal":"admin","permission":"manage"}]}'
expect "acl.replace (deprecated) is accepted from the service" 0
as user docs cmd folders.new "{\"id\":\"$(ulid)\",\"parentId\":\"$BASE\",\"name\":\"x\"}"
expect "user: folders.new → permission_denied (admin and services only)" 1 "permission_denied"
as user docs cmd "folders.$PRIV.acl.replace" '{"rules":[]}'
expect "user: acl.replace → permission_denied" 1 "permission_denied"
as user docs query folders.list --filter "pathPrefix=$P/" --include effectivePermission -o json
perm() { json "{f['path']:f.get('effectivePermission') for f in d}.get('$1','-')" <<<"$OUT"; }
[[ $(perm "$PP/private/") == write && $(perm "$P/infra/servers/") == write ]] \
  && ok "user sees every folder with write, also the one without inheritance and an admin-only rule" || bad "folders of user" "$(perm "$PP/private/") $(perm "$P/infra/servers/")"
as user docs query folders.get --id "$PRIV" --include acl -o json
expect "user: folders.get include acl → permission_denied" 1 "permission_denied"
as external docs query folders.list -o json
[[ "$OUT" == "[]" ]] && ok "external sees no folders" || bad "external sees folders" "$OUT"

# ---- 2. document lifecycle -------------------------------------------------------------------

section "2. document lifecycle as user"
cd "$TMP" || exit 2
# The document contents are Spanish on purpose: Jiku users write in Spanish and search uses the
# Spanish text configuration (es_unaccent: stemming, accents), which sections 3 and 5 exercise.
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
expect "new with a nonexistent relation → invalid_relation" 1 "invalid_relation"
t0=$(ms); as user docs new arq.md --folder "$PP/" --message "First version"; t1=$(ms)
expect "new with real frontmatter" 0 "version 1"
DOC=$(json 'd["id"]' <<<"$OUT")
as user docs new arq.md --folder "$PP/" --id "$DOC"
expect "repeated new with the same id → same document" 0 "$DOC"
as user docs new arq.md --folder "$PP/" --name ARQ.md
expect "repeated name, case-insensitive → already_exists" 1 "already_exists"
as user docs get "$DOC"
[[ $RC == 0 && "$OUT"$'\n' == "$(cat arq.md)"$'\n' ]] && ok "get returns the markdown as is" || bad "get markdown" "$ERR"
as user docs get "$PP/arq.md" -o json
[[ $(json 'd["version"]' <<<"$OUT") == 1 ]] && ok "get -o json" || bad "get -o json"
as user docs query documents.get --id "$DOC" --include relations --include topics -o json
[[ $(json 'sorted(d["topics"])' <<<"$OUT") == "['arquitectura', 'integracion']" ]] && ok "normalized topics" || bad "topics" "$OUT"
[[ $(json 'd["relations"]["tasks"]' <<<"$OUT") == "[$TASK_ID]" ]] && ok "relations stored" || bad "relations" "$OUT"
[[ -n $(json 'd.get("createdByName") or ""' <<<"$OUT") && $(json 'd["clients"]' <<<"$OUT") == "[$CLIENT_ID]" ]] \
  && ok "createdByName and clients/projects in documents.get" || bad "names or clients" "$OUT"
cp arq.md v2.md; echo "Second version." >> v2.md
as user docs put "$DOC" v2.md --base-version 1 --message "v2"
expect "put with --base-version" 0 "version 2"
cp v2.md v3.md; echo "Another one." >> v3.md
as user docs put "$DOC" v3.md --base-version 1
expect "put with an old base → version_conflict" 1 "version_conflict"
as user docs put "$DOC" v2.md --base-version 1
expect "idempotent put (same content, old base) → same version" 0 "version 2"
as user docs history "$DOC" -o json
[[ $(json 'len(d)' <<<"$OUT") == 2 && -n $(json 'd[0].get("authorName") or ""' <<<"$OUT") ]] && ok "history: 2 versions with authorName" || bad "history" "$OUT"
printf '# Note\n' > note.md
as user docs new note.md --folder "$PP/"; NOTE=$(json 'd["id"]' <<<"$OUT")
as user docs mv "$NOTE" "$PP/note-renamed.md" --base-version 1
expect "mv (rename)" 0 "note-renamed.md"
as user docs mv "$NOTE" "$P/infra/" --base-version 1
expect "mv to /infra/ (folders no longer restrict an internal person)" 0 "$P/infra/"
as user docs mv "$NOTE" "$PP/" --base-version 1 >/dev/null
as user docs rm "$NOTE" --base-version 1
expect "rm" 0
as user docs rm "$NOTE" --base-version 1
expect "repeated rm → success" 0
as user docs get "$NOTE"
expect "get of a deleted one marks it" 0 "DELETED"
as user docs restore "$NOTE" --from-version 1 --base-version 1
expect "restore" 0 "version 2"
as user docs new note.md --folder "$PP/" --name note.txt
expect "invalid name → file_name_invalid" 1 "file_name_invalid"
printf -- '---\nvisibility: secret\n---\nhello\n' > fm.md
as user docs new fm.md --folder "$PP/"
expect "invalid frontmatter → frontmatter_invalid" 1 "frontmatter_invalid"
printf -- '---\njiku:\n  foo: [1]\n---\nhello\n' > fm2.md
as user docs new fm2.md --folder "$PP/"
expect "unknown key in jiku: → frontmatter_invalid" 1 "jiku.foo"
{ echo "# Large"; for i in $(seq 1 6000); do echo "Filler line $i to go over the inline content limit."; done; } > large.md
as user docs new large.md --folder "$PP/"
expect "content > 256 KiB through a presigned URL" 0 "version 1"
as user docs get "$PP/large.md"
[[ $RC == 0 && "$OUT"$'\n' == "$(cat large.md)"$'\n' ]] && ok "get of > 256 KiB (contentUrl + checksum)" || bad "get large"
{ echo "# Huge"; head -c 6000000 /dev/zero | tr '\0' a; } > huge.md
as user docs new huge.md --folder "$PP/"
expect "content > 5 MiB → content_too_large" 1 "content_too_large"
# Generated location: without --folder the module derives it from the relations (it goes to the real tree; deleted at the end).
printf -- "---\njiku:\n  projects: [$PROJECT_ID]\n---\n\n# Generated location $RUN\n" > gen.md
as user docs new gen.md
GEN=$(json 'd["id"]' <<<"$OUT" 2>/dev/null); GENPATH=$(json 'd["path"]' <<<"$OUT" 2>/dev/null)
[[ $RC == 0 && "$GENPATH" == /clients/*/$PSLUG/generated-location-${RUN}.md ]] \
  && ok "new without a folder: $GENPATH" || bad "generated location" "rc=$RC $GENPATH $ERR"

# ---- 3. permissions ---------------------------------------------------------------------------

section "3. permissions by visibility"
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
as docs docs new staging.md --folder "$P/infra/servers/"; STG=$(json 'd["id"]' <<<"$OUT")
expect "docs (service) creates a public one in /infra/ (externalBlocked no longer has any effect)" 0
printf -- "---\nvisibility: restricted\ntopics: [e2e-restringido-$RUN]\njiku:\n  projects: [$PROJECT_ID]\n---\n\n# Privado $RUN\n\nrenegociación del contrato\n" > priv.md
as docs docs new priv.md --folder "$PP/private/"; PRIVDOC=$(json 'd["id"]' <<<"$OUT")
expect "docs (service) creates a restricted one" 0
as user docs new note.md --folder "$P/infra/" --name note-infra.md
expect "user writes in /infra/ (any folder)" 0 "version 1"
INFRANOTE=$(json 'd["id"]' <<<"$OUT" 2>/dev/null)
printf -- '---\nvisibility: restricted\n---\n# R\n' > res.md
as user docs new res.md --folder "$PP/"
expect "user creates a restricted one → permission_denied" 1 "permission_denied"
sed 's/^title: \(.*\)/title: \1\nvisibility: public/' v2.md > pub.md
as user docs put "$DOC" pub.md --base-version 2
expect "user marks public (no longer requires manage)" 0 "version 3"
sed 's/^visibility: public/visibility: restricted/' pub.md > resdoc.md
as user docs put "$DOC" resdoc.md --base-version 3
expect "user marks restricted → permission_denied" 1 "permission_denied"
# restricted invisible to human-user and human-external, through every path the CLI reaches.
for who in user external; do
  as "$who" docs get "$PRIVDOC"
  expect "$who: get of a restricted one → document_not_found" 1 "document_not_found"
  as "$who" docs get "$PRIVDOC" --version 1
  expect "$who: get --version of a restricted one → document_not_found" 1 "document_not_found"
  as "$who" docs history "$PRIVDOC"
  expect "$who: history of a restricted one → document_not_found" 1 "document_not_found"
  as "$who" docs query documents.list --filter "pathPrefix=$P/" --filter visibility=restricted -o json
  [[ "$OUT" == "[]" ]] && ok "$who: filter visibility=restricted → empty list" || bad "$who: restricted filter" "$OUT"
  as "$who" docs query documents.list --filter "pathPrefix=$P/" -o json
  ! grep -q "$PRIVDOC" <<<"$OUT" && ok "$who: documents.list does not return it" || bad "$who: documents.list returns the restricted one"
  as "$who" docs search renegociación -o json
  ! grep -q "$PRIVDOC\|renegociaci" <<<"$OUT" && ok "$who: search without the restricted one or its snippets" || bad "$who: search returns the restricted one" "$OUT"
  as "$who" docs query topics.list --filter "prefix=e2e-restringido-$RUN" -o json
  [[ "$OUT" == "[]" ]] && ok "$who: topics.list does not count its topic" || bad "$who: topics.list" "$OUT"
  as "$who" docs query attachments.list --filter "documentId=$PRIVDOC" -o json
  expect "$who: attachments.list → document_not_found" 1 "document_not_found"
  as "$who" docs shares "$PRIVDOC" -o json
  expect "$who: shares → document_not_found" 1 "document_not_found"
  as "$who" docs suggest "$PRIVDOC"
  expect "$who: suggest → document_not_found" 1 "document_not_found"
done
as docs docs search renegociación --filter "pathPrefix=$P/" -o json
[[ $(json 'len(d)' <<<"$OUT") == 1 ]] && ok "docs (service) does see it" || bad "service does not see the restricted one" "$OUT"
as user docs put "$PRIVDOC" priv.md --base-version 1
expect "user writes a restricted one → document_not_found" 1 "document_not_found"
# The external person of the run is a real person who may already have project-permissions on the
# project (removed at the start and given back at the end). jiku-docs caches the
# per-project permissions for 60 s, so it waits until the change shows.
ext_blind() { as external docs get "$DOC"; [[ $RC == 1 ]] && grep -q document_not_found <<<"$ERR"; }
if until_ok 90 ext_blind; then ok "external without project-permissions does not see it"; else bad "external without project-permissions does not see it" "rc=$RC $(head -1 <<<"$ERR")"; fi
if jikudb "INSERT INTO user_project_permissions(user_id, project_id) VALUES ('$E2E_EXTERNAL_ID', $PROJECT_ID) ON CONFLICT DO NOTHING" >/dev/null; then
  ext_sees() { as external docs get "$DOC"; [[ $RC == 0 ]]; }
  if until_ok 90 ext_sees; then ok "external with project-permissions sees the public one (60 s cache)"; else bad "external does not see the public one"; fi
  as external docs get "$DOC" --version 1
  expect "external does not read earlier versions (from when it was internal)" 1 "document_not_found"
  as external docs get "$STG"
  expect "external sees a public one in /infra/ (externalBlocked without effect)" 0
  as external docs get "$PRIVDOC"
  expect "external with project-permissions does not see the project's restricted one either" 1 "document_not_found"
  as external docs query documents.list --filter "pathPrefix=$P/" -o json
  [[ $(json 'sorted(x["id"] for x in d)' <<<"$OUT") == "$(python3 -c "print(sorted(['$DOC','$STG']))")" ]] && ok "external: documents.list only the related public ones" || bad "external list" "$OUT"
  [[ $(json '[x.get("clients") for x in d]' <<<"$OUT") == "[[], []]" ]] && ok "external: no client ids" || bad "external clients" "$OUT"
  as external docs search integración --filter "pathPrefix=$P/" -o json
  [[ $(json '[h["documentId"] for h in d]' <<<"$OUT") == "['$DOC']" ]] && ok "external: search honours permissions" || bad "external search" "$OUT"
  as external docs put "$DOC" pub.md --base-version 3
  [[ $RC != 0 ]] && ok "external never writes (the bus rejects the command plane)" || bad "external wrote"
else
  skip "project-permissions of the external person (could not write to $E2E_JIKU_DB)"
fi

# ---- 4. locks -----------------------------------------------------------------------------------

section "4. locks"
as user docs lock "$DOC" --ttl 60
expect "lock of session A" 0
SESS=$(json 'd["sessionId"]' <<<"$OUT")
cp pub.md l.md; echo "with lock" >> l.md
as docs docs put "$DOC" l.md --base-version 3
expect "put from another session → document_locked with holder" 1 "is editing it"
as user docs query documents.get --id "$DOC" --include lock -o json
[[ -n $(json 'd["lock"].get("holderName") or ""' <<<"$OUT") ]] && ok "lock.holderName" || bad "lock without holderName" "$OUT"
as user docs lock "$DOC" --session "$SESS" --ttl 120
expect "renewal with the same session" 0 "$SESS"
as user docs put "$DOC" l.md --base-version 3 --session "$SESS"
expect "put with the session holding the lock" 0 "version 4"
as user docs unlock "$DOC" --session "$SESS" --force
expect "force by a user → permission_denied (admin or service only)" 1 "permission_denied"
as docs docs unlock "$DOC" --session "$SESS" --force
expect "force by the service" 0
as user docs lock "$DOC" --ttl 10
expect "ttl < 30 → invalid_fields" 1 "ttlSeconds"
as user docs lock "$DOC" --ttl 30
expect "30 s lock" 0
LOCKSEQ=$(docsdb "SELECT coalesce(max(seq),0) FROM changelog")
expired() { [[ $(docsdb "SELECT count(*) FROM changelog WHERE seq > $LOCKSEQ AND type = 'document.unlocked' AND entity_id = '$DOC' AND payload->'changes'->>'reason' = 'expired'") -ge 1 ]]; }
if until_ok 75 expired; then ok "expiry → document.unlocked (reason expired)"; else bad "the lock did not expire"; fi

# ---- 5. search ----------------------------------------------------------------------------------

section "5. search, filters and relations"
printf -- "---\ntitle: Integración con aduana\ntopics: [aduana]\njiku:\n  requirements: [$REQ_ID]\n---\n\n# Aduana\n\nLa integración con aduana usa el webservice. Prueba de carga pendiente.\n" > aduana.md
printf -- "---\ntitle: Reunión semanal\ntopics: [reuniones]\njiku:\n  tasks: [$TASK_ID]\n---\n\n# Reunión\n\nSe habló de la aduana y de los servidores.\n" > reunion.md
printf -- "---\ntitle: Transversal\n---\n\n# Transversal\n\nServidor compartido sin relación.\n" > trans.md
for f in aduana reunion trans; do as user docs new $f.md --folder "$PP/" >/dev/null; done
as user docs rm "$INFRANOTE" --base-version 1 >/dev/null
hits() { as user docs search "$@" --filter "pathPrefix=$P/" -o json; json 'sorted(h["title"] for h in d)' <<<"$OUT"; }
[[ $(hits '"integración con aduana"') == "['Arquitectura e2e', 'Integración con aduana']" ]] && ok "quoted phrase" || bad "phrase" "$(hits '"integración con aduana"')"
[[ $(hits 'aduana -prueba') == "['Reunión semanal']" ]] && ok "-word excludes (with stemming: pruebas)" || bad "-word" "$(hits 'aduana -prueba')"
[[ $(hits 'reunion') == "['Reunión semanal']" ]] && ok "without accent finds with accent" || bad "accents"
[[ $(hits 'servidor') == "['Reunión semanal', 'Servidor de staging e2e', 'Transversal']" ]] && ok "singular finds plural" || bad "plurals" "$(hits servidor)"
as user docs search aduana --filter topic=reuniones --filter "pathPrefix=$P/" -o json
[[ $(json '[h["title"] for h in d]' <<<"$OUT") == "['Reunión semanal']" ]] && ok "topic filter" || bad "topic filter"
as user docs search servidor --filter "projectId=$PROJECT_ID" --filter "pathPrefix=$P/" -o json
[[ $(json 'sorted(h["title"] for h in d)' <<<"$OUT") == "['Reunión semanal', 'Servidor de staging e2e']" ]] && ok "projectId filter in search" || bad "projectId search" "$OUT"
[[ $(json 'sorted((h["title"], h["projectMatch"]) for h in d)' <<<"$OUT") == "[('Reunión semanal', 'indirect'), ('Servidor de staging e2e', 'direct')]" ]] \
  && ok "projectMatch in search" || bad "projectMatch search" "$OUT"
as docs docs search renegociación --filter "pathPrefix=$P/" -o json; A=$(json 'len(d)' <<<"$OUT")
as user docs search renegociación --filter "pathPrefix=$P/" -o json; U=$(json 'len(d)' <<<"$OUT")
[[ "$A/$U" == 1/0 ]] && ok "search does not return what cannot be seen" || bad "search permissions" "$A/$U"
as user docs query documents.list --filter "projectId=$PROJECT_ID" --filter "pathPrefix=$P/" --fields title -o json
[[ $(json 'sorted((x["title"], x["projectMatch"]) for x in d)' <<<"$OUT") == "[('Arquitectura e2e', 'direct'), ('Integración con aduana', 'indirect'), ('Reunión semanal', 'indirect'), ('Servidor de staging e2e', 'direct')]" ]] \
  && ok "documents.list projectId with projectMatch: direct, by requirement, by task" || bad "documents.list projectId" "$OUT"
as user docs query documents.list --filter "pathPrefix=$P/" --filter hasProject=false --fields title -o json
[[ $(json 'sorted(x["title"] for x in d)' <<<"$OUT") == "['Large', 'Note', 'Transversal']" ]] && ok "hasProject=false (\"Sin proyecto\", no project)" || bad "hasProject" "$OUT"
as user docs query documents.list --filter "pathPrefix=$P/" --filter hasTopic=false --fields title -o json
[[ $(json 'sorted(x["title"] for x in d)' <<<"$OUT") == "['Large', 'Note', 'Transversal']" ]] && ok "hasTopic=false (\"Sin tema\", no topic)" || bad "hasTopic" "$OUT"
as user docs query documents.list --filter "pathPrefix=$P/" --filter hasClient=true --fields title -o json
[[ $(json 'sorted(x["title"] for x in d)' <<<"$OUT") == "['Arquitectura e2e']" ]] && ok "hasClient=true" || bad "hasClient" "$OUT"
as user docs get "$PP/trans.md" -o json; TRV=$(json 'd["version"]' <<<"$OUT")
as user docs relate "$PP/trans.md" --add-project "$PROJECT_ID" --base-version "$TRV" -o json
expect "relate --add-project (\"Asignar proyecto\", assign project)" 0
[[ $(json 'd["relations"]["clients"]' <<<"$OUT") == "[$CLIENT_ID]" ]] && ok "adding a project adds its client" || bad "relate client" "$OUT"
as user docs history "$PP/trans.md" -o json
grep -q "Project assigned" <<<"$OUT" && ok "version with an automatic message" || bad "relate message" "$OUT"
as user docs get "$PP/trans.md"
grep -q "Servidor compartido sin relación" <<<"$OUT" && grep -q "projects: \[$PROJECT_ID\]" <<<"$OUT" && ok "relate rewrites only the jiku: block" || bad "relate content" "$OUT"
printf -- "# Minuta $PROJECT_NAME\n\nSe revisó el avance de $PROJECT_NAME.\n" > sug.md
as user docs new sug.md --folder "$PP/"; SUG=$(json 'd["id"]' <<<"$OUT")
as user docs suggest "$SUG" -o json
[[ $RC == 0 ]] && python3 -c "import json,sys; d=json.loads(sys.argv[1]); sys.exit(0 if any(c.get('projectId')==int(sys.argv[2]) for c in d) else 1)" "$OUT" "$PROJECT_ID" \
  && ok "suggest proposes the project named in the title" || bad "suggest" "$OUT"

# ---- 6. attachments ------------------------------------------------------------------------------

section "6. attachments (delegated to core's files.*)"
python3 -c "
import zlib,struct
def c(t,d): return struct.pack('>I',len(d))+t+d+struct.pack('>I',zlib.crc32(t+d)&0xffffffff)
raw=b''.join(b'\x00'+b'\xff\x00\x00'*8 for _ in range(8))
open('diagrama.png','wb').write(b'\x89PNG\r\n\x1a\n'+c(b'IHDR',struct.pack('>IIBBBBB',8,8,8,2,0,0,0))+c(b'IDAT',zlib.compress(raw))+c(b'IEND',b''))"
as user docs attach "$DOC" diagrama.png
expect "attach → link to the web" 0 "/docs/attachments/"
ATTLINK=$(tail -1 <<<"$OUT"); ATT=${ATTLINK##*/}
as user docs query attachments.list --filter "documentId=$DOC" -o json
[[ $(json '[a["id"] for a in d]' <<<"$OUT") == "['$ATT']" ]] && ok "attachments.list" || bad "attachments.list" "$OUT"
as user docs cmd "attachments.$ATT.request-download" '{}'
URL=$(json 'd["downloadUrl"]' <<<"$OUT")
curl -sf -o dl.png "$URL" && cmp -s dl.png diagrama.png && ok "request-download fetches the real file" || bad "attachment download"
printf '#!/bin/sh\n' > x.sh
as user docs attach "$DOC" x.sh
expect "type not allowed → attachment_type_not_allowed" 1 "attachment_type_not_allowed"
as user docs attach "$PRIVDOC" diagrama.png
expect "attaching to a restricted one → document_not_found" 1 "document_not_found"

# ---- 7. share links -----------------------------------------------------------------------------

section "7. share links"
as user docs share "$DOC" --expires 1d
expect "user creates a share link (any internal person)" 0
USHARE_URL=$(head -1 <<<"$OUT"); USHARE=$(grep -o 'shared link [0-9A-Z]*' <<<"$ERR" | awk '{print $3}')
as user docs revoke "$USHARE"
expect "user revokes the share link" 0
[[ $(curl -s -o /dev/null -w '%{http_code}' "$USHARE_URL") == 404 ]] && ok "share link revoked by user → 404" || bad "revoked does not give 404"
as docs docs get "$DOC" -o json; V=$(json 'd["version"]' <<<"$OUT")
cp l.md s.md; printf '\n![diagrama](%s)\n\n<script>alert(1)</script>\n' "$ATTLINK" >> s.md
as docs docs put "$DOC" s.md --base-version "$V" >/dev/null
as docs docs share "$DOC" --expires 2d --note e2e
expect "share as docs (service)" 0
SHARE_URL=$(head -1 <<<"$OUT"); SHARE_ID=$(grep -o 'shared link [0-9A-Z]*' <<<"$ERR" | awk '{print $3}')
[[ "$SHARE_URL" == "$E2E_PUBLIC_URL/p/docs/jds_"* ]] && ok "url {JIKU_PUBLIC_URL}/p/docs/jds_…" || bad "share link url" "${SHARE_URL%%jds_*}"
H=$(curl -s -D - -o page.html "$SHARE_URL")
grep -q '^HTTP/1.1 200' <<<"$H" && ok "GET of the share link → 200" || bad "GET of the share link" "$(head -1 <<<"$H")"
for h in "Content-Security-Policy: default-src 'none'" "X-Robots-Tag: noindex" "Referrer-Policy: no-referrer" "Cache-Control: private, no-store"; do
  grep -qiF "$h" <<<"$H" && ok "header $h" || bad "missing header $h"
done
grep -q "script-src\|'unsafe-eval'" <<<"$(grep -i '^content-security-policy' <<<"$H")" && bad "the CSP allows scripts" || ok "CSP without scripts"
! grep -q 'jiku:\|<script' page.html && ok "no frontmatter or <script> in the HTML" || bad "frontmatter or script in the HTML"
IMG=$(grep -o '/p/docs/jds_[^"]*/attachments/[0-9A-Z]*' page.html | head -1)
[[ -n "$IMG" ]] && [[ $(curl -s -o /dev/null -w '%{http_code}' "$E2E_PUBLIC_URL$IMG") == 302 ]] && ok "attachment of the share link → 302 to a presigned URL" || bad "attachment through the share link"
as docs docs shares "$DOC" -o json
grep -q 'jds_' <<<"$OUT" && bad "shares returns the token" || ok "shares lists without token"
as docs docs revoke "$SHARE_ID"
expect "revoke" 0
[[ $(curl -s -o /dev/null -w '%{http_code}' "$SHARE_URL") == 404 ]] && ok "revoked share link → 404" || bad "revoked does not give 404"
as docs docs share "$PRIVDOC"
expect "share of a restricted one → sharing_blocked" 1 "sharing_blocked"
as docs docs share "$STG" --expires 1d
expect "share in /infra/ (sharingBlocked no longer has any effect)" 0
# A share link older than the restriction: making the document restricted revokes it and the public path gives 404.
printf -- "---\ntitle: Gets restricted e2e\njiku:\n  projects: [$PROJECT_ID]\n---\n\n# Gets restricted\n\ncontent $RUN\n" > lr.md
as docs docs new lr.md --folder "$PP/"; LR=$(json 'd["id"]' <<<"$OUT")
as docs docs share "$LR" --expires 1d; LRURL=$(head -1 <<<"$OUT")
[[ $(curl -s -o /dev/null -w '%{http_code}' "$LRURL") == 200 ]] && ok "share link of an internal one → 200" || bad "share link of an internal one"
sed 's/^title: \(.*\)/title: \1\nvisibility: restricted/' lr.md > lr2.md
as docs docs put "$LR" lr2.md --base-version 1
expect "the service makes it restricted" 0 "version 2"
[[ $(curl -s -o /dev/null -w '%{http_code}' "$LRURL") == 404 ]] && ok "share link older than the restriction → 404" || bad "the share link survives the restriction"
[[ $(docsdb "SELECT revoke_reason FROM shares WHERE document_id = '$LR'") == restricted ]] && ok "revokeReason restricted" || bad "revokeReason"
as user docs get "$LR"
expect "user no longer sees it" 1 "document_not_found"
as docs docs share "$DOC" --expires 200d
expect "expiry > 180 days → share_expiry_invalid" 1 "share_expiry_invalid"
printf '# Accesos\n\npassword: Sup3rS3creta!\n' > sec.md
JIKU_VIA=agent as docs docs new sec.md --folder "$PP/"
expect "possible secret → warning, does not block" 0 "possible_secret_detected"
as docs docs share "$PP/sec.md"
expect "share of a doc with a secret requires --ack-secret-warning" 1 "secret_warning_not_acknowledged"
as docs docs share "$PP/trans.md" --expires 1d; TURL=$(head -1 <<<"$OUT")
as docs docs get "$PP/trans.md" -o json; TV=$(json 'd["version"]' <<<"$OUT")
as docs docs rm "$PP/trans.md" --base-version "$TV" >/dev/null
[[ $(curl -s -o /dev/null -w '%{http_code}' "$TURL") == 404 ]] && ok "deleting the document revokes its share links" || bad "share link survives the deletion"

# ---- 8. events and sync --------------------------------------------------------------------------

section "8. events, feed and sync"
if UNPUB=$(docsdb "SELECT count(*) FROM changelog WHERE published_at IS NULL AND occurred_at < now() - interval '10 seconds'"); then
  [[ "$UNPUB" == 0 ]] && ok "outbox: everything published (published_at)" || bad "outbox: $UNPUB unpublished events"
  LEAK=$(docsdb "SELECT count(*) FROM changelog WHERE payload::text LIKE '%grabaciones van al bucket%' OR payload::text LIKE '%jds\_%'")
  [[ "$LEAK" == 0 ]] && ok "no event carries content or tokens" || bad "events with content or tokens: $LEAK"
else
  skip "changelog (no access to $E2E_DOCS_DB)"
fi
as user docs query documents.changes --payload "{\"sinceSeq\":${START_SEQ:-0},\"limit\":500}" -o json
grep -q "$DOC" <<<"$OUT" && ! grep -q "$PRIVDOC" <<<"$OUT" && ! grep -q "$LR" <<<"$OUT" && ok "documents.changes without the restricted ones (nor their earlier history)" || bad "documents.changes"
as external docs query documents.changes --payload "{\"sinceSeq\":${START_SEQ:-0},\"limit\":500}" -o json
! grep -q "$PRIVDOC\|$LR" <<<"$OUT" && ok "external: documents.changes without the restricted ones" || bad "external: documents.changes"
SYNC=$TMP/sync
as user docs sync "$SYNC" --once --only "$P/"
expect "sync --once" 0
[[ -f "$SYNC$PP/arq.md" && -w "$SYNC$P/infra/servers/staging.md" ]] && ok "sync fetches the documents; there are no read-only folders any more" || bad "sync --once: files"
[[ ! -e "$SYNC$PP/private" || -z "$(ls -A "$SYNC$PP/private" 2>/dev/null)" ]] && ! grep -rqs "renegociaci" "$SYNC" && ok "sync of user: no restricted one on disk" || bad "sync fetched a restricted one"
grep -q 'deleted in Jiku' <<<"$ERR" && bad "sync applied an already restored deletion" || ok "sync does not reapply restored deletions"
idenv user; env "${ID_ENV[@]}" "$JIKU_BIN" docs sync "$SYNC" --only "$P/" 2>"$TMP/sync.log" &
SYNC_PID=$!
until_ok 20 grep -q 'online' "$TMP/sync.log" || bad "the daemon did not go online"
as docs docs get "$PP/reunion.md" -o json; RV=$(json 'd["version"]' <<<"$OUT")
cp reunion.md r.md; echo "Remote change by the service." >> r.md
as docs docs put "$PP/reunion.md" r.md --base-version "$RV" >/dev/null; t0=$(ms)
if until_ok 15 grep -q 'Remote change by the service' "$SYNC$PP/reunion.md"; then ok "remote change → local file in $(( $(ms) - t0 )) ms"; else bad "the remote change did not arrive"; fi
echo "Local change." >> "$SYNC$PP/aduana.md"; t0=$(ms)
remote_has() { as user docs get "$PP/aduana.md"; grep -q 'Local change' <<<"$OUT"; }
if until_ok 20 remote_has; then ok "local change → uploaded in $(( $(ms) - t0 )) ms"; else bad "the local change was not uploaded"; fi
kill "$SYNC_PID"; wait "$SYNC_PID" 2>/dev/null; SYNC_PID=""
echo "Offline local edit." >> "$SYNC$PP/aduana.md"
as docs docs get "$PP/aduana.md" -o json; AV=$(json 'd["version"]' <<<"$OUT"); json 'd["content"]' <<<"$OUT" > ad.md
echo "Concurrent remote edit." >> ad.md
as docs docs put "$PP/aduana.md" ad.md --base-version "$AV" >/dev/null
as user docs sync "$SYNC" --once --only "$P/"
ls "$SYNC$PP/" | grep -q 'aduana.remoto-v' && ok "conflict → .remoto-vN.md" || bad "conflict without a remote copy" "$ERR"

# ---- 8b. history compaction ----------------------------------------------------------------------

# Document with seven saves in a row (one with a message); all but the current one are aged in the
# database and it is compacted on demand inside the container (docs/history-compaction-v1.md).
section "8b. history compaction"
if docsdb "SELECT 1" >/dev/null 2>&1; then
  cp arq.md h.md
  as user docs new h.md --folder "$PP/" --name history.md
  expect "document to compact" 0 "version 1"
  HDOC=$(json 'd["id"]' <<<"$OUT")
  for i in 1 2 3 4 5 6 7; do
    echo "Save $i." >> h.md
    if [[ $i == 3 ]]; then as user docs put "$HDOC" h.md --base-version "$i" --message "e2e milestone"
    else as user docs put "$HDOC" h.md --base-version "$i"; fi
    [[ $RC == 0 ]] || bad "put $i of the history" "$ERR"
  done
  docsdb "UPDATE document_versions SET created_at = created_at - interval '3 days' WHERE document_id = '$HDOC' AND version < 8" >/dev/null
  [[ $(docsdb "SELECT string_agg(kind || ':' || protected, ',' ORDER BY version) FROM document_versions WHERE document_id = '$HDOC'") == \
    "create:true,update:false,update:false,update:true,update:false,update:false,update:false,update:false" ]] \
    && ok "kind and protected on write (create, message)" || bad "kind/protected on write"
  DRY=$(docker exec "$E2E_DOCS_CONTAINER" jiku-docs history compact --document "$HDOC" --dry-run 2>/dev/null)
  grep -q 'versionsPruned=4 ' <<<"$DRY" && [[ $(docsdb "SELECT count(*) FROM document_versions WHERE document_id = '$HDOC'") == 8 ]] \
    && ok "history compact --dry-run reports 4 and deletes nothing" || bad "dry-run" "$DRY"
  HKEY3=$(docsdb "SELECT storage_key FROM document_versions WHERE document_id = '$HDOC' AND version = 3")
  RUN=$(docker exec "$E2E_DOCS_CONTAINER" jiku-docs history compact --document "$HDOC" 2>/dev/null)
  [[ $(docsdb "SELECT string_agg(version::text, ',' ORDER BY version) FROM document_versions WHERE document_id = '$HDOC'") == "1,4,7,8" ]] \
    && ok "compaction: the creation, the one with a message, the last of the block and the current one remain" || bad "compaction" "$RUN"
  [[ $(docsdb "SELECT count(*) FROM pruned_objects WHERE document_id = '$HDOC'") == 0 ]] && grep -q 'objectsTrashed=4 ' <<<"$RUN" \
    && ok "objects moved to the trash (pruned_objects empty, objectsTrashed=4)" || bad "pruned_objects with rows" "$RUN"
  # The four pruned objects ended up in trash/ and none of them under docs/ (with the mc of
  # Jiku's MinIO and its root credentials, inside the container).
  if docker exec "$E2E_STORAGE_CONTAINER" sh -c 'mc alias set e2e http://127.0.0.1:9000 "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD"' >/dev/null 2>&1; then
    s3count() { docker exec "$E2E_STORAGE_CONTAINER" mc ls --recursive "e2e/$E2E_DOCS_BUCKET/$1" 2>/dev/null | grep -c '\.md$'; }
    [[ $(s3count "trash/docs/$HDOC/") == 4 && $(s3count "docs/$HDOC/") == 4 ]] \
      && ok "trash/: the 4 pruned objects are in trash/docs/<id>/ and under docs/<id>/ only the 4 kept remain" \
      || bad "trash/" "trash=$(s3count "trash/docs/$HDOC/") docs=$(s3count "docs/$HDOC/")"
  else
    skip "trash/ over S3 (no mc in $E2E_STORAGE_CONTAINER)"
  fi
  as user docs query documents.get --id "$HDOC" -o json
  [[ $(json 'd["prunedVersionsCount"]' <<<"$OUT") == 4 ]] && ok "documents.get: prunedVersionsCount" || bad "prunedVersionsCount" "$OUT"
  as user docs get "$HDOC" --version 3
  expect "get of a pruned version → version_pruned (exits with 5)" 5 "version_pruned"
  as user docs restore "$HDOC" --from-version 2 --base-version 8
  expect "restore from a pruned version → version_pruned (exits with 5)" 5 "version_pruned"
  as docs docs cmd shares.new "{\"id\":\"$(ulid)\",\"documentId\":\"$HDOC\",\"mode\":\"pinned\",\"version\":5,\"expiresAt\":\"$(date -u -d '+1 day' +%Y-%m-%dT%H:%M:%S.000Z)\",\"includeAttachments\":false}"
  [[ $RC != 0 ]] && grep -q version_pruned <<<"$OUT$ERR" && ok "shares.new pinned on a pruned version → version_pruned" \
    || bad "shares.new pinned on a pruned version" "$ERR"
  as user docs history "$HDOC" -o json
  grep -q '"protected"' <<<"$OUT" && grep -q '"kind"' <<<"$OUT" && ok "history shows kind and protected" || bad "history without kind/protected" "$OUT"
  RUN2=$(docker exec "$E2E_DOCS_CONTAINER" jiku-docs history compact --document "$HDOC" 2>/dev/null)
  grep -q 'versionsPruned=0 ' <<<"$RUN2" && ok "second run: deletes nothing" || bad "second run" "$RUN2"
  # Recover version 3 from the trash: object and history row (it becomes protected).
  REST=$(docker exec "$E2E_DOCS_CONTAINER" jiku-docs storage restore-from-trash --key "$HKEY3" --version-row 2>/dev/null)
  grep -q '"objectRestored":true' <<<"$REST" && grep -q '"rowRestored":true' <<<"$REST" \
    && [[ $(docsdb "SELECT string_agg(version::text || ':' || protected, ',' ORDER BY version) FROM document_versions WHERE document_id = '$HDOC'") == \
      "1:true,3:true,4:true,7:false,8:false" ]] \
    && ok "storage restore-from-trash --version-row: the object and version 3 come back, protected" || bad "restore-from-trash" "$REST"
  as user docs get "$HDOC" --version 3
  expect "the recovered version can be read again" 0
  if M=$(curl -fsS "${E2E_DOCS_METRICS:-http://127.0.0.1:8090/metrics}" 2>/dev/null); then
    grep -q '^docs_history_pruned_objects_pending' <<<"$M" && ok "docs_history_* metrics" || bad "missing docs_history_* metrics"
    grep -q '^# TYPE docs_trash_objects_moved_total' <<<"$M" && ok "docs_trash_objects_moved_total metric" || bad "missing docs_trash_objects_moved_total"
  else
    skip "metrics (no access to /metrics)"
  fi
else
  skip "history compaction (no access to $E2E_DOCS_DB)"
fi

# ---- 9. module down and deregister --------------------------------------------------------------

section "9. module down and deregister"
if [[ "$E2E_DISRUPTIVE" == 1 ]]; then
  docker stop "$E2E_DOCS_CONTAINER" >/dev/null
  as user docs get "$DOC"
  expect "module down: jiku docs get exits with 4" 4
  docker start "$E2E_DOCS_CONTAINER" >/dev/null
  healthy() { [[ $(docker inspect -f '{{.State.Health.Status}}' "$E2E_DOCS_CONTAINER") == healthy ]]; }
  until_ok 60 healthy
  as user docs get "$DOC"
  expect "back to available" 0
  docker exec "$E2E_DOCS_CONTAINER" jiku-docs module deregister >/dev/null 2>&1
  as user docs status
  expect "deregister → not_installed (exits with 3)" 3 "not_installed"
  docker restart "$E2E_DOCS_CONTAINER" >/dev/null; until_ok 60 healthy
  as user docs status
  expect "restarting registers it again" 0 "available"
else
  skip "module down and deregister (E2E_DISRUPTIVE=1 to run it)"
fi

# ---- 10. logs ------------------------------------------------------------------------------------

section "10. logs"
for c in "$E2E_DOCS_CONTAINER" jiku-local-core jiku-local-api jiku-local-auth-callout-1 jiku-local-nats; do
  docker inspect "$c" >/dev/null 2>&1 || { skip "logs of $c (does not exist)"; continue; }
  L=$(docker logs --since 30m "$c" 2>&1)
  n=$(grep -cE 'grabaciones van al bucket|renegociaci|Sup3rS3creta|jds_[A-Za-z0-9_-]{20}|X-Amz-Signature=' <<<"$L")
  [[ "$n" == 0 ]] && ok "$c: no content, tokens or presigned URLs" || bad "$c: $n lines with content or secrets"
done
n=$(docker logs --since 30m "$E2E_DOCS_CONTAINER" 2>&1 | grep -c '"level":"ERROR"')
[[ "$n" == 0 ]] && ok "jiku-docs without errors" || bad "jiku-docs: $n errors in the log"

# ---- cleanup ---------------------------------------------------------------------------------------

# P-1: folders no longer hide anything, so the run deletes (logically) its documents so that they do not
# show up in the persons' web: the service lists and deletes them. jiku-docs does not delete folders.
section "cleanup"
if [[ "${E2E_KEEP_VISIBLE:-0}" != 1 ]]; then
  as docs docs query documents.list --filter "pathPrefix=$P/" --fields version -o json
  CLEAN=$(json '" ".join(x["id"]+":"+str(x["version"]) for x in d)' <<<"$OUT")
  [[ -n "${GEN:-}" ]] && { as docs docs get "$GEN" -o json; CLEAN="$CLEAN $GEN:$(json 'd["version"]' <<<"$OUT")"; }
  for it in $CLEAN; do
    as docs docs unlock "${it%%:*}" --session "$(ulid)" --force >/dev/null 2>&1
    as docs docs rm "${it%%:*}" --base-version "${it##*:}" >/dev/null || bad "rm ${it%%:*}" "$ERR"
  done
  as user docs query documents.list --filter "pathPrefix=$P/" -o json
  [[ "$OUT" == "[]" ]] && ok "the documents of the run $P/ were deleted (E2E_KEEP_VISIBLE=1 keeps them)" || bad "user still sees documents of $P/" "$OUT"
else
  skip "cleanup (E2E_KEEP_VISIBLE=1)"
fi

# ---- summary -------------------------------------------------------------------------------------

if [[ "$GRANT_REMOVED" == 1 ]]; then
  jikudb "INSERT INTO user_project_permissions(user_id, project_id) VALUES ('$E2E_EXTERNAL_ID', $PROJECT_ID) ON CONFLICT DO NOTHING" >/dev/null 2>&1 && GRANT_REMOVED=0
elif [[ "${E2E_CLEAN_GRANT:-1}" == 1 ]]; then
  jikudb "DELETE FROM user_project_permissions WHERE user_id = '$E2E_EXTERNAL_ID' AND project_id = $PROJECT_ID" >/dev/null 2>&1
fi
printf '\n\033[1m%d ok, %d failures, %d skipped\033[0m  (everything was left under %s/)\n' "$PASS" "${#FAILS[@]}" "${#SKIPS[@]}" "$P"
for f in "${FAILS[@]}"; do echo "  FAIL: $f"; done
[[ ${#FAILS[@]} == 0 ]]
