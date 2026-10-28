# Changelog

Formato: [Keep a Changelog](https://keepachangelog.com/es-ES/1.1.0/). Versionado: [SemVer](https://semver.org/lang/es/).

## Política de versionado

El servicio tiene un solo número de versión, el del tag `vX.Y.Z`. No está escrito en ningún archivo de
código: el binario lo recibe al compilar (`ARG VERSION` del `Dockerfile`) y `jiku-docs version` lo
imprime. Los contratos de `docs/apis/` tienen su propio `info.version`, que se mueve con cada cambio de
forma y no con las releases; el bus además lleva el segmento `v1` en los subjects, que es lo que permite
que consumidores viejos y nuevos convivan durante una migración.

| Bump      | Cuándo |
| --------- | ------ |
| **major** | Un cambio incompatible en el contrato del bus (comandos, consultas, eventos, notificaciones), en el protocolo de módulos, o una migración de datos destructiva. |
| **minor** | Funcionalidad nueva, compatible hacia atrás. |
| **patch** | Arreglos y cambios internos sin efecto en ningún contrato. |

El esquema de la base no se versiona aparte: las migraciones de `internal/store/migrations/` corren al
arrancar (`serve` o `migrate`) y se espera que sean aditivas.

### Cortar una release

Se mueve `[Unreleased]` a una sección `## [X.Y.Z] - AAAA-MM-DD`, se commitea, se lleva a `main` y se
taguea:

```sh
git tag vX.Y.Z && git push origin vX.Y.Z
```

Pushear el tag es lo que publica. `release.yml` verifica que el CHANGELOG tenga la sección del tag, corre
vet, lint y la suite completa, y recién entonces publica `gravadigital/jiku-docs` en Docker Hub con los
tags `X.Y.Z`, `X.Y` y `latest`. Un tag sin su sección falla antes de compilar nada. El paso a paso para
agentes está en [CLAUDE.md](CLAUDE.md#cortar-una-release).

## [Unreleased]

## [1.0.0] - 2026-10-27

Primera versión. Módulo opcional de documentación markdown para Jiku, que reemplaza a HedgeDoc. Atiende por el bus
NATS de Jiku, sin HTTP de negocio; los frontends llegan por la api de Jiku y las personas, agentes y servicios
directo al bus (cliente oficial: `jiku-go`). Contratos en `docs/apis/`: comandos, consultas y eventos en 1.3.0,
registro de módulos en 1.0.0.

### Added

- **Servicio `jiku-docs` en Go 1.25** con dos servicios NATS micro y queue group propio: `jiku-docs-commands`
  (19 comandos) y `jiku-docs-queries` (14 consultas). La identidad sale del subject, garantizada por el
  auth-callout; clases de caller `person-internal`, `person-external`, `service` y la api de Jiku con `actor`
  (`{ id, roles, name?, username?, email? }`).
- **Documentos** con frontmatter, versiones, locks de edición y borrado lógico: `documents.new` (sin carpeta: la
  ubicación la genera el servidor a partir de las relaciones y el título), `update`, `move`, `delete`, `restore`,
  `lock`/`unlock`, subida de contenido grande por URL prefirmada (`request-content-upload`), `relations.update`
  (reescribe solo el bloque `jiku:`, idempotente) y `suggest-relations` (hasta 5 proyectos o clientes sugeridos
  por título y contenido).
- **Lectura con la gramática de core**: filtros (escalar, lista, rango, un nivel de `or`), orden con `id` como
  desempate, paginación keyset con presupuesto de bytes, `fields`, `include`, `count` y `meta.describe` derivado
  de las mismas fichas que validan. Filtros `hasProject`, `hasClient` y `hasTopic`; `projectMatch`
  (`direct`/`indirect`) con `filter.projectId`; ítems con `clients`, `projects`, `createdByName`,
  `updatedByName`; `topics.list` con conteos.
- **Búsqueda full-text** en PostgreSQL (`es_unaccent`), un resultado por documento con el mejor fragmento.
- **Historial de versiones** (`documents.versions.list` con `kind`, `protected` y `authorName`) y **compactación
  diaria**: se conservan todas las versiones de menos de 24 h, la última de cada bloque de edición hasta 30 días y
  después la última de cada día. Nunca se compactan la actual, la creación, las restauraciones, las versiones con
  mensaje, las de servicios ni las fijadas por un link activo. Error `version_pruned` al pedir una compactada;
  `prunedVersionsCount` en `documents.get`. Comando `jiku-docs history compact [--document <id>] [--dry-run]`.
- **Papelera** en el bucket: todo objeto que el servicio borra pasa por `papelera/` (7 días por defecto) y se
  recupera con `jiku-docs storage restore-from-trash --key <clave> [--version-row]`. El bucket no usa versionado.
- **Adjuntos** delegados en el servicio de archivos de core (`attachments.*`), con link estable
  `{JIKU_WEB_URL}/docs/attachments/{id}`.
- **Links compartidos** de solo lectura (`shares.*`, token `jds_`, modos `latest` y `pinned`), HTML sanitizado,
  reintento idempotente de 5 min y auditoría de accesos con la IP en HMAC. Detector de secretos: aviso
  `possible_secret_detected` y confirmación obligatoria para compartir.
- **Eventos** por outbox transaccional hacia el stream JetStream `JIKU_DOCS_EVENTS` (`Nats-Msg-Id = eventId`):
  `document.*`, `attachment.*`, `share.*`, `folder.*`. **Notificaciones** por persona en
  `{instance}.{userId}.docs-notify.v1.*`, incluido `access.changed`. Feed de cambios `documents.changes` para
  sincronizar.
- **Nombres de autor** (`createdByName`, `updatedByName`, `authorName`, `lock.holderName`): los del actor o,
  si no los trae, los de `users.list` de core, con caché en memoria; si core no responde quedan `null` sin
  fallar la operación.
- **Registro en el KV `JIKU_MODULES`** (clave `docs`) al estar listo, y `jiku-docs module deregister` para
  desinstalar.
- **Importador de HedgeDoc** (`jiku-docs import hedgedoc`): planilla de mapeo a carpetas y relaciones, temas desde
  los `tags`, reescritura de enlaces entre notas, adjuntos, `--dry-run` y `--check-relations`, informe por nota y
  reimportación idempotente que no pisa lo editado en jiku-docs. Las notas sin mapeo entran `restricted`.
- **Operación**: subcomandos `serve`, `migrate`, `reindex`, `storage init`, `healthcheck` y `version`; logs JSON
  sin contenido, métricas Prometheus, `/healthz`, `/readyz` y `/metrics`. Indexador (durable
  `jiku-docs-indexer`), vencimiento de locks, purgas y revisión diaria de relaciones contra core.
- Imagen `gravadigital/jiku-docs` (distroless, sin root), `docker-compose.local.yml` sobre el stack local de Jiku
  y workflows de CI, imágenes dev y release.

### Security

- **Permisos por clase de caller y visibilidad**, deny-by-default. Las personas internas (`admin`, `user`) leen y
  escriben todo lo `internal` y `public`; un externo solo lee lo `public` vinculado a los proyectos de sus
  `project-permissions`. Lo que no se puede ver responde `*_not_found`, y nunca se concede acceso si core no
  responde (`upstream_unavailable`).
- **Visibilidad `restricted`**: solo `admin` y servicios. Para un `user` o un externo no existe por ninguna vía
  (`list`, `count`, `get` con y sin `version`, `search` y fragmentos, `changes`, `versions.list`, `topics.list`,
  `attachments.*`, `shares.*`, `docs-notify` ni `suggest-relations`). Solo `admin` o un servicio la ponen o la
  quitan. Pasar un documento a `restricted` revoca sus links, y un `restricted` no admite links
  (`sharing_blocked`).
- Un externo recibe los documentos sin frontmatter (tiene ids internos de requisitos y tareas) y no puede leer
  versiones anteriores, que podían ser de cuando el documento era `internal`.
- Ni contenido, ni tokens, ni URLs prefirmadas en logs o eventos.

### Deprecated

- **ACL de carpetas**: `folders.acl.replace`, `inheritAcl`, `include: [acl]`, `externalBlocked` y
  `sharingBlocked` siguen en el contrato pero no tienen efecto: los permisos los decide la visibilidad. Las
  carpetas solo ordenan los archivos (CLI y sync); `folders.*` es solo para `admin` y servicios.

### Notas para instalaciones nuevas

No hay instalaciones anteriores. Para instalar:

- **Requiere una versión de Jiku que todavía no está publicada**: la de la rama `feat/jiku-docs` del repo de
  Jiku (REQ-016 a REQ-022). Core tiene que exponer `roles` e `identityType` en `users.list` a los conectores y no
  purgar los `files` sin vínculo; el auth-callout necesita la regla `match: docs` con su plantilla; la api y la
  web traen las pantallas de documentación. Detalle en "Requisitos en Jiku" del README.
- **Zitadel**: crear el rol `docs` en el proyecto y un machine user (Access Token Type JWT) con los roles
  `internal-app` y `docs`; su JSON key va en `DOCS_SERVICE_USER_KEY_B64` (o `_FILE`).
- **Infraestructura propia**: un PostgreSQL 16 (`DATABASE_URL`; las migraciones corren solas al arrancar) y un
  bucket S3 para el contenido (en producción, un Space de DigitalOcean con `DOCS_S3_FORCE_PATH_STYLE=false`).
  Correr `jiku-docs storage init` una vez para crearlo y aplicar las reglas de ciclo de vida.
- **Variables obligatorias** para `serve`: `NATS_CREDS`, `ZITADEL_PROJECT_ID`, `DOCS_SERVICE_USER_KEY_B64` (o
  `_FILE`), `JIKU_API_USER_ID` (el `sub` del service user de la api de Jiku), `JIKU_WEB_URL`, `JIKU_PUBLIC_URL`,
  `DATABASE_URL`, `DOCS_S3_ENDPOINT`, `DOCS_S3_BUCKET`, `DOCS_S3_ACCESS_KEY`, `DOCS_S3_SECRET_KEY` y
  `DOCS_SECRET_KEY` (32 bytes en base64: `openssl rand -base64 32`). Todas, con las opcionales, en `.env.dist`.
- El KV `JIKU_MODULES` lo crea el despliegue de Jiku; sin él jiku-docs arranca igual pero la web y los clientes
  no lo ven instalado.
- La primera vez que jiku-docs se conecta a una base de Jiku nueva, core puede rechazar su primera consulta
  mientras crea la ficha del usuario de servicio; el arranque sigue y al reiniciar queda bien.
