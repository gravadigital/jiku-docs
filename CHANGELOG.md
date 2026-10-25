# Changelog

Formato: [Keep a Changelog](https://keepachangelog.com/es-ES/1.1.0/). Versionado: [SemVer](https://semver.org/lang/es/).

## [Unreleased]

### Security

- Un externo (`person-external`) recibe `documents.get` sin frontmatter, en `content` y en `contentUrl`
  (copia derivada bajo `renders/`): el frontmatter tiene ids internos de requisitos y tareas (D-11). En esa
  respuesta y en `documents.versions.list`, `checksum` y `sizeBytes` son los del cuerpo que recibe.
- Un externo ya no lee versiones anteriores de un documento público (`documents.get` con `version`, y
  `documents.versions.list`): podían ser de cuando era `internal` y tener lo que se quitó para publicarlo.

### Added

- Servicio `jiku-docs` en Go 1.25: servicios NATS micro `jiku-docs-commands` (18 comandos) y
  `jiku-docs-queries` (13 consultas) con queue group propio, identidad desde el subject, clases de caller
  (`person-internal`, `person-external`, `service`, la `api` con `actor`), ACL heredable por carpeta,
  validación deny-by-default y `*_not_found` para lo no visible.
- Gramática de lectura de core: filtros (escalar, lista, rango, un nivel de `or`), orden con `id` como
  desempate, paginación keyset con presupuesto de bytes, `fields`, `include`, `count` y `meta.describe`
  derivado de las mismas fichas que validan.
- PostgreSQL 16 propio con migraciones embebidas y búsqueda full-text `es_unaccent` (coincidencia por
  documento, mejor fragmento como snippet).
- Outbox (LISTEN/NOTIFY + barrido de 5 s) hacia JetStream `JIKU_DOCS_EVENTS` (creado idempotente,
  `Nats-Msg-Id = eventId`) y notificaciones `docs-notify` por persona con `read`.
- Indexador (durable `jiku-docs-indexer`), vencimiento de locks, purgas y revisión diaria de relaciones.
- Adjuntos delegados en el servicio de archivos de core (D-3); bucket S3 propio solo para contenido (D-4).
- Links compartidos con token `jds_`, reintento idempotente de 5 min (token cifrado), HTML sanitizado,
  auditoría de accesos con IP en HMAC.
- Detector de secretos (aviso `possible_secret_detected`, confirmación obligatoria para compartir).
- Registro en el KV `JIKU_MODULES` (clave `docs`) y `jiku-docs module deregister`.
- Subcomandos `migrate`, `reindex`, `storage init`, `healthcheck` e `import hedgedoc` (D-9).
- Logs JSON sin contenido, métricas Prometheus, `/healthz` y `/readyz`.
- Dockerfile (distroless, no root), `docker-compose.local.yml` sobre la red `jiku-local_jiku`, `.env.dist`
  y workflows de CI, imágenes dev y release (`gravadigital/jiku-docs`).
- Tests unitarios, de contrato y de integración (PostgreSQL 16 y MinIO reales, NATS embebido, doble de core).

### Changed (contratos 1.0.0 → 1.1.0)

- `jiku-docs-queries.yaml`
  - `meta.describe` acepta `resources` (como core): omitido describe todos, vacío o desconocido es `invalid_fields`.
  - Filtro `path` (exacto, sin distinguir mayúsculas) en `documents.list` y `folders.list`; en carpetas
    acepta la ruta con o sin `/` final y `"/"` trae la raíz.
  - Formato de rutas fijado: la `path` de una carpeta empieza y termina con `/`; la de un documento es la de
    su carpeta más el nombre de archivo.
  - `documents.changes`: `nextSeq` es el último seq examinado o `sinceSeq` si no hubo nada; `sinceSeq: 0`
    nunca responde `invalid_cursor`; cada item lleva `checksum`.
  - `shares.resolve` / `shares.attachment` aceptan `client: { ip, userAgent }` para la auditoría de accesos
    (la especificación pide IP en hash y agente de usuario, que solo la api conoce); un token mal formado
    responde `share_not_found`, como cualquier link inválido.
  - `folders.get` con `include: [acl]` sin `manage` responde `permission_denied`.
  - `page.limit` mayor a 200 se recorta en silencio (como core) en vez de ser inválido.
  - Notas: los externos no ven carpetas; `documents.search` devuelve un resultado por documento;
    `documents.get` con `version` devuelve los metadatos de esa versión.
  - `Actor` (D-11): `{ id, roles, name?, username?, email? }` (`id` y `roles` obligatorios), el mismo sobre
    que la api manda a core. La clase de la persona y las reglas ACL por rol salen de `actor.roles`; la api
    con `actor` ya no consulta `users.list` (un usuario web que solo lee tenía roles viejos en el espejo de
    core). Un `actor` sin `roles` es `invalid_fields`; con roles sin producto, `caller_not_authorized`
    (antes, un id sin fila en `users` era `unknown_caller`).
  - Nota: un externo recibe `documents.get` sin frontmatter; `checksum`/`sizeBytes` de esa respuesta son
    los del cuerpo. En `documents.list`, `documents.changes` y eventos, `checksum` identifica la versión guardada.
- `jiku-docs-commands.yaml`
  - `Actor` (D-11): mismo esquema que en consultas; autoría siempre `actor.id`.
  - `link` de adjuntos: `{JIKU_WEB_URL}/docs/attachments/{id}` (D-5).
  - Adjuntos delegados en core (D-3): `attachments_not_configured` ya no figura en las operaciones;
    se agregan `upstream_unavailable` y, en `confirm`, `permission_denied`.
  - `via` de servicios: siempre `service` en versiones y eventos; en `documents.lock` el `via` del lock se
    guarda tal como llega.
  - Subida de contenido: el `PUT` prefirmado no firma `Content-Type` (recomendado `text/markdown`).
  - `shares.new`: `version` con `mode: latest` es `invalid_fields`.
- `jiku-docs-events.yaml`
  - `docs-events` y `docs-notify` son subjects fijos, no derivan de `serviceName`.
  - `folder.updated` también se notifica como `access.changed`.
  - `changes.reason` (`released` / `forced` / `expired`) en `document.unlocked`; `changes.folderId` en
    `document.moved`; `snapshot.sharingBlocked` en eventos de carpeta.
- `jiku-modules.yaml`: `serviceName` solo nombra los servicios micro; permisos de lectura/observación del KV
  que necesitan los lectores.
- `especificacion-v1.md`: los links de adjuntos usan `JIKU_WEB_URL` (D-5).

### Decisiones de implementación

- Quien crea una carpeta sin herencia (o la apaga) conserva una regla `manage` explícita, para evitar
  carpetas inalcanzables.
- `documents.{id}.move` no crea versión nueva (el contenido no cambia); la respuesta trae la versión actual.
- Carpetas con borrado lógico (para que borrar dos veces sea `success`).
- El importador omite por defecto las notas anónimas de HedgeDoc (spam) salvo que estén mapeadas.
