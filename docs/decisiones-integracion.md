# jiku-docs: decisiones de integración (addendum a la especificación v1)

Fecha: 2026-10-24. Este documento **manda sobre `especificacion-v1.md`** donde se contradicen. Los
contratos de `docs/apis/` siguen siendo la fuente de verdad de forma; donde una decisión de acá cambie
una forma, el repo `jiku-docs` actualiza el contrato y lo anota en su CHANGELOG.

Repos involucrados (rutas locales):

| Repo | Ruta | Rama de trabajo |
| --- | --- | --- |
| jiku-docs (servicio nuevo, Go) | `/home/lautaro/Documentos/proyectos/interno/gestor/conectores/jiku-docs` | `dev` |
| jiku (monorepo, grava-workflow) | `/home/lautaro/Documentos/proyectos/interno/gestor/jiku` | `feat/jiku-docs` (desde `dev`) |
| jiku-go (librería + CLI, **público**) | `/home/lautaro/Documentos/proyectos/interno/gestor/jiku-go` | `feat/jiku-docs` (desde `dev`) |

Nadie hace `git push`. Las credenciales viven en `jiku-docs/.local/` (ignorada) y nunca se commitean ni se
copian a otro repo.

## D-1. Identidad del servicio: rol `docs` + `internal-app`

- El machine user `jiku_docs` (userId `396604660191854594`) tiene **dos roles** en Zitadel: `internal-app`
  y `docs`.
- **core no cambia por esto.** Para core es un `internal-app` más: clase `connector` (sin recorte de filas),
  `identity_type = service`, `queries: ALL`, `commands: ALL`. El rol `docs` es desconocido para core y se
  ignora (verificar que un rol desconocido en `users.roles` no rompa `authorize-caller`, `caller-class` ni
  `identity-type`; si rompe, se corrige en jiku con test).
- **auth-callout:** nueva regla `match: docs` **por encima** de `internal-app` en `rules.yaml`, con plantilla
  nueva `templates/docs.yaml` = permisos de `connector.yaml` (para llamar a core) **más** los de servidor:
  - sub `{instance}.*.jiku-docs-commands.v1.>` y `{instance}.*.jiku-docs-queries.v1.>` (queue group), `$SRV.>`;
  - `response` como `core.yaml`;
  - pub `{instance}.docs-events.v1.>` y `{instance}.*.docs-notify.v1.>`;
  - administrar el stream `JIKU_DOCS_EVENTS` (`$JS.API.STREAM.*.JIKU_DOCS_EVENTS`, etc.);
  - escribir y leer solo la clave `docs` de `KV_JIKU_MODULES`.
  Verificar cómo matchea el callout con varios roles (primera regla que coincide con cualquiera de los roles).
- Donde la especificación dice `docs-service`, leer `docs`.

## D-2. Quién puede hablar con jiku-docs

- `connector.yaml` (`internal-app`: la api y cualquier conector) suma: pub en
  `{instance}.{user_id}.jiku-docs-commands.v1.>` y `.jiku-docs-queries.v1.>`, consumir `JIKU_DOCS_EVENTS` con
  durable propio, leer/observar `KV_JIKU_MODULES` y `$SRV.PING.>` (o `$SRV.PING.jiku-docs-queries`). Aceptado:
  cualquier `internal-app` es clase `service` en jiku-docs y lee todo.
- `person-internal.yaml`: pub comandos y consultas de docs bajo su `user_id`, sub
  `{instance}.{user_id}.docs-notify.v1.>`, leer/observar el KV.
- `person-external.yaml`: solo consultas de docs bajo su `user_id`, leer/observar el KV. Sin notify.
- Permisos exactos de lectura/observación del KV que necesita jiku-go (lo verificó su implementación):
  `$JS.API.STREAM.INFO.KV_JIKU_MODULES`, `$JS.API.DIRECT.GET.KV_JIKU_MODULES.$KV.JIKU_MODULES.>`,
  `$JS.API.CONSUMER.CREATE.KV_JIKU_MODULES` y `$JS.API.CONSUMER.CREATE.KV_JIKU_MODULES.>`,
  `$JS.FC.KV_JIKU_MODULES.>` (pub) y `$SRV.PING.jiku-docs-queries` (request). Van en `connector.yaml`,
  `person-internal.yaml`, `person-external.yaml` y `docs.yaml`.
- **La api se distingue por `user-id`**: jiku-docs recibe `JIKU_API_USER_ID` (local: `390014149763006466`,
  el `userId` de `jiku/local/api-su.json`). Solo ese caller puede mandar `actor` y usar `shares.resolve` /
  `shares.attachment`.

## D-3. Adjuntos: servicio de archivos de core (no hay bucket de adjuntos en jiku-docs)

- jiku-docs **no** tiene bucket ni credenciales de adjuntos. Los comandos `attachments.*` de jiku-docs mantienen
  su forma en el contrato, pero por dentro delegan en core, actuando como el servicio `jiku_docs` (sin `actor`):
  - `attachments.request-upload` → verifica `write` en el documento → `files.request-upload` en core → guarda
    `attachments(id, document_id, core_file_id, …, status=pending)` → responde `uploadUrl` de core.
  - `attachments.{id}.confirm` → pide `files.{fileId}.request-download` a core, descarga el objeto y verifica el
    SHA-256 declarado → `ready`. (core no verifica bytes.)
  - `attachments.{id}.request-download` / `shares.attachment` → verifica permisos en jiku-docs →
    `files.{fileId}.request-download` en core → devuelve la URL prefirmada.
  - `attachments.{id}.delete` → lógico en jiku-docs.
- Política de tipos y tamaños: la de core (`system_settings`); jiku-docs no duplica la allowlist y traduce los
  errores de core a `attachment_type_not_allowed` / `attachment_too_large`. Core caído → `upstream_unavailable`.
- La columna `attachments.storage_key` pasa a `core_file_id`. `attachments_not_configured` no se emite en v1
  (queda en el catálogo por compatibilidad).
- **A verificar en jiku:** que core no purgue `files` sin vincular a una entidad de Jiku (los de docs nunca se
  vinculan a requisitos/tareas). Si existe esa purga o la política lo impide, el REQ de jiku incluye el ajuste
  mínimo en core (p. ej. marcar el archivo como propiedad de un servicio externo).

## D-4. Storage de contenido: S3 propio (DigitalOcean Spaces en prod, MinIO en local)

- Un solo bucket propio de jiku-docs para el contenido versionado y las subidas grandes
  (`docs/<documentId>/<version>-<sha256>.md`, `uploads/<contentUploadId>`). En prod es un Space de
  DigitalOcean con credenciales propias; en local, bucket `jiku-docs-content` en el **MinIO del stack de jiku**
  (`http://172.28.0.1:9000` desde contenedores / `http://localhost:9000` desde el host, `jiku-local` /
  `jiku-local-secret`, path-style). El compose de jiku-docs crea el bucket de forma idempotente.

## D-5. Enlaces a adjuntos y ruta pública

- La api valida Bearer tokens; el navegador no le habla directo. Por eso el **link estable** que se pega en el
  markdown es relativo a la web: `{JIKU_WEB_URL}/docs/attachments/{id}`. La web lo atiende con un route handler
  (sesión → api `GET /api/docs/attachments/{id}` con Bearer → reenvía el 302), igual que
  `web/src/app/api/attachments/[id]/download/route.ts`. Los visores de web y opus-web reconocen ese patrón (y el
  path relativo `/docs/attachments/{id}`) y lo reescriben a su propio proxy. jiku-docs arma el `link` con
  `JIKU_WEB_URL`.
- Links compartidos: `url = {JIKU_PUBLIC_URL}/p/docs/{token}`; jiku-docs recibe `JIKU_PUBLIC_URL`. La ruta pública
  la sirve la api sin sesión (sección 9.8 de la especificación). En local, `JIKU_PUBLIC_URL=http://127.0.0.1:3100`
  (host distinto de `localhost`, sin cookies compartidas). Si en algún deploy la api no es pública, se documenta
  cómo exponer solo `/p/docs/*`.

## D-6. Registro de módulos

- jiku crea el KV `JIKU_MODULES` de forma idempotente (script como `create-events-stream.sh`, invocado por
  `deploy/local.sh up`) y lo documenta en `docs/apis/jiku-modules.yaml` (copiado de este repo).
- jiku-docs solo escribe su clave `docs` al arrancar; `jiku-docs module deregister` la borra.
- jiku-docs crea su stream `JIKU_DOCS_EVENTS` al arrancar (idempotente).

## D-7. Despliegue local de jiku-docs

- jiku-docs tiene su propio `docker-compose.local.yml` (Postgres 16 propio + el servicio + un init que crea el
  bucket) unido a la red externa del stack de jiku (`jiku-local_jiku`, verificar nombre real). No modifica el
  compose de jiku.
- Variables (nombres finales los decide el repo jiku-docs, documentados en su `.env.dist`): instancia NATS
  (`dev`), URL de NATS, creds del sentinel (`jiku/deploy/nats/creds/sentinel-client.creds`), key del machine user
  en base64 (`DOCS_SERVICE_USER_KEY_B64`), issuer y project id de Zitadel (los de `jiku/deploy/.env`),
  `JIKU_API_USER_ID`, `JIKU_WEB_URL`, `JIKU_PUBLIC_URL`, S3 de contenido, DSN de Postgres.
- CI de jiku-docs: tests, build de imagen Docker y publicación en Docker Hub (`gravadigital/jiku-docs`), con el
  mismo esquema que `jiku/.github/workflows/{ci,dev-images,release}.yml`. El repo de GitHub todavía no existe:
  se deja listo y se valida localmente (tests + `docker build`).

## D-8. Usuarios de prueba (keys en `jiku-docs/.local/`)

| Archivo | Rol | userId |
| --- | --- | --- |
| `jiku_docs.service_user.json` | `internal-app` + `docs` | `396604660191854594` |
| `testing_admin.service_user.json` | `admin` | `396554146460729346` |
| `testing_user.service_user.json` | `user` | `396554027241832450` |
| `testing_external_user.service_user.json` | `external-user` | `396554218317545474` |

## D-9. Importación desde HedgeDoc

- Dump MariaDB en `jiku-docs/.local/codimd-2026-10-24.sql` (HedgeDoc 1.10, tablas `Notes`, `Users`, …). Las
  imágenes están en un Space público (`grava-internal-01/docs-grava-io`) y se bajan por URL.
- El importador vive en jiku-docs (`jiku-docs import hedgedoc` o script Go): lee una base MariaDB local cargada con
  el dump, aplica una planilla de mapeo (CSV nota → carpeta; sin asignar → `/importado/`, solo admin), crea los
  documentos con `documents.new` como servicio, sube las imágenes como adjuntos (D-3), reescribe los enlaces y
  emite un informe de notas con posibles secretos. El dump nunca se commitea ni se copia fuera de `.local/`.

## D-10. Roles e identidad del caller desde core (seguimiento en jiku)

- jiku-docs clasifica al caller con `users.list {filter:{id}, include:["roles","identityType"]}`. Hoy
  `core/src/queries/users/users-spec.ts` excluye a propósito `roles` e `identityType` (CA-7: un externo podría
  enumerar quién es admin).
- Decisión: core los declara **incluibles solo para la clase `connector`** (roles `internal-app`, y por lo tanto
  `docs`). Para personas internas y externas siguen sin existir (`invalid_fields`), así CA-7 se mantiene. Ni filtro
  ni orden, solo `include`. Test que pruebe ambos lados.
- Va como REQ-017 de seguimiento en jiku. Los enlaces entre documentos usan la ruta que ya sirve la web,
  `{JIKU_WEB_URL}/docs/{id}`; el importador de HedgeDoc los reescribe a ese formato.

## D-11. La api manda el `actor` completo y jiku-docs usa sus roles

- Problema (prueba e2e en navegador): core actualiza los roles de una persona en `users` solo cuando esa persona ejecuta
  un comando de core o se conecta directo a NATS. Un usuario web que solo lee queda con roles viejos o sin fila, y
  jiku-docs le responde `caller_not_authorized` / `unknown_caller`.
- Decisión: la api manda a jiku-docs el mismo `actor` que manda a core (`buildActor`):
  `{ id, roles, name?, username?, email? }`, con `roles` tomados del token web ya verificado. Solo la api puede mandar
  `actor` (se identifica por `JIKU_API_USER_ID`). jiku-docs, cuando el caller es la api y el `actor` trae `roles`,
  clasifica a la persona con esos roles (`admin`/`user` → `person-internal`, `external-user` → `person-external`,
  otro → sin acceso) y no consulta `users.list` para ese dato. Los `project-permissions` del externo se siguen
  consultando a core.
- Contrato: el esquema `Actor` de los tres contratos de jiku-docs pasa a `{ id (req), roles (req, array de string),
  name?, username?, email? }`, `additionalProperties: false`.
  En `jiku-docs-events.yaml` el esquema `Actor` es otro: describe al autor de un evento (`{ id, name, via }`), no lo
  que manda la api, y no cambia; el `actor` de entrada solo existe en comandos y consultas.
- La api **no** acepta tokens de machine users (decisión del dueño, 2026-10-25): las pruebas HTTP se hacen con
  usuarios humanos vía navegador.
- Además: un `person-external` recibe el contenido de `documents.get` **sin frontmatter** (contiene ids internos de
  requisitos y tareas); opus-web muestra como texto los enlaces a documentos (`/docs/{id}`) en lugar de enlazar a la
  web interna.
