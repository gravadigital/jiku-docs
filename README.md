# jiku-docs

Módulo de documentación de Jiku: guarda la documentación de Grava en markdown, con carpetas y
permisos heredables, versiones, vínculos con clientes, proyectos, requisitos y tareas de Jiku,
búsqueda por texto completo, locks de edición, notificaciones en tiempo real, links públicos de solo
lectura y adjuntos. Reemplaza a HedgeDoc.

Como `core`, no tiene HTTP de negocio: atiende request/reply en el bus NATS de Jiku, toma la identidad
del subject (garantizada por el auth-callout) y publica eventos en JetStream. Los frontends llegan por la
`api` de Jiku; personas, agentes y servicios llegan directo al bus (cliente oficial:
[`jiku-go`](https://github.com/gravadigital/jiku-go)).

- Especificación: [`docs/especificacion-v1.md`](docs/especificacion-v1.md)
- Decisiones de integración (mandan sobre la especificación): [`docs/decisiones-integracion.md`](docs/decisiones-integracion.md)
- Contratos AsyncAPI 3.0 (fuente de verdad de forma): [`docs/apis/`](docs/apis/)
- Cambios: [`CHANGELOG.md`](CHANGELOG.md)

## Arquitectura

```
        api de Jiku (actor) ─┐     personas / agentes (jiku-go)      servicios (internal-app)
                             ▼                 ▼                              ▼
                  NATS + auth-callout ── {instance}.{userId}.jiku-docs-{commands|queries}.v1.{método}
                             │
                             ▼
   jiku-docs ── micro jiku-docs-commands (18) / jiku-docs-queries (13), queue group propio
     │  ├─ ACL de carpetas, clases de caller, validación estricta, versionado, locks
     │  ├─ users / project-permissions / entidades / files.*  ──▶ core (jiku-queries, jiku-commands)
     │  ├─ PostgreSQL 16 propio (metadatos, changelog, índice es_unaccent)
     │  └─ bucket S3 propio (una versión por objeto, subidas grandes, papelera/)
     │
     ├─ outbox: changelog ─▶ JetStream JIKU_DOCS_EVENTS (Nats-Msg-Id = eventId)
     │                    └▶ {instance}.{userId}.docs-notify.v1.* (personas con read)
     ├─ indexador: durable jiku-docs-indexer sobre JIKU_DOCS_EVENTS
     ├─ janitor: vencimiento de locks, purgas, revisión diaria de relaciones
     └─ registro en el KV JIKU_MODULES (clave docs) al estar listo
```

| Paquete | Responsabilidad |
| --- | --- |
| `cmd/jiku-docs` | Binario y subcomandos (`serve`, `migrate`, `reindex`, `module deregister`, `storage init`, `storage restore-from-trash`, `history compact`, `import hedgedoc`, `healthcheck`) |
| `internal/app` | Cableado del proceso: stream, servicios micro, outbox, indexador, janitor, registro |
| `internal/bus` | Registro de los dos servicios NATS micro (patrón del `BusHost` de core) |
| `internal/service` | Lógica: 18 comandos, 13 consultas, ACL, clases de caller, eventos, purgas, revisión de relaciones |
| `internal/query` | Gramática de lectura de core: filtros, `or`, orden, keyset, `fields`, `include`, `count`, `meta.describe` |
| `internal/validate` | Decodificación deny-by-default (`invalid_fields` con `{field, value, allowed}`) |
| `internal/protocol` | Sobre de respuesta, catálogo de errores, gramática de subjects, inbox, ULID |
| `internal/caller`, `internal/acl` | Clases de caller y permisos efectivos heredables |
| `internal/coreapi` | Cliente de core (sobre `jiku-go`) con la caché de la especificación (60 s / 5 min) |
| `internal/markdown`, `internal/secrets` | Frontmatter, títulos, temas, fragmentos, HTML sanitizado, detector de secretos |
| `internal/store`, `internal/blob` | PostgreSQL (migraciones embebidas) y bucket S3 |
| `internal/outbox`, `internal/indexer`, `internal/janitor`, `internal/registry`, `internal/metrics` | Procesos de fondo y observabilidad |
| `internal/importer/hedgedoc` | Importador desde HedgeDoc (D-9) |
| `internal/contract`, `internal/testutil`, `test/integration` | Validación contra `docs/apis/` y entorno de integración |

## Requisitos en Jiku (para el alta)

jiku-docs no modifica Jiku; estos son los cambios que el repo de Jiku tiene que tener (rama `feat/jiku-docs`):

1. **Machine user `jiku_docs`** (Access Token Type JWT) con roles `internal-app` y `docs` (D-1).
2. **core: `users.list` debe exponer `roles` e `identityType`** como incluibles, al menos para callers de
   clase `connector`. Hoy la ficha de `users` no los declara y **sin ellos jiku-docs no puede clasificar a
   nadie** (toda llamada de persona falla con `upstream_unavailable`). jiku-docs pide
   `users.list { filter: { id }, include: ["roles", "identityType"] }`; los nombres son configurables con
   `CORE_USER_INCLUDES` (si core solo expone `roles`, usar `CORE_USER_INCLUDES=roles`: el tipo de identidad
   se deriva del rol `internal-app`/`core`, como hace core).
3. **core no debe purgar `files` sin vínculo** a una entidad de Jiku (los adjuntos de docs nunca se vinculan, D-3).
4. **auth-callout**: regla `match: docs` arriba de `internal-app` con plantilla `docs.yaml`. Además de lo de
   `connector.yaml`, jiku-docs necesita exactamente:

   | Para qué | Permiso |
   | --- | --- |
   | Atender los servicios micro | sub `{instance}.*.jiku-docs-commands.v1.>` y `{instance}.*.jiku-docs-queries.v1.>` (queue groups `jiku-docs-commands` / `jiku-docs-queries`), sub `$SRV.>`, `allow_responses` como `core.yaml` |
   | Eventos y notificaciones | pub `{instance}.docs-events.v1.>` y `{instance}.*.docs-notify.v1.>` |
   | Stream y consumidor del indexador | pub `$JS.API.INFO`, `$JS.API.STREAM.CREATE.JIKU_DOCS_EVENTS`, `$JS.API.STREAM.UPDATE.JIKU_DOCS_EVENTS`, `$JS.API.STREAM.INFO.JIKU_DOCS_EVENTS`, `$JS.API.CONSUMER.CREATE.JIKU_DOCS_EVENTS` y `.>`, `$JS.API.CONSUMER.DURABLE.CREATE.JIKU_DOCS_EVENTS.>`, `$JS.API.CONSUMER.INFO.JIKU_DOCS_EVENTS.>`, `$JS.API.CONSUMER.MSG.NEXT.JIKU_DOCS_EVENTS.jiku-docs-indexer`, `$JS.ACK.JIKU_DOCS_EVENTS.>` |
   | Registro de módulo | pub `$JS.API.STREAM.INFO.KV_JIKU_MODULES` y `$KV.JIKU_MODULES.docs` (escritura y borrado de su clave) |
   | Respuestas | sub `_INBOX.<hash(userId)>.>` (lo arma jiku-go) |

   Los lectores del registro (api, conectores, personas) necesitan los permisos que documenta
   [`docs/apis/jiku-modules.yaml`](docs/apis/jiku-modules.yaml).
5. **KV `JIKU_MODULES`** creado por el despliegue de Jiku (D-6). Si no existe, jiku-docs arranca igual y
   reintenta registrarse cada 30 s.
6. **api: `actor` completo** (D-11). La api manda a jiku-docs el mismo `actor` que a core (`buildActor`):
   `{ id, roles, name?, username?, email? }`, con los roles del token web verificado. jiku-docs clasifica a
   la persona con `actor.roles` (no consulta `users.list`, cuyo espejo de roles puede estar viejo) y un
   `actor` sin `roles` es `invalid_fields`. Un externo recibe `documents.get` sin frontmatter.

## Correr local contra el stack de Jiku

1. Levantar Jiku (`jiku/deploy/local.sh up`): crea la red `jiku-local_jiku`, NATS + auth-callout, MinIO y el KV.
2. Configurar:

   ```sh
   cp .env.dist .env
   # key del machine user, en base64 (la JSON key vive en .local/, nunca se commitea)
   echo "DOCS_SERVICE_USER_KEY_B64=$(base64 -w0 .local/jiku_docs.service_user.json)" >> .env
   echo "DOCS_SECRET_KEY=$(openssl rand -base64 32)" >> .env
   ```

   Revisar `JIKU_API_USER_ID` (userId de `jiku/local/api-su.json`), `ZITADEL_PROJECT_ID` y `NATS_INSTANCE`
   contra `jiku/deploy/.env`.
3. Levantar:

   ```sh
   docker compose -f docker-compose.local.yml up -d --build
   docker logs -f jiku-docs-local                       # "jiku-docs ready", "module registered"
   curl -s localhost:8090/readyz
   nats micro info jiku-docs-queries                    # con las creds de un usuario del bus
   ```

   El compose levanta un Postgres 16 propio (`127.0.0.1:5442`), corre `jiku-docs storage init` (crea
   `jiku-docs-content` sin versionado en el MinIO de Jiku, o lo suspende si estaba activado, y aplica las
   reglas de ciclo de vida) y el servicio, con `NATS_URL=nats://nats:4222` y
   las creds del sentinel de `../../jiku/deploy/nats/creds/sentinel-client.creds` (cambiar con
   `JIKU_DEPLOY_DIR`).
4. Probar con jiku-go: `jiku docs status`, `jiku docs ls /`, etc.

Sin Docker para el servicio: `go run ./cmd/jiku-docs` con las variables de `.env` exportadas y
`DATABASE_URL` apuntando al Postgres del compose (`docker compose ... up -d docs-database docs-storage-init`).

## Variables de entorno

Todas están documentadas en [`.env.dist`](.env.dist). Las obligatorias para `serve`:
`NATS_CREDS`, `ZITADEL_PROJECT_ID`, `DOCS_SERVICE_USER_KEY_B64` (o `_FILE`), `JIKU_API_USER_ID`,
`JIKU_WEB_URL`, `JIKU_PUBLIC_URL`, `DATABASE_URL`, `DOCS_S3_ENDPOINT`, `DOCS_S3_BUCKET`,
`DOCS_S3_ACCESS_KEY`, `DOCS_S3_SECRET_KEY` y `DOCS_SECRET_KEY` (32 bytes, base64).

En producción el bucket es un Space de DigitalOcean propio (`DOCS_S3_ENDPOINT=https://<región>.digitaloceanspaces.com`,
`DOCS_S3_FORCE_PATH_STYLE=false`), sin versionado y con las reglas de ciclo de vida de `uploads/` y `papelera/`
(`jiku-docs storage init` lo crea si la credencial lo permite y aplica las reglas; ver "Spaces" abajo).

## Operación

| Subcomando | Qué hace |
| --- | --- |
| `jiku-docs serve` (default) | Migra, crea/actualiza el stream, registra los servicios micro, arranca outbox, indexador y janitor y escribe la clave `docs` de `JIKU_MODULES` |
| `jiku-docs migrate` | Aplica migraciones (también lo hace `serve`; seguras con varias réplicas) |
| `jiku-docs reindex` | Regenera el índice de búsqueda completo (en proceso; no inyecta eventos en el stream) |
| `jiku-docs module deregister` | Borra la clave `docs` (desinstalar). Un apagado normal **no** la borra |
| `jiku-docs storage init` | Crea el bucket sin versionado (si lo tenía activado, lo suspende y lo avisa en el log), aplica las reglas de ciclo de vida (`uploads/` a 1 día, `papelera/` a `DOCS_TRASH_RETENTION_DAYS`) y las relee |
| `jiku-docs storage restore-from-trash --key <clave> [--version-row]` | Devuelve un objeto desde `papelera/` a su clave original (nunca pisa uno existente; la copia de la papelera queda hasta que vence). Con `--version-row` también vuelve a poner en el historial la versión compactada (ver "Papelera" abajo). Imprime una línea JSON con lo que hizo |
| `jiku-docs history compact [--document <id>] [--dry-run]` | Compacta el historial de versiones ahora (solo base y bucket, sin bus). `--dry-run` imprime una línea JSON por documento con cada versión, si se conserva y el motivo, y no toca nada. Si la corrida diaria tiene el lock, sale con error |
| `jiku-docs import hedgedoc` | Importador (ver abajo) |
| `jiku-docs healthcheck` | Consulta `/readyz` (HEALTHCHECK de la imagen) |

**HTTP de operación** (`DOCS_HTTP_ADDR`, default `:8080`): `/healthz`, `/readyz` (base y NATS) y `/metrics`
(Prometheus). La salud de negocio es la de NATS micro: `nats micro info jiku-docs-commands`.

**Métricas y alertas sugeridas**

| Métrica | Alerta |
| --- | --- |
| `jiku_docs_outbox_oldest_unpublished_seconds` | > 60 |
| `jiku_docs_indexer_lag_seconds` | > 300 |
| `jiku_docs_requests_total{code="upstream_unavailable"}` | tasa sostenida |
| `jiku_docs_request_duration_seconds`, `jiku_docs_locks_active`, `jiku_docs_notifications_total`, `jiku_docs_outbox_published_total` | — |
| `docs_history_last_success_timestamp_seconds` | más de 48 h atrás (la compactación falla o no corre); también `docs_history_runs_total{result="failure"}` |
| `docs_history_pruned_objects_oldest_seconds` | > 86400 (el paso a la papelera no avanza); `docs_history_pruned_objects_pending` da el volumen |
| `docs_trash_objects_moved_total{source}` | — (objetos movidos a `papelera/`: `compaction`, `orphans`, `deleted-documents`) |
| `docs_history_versions_pruned_total`, `docs_history_bytes_freed_total`, `docs_history_run_duration_seconds` | — |

**Logs**: JSON por stderr con método, caller, actor, `correlationId`, duración y `errorCode`. Nunca contenido,
tokens ni URLs prefirmadas.

**Tareas periódicas** (una sola réplica a la vez, por advisory lock): vencimiento de locks con
`document.unlocked` (cada 10 s), borrado de cifrados de reintento de shares (5 min), subidas vencidas,
adjuntos pendientes de más de 24 h, documentos borrados hace más de 30 días (sus objetos van a `papelera/`),
changelog de más de 30 días, objetos huérfanos (barrido semanal y paginado: una página de 1.000 objetos por
minuto, con el cursor en `service_state`; el contenido huérfano va a `papelera/`), accesos a links de más de 180 días y la revisión diaria
de relaciones contra core (`DOCS_RELATION_REVIEW_HOUR_UTC`), que quita relaciones a entidades
inexistentes reescribiendo el frontmatter en una versión nueva firmada por el servicio.

**Compactación del historial** ([`docs/compactacion-historial-v1.md`](docs/compactacion-historial-v1.md)):
cada guardado sigue creando una versión, y una vez por día, a partir de `DOCS_HISTORY_COMPACTION_HOUR` en
`DOCS_HISTORY_TIMEZONE`, se borran las versiones intermedias viejas: menos de 24 h, todas; hasta 30 días, la
última de cada bloque de edición (mismo autor, mismo `via`, pausas de menos de 30 min); más viejas, la última
de cada día. Nunca se borran la versión actual, la creación, las restauraciones, las versiones con `message`,
las de `via: service` ni las fijadas por un link `pinned` activo; las de agente siguen la regla de bloques
(para proteger una, guardarla con `--message`). Procesa documento por documento con `FOR UPDATE` y
transacciones cortas, y después del commit mueve los objetos a `papelera/` vía `pruned_objects` (copias en
paralelo y `DeleteObjects` en lotes). Procesa los documentos con actividad nueva, con un link fijado que
venció o se revocó, y los que tienen alguna versión no protegida que cruzó el umbral de 24 h o el de 30 días
desde su última compactación (así la sesión del día anterior se compacta en la corrida siguiente aunque nadie
vuelva a editar). Corre en una sola réplica (`pg_try_advisory_lock`), también frente a un `history compact` manual.
Los parámetros están en `.env.dist`; `DOCS_HISTORY_COMPACTION_ENABLED=false` la apaga.

**Papelera.** Todo objeto de contenido que el servicio borra pasa por `papelera/`: las versiones compactadas,
el contenido huérfano del barrido semanal y las versiones de los documentos purgados a los 30 días de
borrados. Mover es `CopyObject` a `papelera/<clave original>` (en paralelo) y después `DeleteObjects` de los
originales en lotes. Una fila de `pruned_objects` se elimina recién cuando el objeto quedó en la papelera y
el original ya no existe; si el proceso se corta entre la copia y el borrado, la siguiente corrida lo
retoma. Las subidas en tránsito (`uploads/`) y las páginas renderizadas (`renders/`) no son contenido y se
borran directo. La papelera no la barre nadie: la vacía su regla de ciclo de vida.

**Bucket y ciclo de vida.** `storage init` deja el bucket sin versionado y dos reglas de expiración por
tiempo: `uploads/` a 1 día y `papelera/` a `DOCS_TRASH_RETENTION_DAYS` (default 7). Relee las reglas y avisa
en el log si el proveedor no las guardó. Verificado contra MinIO (tests `TestStorageInit*`).

**Spaces.** Las dos reglas son expiraciones por tiempo bajo un prefijo, que DigitalOcean Spaces soporta
("Supported for time-based expiration and removing incomplete multipart uploads",
[S3 Compatibility](https://docs.digitalocean.com/products/spaces/reference/s3-compatibility/)). Ya no hace
falta `NoncurrentVersionExpiration`, que Spaces no confirma: el versionado del bucket dejó de ser la red de
seguridad. `DOCS_HISTORY_COMPACTION_ENABLED=true` es el default seguro también en producción. Si el bucket
tenía el versionado activado (lo activaba `storage init` antes de la papelera), `storage init` lo suspende
(`Suspended`) y lo avisa en el log; las versiones viejas que ya guardaba **no** se borran solas: cuando ya no
hagan falta, borrarlas a mano (por ejemplo `aws s3api list-object-versions` y `delete-object --version-id`
sobre las que no son actuales, o `mc rm --recursive --versions --non-current` en MinIO).

**Recuperar una versión compactada por error** (dentro de `DOCS_TRASH_RETENTION_DAYS`):

1. Encontrar la clave. Es `docs/<documentId>/<version>-<sha256>.md`; en la papelera está como
   `papelera/docs/<documentId>/...`. Por ejemplo, con el cliente de S3 que se use:
   `mc ls --recursive <alias>/jiku-docs-content/papelera/docs/<documentId>/` (el nombre empieza con el número
   de versión). Si la compactación es reciente, el log por documento (`--dry-run` o nivel debug) muestra qué
   versiones borró.
2. Devolverla, objeto y fila del historial, con un solo comando:

   ```sh
   jiku-docs storage restore-from-trash --key docs/<documentId>/<version>-<sha256>.md --version-row
   # {"key":"docs/...","objectRestored":true,"documentId":"...","version":3,"rowRestored":true}
   ```

   La copia de la papelera guarda la fila de la versión (autor, `via`, fecha, checksum, tamaño, título) como
   metadata del objeto, así que no hace falta escribir SQL a mano. El comando primero copia el objeto a su
   clave original (si ya existe no lo pisa) y después, en una transacción, inserta la fila en
   `document_versions` marcada `protected` (la próxima compactación no la vuelve a borrar) y descuenta
   `pruned_versions_count`. Se niega si el documento ya no existe o está borrado (restaurarlo primero), si la
   versión no es anterior a la actual o si esa versión ya existe con otro objeto. Es idempotente.
3. Sin `--version-row` devuelve solo el objeto: sirve para el contenido huérfano o de un documento purgado,
   que no tienen fila que restaurar. Un objeto devuelto sin fila vuelve a ser huérfano y el próximo barrido
   lo manda otra vez a la papelera.

**Respaldo**: backup diario de la base (30 días); el bucket no tiene versionado: la red de seguridad de lo
que borra el servicio es `papelera/`. Restaurar un backup anterior a una compactación puede dejar filas de
versiones cuyo objeto está en la papelera (devolverlo con `restore-from-trash` sin `--version-row`) o ya
venció (más de `DOCS_TRASH_RETENTION_DAYS`): al restaurar, verificar los objetos y descartar las filas sin
objeto. El índice no se respalda: `reindex`.

**Varias réplicas**: los comandos y consultas se reparten por queue group; outbox y janitor eligen líder
con advisory locks de PostgreSQL; el indexador comparte el durable.

## Importar desde HedgeDoc (D-9)

El dump nunca sale de `.local/` ni se commitea. El importador lee una base MariaDB cargada con el dump y
crea los documentos en proceso, como el servicio jiku-docs.

```sh
# 1. MariaDB temporal con el dump (puerto que no choca con nada de Jiku)
docker run -d --name jiku-docs-hedgedoc-db -e MARIADB_ROOT_PASSWORD=hedgedoc -e MARIADB_DATABASE=codimd \
  -p 127.0.0.1:33306:3306 mariadb:10.11
docker exec -i jiku-docs-hedgedoc-db mariadb -uroot -phedgedoc codimd < .local/codimd-2026-10-24.sql

# 2. Planilla de mapeo a completar (nota -> carpeta)
go run ./cmd/jiku-docs import hedgedoc --export-mapping .local/hedgedoc-mapping.csv

# 3. Simulación: plan + informe, sin escribir ni descargar nada
#    (--check-relations además valida las relaciones contra core por el bus, solo lectura)
go run ./cmd/jiku-docs import hedgedoc --mapping .local/hedgedoc-mapping.csv --dry-run

# 4. Importación real (variables de .env exportadas: base, bucket, bus)
go run ./cmd/jiku-docs import hedgedoc --mapping .local/hedgedoc-mapping.csv
```

- **Planilla**: CSV con columnas `note` (id, shortid o alias), `folder` (`/infra/servidores/`) y opcionales
  `fileName` y `skip`; las demás columnas se ignoran (sirven para revisar). Una fila sin `folder`, `fileName`
  ni `skip` no cuenta como mapeada. Las notas sin carpeta van a `/importado/`, que se crea sin herencia y con
  una única regla `manage` para el rol `admin`.
- **Relaciones y temas**: columnas opcionales `projects`, `clients`, `requirements` y `tasks` (ids de Jiku
  separados por `;` o `,`) y `topics` (separados por `;`). Con algún valor, la versión 1 sale con el bloque
  `jiku:` y los `topics` en el frontmatter, fusionados con lo que ya tenga (sin duplicar; temas
  normalizados como el servicio). Solas no seleccionan una nota: aplican a las que se importan. Si core
  responde `invalid_relation`, la nota se reintenta sin esa relación y el informe dice cuál se descartó y
  por qué (`droppedRelations`); con `--dry-run --check-relations` se ven antes de importar.
- **Qué se importa**: notas con contenido. Las anónimas (sin dueño) se omiten salvo que estén mapeadas en la planilla o se
  pase `--include-anonymous`: en el dump real son ~10.000 y casi todas spam. `--only-mapped` importa solo lo
  mapeado y `--limit N` sirve para probar.
- **Contenido**: frontmatter con `title`, `topics` (de los `tags` de HedgeDoc) y un bloque `hedgedoc` con id,
  shortid, alias, fechas, autor, último editor, permiso y las claves originales; fechas de creación y
  modificación originales; el autor original en el `message` de la versión 1.
- **Imágenes**: las URLs bajo `--asset-prefix` (default: el Space `grava-internal-01/docs-grava-io` y
  `docs.grava.io/uploads/`) se descargan y suben como adjuntos por el servicio de archivos de core (D-3); el
  markdown queda con `{JIKU_WEB_URL}/docs/attachments/{id}`.
- **Enlaces entre notas** (`{--hedgedoc-url}/{alias|shortid|id}`, `/s/…`, `/p/…`) se reescriben a
  `{JIKU_WEB_URL}/docs/{id}`; los que no resuelven quedan como estaban y se informan.
- **Idempotente**: ids deterministas (ULID con la fecha de la nota); reimportar no duplica. En una
  reimportación las notas ya importadas con el mismo contenido quedan `unchanged` y los adjuntos ya
  confirmados no se vuelven a descargar; solo se reintentan los que fallaron. Si el contenido cambió (por
  ejemplo, la planilla ahora trae relaciones), la nota se actualiza con `documents.{id}.update` sobre la
  versión actual (`updated`, mensaje "Reimportado de HedgeDoc…"), siempre que la última versión sea del
  importador; si alguien la editó, movió, renombró o borró en jiku-docs, queda `exists` y no se pisa.
- **Informe** (`--report`, default `.local/hedgedoc-import-report.csv`): estado por nota, ruta destino,
  adjuntos, enlaces sin resolver, relaciones y temas finales, relaciones descartadas y **posibles secretos** (regla y línea, nunca el valor). Esas notas fueron
  públicas en HedgeDoc: hay que rotar esas credenciales antes de apagarlo.

## Desarrollo

```sh
go build ./... && go vet ./... && golangci-lint run ./...
go test -race ./...            # unitarios, de contrato e integración (requiere Docker)
JIKU_DOCS_SKIP_INTEGRATION=1 go test ./...   # sin Docker
docker build -t gravadigital/jiku-docs:local .
```

Los tests de integración levantan PostgreSQL 16 y MinIO con testcontainers (contenedores
`jiku-docs-test-*`, puertos aleatorios), un `nats-server` embebido con JetStream y un doble estricto de
core, y validan **cada** pedido, respuesta, evento y notificación contra `docs/apis/`. Los tests de contrato
verifican además que las fichas de recursos (las que alimentan `meta.describe`) coinciden con el YAML.

`test/e2e/local-bus.sh` recorre el módulo de punta a punta por el bus real, con el stack local levantado y
el CLI `jiku` de jiku-go como admin, user y external-user (árbol y ACL, documentos, permisos, locks,
búsqueda, adjuntos, links, feed, sync y logs; `E2E_DISRUPTIVE=1` suma módulo caído y deregister). Las
variables y sus defaults están en su encabezado.

`jiku-go` se usa en su versión publicada (`v1.3.0`, desde el proxy de Go); no hace falta ningún `replace`.

CI (`.github/workflows/`): `ci.yml` (build, vet, lint, tests, `docker build`), `dev-images.yml` (imagen
`gravadigital/jiku-docs:dev` en cada push a `dev`) y `release.yml` (tags `vX.Y.Z` → `X.Y.Z`, `X.Y`,
`latest`; exige la sección del CHANGELOG). Secrets: `DOCKERHUB_USERNAME`, `DOCKERHUB_TOKEN`.
