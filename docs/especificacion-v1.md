# jiku-docs: especificación técnica v1

Oct 24, 2026 · @Lautaro Alvarez

## 1. Resumen, alcance y requisitos

`jiku-docs` es un servicio nuevo del ecosistema Jiku que almacena la documentación de Grava en markdown, con permisos por carpeta, vinculación con entidades de Jiku, búsqueda por texto completo y sincronización en tiempo real. Reemplaza a la instancia actual de HedgeDoc, que no tiene API y expone todos los documentos públicamente.

Sigue el mismo patrón que `core`: no tiene HTTP propio, atiende request/reply en el bus NATS de Jiku, toma la identidad del subject (garantizada por el auth-callout) y publica eventos en JetStream. Los frontends llegan a través de la `api` de Jiku; las personas (daemon de sincronización, agentes) y los servicios llegan directo al bus.

### Alcance de la v1

- Carpetas jerárquicas con permisos heredables.
- Documentos markdown versionados, con frontmatter que los vincula a clientes, proyectos, requisitos y tareas de Jiku.
- Adjuntos en S3 enlazados desde el markdown.
- Catálogo indexado: búsqueda por texto completo, filtrable por metadatos.
- Sesiones de edición con lock y control de versión, para trabajo asistido por agentes.
- Notificaciones en tiempo real por persona y feed de cambios para reconciliar.
- Links públicos de solo lectura para compartir un documento con terceros, con vencimiento y revocación.
- Comandos de documentación y daemon de sincronización dentro de jiku-go, usado por personas y agentes.
- Importación desde HedgeDoc.

### Fuera de alcance de la v1

- Edición colaborativa simultánea (CRDT). El modelo es un editor y muchos lectores en vivo; la arquitectura permite sumarla después.
- Formatos que no sean markdown en el árbol de documentos.
- Almacenamiento de secretos. Los documentos describen accesos, pero las credenciales viven en un gestor de secretos.
- Búsqueda semántica (embeddings). La búsqueda es por texto completo; el contrato ya prevé los modos `semantic` e `hybrid` para agregarla sin cambios incompatibles.
- Adaptador WebDAV.

### Requisitos funcionales

| Id | Requisito |
| --- | --- |
| RF-01 | Toda operación está autenticada por la identidad de Jiku (Zitadel vía auth-callout), salvo la lectura por link compartido |
| RF-02 | Permisos de lectura, escritura y administración por carpeta, heredados a subcarpetas |
| RF-03 | Un documento vive en una carpeta y puede vincularse con varios clientes, proyectos, requisitos y tareas |
| RF-04 | Documentos transversales (infraestructura, interno, buenas prácticas) conviven con documentos de cliente |
| RF-05 | Solo se aceptan archivos `.md` en el árbol de documentos |
| RF-06 | Adjuntos en S3, enlazados desde el markdown, con los permisos del documento |
| RF-07 | Historial de versiones con autor, fecha y origen (persona, agente o servicio) |
| RF-08 | Un agente puede abrir, editar y guardar un documento en nombre de la persona con la que trabaja |
| RF-09 | Quien tiene un documento abierto o sincronizado recibe los cambios en segundos |
| RF-10 | Búsqueda por contenido, proyecto, cliente, tema, fecha y autor, respetando permisos |
| RF-11 | Los clientes externos pueden ver documentos públicos de los proyectos que ya ven en Jiku |
| RF-12 | Jiku puede listar los documentos vinculados a un proyecto, requisito o tarea |
| RF-13 | Quien administra una carpeta puede generar un link público de solo lectura de un documento, con vencimiento obligatorio, y revocarlo en cualquier momento |
| RF-14 | El módulo es opcional: Jiku descubre si está instalado y disponible, y habilita o deshabilita cada funcionalidad de documentación en la api, la web y los conectores sin cambios de configuración |

### Requisitos no funcionales

| Id | Requisito |
| --- | --- |
| RNF-01 | Latencia p95 de consultas simples menor a 300 ms; búsqueda por texto menor a 500 ms |
| RNF-02 | Una notificación de cambio llega a los clientes conectados en menos de 3 s |
| RNF-03 | Ningún cambio confirmado se pierde: outbox para eventos y feed de cambios reconciliable |
| RNF-04 | Comandos seguros de reintentar (idempotencia por id y versión base) |
| RNF-05 | Deny-by-default en permisos, campos de filtro y claves de payload, como el resto de Jiku |
| RNF-06 | Escala objetivo: 30 usuarios, 20.000 documentos, documentos de hasta 5 MiB |
| RNF-07 | Contratos AsyncAPI como fuente de verdad, validados en tests |

## 2. Arquitectura

`jiku-docs` es un proceso Go con dos servicios NATS micro (`jiku-docs-commands` y `jiku-docs-queries`), una base PostgreSQL propia y dos buckets S3. No accede a la base de Jiku: todo lo que necesita de Jiku lo consulta a `core` por `jiku-queries`.

```
   web / opus-web            jiku (CLI y sync)            servicios de Jiku
        │ HTTP              (persona con su token)       (rol internal-app)
        ▼                             │                           │
   api de Jiku ── actor ──┐           │                           │
                          ▼           ▼                           ▼
                 NATS + auth-callout (identidad en el subject) ◀─────────────┐
                          │                       │                          │
                          ▼                       ▼                          │
                        core               jiku-docs ── outbox ──▶ JIKU_DOCS_EVENTS
                          │             │      │      │                    │
                    PostgreSQL Jiku     │      │      └─▶ docs-notify        ▼
                                        ▼      ▼          (por persona)   indexador
                              Postgres docs   S3 docs / S3 adjuntos
```

### Componentes

| Componente | Responsabilidad | Tecnología |
| --- | --- | --- |
| `jiku-docs` (servicio) | Comandos y consultas, autorización, versionado, locks, validación de markdown y frontmatter, outbox | Go, NATS micro |
| Publicador de outbox | Lee el changelog confirmado y publica en `JIKU_DOCS_EVENTS` y en `docs-notify` de cada persona autorizada | Go, mismo binario |
| Indexador | Consume `JIKU_DOCS_EVENTS`, fragmenta el texto y mantiene el catálogo | Go, mismo binario o proceso aparte |
| Postgres docs | Árbol, ACL, documentos, versiones, relaciones, changelog, índice (full-text) | PostgreSQL 16 |
| S3 docs | Contenido de cada versión de cada documento | Bucket con versionado activado |
| S3 adjuntos | Bytes de los adjuntos | Bucket aparte, sin acceso público |
| `jiku-go` | Librería y CLI existentes; suman el grupo `jiku docs`, el daemon `jiku docs sync` y los paquetes `modules` y `modules/docs` | Go |
| api de Jiku | Rutas HTTP para los frontends, descubrimiento del módulo y redirección de adjuntos | Existente, se extiende |

### Decisiones de diseño

1. **El storage es la fuente de verdad del contenido; Postgres la de los metadatos.** Cada versión es un objeto inmutable en S3. El índice es derivado y se puede regenerar.
2. **El frontmatter es la fuente de verdad de las relaciones, temas y visibilidad.** Se parsea en cada escritura. Así un documento editado localmente puede vincularse a un proyecto sin usar otra herramienta.
3. **La identidad la pone el subject.** `jiku-docs` no valida tokens; confía en el auth-callout, igual que `core`.
4. **Los comandos son idempotentes.** Ids generados por el cliente (ULID) y versión base obligatoria en las modificaciones.
5. **Los eventos no se pierden.** Se escriben en el changelog en la misma transacción del comando y se publican desde ahí (outbox).
6. **Dos canales de eventos.** Stream completo solo para servicios; notificaciones filtradas por permisos para personas.
7. **Un editor, muchos lectores.** Lock por sesión con vencimiento; los demás ven los cambios en vivo.

8) **El módulo es opcional.** Jiku no guarda datos de documentación y descubre el módulo por un registro en el bus; sin él, Jiku funciona igual (sección 13).

## 3. Modelo de dominio

Cada documento tiene una **ubicación** (la carpeta, que define quién lo ve) y **relaciones** (de qué trata, que nunca dan acceso). Esto permite documentos transversales sin duplicarlos.

### Entidades

| Entidad | Descripción | Identificador |
| --- | --- | --- |
| Carpeta | Nodo del árbol. Tiene reglas de acceso, decide si hereda las del padre y si permite clientes externos (`externalBlocked`) y links compartidos (`sharingBlocked`) | ULID |
| Documento | Archivo markdown dentro de una carpeta, con versión actual y metadatos derivados del frontmatter | ULID |
| Versión | Contenido inmutable de un documento en un momento, con autor y origen | `documentId` + número correlativo |
| Relación | Vínculo de un documento con un cliente, proyecto, requisito o tarea de Jiku | Id numérico de Jiku + tipo |
| Tema | Etiqueta libre normalizada (`infra`, `accesos`, `reuniones`) | Slug |
| Adjunto | Archivo binario en S3, perteneciente a un documento | ULID |
| Sesión de edición | Lock temporal de un documento por una persona, opcionalmente asistida por un agente | `sessionId` (ULID) |
| Link compartido | Credencial al portador que da lectura anónima a un solo documento, con vencimiento | ULID (el token secreto solo se guarda como hash) |

### Árbol de carpetas recomendado

```
/interno/                procesos, administración, equipo
/infra/                  servidores, accesos, runbooks
/buenas-practicas/       estándares, Grava Workflow
/clientes/<cliente>/<proyecto>/
    reuniones/           resúmenes de reuniones
    tecnico/             arquitectura, decisiones, integraciones
```

El árbol se organiza por quién debe ver, no por tema. Un documento sobre el servidor de staging vive en `/infra/` y se relaciona con los proyectos que lo usan; al buscar "documentos del proyecto X" aparece junto con los de `/clientes/<cliente>/X/`, siempre que la persona pueda leer `/infra/`.

### Visibilidad

Cada documento tiene `visibility`: `internal` (default) o `public`, con el mismo significado que `visibilityLevel` en Jiku. Solo los documentos `public` pueden llegar a clientes externos (sección 4). Marcar un documento como `public` exige permiso `manage` sobre su carpeta.

### Formato del documento

UTF-8, fin de línea LF (el servicio normaliza CRLF), tamaño máximo 5 MiB. Nombre de archivo: `^[^/\\]{1,200}\.md$`, único por carpeta sin distinguir mayúsculas.

El frontmatter YAML es opcional:

```markdown
---
title: Acceso al servidor de staging
visibility: internal
topics: [infra, accesos]
jiku:
  clients: [4]
  projects: [12, 31]
  requirements: [812]
  tasks: []
---

# Acceso al servidor de staging
...
```

Reglas de parseo:

- `title`: si falta, se toma el primer encabezado `#`; si tampoco hay, el nombre de archivo sin extensión.
- `visibility`: `internal` o `public`. Otro valor es error `frontmatter_invalid`.
- `topics`: lista de strings; se normalizan a kebab-case sin acentos. Los temas se crean al usarse.
- `jiku`: bloque estricto. Solo acepta `clients`, `projects`, `requirements` y `tasks`, con listas de ids numéricos. Cada id se valida contra `core`; uno inexistente o no visible para quien escribe es error `invalid_relation`.
- Otras claves de primer nivel se conservan en el contenido y se ignoran.

Como el frontmatter es parte del contenido, cambiar relaciones, temas o visibilidad es una actualización normal del documento y genera versión.

### Enlaces a adjuntos

Los adjuntos se enlazan con una URL estable de la web de Jiku (D-5: `{JIKU_WEB_URL}/docs/attachments/{id}`, que la web atiende con su sesión y reenvía a la `api`), nunca con la URL de S3:

```markdown
![Diagrama de red]({JIKU_WEB_URL}/docs/attachments/01JBX7Q2M4N8P6R3T5V9W1Y0ZA)
```

La `api` resuelve esa ruta verificando permisos y redirige a una URL prefirmada de corta duración (sección 9).

## 4. Identidad y autorización

`jiku-docs` no valida tokens: el `user-id` del subject es la identidad, y el auth-callout garantiza que nadie publique bajo un id ajeno. Sobre esa identidad, el servicio decide permisos con su propia ACL y con datos que consulta a `core`.

### Clases de caller

| Clase | Cómo se reconoce | Identidad efectiva | Lectura | Escritura |
| --- | --- | --- | --- | --- |
| `person-internal` | `users.identity_type = person` y rol interno | El `user-id` del subject | Según ACL de carpetas | Según ACL de carpetas |
| `person-external` | `users.identity_type = person` y rol de cliente | El `user-id` del subject | Solo documentos `public` vinculados a proyectos visibles en `project-permissions` | Nunca |
| `api` | El `user-id` es el de la `api` de Jiku | La persona declarada en `actor` | Según la clase de esa persona | Según la clase de esa persona |
| `service` | `identity_type = service` con rol `internal-app` | El `user-id` del subject | Todo, sin recorte | Todo; queda atribuido al servicio |

Mapeo de roles de Zitadel a clases: `admin` y `user` son `person-internal`; `external-user` es `person-external`; `internal-app` es `service`. Cualquier otro rol (`core`, `bus-observer`) no tiene acceso a `jiku-docs`.

Reglas de `actor`, iguales a las de `core`:

- Solo la `api` puede mandar la clave `actor`. Si la manda cualquier otro caller: `invalid_fields`.
- Si la `api` no manda `actor`, la llamada se trata como del servicio `api`.
- Los campos de autoría del payload no existen: el autor siempre es la identidad efectiva.

### Datos que se consultan a core

| Dato | Consulta | Caché |
| --- | --- | --- |
| Roles e `identity_type` del caller | `users.list` con `filter.id` | 60 s por usuario |
| Proyectos visibles para un externo | `project-permissions.list` con `filter.userId` | 60 s por usuario |
| Existencia de entidades en relaciones | `clients.list`, `projects.list`, `requirements.list`, `tasks.list` con `filter.id` | 5 min |

Si `core` no responde, las operaciones que dependen de esos datos fallan con `upstream_unavailable` (nunca se concede acceso por defecto). Los datos en caché se siguen usando hasta su vencimiento.

Como `core` no publica eventos de clientes ni proyectos, una tarea diaria revisa las relaciones de todos los documentos contra `core` y quita las que apuntan a entidades que ya no existen, registrándolo en el historial del documento.

Un usuario sin fila en `users` (por el problema de espejo descrito en la guía de conectores, §3.4) recibe `unknown_caller`.

### ACL de carpetas

Cada regla es: carpeta + principal + permiso.

| Campo | Valores |
| --- | --- |
| `principalType` | `role` (un rol de Zitadel, tal como figura en `users.roles`) o `user` (un `user-id`) |
| `principal` | El rol o el `user-id` |
| `permission` | `read`, `write` (incluye read) o `manage` (incluye write, permite cambiar ACL, mover carpetas y publicar) |

Permiso efectivo de una persona sobre una carpeta:

1. Se juntan las reglas de la carpeta que coinciden con la persona (por `user-id` o por cualquiera de sus roles).
2. Si la carpeta tiene `inheritAcl = true`, se suman las reglas efectivas de la carpeta padre, recursivamente.
3. El permiso efectivo es el máximo encontrado. Sin reglas, no hay acceso.

No existen reglas de denegación: para restringir una subcarpeta se pone `inheritAcl = false` y se definen sus reglas explícitas. La carpeta raíz tiene `inheritAcl = false` y una regla `manage` para el rol admin.

### Reglas de visibilidad en lecturas

- Un documento o carpeta que la persona no puede leer responde `*_not_found`, sin distinguir "no existe" de "no es visible", igual que `core`.
- Una escritura sobre algo que se puede leer pero no escribir responde `permission_denied`.
- Los resultados de `list`, `search` y `changes` se filtran por permisos antes de paginar.

### Clientes externos

Un usuario externo ve un documento si se cumplen las tres condiciones: `visibility = public`, el documento tiene una relación con un proyecto (o un requisito o tarea de un proyecto) que aparece en sus `project-permissions`, y la carpeta del documento no tiene la marca `externalBlocked = true`. Esa marca permite cerrar carpetas enteras (por ejemplo `/infra/`) a clientes aunque alguien publique un documento por error.

### Links compartidos

Un link compartido es una credencial al portador: quien tiene el link lee el documento sin identificarse. Por eso es la única lectura que no pasa por el bus; la resuelve la `api` de Jiku en una ruta HTTP pública (sección 9.8).

Convive con el acceso de clientes con cuenta, pero con otro propósito. Para clientes que tienen usuario en opus-web, lo correcto es `visibility: public` vinculado a sus proyectos: queda atado a su identidad y se revoca con su acceso. El link es para casos puntuales: alguien sin cuenta, un proveedor, un documento que se envía una vez.

| Regla | Definición |
| --- | --- |
| Quién crea | Permiso `manage` sobre la carpeta del documento |
| Quién revoca | `manage` sobre la carpeta, o quien creó el link |
| Carpetas bloqueadas | `sharingBlocked = true` impide crear links en esa carpeta y sus descendientes (se hereda; no se puede desactivar en una subcarpeta de una bloqueada). Recomendado en `/infra/` e `/interno/` |
| Token | 256 bits aleatorios en base64url con prefijo `jds_` (permite detectarlo con escáneres de secretos). Se devuelve una sola vez; se guarda solo su SHA-256 |
| Modo | `latest` muestra siempre la versión actual; `pinned` muestra una versión fija |
| Vencimiento | Obligatorio. Default 30 días, máximo 180 |
| Adjuntos | Incluidos por defecto; desactivables por link. Solo se sirven adjuntos del documento compartido |
| Qué se muestra | Solo el cuerpo renderizado a HTML sanitizado. El frontmatter se quita siempre (contiene ids de Jiku y temas internos). Los enlaces a otros documentos se muestran como texto |
| Invalidación | Revocar, vencer, borrar el documento o bloquear la carpeta corta el link en el siguiente acceso. Borrar el documento revoca sus links: restaurarlo no los reactiva |
| Secretos | Si el detector de secretos marcó el documento, `shares.new` exige `acknowledgeSecretWarning: true` |
| Auditoría | Cada link registra creador, fecha, contador de accesos y último acceso. Cada acceso se guarda con IP hasheada y agente de usuario truncado |

Un link inválido por cualquier motivo responde lo mismo (`share_not_found`, y 404 en la ruta pública), para no revelar si existió, venció o fue revocado.

## 5. Protocolo en el bus

El protocolo copia las convenciones de `core` (gramática de subjects, forma de la respuesta, filtros y paginación) para que quien ya integró Jiku no tenga que aprender nada nuevo. Las diferencias son tres: idempotencia, outbox y transferencia de contenido.

### Subjects

```
Comandos:       {instance}.{user-id}.jiku-docs-commands.v1.{método}
                prod.281234567890123456.jiku-docs-commands.v1.documents.01JBX7Q2M4.update
Consultas:      {instance}.{user-id}.jiku-docs-queries.v1.{método}
                prod.281234567890123456.jiku-docs-queries.v1.documents.search
Eventos:        {instance}.docs-events.v1.{entidad}.{acción}
                prod.docs-events.v1.document.updated
Notificaciones: {instance}.{user-id}.docs-notify.v1.{entidad}.{acción}
                prod.281234567890123456.docs-notify.v1.document.updated
```

| Segmento | Valor |
| --- | --- |
| `instance` | `dev` / `prod`, igual que `NATS_INSTANCE` de Jiku |
| `user-id` | El `sub` del token del caller, crudo |
| `version` | `v1`. Comandos y consultas se versionan juntos; eventos y notificaciones, por separado |
| `método` | En comandos, los ids van en el subject (`documents.{id}.update`). En consultas, en el payload |

Los ids ULID son válidos como token de subject (26 caracteres alfanuméricos en mayúscula).

### Conexión de los callers

Igual que con `core`: credenciales del sentinel, token de Zitadel con refresco en caliente e `inboxPrefix` derivado del `user-id` (`_INBOX.` + primeros 16 caracteres de `lowercase(base32_sin_padding(sha256(userId)))`). Sin `inboxPrefix` las respuestas no llegan y el síntoma es un timeout.

### Respuesta

```jsonc
{ "status": "success", "data": { ... } }
{ "status": "failure", "errorCode": "version_conflict", "errorMessage": "...", "errorDetails": { "currentVersion": 8 } }
```

Se decide siempre por `errorCode`; `errorMessage` es texto en español para la interfaz.

### Catálogo de errores

| Código | Significado | Reintento |
| --- | --- | --- |
| `invalid_fields` | Clave desconocida, campo no declarado en filtro, orden o fields, o `actor` enviado por un caller que no es la `api` | No: bug del caller |
| `invalid_cursor` | Cambió `filter` o `sort` entre páginas | Reiniciar paginación |
| `document_not_found` / `folder_not_found` / `attachment_not_found` | No existe o no es visible | No |
| `permission_denied` | Se puede leer pero no escribir o administrar | No |
| `version_conflict` | `baseVersion` distinta de la actual. `errorDetails.currentVersion` | Releer, combinar y reintentar |
| `document_locked` | Otra sesión tiene el lock. `errorDetails`: `holder`, `via`, `expiresAt` | Esperar o pedir liberación |
| `lock_not_held` | La sesión no tiene el lock que intenta renovar o liberar | Volver a tomarlo |
| `already_exists` | Un `id` de cliente ya usado con otro contenido, o nombre de archivo repetido en la carpeta | No |
| `file_name_invalid` | Nombre que no termina en `.md` o con caracteres prohibidos | No |
| `frontmatter_invalid` | YAML mal formado o valor inválido. `errorDetails`: `line`, `field`, `reason` | No |
| `invalid_relation` | Id de Jiku inexistente o no visible. `errorDetails`: `entityType`, `entityId` | No |
| `content_too_large` | Más de 5 MiB, o más de 256 KiB inline | Usar subida por URL |
| `checksum_mismatch` | El SHA-256 declarado no coincide con el contenido | Recalcular |
| `upload_not_found` | `contentUploadId` vencido o inexistente | Pedir otra URL |
| `folder_not_empty` | Se intenta borrar una carpeta con contenido | No |
| `attachment_type_not_allowed` / `attachment_too_large` | Política de adjuntos | No |
| `query_timeout` | La consulta superó el tiempo de la base | Achicar el filtro |
| `unknown_caller` / `caller_not_authorized` | Sin fila en `users` o rol sin permiso | Reconectar |
| `upstream_unavailable` | `core` no respondió una consulta necesaria | Reintentar con backoff |
| `internal_error` | Cualquier otra cosa | Reintentar con backoff |

El catálogo puede crecer dentro de `v1`: un código desconocido se trata como error genérico.

Errores de links compartidos:

| Código | Significado | Reintento |
| --- | --- | --- |
| `share_not_found` | Link inexistente, revocado, vencido, de un documento borrado o de una carpeta bloqueada (no se distingue) | No |
| `sharing_blocked` | La carpeta o un ancestro tiene `sharingBlocked` | No |
| `share_expiry_invalid` | `expiresAt` en el pasado o más allá del máximo (180 días) | Corregir la fecha |
| `secret_warning_not_acknowledged` | El documento tiene una alerta de posible secreto y no se envió `acknowledgeSecretWarning: true` | Revisar y confirmar |

Errores de configuración del módulo (sección 13):

| Código | Significado | Reintento |
| --- | --- | --- |
| `semantic_search_not_configured` | `documents.search` con `mode` `semantic` o `hybrid` (no disponibles en la v1) | Usar `mode: text` |
| `attachments_not_configured` | Operación de adjuntos sin bucket configurado | No |

### Timeouts y límites

| Parámetro | Valor |
| --- | --- |
| Timeout recomendado para comandos | 5 s |
| Timeout recomendado para consultas | 10 s (la base corta a 8 s con `query_timeout`) |
| Contenido inline en comandos y respuestas | Hasta 256 KiB |
| Tamaño máximo de un documento | 5 MiB |
| URLs prefirmadas | Vencen a los 300 s |
| `page.limit` | Default 50, máximo 200 |

Por encima de 256 KiB el contenido viaja por S3: el comando `documents.request-content-upload` devuelve una URL de subida y un `contentUploadId`, y `documents.get` devuelve `contentUrl` en lugar de `content`.

### Idempotencia

A diferencia de `core`, todos los comandos de `jiku-docs` se pueden reintentar tras un timeout:

| Comando | Mecanismo |
| --- | --- |
| Altas (`folders.new`, `documents.new`) | El cliente genera el `id` (ULID). Repetir con el mismo `id` y el mismo contenido devuelve el recurso existente; con otro contenido, `already_exists` |
| Modificaciones de documento | `baseVersion` obligatoria. Si el contenido enviado es idéntico a la versión actual (mismo SHA-256), responde `success` con esa versión aunque `baseVersion` haya quedado vieja |
| Locks | Tomar un lock que la misma sesión ya tiene lo renueva |
| Bajas | Borrar algo ya borrado responde `success` |

### Filtros, orden y paginación

Misma gramática que `jiku-queries`: `filter` (con operadores `gte`, `lte` y un solo nivel de `or`), `sort` (con `-` para descendente, siempre se agrega `id`), `page` con cursor keyset, `fields`, `include` y `count`. La ausencia de `page.cursor` es la única señal de fin. `meta.describe` devuelve los campos, filtros, órdenes y enums de cada recurso.

## 6. Comandos (`jiku-docs-commands`)

18 comandos. Todos responden con `{ status, data }`; en altas y modificaciones `data` trae como mínimo `id` y, en documentos, `version`. El detalle de cada payload está en el contrato `jiku-docs-commands.yaml`; los esquemas rechazan claves desconocidas.

### Carpetas

| Método | Payload | Respuesta | Permiso | Errores específicos |
| --- | --- | --- | --- | --- |
| `folders.new` | `id`, `parentId`, `name`, `inheritAcl?` (default `true`), `externalBlocked?`, sharingBlocked? | `{ id, path }` | `manage` en el padre | `folder_not_found`, `already_exists` |
| `folders.{id}.edit` | `name?`, `parentId?` (mover), `inheritAcl?`, `externalBlocked?`, sharingBlocked? | `{ id, path }` | `manage` en la carpeta y, si se mueve, en el destino | `folder_not_found`, `already_exists`, `invalid_fields` (mover dentro de sí misma) |
| `folders.{id}.delete` | vacío | `{ id }` | `manage` | `folder_not_empty` |
| `folders.{id}.acl.replace` | `rules: [{ principalType, principal, permission }]` (máx. 100) | `{ id, rules }` | `manage` | `invalid_fields` |

### Documentos

| Método | Payload | Respuesta | Permiso | Errores específicos |
| --- | --- | --- | --- | --- |
| `documents.new` | `id`, `folderId`, `fileName`, `content` o `contentUploadId`, `checksum`, `via?`, `message?` | `{ id, version: 1, path }` | `write` en la carpeta | `already_exists`, `file_name_invalid`, `frontmatter_invalid`, `invalid_relation`, `content_too_large`, `checksum_mismatch`, `upload_not_found` |
| `documents.{id}.update` | `baseVersion`, `content` o `contentUploadId`, `checksum`, `sessionId?`, `via?`, `message?` | `{ id, version }` | `write` | `version_conflict`, `document_locked`, `frontmatter_invalid`, `invalid_relation`, `content_too_large`, `checksum_mismatch` |
| `documents.{id}.move` | `baseVersion`, `folderId?`, `fileName?` | `{ id, version, path }` | `write` en origen y destino | `version_conflict`, `document_locked`, `already_exists`, `file_name_invalid` |
| `documents.{id}.delete` | `baseVersion` | `{ id }` | `write` | `version_conflict`, `document_locked` |
| `documents.{id}.restore` | `fromVersion`, `baseVersion` | `{ id, version }` | `write` | `version_conflict`, `document_not_found` (versión inexistente) |
| `documents.{id}.lock` | `sessionId`, `via` (`human` / `agent`), `ttlSeconds?` (30 a 300, default 120) | `{ id, sessionId, expiresAt }` | `write` | `document_locked` |
| `documents.{id}.unlock` | `sessionId`, `force?` | `{ id }` | `write`; `force` exige `manage` | `lock_not_held` |
| `documents.request-content-upload` | `checksum`, `size` | `{ contentUploadId, uploadUrl, expiresIn }` | Cualquier persona interna o servicio | `content_too_large` |

Notas:

- **`via`** registra el origen del cambio: `human`, `agent` o `service`. Para servicios se fuerza `service`. No afecta permisos; es trazabilidad.
- **`delete`** es lógico: el documento deja de listarse, se conservan sus versiones y se puede restaurar con `documents.{id}.restore` por 30 días; después se elimina definitivamente junto con sus versiones.
- **`restore`** crea una versión nueva con el contenido de `fromVersion`; nunca reescribe historia. Sobre un documento borrado, lo reactiva.
- **Lock:** si un documento tiene lock, las escrituras de otras sesiones fallan con `document_locked`. Sin lock, cualquier persona con `write` puede escribir, siempre con `baseVersion`. El lock se renueva repitiendo `lock` con el mismo `sessionId` antes de `expiresAt`.
- **Contenido por URL:** el cliente pide `request-content-upload`, hace `PUT` del contenido a `uploadUrl` y después manda `contentUploadId` en `new` o `update`. El servicio verifica el SHA-256 al consumirlo. Un `contentUploadId` se usa una sola vez.

### Adjuntos

| Método | Payload | Respuesta | Permiso | Errores específicos |
| --- | --- | --- | --- | --- |
| `attachments.request-upload` | `id`, `documentId`, `fileName`, `mimeType`, `fileSize`, `checksum` | `{ id, uploadUrl, expiresIn, link }` | `write` en el documento | `attachment_type_not_allowed`, `attachment_too_large`, `already_exists` |
| `attachments.{id}.confirm` | vacío | `{ id, status: "ready" }` | El mismo caller que pidió la subida | `upload_not_found`, `checksum_mismatch` |
| `attachments.{id}.request-download` | vacío | `{ id, downloadUrl, expiresIn }` | `read` en el documento | `attachment_not_found` |
| `attachments.{id}.delete` | vacío | `{ id }` | `write` en el documento | `attachment_not_found` |

`link` es la URL estable para pegar en el markdown (`{JIKU_WEB_URL}/docs/attachments/{id}`, D-5). Un adjunto queda en estado `pending` hasta `confirm`; los pendientes de más de 24 h se borran. La política de tipos y tamaños (default: imágenes, PDF, planillas, comprimidos; 50 MiB) se configura en el servicio.

### Links compartidos

| Método | Payload | Respuesta | Permiso | Errores específicos |
| --- | --- | --- | --- | --- |
| `shares.new` | `id`, `documentId`, `mode` (`latest` / `pinned`), `version?` (obligatorio con `pinned`), `expiresAt`, `includeAttachments?` (default `true`), `note?`, `acknowledgeSecretWarning?` | `{ id, token, url, expiresAt }` | `manage` en la carpeta | `document_not_found`, `permission_denied`, `sharing_blocked`, `share_expiry_invalid`, `secret_warning_not_acknowledged`, `already_exists` |
| `shares.{id}.revoke` | vacío | `{ id, revokedAt }` | `manage` en la carpeta o creador del link | `share_not_found`, `permission_denied` |

`token` y `url` aparecen solo en la respuesta de `shares.new`; no se pueden volver a consultar. Si se pierde el link, se revoca y se crea otro. Reintentar `shares.new` con el mismo `id` tras un timeout devuelve el mismo link solo dentro de los 5 minutos (el servicio guarda el token cifrado ese tiempo para cubrir el caso); después responde `already_exists` sin token.

## 7. Consultas (`jiku-docs-queries`)

13 consultas, con la misma gramática que `jiku-queries`. Todas filtran por permisos antes de devolver o paginar.

| Método | Para qué | Parámetros principales |
| --- | --- | --- |
| `meta.describe` | Describe recursos, campos, filtros, órdenes y enums | ninguno |
| `folders.list` | Navegar el árbol | `filter`: `parentId`, `pathPrefix`, `id[]`; `include`: `effectivePermission` |
| `folders.get` | Detalle de una carpeta | `id`; `include`: `acl` (solo con `manage`), `effectivePermission` |
| `documents.list` | Catálogo filtrable | ver filtros abajo |
| `documents.get` | Documento con su contenido | `id`, `version?`; `include`: `content`, `relations`, `topics`, `lock`, `attachments` |
| `documents.versions.list` | Historial | `documentId`; orden por `version` |
| `documents.search` | Búsqueda por texto completo | `query`, `mode`, `filter`, `limit` |
| `documents.changes` | Feed de cambios para reconciliar | `sinceSeq`, `limit`, `filter.pathPrefix?` |
| `topics.list` | Temas en uso, con cantidad de documentos | `filter.prefix?` |
| `attachments.list` | Adjuntos de un documento | `filter.documentId` |

### Filtros de `documents.list`

| Filtro | Tipo | Notas |
| --- | --- | --- |
| `id` | ULID o lista | Traer varios por id |
| `folderId` | ULID | Solo esa carpeta |
| `pathPrefix` | string | Esa carpeta y sus descendientes (`/clientes/acme/`) |
| `clientId`, `projectId`, `requirementId`, `taskId` | entero o lista | Por relación con Jiku. `projectId` incluye documentos vinculados a requisitos o tareas de ese proyecto |
| `topic` | string o lista | Coincidencia exacta del slug |
| `visibility` | `internal` / `public` |  |
| `createdBy`, `updatedBy` | user-id o lista | Autor de la primera o la última versión |
| `createdAt`, `updatedAt` | rango (`gte`, `lte`) | ISO 8601 UTC |
| `fileName` | string | Prefijo, sin distinguir mayúsculas |
| `deleted` | boolean | Default `false` |

Orden permitido: `updatedAt`, `createdAt`, `title`, `path`. Campos base: `id`, `title`, `path`, `folderId`, `version`, `visibility`, `updatedAt`, `updatedBy`. `documents.list` nunca devuelve contenido; para eso está `documents.get`.

### `documents.get`

```jsonc
// pedido
{ "id": "01JBX7Q2M4N8P6R3T5V9W1Y0ZA", "include": ["content", "relations", "lock"] }

// respuesta
{ "status": "success", "data": {
  "id": "01JBX7Q2M4N8P6R3T5V9W1Y0ZA", "title": "Acceso al servidor de staging",
  "path": "/infra/servidores/staging.md", "version": 7, "checksum": "sha256:9f2c…",
  "visibility": "internal", "updatedAt": "2026-10-24T13:02:11.123Z", "updatedBy": "281234567890123456",
  "content": "---\ntitle: …",          // o "contentUrl" si supera 256 KiB
  "relations": { "clients": [4], "projects": [12, 31], "requirements": [812], "tasks": [] },
  "lock": { "sessionId": "01JC…", "holder": "281234567890123456", "via": "agent", "expiresAt": "…" }
} }
```

### `documents.search`

```jsonc
{
  "query": "\"integración con aduana\" -prueba",  // comillas para frases, OR, y -palabra para excluir
  "mode": "text",                              // único valor en la v1; semantic o hybrid → semantic_search_not_configured
  "filter": { "clientId": 4, "updatedAt": { "gte": "2026-06-01T00:00:00Z" } },
  "limit": 20                                   // máx. 50
}
```

Respuesta: lista de fragmentos con `documentId`, `version`, `title`, `path`, `headingPath` (los encabezados que contienen al fragmento), `snippet` (hasta 500 caracteres), `score`, `updatedAt` y `updatedBy`. Acepta los mismos filtros que `documents.list`. No pagina: devuelve los mejores `limit` resultados.

### `documents.changes`

Feed ordenado por un número de secuencia global y monotónico (`seq`). Es lo que usa el daemon al reconectar.

```jsonc
// pedido
{ "sinceSeq": 18340, "limit": 500, "filter": { "pathPrefix": "/clientes/" } }

// respuesta
{ "status": "success", "data": {
  "items": [ { "seq": 18341, "type": "document.updated", "documentId": "01JB…", "version": 8, "path": "/clientes/acme/pzt/reuniones/2026-10-24.md" } ],
  "nextSeq": 18341, "hasMore": false
} }
```

Incluye solo cambios de documentos que la persona puede leer en el momento de la consulta. Los permisos pueden cambiar después de un evento, así que un documento que dejó de ser visible no aparece como borrado: el daemon lo detecta con la reconciliación completa (sección 10). El feed conserva 30 días; un `sinceSeq` más viejo responde `invalid_cursor` y obliga a reconciliar completo.

### Consultas de links compartidos

| Método | Quién | Parámetros | Devuelve |
| --- | --- | --- | --- |
| `shares.list` | `manage` en la carpeta del documento | `filter`: `documentId`, `status` (`active` / `revoked` / `expired`), `createdBy` | Links con `id`, `mode`, `version`, `expiresAt`, `revokedAt`, `createdBy`, `accessCount`, `lastAccessAt`, `note`. Nunca el token |
| `shares.resolve` | Solo la `api` (sin `actor`) | `token` | `{ shareId, documentId, title, version, html, attachments: [{ id, fileName, mimeType }] }` o `share_not_found` |
| `shares.attachment` | Solo la `api` (sin `actor`) | `token`, `attachmentId` | `{ downloadUrl, expiresIn }` o `share_not_found` |

`shares.resolve` y `shares.attachment` responden `caller_not_authorized` a cualquier caller que no sea la `api`. `html` llega sanitizado, sin frontmatter y con los enlaces a adjuntos ya reescritos a la ruta pública; si supera 256 KiB, viene `htmlUrl` (prefirmada, 300 s) en su lugar. Cada `resolve` exitoso incrementa el contador de accesos y registra el acceso.

## 8. Eventos, notificaciones y feed de cambios

Cada cambio confirmado se escribe en el changelog en la misma transacción del comando. Desde ahí salen tres cosas: el evento completo para servicios, la notificación filtrada para personas y el feed `documents.changes`. Ningún cambio confirmado puede quedar sin evento.

### Outbox

```
comando → valida → transacción { escribe metadatos + inserta changelog(seq) } → COMMIT → responde
                                                  │
                    publicador de outbox ◀───────┘  (LISTEN/NOTIFY + barrido cada 5 s)
                         │
                         ├─▶ JetStream JIKU_DOCS_EVENTS   (Nats-Msg-Id = eventId: dedupe)
                         ├─▶ docs-notify de cada persona autorizada
                         └─▶ marca published_at en el changelog
```

El contenido se sube a S3 antes de abrir la transacción, con una clave que incluye el checksum. Si la transacción falla queda un objeto huérfano que un proceso de limpieza borra; nunca queda un metadato apuntando a un objeto inexistente.

### Stream `JIKU_DOCS_EVENTS`

| Parámetro | Valor |
| --- | --- |
| Subjects | `{instance}.docs-events.v1.>` |
| Retención | `limits`, `max_age` 30 días |
| Entrega | At-least-once; deduplicar por `eventId` |
| Duplicados en publicación | Ventana de deduplicación de 2 min con `Nats-Msg-Id` |
| Consumidores | Solo servicios: indexador, Jiku y conectores autorizados. Las personas no tienen permiso sobre el stream |

### Catálogo de eventos (15)

| Evento | Lo emite |
| --- | --- |
| `document.created` | `documents.new` |
| `document.updated` | `documents.{id}.update` |
| `document.moved` | `documents.{id}.move` |
| `document.deleted` | `documents.{id}.delete` |
| `document.restored` | `documents.{id}.restore` |
| `document.locked` | `documents.{id}.lock` (al tomarlo, no al renovarlo) |
| `document.unlocked` | `documents.{id}.unlock` o vencimiento del lock |
| `folder.created` | `folders.new` |
| `folder.updated` | `folders.{id}.edit` |
| `folder.deleted` | `folders.{id}.delete` |
| `folder.acl.changed` | `folders.{id}.acl.replace` |
| `attachment.created` | `attachments.{id}.confirm` |
| `attachment.deleted` | `attachments.{id}.delete` |
| `share.created` | `shares.new`. Nunca lleva el token |
| `share.revoked` | `shares.{id}.revoke`, y la revocación automática al borrar el documento (`changes.reason`: `manual` / `document_deleted`) |

Los eventos `share.*` solo van al stream; no generan notificaciones por persona. Los accesos a un link no generan eventos: quedan en el registro de accesos y en los contadores de `shares.list`.

### Sobre del evento

```jsonc
{
  "eventId": "01JC…",                       // ULID, clave de deduplicación
  "seq": 18341,                             // posición en el changelog
  "type": "document.updated",
  "version": "v1",
  "occurredAt": "2026-10-24T13:02:11.123Z",
  "correlationId": "…",
  "actor": { "id": "281234567890123456", "name": "Lautaro", "via": "agent" },
  "entity": { "type": "document", "id": "01JB…", "folderId": "01JA…", "path": "/infra/servidores/staging.md" },
  "snapshot": { "title": "…", "version": 8, "checksum": "sha256:…", "visibility": "internal",
                "relations": { "projects": [12, 31] }, "topics": ["infra"] },
  "changes": { "version": { "from": 7, "to": 8 }, "relations": { "added": { "projects": [31] } } }
}
```

El sobre nunca lleva el contenido del documento: quien lo necesite lo pide con `documents.get`. `snapshot` es el estado de los metadatos después del cambio.

### Notificaciones por persona

Subject: `{instance}.{user-id}.docs-notify.v1.{entidad}.{acción}`. Son mensajes NATS simples (sin JetStream): si la persona no está conectada, se pierden, y el feed `documents.changes` cubre el hueco.

- Mismo sobre que el evento, sin `changes.relations` ni `snapshot.relations` (pueden revelar proyectos que la persona no ve).
- Se envían a todas las personas con `read` sobre el documento en ese momento, estén conectadas o no, calculado por el publicador de outbox. Con la escala objetivo el costo es despreciable y no hace falta registrar presencia.
- `folder.acl.changed` se notifica a todas las personas internas como `access.changed`, sin detalle: es la señal para que el daemon reconcilie.

### Consumir el stream desde un servicio

Igual que `JIKU_EVENTS`: durable propio con nombre único, ack explícito, `filter_subject` con la versión (`{instance}.docs-events.v1.>`), deduplicación persistida por `eventId` e ignorar los eventos que provocó el propio servicio (`actor.id`).

## 9. Flujos principales

Ocho recorridos cubren los casos de uso de la v1. En todos, la identidad sale del subject y cada escritura genera versión, changelog y evento.

### 9.1 Trabajar un documento con un agente

1. La persona le pide al agente trabajar sobre un tema. El agente ejecuta `jiku docs search` o `jiku docs query documents.list` y elige el documento.
2. El agente ejecuta `jiku docs get <id> -o json` y guarda `version` como versión base.
3. Si el documento tiene lock de otra sesión (lo informa `get`), el agente avisa quién lo está editando y ofrece trabajar en solo lectura.
4. Para una edición larga, toma el lock con `jiku docs lock <id> --session <ulid> --ttl 300` y lo renueva repitiendo el comando antes de que venza.
5. Cada cambio que la persona acepta se guarda con `jiku docs put <id> <archivo> --base-version <n> --session <ulid> --message "..."`, con `JIKU_VIA=agent` en el entorno. La respuesta trae la nueva versión, que pasa a ser la base.
6. Cada guardado dispara `document.updated`: la web y los daemons de quienes pueden leerlo actualizan el documento en segundos.
7. Al terminar, el agente ejecuta `jiku docs unlock <id> --session <ulid>`. Si no lo hace, el lock vence solo.

Si en el paso 5 llega `version_conflict` (alguien escribió sin lock, por ejemplo desde un daemon offline que reconectó), el agente relee con jiku docs get, muestra la diferencia y el agente propone una combinación antes de reintentar.

### 9.2 Cambio remoto en un archivo sincronizado

1. Otra persona guarda una versión nueva.
2. El publicador de outbox envía `docs-notify.v1.document.updated` a cada persona conectada con `read`.
3. El daemon recibe la notificación, compara la versión con la que tiene registrada y, si es mayor, llama a `documents.get`.
4. Si el archivo local no tiene cambios pendientes, lo reemplaza de forma atómica (escribe en temporal y renombra). El editor lo recarga.
5. Si el archivo local tiene cambios sin subir, guarda la versión remota como `<nombre>.remoto-v<versión>.md` junto al original y avisa al usuario.
6. El daemon actualiza su `lastSeq` local con el `seq` del evento.

### 9.3 Cambio local subido por el daemon

1. El daemon detecta el cambio en disco (fsnotify) y espera 2 s sin cambios nuevos (debounce).
2. Calcula el SHA-256. Si coincide con la versión registrada, no hace nada.
3. Llama a `documents.{id}.update` con `baseVersion` = versión registrada y `via: human`.
4. Con `success`, registra la nueva versión. Con `version_conflict`, descarga la versión remota como copia y avisa. Con `document_locked`, muestra quién edita y reintenta cuando llegue `document.unlocked`.
5. Un archivo `.md` nuevo en una carpeta sincronizada se crea con `documents.new` (id generado por el daemon). Un archivo con otra extensión se ignora y se avisa.

### 9.4 Reconexión y reconciliación

1. Al arrancar o reconectar, el daemon llama a `documents.changes` con su `lastSeq` y aplica cada cambio como en 9.2.
2. Si recibe `invalid_cursor` (pasó más tiempo que la retención del feed) o `access.changed`, hace reconciliación completa: `documents.list` paginado con `fields: [id, path, version, checksum]` y compara con su registro local.
3. Los documentos locales que ya no aparecen en el listado se mueven a una carpeta `.jiku-docs/sin-acceso/` y se avisa, en lugar de borrarlos.
4. Además, el daemon hace una reconciliación completa cada 24 h.

### 9.5 Subir y ver un adjunto

1. El cliente llama a `attachments.request-upload` con un `id` propio y los datos del archivo.
2. Hace `PUT` del archivo a `uploadUrl`, directo a S3.
3. Llama a `attachments.{id}.confirm`; el servicio verifica que el objeto existe y que el checksum coincide.
4. Inserta `link` en el markdown y guarda el documento con `update`.
5. Al abrir el enlace (`{JIKU_WEB_URL}/docs/attachments/{id}`, D-5), la web lo reenvía con la sesión de la persona a la `api` (`GET /api/docs/attachments/{id}`), que la autentica, llama a `attachments.{id}.request-download` como `actor` y responde 302 a la URL prefirmada.

### 9.6 Documentos de un proyecto en Jiku

1. Una persona abre un proyecto en la web de Jiku.
2. La `api` llama a `documents.list` con `filter.projectId` y `actor`.
3. `jiku-docs` devuelve los documentos vinculados al proyecto, a sus requisitos o a sus tareas, que esa persona puede leer, incluidos los transversales de `/infra/` o `/buenas-practicas/`.
4. Para un cliente externo en opus-web, el mismo llamado devuelve solo los `public` (sección 4).

### 9.7 Importación desde HedgeDoc

1. Exportar las notas de HedgeDoc en markdown con su metadato (título, autor, fechas).
2. Una persona del equipo asigna a cada nota una carpeta destino en una planilla de mapeo; las no asignadas van a `/importado/` con acceso solo para administración.
3. Un script crea los documentos con `documents.new` como servicio, agrega el frontmatter con la fecha original y conserva el autor original en el campo `message` de la primera versión.
4. Antes de apagar HedgeDoc, revisar las notas con datos sensibles: hasta hoy fueron públicas, así que cualquier credencial que aparezca debe rotarse.

### 9.8 Compartir un documento por link público

1. Desde la web (o el agente, con confirmación de la persona) se elige el documento, el modo (`latest` o `pinned`), el vencimiento y si se incluyen adjuntos.
2. La `api` llama a `shares.new` con `actor`. `jiku-docs` verifica `manage` y que la carpeta no tenga `sharingBlocked`, genera el token, guarda su hash y responde `token` y `url`.
3. La web muestra el link una sola vez para copiarlo, con la fecha de vencimiento.
4. El destinatario abre `https://{JIKU_PUBLIC_URL}/p/docs/{token}`. La `api` atiende la ruta sin sesión, aplica rate limiting por IP y llama a `shares.resolve` como servicio.
5. Si el link es válido, la `api` responde una página de solo lectura con el `html` recibido y las cabeceras `X-Robots-Tag: noindex`, `Referrer-Policy: no-referrer`, `Cache-Control: private, no-store` y una CSP que no permite scripts.
6. Las imágenes y adjuntos de la página apuntan a `/p/docs/{token}/attachments/{id}`; la `api` llama a `shares.attachment` y responde 302 a una URL prefirmada de corta duración.
7. Si el link es inválido por cualquier motivo, la `api` responde 404 con una página genérica.
8. Para cortar el acceso, alguien con `manage` (o el creador) llama a `shares.{id}.revoke`; el siguiente acceso ya responde 404.

## 10. Clientes: jiku-go

El cliente oficial del módulo es [`jiku-go`](https://github.com/gravadigital/jiku-go), el binario y librería que ya existe para hablar con Jiku por NATS. No hay daemon ni servidor MCP aparte: personas y agentes usan el mismo CLI, y los servicios en Go la misma librería. `jiku-go` ya resuelve el inbox prefix, el token con refresco, los subjects y la validación contra `meta.describe`; el módulo reutiliza todo eso.

### 10.1 Librería

| Paquete | Qué agrega |
| --- | --- |
| `modules` | Lee `JIKU_MODULES`, hace el ping y devuelve el estado (`not_installed`, `incompatible`, `unavailable`, `available`) y el `serviceName`. Opt-in, como `events` |
| `modules/docs` | Cliente de `jiku-docs` sobre el mismo `jiku.Client`: las operaciones genéricas (`List`, `Get`, `Iterate`, `Contract`) apuntadas al servicio del registro, más helpers para lo que tiene pasos propios (`Search`, `GetContent`, `Put`, `New`, `Lock`, `Attach`, `Share`) |

Notas de diseño:

- El paquete se llama `modules/docs` porque `docs/` ya es la carpeta de documentación del repositorio.
- El nombre del servicio sale del registro (`serviceName`); nada queda compilado con `jiku-docs`.
- Como `jiku-docs` responde `meta.describe` con la misma forma que `core`, el contrato como dato, la validación previa de filtros y la paginación por cursor funcionan sin cambios.
- Los códigos de error del módulo se agregan como constantes (`CodeVersionConflict`, `CodeDocumentLocked`, `CodeShareNotFound`, etc.), para usar con `IsCode`.
- El propio servicio `jiku-docs` usa la librería para consultar a `core` con su machine user.

### 10.2 CLI: grupo `jiku docs`

Dos niveles. Los genéricos son los comandos que ya existen para `core`, apuntados al módulo:

| Comando | Qué hace |
| --- | --- |
| `jiku docs status` | Estado del módulo y `serviceName` |
| `jiku docs describe [recurso]` | Contrato del módulo, desde su `meta.describe` |
| `jiku docs query <método>` | Cualquier consulta, con `--filter`, `--sort`, `--fields`, `--include`, `--all` y validación previa |
| `jiku docs cmd <método> <json>` | Cualquier comando, con el id en el método |

Los ergonómicos ocultan los pasos que un agente haría mal a mano:

| Comando | Qué hace |
| --- | --- |
| `jiku docs ls [ruta]` | Carpetas y documentos de una ruta |
| `jiku docs search <texto>` | `documents.search`, con `--mode` y `--filter` |
| `jiku docs get <id o ruta>` | Markdown crudo a stdout; versión, checksum y lock a stderr. `-o json` devuelve todo junto; `--version N` una versión anterior |
| `jiku docs put <id> <archivo>` | `documents.{id}.update` con `--base-version` obligatorio, `--session` y `--message` opcionales |
| `jiku docs new <archivo> --folder <ruta o id>` | `documents.new` |
| `jiku docs mv`, `rm`, `restore` | Mover, borrar y restaurar, siempre con `--base-version` |
| `jiku docs history <id>` | Historial de versiones |
| `jiku docs lock <id>`, `unlock <id>` | Sesión de edición con `--session` y `--ttl` |
| `jiku docs attach <id> <archivo>` | Pide la subida, sube el archivo, confirma e imprime el link para el markdown |
| `jiku docs share <id> --expires 30d` | Crea un link compartido (`--pinned`, `--no-attachments`, `--note`) e imprime la URL, que no se puede volver a consultar |
| `jiku docs shares <id>`, `revoke <shareId>` | Lista y revoca links |
| `jiku docs edit <id>` | Para personas: toma el lock, abre `$EDITOR`, guarda y libera, renovando el lock mientras el editor está abierto |
| `jiku docs sync [carpeta]` | El daemon de sincronización (10.4) |

Lo que hacen todos los comandos ergonómicos sin que nadie lo pida: generar el ULID de las altas (reintentar es seguro), calcular el SHA-256, usar la subida por URL cuando el contenido supera 256 KiB, aceptar una ruta (`/infra/servidores/staging.md`) donde va un id y enviar `via` según `JIKU_VIA` (10.5).

```
jiku docs search "integración con aduana" --filter clientId=4
jiku docs get /infra/servidores/staging.md > staging.md
jiku docs put 01JBX7Q2M4N8P6R3T5V9W1Y0ZA staging.md --base-version 7 --message "Nuevo puerto SSH"
jiku docs share 01JBX7Q2M4N8P6R3T5V9W1Y0ZA --expires 15d --note "Proveedor de hosting"
```

Si el módulo no está instalado o no responde, cualquier `jiku docs` sale con código distinto de cero y un mensaje que distingue los dos casos.

### 10.3 Cambios en el resto de jiku-go

| Comando | Cambio |
| --- | --- |
| `jiku doctor` | Una línea más para el módulo: no instalado, incompatible, caído o disponible, con el tiempo de respuesta del ping |
| `jiku batch` | Campo opcional `service` en cada línea (`core` por defecto, `docs`), para mezclar consultas de ambos en una sola conexión |
| `jiku raw` | Sin cambios: `--service jiku-docs-queries` ya funciona |

La compatibilidad semántica de `jiku-go` cubre los nombres de comandos y flags nuevos desde la versión que los incorpore.

### 10.4 Daemon `jiku docs sync`

Corre en primer plano o como servicio del sistema (systemd o launchd) y espeja en una carpeta local los documentos que la persona puede leer.

| Aspecto | Definición |
| --- | --- |
| Login | El de `jiku-go`: `jiku login` (device flow) para personas, key file para uso desatendido |
| Carpeta local | `~/JikuDocs/` por defecto; refleja el árbol con las mismas rutas |
| Alcance | Todo lo legible, o una selección de carpetas configurada por la persona |
| Estado local | SQLite en `~/JikuDocs/.jiku-docs/state.db`: `documentId`, ruta, versión, checksum, `lastSeq` |
| Permisos locales | Archivos de carpetas con solo `read` quedan de solo lectura en disco |
| Tiempo real | Suscripción a `{instance}.{user-id}.docs-notify.v1.>` |
| Reconciliación | `documents.changes` al reconectar; completa ante `access.changed`, `invalid_cursor` o cada 24 h |
| Conflictos | Nunca pisa cambios locales: guarda la versión remota como copia y avisa |
| Ignorados | Archivos que no son `.md`, ocultos y temporales de editores (`.swp`, `~`, `.#*`) |
| Estado visible | Log en stderr y `jiku docs sync status` (últimos cambios y conflictos pendientes). Una interfaz gráfica queda fuera de la v1 |

Los adjuntos no se sincronizan: quedan como enlaces que se abren en el navegador.

### 10.5 Agentes

Los agentes usan el CLI con el contrato que ya tienen a mano; se le agregan los comandos de `jiku docs`. No hay servidor MCP en la v1.

- **Autoría.** El CLI corre con el token de la persona (`jiku login`), así que lo que escribe un agente queda a nombre de ella. Para registrar que lo hizo un agente, su entorno define `JIKU_VIA=agent`; sin la variable, el CLI envía `via: human`.
- **Sesiones.** Cada invocación del CLI es un proceso aparte, así que un agente no mantiene un lock renovado en segundo plano. La protección real es `--base-version`; para ediciones largas, el agente toma el lock con `--ttl 300` y lo renueva repitiendo `jiku docs lock` con la misma `--session`.
- **Reglas en el contrato del agente.** Buscar antes de crear, para no duplicar. Citar documento y versión al responder con información de la documentación. No guardar cambios que la persona no aceptó. No ejecutar `jiku docs share` ni `revoke` sin confirmación explícita: el CLI no puede imponer ese gate, depende de estas instrucciones.
- **Agentes sin shell.** Si aparece alguno (por ejemplo, un asistente en el navegador), un subcomando `jiku mcp` puede exponer las mismas operaciones de la librería como herramientas MCP, con aprobación por herramienta en el cliente.

### 10.6 Adaptador WebDAV (opcional)

Un proceso aparte que expone el árbol por WebDAV para quien quiera montar una unidad o usar herramientas estándar. Traduce cada operación a comandos y consultas del bus (`PROPFIND` a `folders.list` y `documents.list`, `GET` a `documents.get`, `PUT` a `documents.new` o `update` usando el ETag como versión base, `LOCK` a `documents.{id}.lock`). Como los clientes WebDAV nativos no soportan OIDC, se autentica con tokens personales emitidos desde la web de Jiku. No forma parte de la v1.

## 11. Modelo de datos e indexación

Base PostgreSQL propia (`jiku_docs`), separada de la de Jiku. El servicio usa un usuario con escritura; el indexador puede usar el mismo o uno restringido a las tablas de índice.

### Tablas

```sql
CREATE TABLE folders (
  id               text PRIMARY KEY,             -- ULID
  parent_id        text REFERENCES folders(id),
  name             text NOT NULL,
  path             text NOT NULL UNIQUE,         -- materializado: '/clientes/acme/pzt/'
  inherit_acl      boolean NOT NULL DEFAULT true,
  external_blocked boolean NOT NULL DEFAULT false,
  sharing_blocked  boolean NOT NULL DEFAULT false,  -- efectivo = OR con los ancestros
  created_by       text NOT NULL,
  created_at       timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX folders_name_uq ON folders (parent_id, lower(name));

CREATE TABLE folder_rules (
  folder_id      text NOT NULL REFERENCES folders(id) ON DELETE CASCADE,
  principal_type text NOT NULL CHECK (principal_type IN ('role','user')),
  principal      text NOT NULL,
  permission     text NOT NULL CHECK (permission IN ('read','write','manage')),
  PRIMARY KEY (folder_id, principal_type, principal)
);

CREATE TABLE documents (
  id              text PRIMARY KEY,              -- ULID generado por el cliente
  folder_id       text NOT NULL REFERENCES folders(id),
  file_name       text NOT NULL,
  title           text NOT NULL,
  visibility      text NOT NULL DEFAULT 'internal' CHECK (visibility IN ('internal','public')),
  current_version integer NOT NULL,
  checksum        text NOT NULL,                 -- 'sha256:<hex>' de la versión actual
  size_bytes      integer NOT NULL,
  created_by      text NOT NULL,
  created_at      timestamptz NOT NULL,
  updated_by      text NOT NULL,
  updated_at      timestamptz NOT NULL,
  deleted_at      timestamptz
);
CREATE UNIQUE INDEX documents_name_uq ON documents (folder_id, lower(file_name)) WHERE deleted_at IS NULL;

CREATE TABLE document_versions (
  document_id text NOT NULL REFERENCES documents(id),
  version     integer NOT NULL,
  storage_key text NOT NULL,                     -- 'docs/<documentId>/<version>-<sha256>.md'
  checksum    text NOT NULL,
  size_bytes  integer NOT NULL,
  author      text NOT NULL,
  via         text NOT NULL CHECK (via IN ('human','agent','service')),
  session_id  text,
  message     text,
  created_at  timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (document_id, version)
);

CREATE TABLE document_relations (
  document_id text NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
  entity_type text NOT NULL CHECK (entity_type IN ('client','project','requirement','task')),
  entity_id   integer NOT NULL,
  project_id  integer,                           -- proyecto resuelto, para filtrar por proyecto
  PRIMARY KEY (document_id, entity_type, entity_id)
);

CREATE TABLE document_topics (
  document_id text NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
  topic       text NOT NULL,
  PRIMARY KEY (document_id, topic)
);

CREATE TABLE document_locks (
  document_id text PRIMARY KEY REFERENCES documents(id),
  session_id  text NOT NULL,
  holder      text NOT NULL,
  via         text NOT NULL,
  expires_at  timestamptz NOT NULL
);

CREATE TABLE attachments (
  id          text PRIMARY KEY,
  document_id text NOT NULL REFERENCES documents(id),
  file_name   text NOT NULL,
  mime_type   text NOT NULL,
  size_bytes  bigint NOT NULL,
  checksum    text NOT NULL,
  storage_key text NOT NULL,
  status      text NOT NULL CHECK (status IN ('pending','ready','deleted')),
  uploaded_by text NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE content_uploads (
  id          text PRIMARY KEY,
  checksum    text NOT NULL,
  size_bytes  integer NOT NULL,
  storage_key text NOT NULL,
  requested_by text NOT NULL,
  expires_at  timestamptz NOT NULL,
  consumed_at timestamptz
);

CREATE TABLE changelog (
  seq          bigserial PRIMARY KEY,
  event_id     text NOT NULL UNIQUE,            -- ULID
  type         text NOT NULL,
  entity_type  text NOT NULL,
  entity_id    text NOT NULL,
  folder_id    text,
  payload      jsonb NOT NULL,                  -- sobre completo del evento
  occurred_at  timestamptz NOT NULL DEFAULT now(),
  published_at timestamptz
);
CREATE INDEX changelog_unpublished ON changelog (seq) WHERE published_at IS NULL;
```

`project_id` en `document_relations` se resuelve al guardar (para requisitos y tareas se consulta a `core`) y permite que `filter.projectId` incluya documentos vinculados a requisitos o tareas del proyecto.

Links compartidos:

```sql
CREATE TABLE shares (
  id                  text PRIMARY KEY,           -- ULID generado por el cliente
  document_id         text NOT NULL REFERENCES documents(id),
  token_hash          bytea NOT NULL UNIQUE,      -- sha256(token); el token nunca se guarda en claro
  token_retry_cipher  bytea,                      -- token cifrado para reintentos; se borra a los 5 min
  mode                text NOT NULL CHECK (mode IN ('latest','pinned')),
  version             integer,                    -- obligatorio con mode = 'pinned'
  include_attachments boolean NOT NULL DEFAULT true,
  note                text,
  created_by          text NOT NULL,
  created_at          timestamptz NOT NULL DEFAULT now(),
  expires_at          timestamptz NOT NULL,
  revoked_at          timestamptz,
  revoked_by          text,
  revoke_reason       text CHECK (revoke_reason IN ('manual','document_deleted')),
  access_count        bigint NOT NULL DEFAULT 0,
  last_access_at      timestamptz,
  CHECK (mode = 'latest' OR version IS NOT NULL)
);
CREATE INDEX shares_document ON shares (document_id);

CREATE TABLE share_accesses (
  share_id      text NOT NULL REFERENCES shares(id) ON DELETE CASCADE,
  accessed_at   timestamptz NOT NULL DEFAULT now(),
  kind          text NOT NULL CHECK (kind IN ('document','attachment')),
  attachment_id text,
  ip_hash       bytea NOT NULL,                   -- HMAC de la IP con una clave del servicio
  user_agent    text                              -- truncado a 200 caracteres
);
CREATE INDEX share_accesses_share ON share_accesses (share_id, accessed_at DESC);
```

Los registros de `share_accesses` se conservan 180 días.

### Storage

| Bucket | Clave | Notas |
| --- | --- | --- |
| `jiku-docs-content` | `docs/<documentId>/<version>-<sha256>.md` | Objetos inmutables; versionado del bucket activado como red de seguridad |
| `jiku-docs-content` | `uploads/<contentUploadId>` | Contenido grande en tránsito; regla de ciclo de vida a 24 h |
| `jiku-docs-attachments` | `att/<attachmentId>` | Sin acceso público; solo URLs prefirmadas |

Solo `jiku-docs` tiene credenciales de los buckets, igual que `core` con los suyos.

### Indexación

La búsqueda es por texto completo con PostgreSQL, sin servicios adicionales.

```sql
CREATE EXTENSION IF NOT EXISTS unaccent;
CREATE TEXT SEARCH CONFIGURATION es_unaccent (COPY = spanish);
ALTER TEXT SEARCH CONFIGURATION es_unaccent
  ALTER MAPPING FOR hword, hword_part, word WITH unaccent, spanish_stem;

CREATE TABLE index_chunks (
  document_id  text NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
  version      integer NOT NULL,
  chunk_no     integer NOT NULL,
  heading_path text[] NOT NULL,
  body         text NOT NULL,
  tsv          tsvector NOT NULL,   -- título y encabezados con peso A, cuerpo con peso B (es_unaccent)
  PRIMARY KEY (document_id, chunk_no)
);
CREATE INDEX index_chunks_tsv ON index_chunks USING gin (tsv);
```

Proceso del indexador por cada `document.created`, `updated`, `moved`, `restored` o `deleted`:

1. Si el documento fue borrado, elimina sus fragmentos y termina.
2. Lee la versión indicada en el evento (si ya hay una más nueva indexada, descarta el evento).
3. Quita el frontmatter del texto indexable y divide por encabezados; secciones de más de \~800 palabras se cortan por párrafos.
4. Calcula el `tsvector` de cada fragmento (título del documento y encabezados con peso A, cuerpo con peso B) y reemplaza los fragmentos del documento en una transacción.

Búsqueda: la consulta se interpreta con `websearch_to_tsquery('es_unaccent', ...)`, que admite frases entre comillas, `OR` y exclusión con `-`. Los resultados se ordenan por `ts_rank_cd`, se filtran por permisos y metadatos con un `JOIN` a las carpetas legibles por la persona (calculadas una vez por consulta) y el `snippet` se arma con `ts_headline`. Con `es_unaccent`, las variantes de una palabra ("aduana", "aduanas") coinciden y los acentos no importan.

El índice se regenera completo con un comando de mantenimiento que recorre `documents` y encola un evento sintético por documento. Si más adelante se agrega búsqueda semántica, se suma una columna de vectores a esta tabla y un modelo de embeddings, sin cambiar los contratos.

## 12. Cambios en Jiku, seguridad y operación

Integrar `jiku-docs` requiere cambios de seguridad en el repositorio de Jiku (rol, plantillas y reglas del auth-callout) y nuevas rutas en la `api`. En `core` solo se registra el rol nuevo; nadie toca su base.

### Cambios en el auth-callout

| Cambio | Archivo | Detalle |
| --- | --- | --- |
| Rol `docs-service` | `rules.yaml` + plantilla nueva `templates/docs-service.yaml` | Suscribirse a `{instance}.*.jiku-docs-commands.v1.>` y `.jiku-docs-queries.v1.>` (con queue group); publicar en `{instance}.{su-id}.jiku-queries.v1.>`; publicar en `{instance}.docs-events.v1.>` y `{instance}.*.docs-notify.v1.>`; administrar el stream `JIKU_DOCS_EVENTS`; consumir `JIKU_EVENTS` (opcional) |
| Plantillas de persona | `templates/<persona>.yaml` | Publicar en `{instance}.{su-id}.jiku-docs-commands.v1.>` y `.jiku-docs-queries.v1.>`; suscribirse a `{instance}.{su-id}.docs-notify.v1.>` |
| Plantilla de conector | `templates/connector.yaml` | Publicar en los subjects de comandos y consultas de docs; consumir `JIKU_DOCS_EVENTS` con durable propio |
| Plantilla de la `api` | la que corresponda | Publicar en los subjects de comandos y consultas de docs |

### Cambios en core

| Cambio | Archivo |
| --- | --- |
| Entrada `docs-service` en `ROLE_METHODS` con `queries: ALL` y `commands: []` | `core/src/authorize-caller.ts` |
| `docs-service` en `SERVICE_ROLES` | `core/src/events/auth/identity-type.ts` |

### Cambios en la api y los frontends

| Cambio | Detalle |
| --- | --- |
| Rutas proxy de documentos | `GET /docs/documents`, `GET /docs/documents/{id}`, `GET /docs/search`, etc., que traducen a consultas y comandos de `jiku-docs` con `actor` |
| Redirección de adjuntos | `GET /docs/attachments/{id}`: autentica, llama a `attachments.{id}.request-download` y responde 302 |
| Rutas de links compartidos | `POST /docs/documents/{id}/shares`, `GET /docs/documents/{id}/shares`, `DELETE /docs/shares/{id}`, con `actor` |
| Ruta pública | `GET /p/docs/{token}` y `GET /p/docs/{token}/attachments/{id}`, sin sesión: llaman a `shares.resolve` / `shares.attachment` sin `actor`, con rate limiting por IP (60 pedidos por minuto por IP) y las cabeceras de la sección 9.8. Servidas en un host propio (`JIKU_PUBLIC_URL`), separado del de la aplicación, para que no compartan cookies |
| Panel de documentos | En la ficha de proyecto, requisito y tarea de `web`; en `opus-web`, solo documentos `public` |
| Visor y editor | Visor markdown en `web`; edición con lock y versión base, igual que el CLI; botón "Compartir" con la gestión de links del documento |

### Alta del servicio

1. Machine user en Zitadel con Access Token Type JWT y rol `docs-service`.
2. JSON key entregada al servicio; refresco en caliente del token.
3. Creds del sentinel, URL de NATS y `NATS_INSTANCE`.
4. Creación idempotente del stream `JIKU_DOCS_EVENTS` y del KV al arrancar.
5. Manejo de `unknown_caller` al arrancar como señal de reconexión (guía de conectores, §3.4).

### Seguridad

- Deny-by-default: sin regla no hay acceso; claves desconocidas en payloads son error.
- Nada de contenido de documentos en logs ni en eventos; solo ids, versiones, `correlationId` y `errorCode`.
- URLs prefirmadas de 300 s; nunca se guardan ni se envían en eventos.
- Buckets sin acceso público, cifrado en reposo y credenciales solo en `jiku-docs`.
- Las notificaciones por persona no revelan relaciones con proyectos que la persona no ve.
- Los documentos no deben contener secretos. Un detector de patrones de credenciales (claves privadas, tokens con formatos conocidos) marca el documento y avisa a la persona en la respuesta del comando, sin bloquear el guardado.
- Revocar el acceso de una persona en Zitadel tiene efecto en el bus en su próxima autenticación; para un corte inmediato se fuerza la reconexión, como con cualquier identidad de Jiku.

### Observabilidad

| Señal | Detalle |
| --- | --- |
| Logs | Estructurados en JSON: método, caller, `correlationId`, duración, `errorCode` |
| Métricas | Latencia y errores por método; antigüedad del changelog sin publicar; lag del indexador; locks activos; fan-out de notificaciones |
| Alertas | Changelog sin publicar con más de 60 s; indexador con lag de más de 5 min; tasa de `upstream_unavailable` |
| Salud | Endpoints de NATS micro (`nats micro info jiku-docs-commands`) |

### Respaldo

Backup diario de `jiku_docs` con retención de 30 días y versionado en los buckets. Restaurar la base a un punto anterior es seguro porque los objetos en S3 son inmutables: ninguna versión referenciada desaparece. El índice no se respalda; se regenera.

## 13. Módulo opcional y autodescubrimiento

`jiku-docs` es un módulo opcional: Jiku funciona igual sin él y, cuando está instalado, lo descubre leyendo una clave en el bus. La primera versión es deliberadamente mínima: saber si el módulo está, cómo se llama su servicio para conectarse y si responde. Jiku no guarda ningún dato de documentación, así que instalar o desinstalar el módulo no deja nada roto.

### Registro

El despliegue base de Jiku crea un bucket KV `JIKU_MODULES` (script idempotente, igual que el stream de eventos), aunque no haya módulos. Al arrancar, cuando ya puede atender, `jiku-docs` escribe su clave una vez:

```jsonc
// clave: docs
{
  "module": "docs",
  "serviceName": "jiku-docs",
  "protocol": "v1",
  "registeredAt": "2026-10-24T12:00:00.000Z"
}
```

| Campo | Para qué |
| --- | --- |
| `module` | Nombre corto del módulo; es la clave en el KV |
| `serviceName` | Prefijo de los servicios micro y subjects. Por convención, el módulo atiende `{serviceName}-commands` y `{serviceName}-queries` |
| `protocol` | Versión de los contratos. Permite distinguir una incompatibilidad de una caída |
| `registeredAt` | Informativo, para operación |

Un apagado normal no borra la clave (un reinicio no es una desinstalación). Para desinstalar: `jiku-docs module deregister` borra la clave y después se detiene el servicio.

### Estado

La `api` lee la clave al arrancar y la observa con un watch. Para saber si el módulo responde usa el ping estándar de NATS micro (`$SRV.PING.{serviceName}-queries`) cada 30 s y cada vez que una llamada falla con `no responders`.

| Estado | Condición | api | Interfaz |
| --- | --- | --- | --- |
| `not_installed` | No existe la clave | Rutas `/docs/*` y `/p/docs/*` responden 404 `module_not_installed` | No aparece nada de documentación |
| `incompatible` | `protocol` distinto del que soporta la `api` | Igual que `not_installed`, más un log de error | Igual que `not_installed` |
| `unavailable` | Clave presente, ping sin respuesta | Rutas responden 503 `module_unavailable` | Menú y paneles visibles con un aviso; acciones deshabilitadas |
| `available` | Clave presente, ping con respuesta | Rutas habilitadas | Todo habilitado |

La web y opus-web consultan `GET /modules` (autenticado) al iniciar sesión, cada 60 s y al recibir `module_unavailable`. Responde `[{ module, status }]`.

### Funciones que dependen de configuración

No se anuncian en el registro. Si una función del módulo no está configurada, el comando o la consulta responde un error específico y la interfaz lo muestra:

| Función | Si no está configurada |
| --- | --- |
| Búsqueda semántica o híbrida (no incluida en la v1) | `documents.search` con `mode` distinto de `text` responde `semantic_search_not_configured` |
| Adjuntos (sin bucket) | `attachments.*` responde `attachments_not_configured` |
| Links compartidos (sin `JIKU_PUBLIC_URL` en la `api`) | La `api` responde 404 en `/p/docs/*` y la web oculta "Compartir" |

Si más adelante hace falta que la interfaz sepa esto de antemano, se agrega un campo opcional al registro sin romper la `v1`.

### Quién usa el registro

| Parte | Comportamiento |
| --- | --- |
| `api` | Watch del KV, ping, `GET /modules` y respuesta de las rutas según el estado |
| Conectores y agentes | Leen la clave al arrancar; solo exponen herramientas de documentación si existe y el ping responde |
| Daemon de sync | Si el módulo no responde, sigue offline y reconcilia al volver; si la clave desaparece, deja de sincronizar y avisa, sin borrar archivos |
| `core` | Sin cambios |

### Permisos en el auth-callout

| Rol | Permiso |
| --- | --- |
| `docs-service` | Escribir y leer solo la clave `docs` del KV `JIKU_MODULES` |
| `api` | Leer y observar el KV; publicar `$SRV.PING.jiku-docs-queries` |
| Conectores y personas | Leer y observar el KV |

Leer y observar un KV implica permisos sobre `$JS.API.DIRECT.GET.KV_JIKU_MODULES.>` y para crear consumidores efímeros sobre `KV_JIKU_MODULES`; escribir una clave implica publicar en `$KV.JIKU_MODULES.<clave>`.

### Contrato

`jiku-modules.yaml` describe el bucket y el esquema de la clave. Pertenece al repositorio de Jiku, porque lo usarán todos los módulos. En el contrato HTTP de la `api`, las rutas `/docs/*`, `/p/docs/*` y `/modules` se marcan con `x-jiku-module: docs` y documentan `module_not_installed` y `module_unavailable`.

## 14. Contratos y referencias

Los cuatro contratos AsyncAPI 3.0 se entregan junto con este documento y mandan sobre él ante cualquier diferencia de forma, igual que en Jiku:

| Archivo | Contenido |
| --- | --- |
| `jiku-docs-commands.yaml` | 18 comandos con payloads, respuestas, permiso requerido (`x-permission`) y errores posibles (`x-error-codes`) |
| `jiku-docs-queries.yaml` | 13 consultas con filtros, órdenes, campos, includes y respuestas paginadas |
| `jiku-docs-events.yaml` | Stream `JIKU_DOCS_EVENTS`, notificaciones `docs-notify` y sus sobres |

El cuarto contrato, `jiku-modules.yaml`, describe el registro de módulos (sección 13): el bucket `JIKU_MODULES`, el esquema de la clave y la convención de nombres de servicio. Va en `docs/apis/` del repositorio de Jiku, porque lo comparten todos los módulos.

Se sugiere ubicarlos en `docs/apis/` del repositorio de `jiku-docs` y validarlos en CI, como los de `core`.

### Referencias

- [Guía para construir un conector de Jiku (v1.6.0)](https://github.com/gravadigital/jiku/tree/v1.6.0)
- [Contratos de core: `docs/apis/`](https://github.com/gravadigital/jiku/tree/v1.6.0/docs/apis)
- [Política del auth-callout](https://github.com/gravadigital/jiku/blob/v1.6.0/deploy/nats/auth-callout/README.md)
- [Implementación de referencia de subjects e inbox](https://github.com/gravadigital/jiku/blob/v1.6.0/packages/nats-protocol/src/index.ts)
- [Consumidor de eventos de referencia](https://github.com/gravadigital/jiku/blob/v1.6.0/core/tests/tools/events-test-consumer.ts)
