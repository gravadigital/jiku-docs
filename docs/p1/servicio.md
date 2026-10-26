# P-1 · Documentación sin carpetas: cambios en el servicio (contrato 1.3.0)

Fecha: 2026-10-26. Origen: decisiones del dueño en `docs/backlog-jiku-web.md` (P-1) y la propuesta de pantallas
`_research/jiku-documentacion-propuesta-b/`. Este documento **manda sobre `especificacion-v1.md`** donde se
contradicen, y es la referencia común para jiku-docs, jiku-go y la web de Jiku.

## 1. Permisos: visibilidad en lugar de carpetas

La ACL de carpetas deja de decidir el acceso de las personas. Decide la **clase** del caller y la **visibilidad** del
documento.

| Visibilidad | `admin` | `user` | `external-user` | servicio (`internal-app`) |
| --- | --- | --- | --- | --- |
| `internal` (default) | lee y escribe | lee y escribe | nunca | todo |
| `public` | lee y escribe | lee y escribe | lee, si está vinculado a un proyecto de sus `project-permissions` | todo |
| `restricted` (nueva) | lee y escribe | **nunca** | **nunca** | todo |

Reglas:

- **Lo invisible no existe.** Un `restricted` para un `user` o un externo no existe por **ninguna vía**: `documents.list`,
  `get`, `search` (y sus fragmentos), `changes`, `versions.list`, `topics.list` (los conteos no lo cuentan), `attachments.*`,
  `shares.*`, `docs-notify`, `documents.suggest-relations`, el feed del daemon de sync, y los conteos de cualquier
  respuesta. Siempre responde `document_not_found` o lo omite, igual que hoy con lo no visible. Cada vía tiene su test.
- **Quién cambia la visibilidad:**
  - Una persona interna (`user` o `admin`) puede poner `internal` o `public`.
  - Solo `admin` puede poner o quitar `restricted`. Un `user` que lo intenta recibe `permission_denied`.
  - Como el frontmatter es contenido, la regla se aplica en cada `new`, `update` y `restore`: un `user` no puede crear un
    documento `restricted` ni restaurar una versión que lo vuelva `restricted`.
- **Links compartidos:**
  - Cualquier persona interna puede crear y revocar links de documentos `internal` o `public`.
  - Un documento `restricted` no admite links (`sharing_blocked`). Pasar un documento a `restricted` revoca sus links
    activos, igual que borrarlo.
- **Forzar el desbloqueo:** `unlock` con `force` lo puede hacer `admin`; antes requería `manage`.
- **Carpetas internas:** las carpetas siguen existiendo, como organización de los archivos para el daemon de sync, pero ya
  no definen permisos para personas.
  - `folders.*` y `folders.{id}.acl.replace` quedan solo para `admin` y servicios; para un `user` responden
    `permission_denied`.
  - Las reglas ACL quedan **sin efecto**: se documentan como obsoletas en el contrato, sin borrarlas de la base.
  - `externalBlocked` y `sharingBlocked` también quedan sin efecto, reemplazados por la visibilidad.
- **Notificaciones:** `docs-notify` se calcula con este modelo. Un documento `restricted` solo notifica a admins.

### Migración de datos existentes (sin cambiar lo que hoy ve cada uno hacia menos seguro)

Al aplicar la migración:

1. Todo documento que hoy **no** puede leer una persona con rol `user` según la ACL vigente pasa a `restricted`. Esto
   incluye `/importado/` y cualquier carpeta sin herencia sin regla para `user`.
2. Todo documento `public` en una carpeta con `externalBlocked` (efectivo, heredado) pasa a `internal`, porque hoy un
   externo no lo ve.
3. Los links activos de los documentos que pasan a `restricted` se revocan (`revoke_reason = 'restricted'`).
4. La migración registra cada cambio en el historial del documento: una versión nueva de servicio con mensaje, porque
   la visibilidad vive en el frontmatter.

## 2. Ubicación generada por el servidor

- **Carpeta opcional en `documents.new`.** Si falta `folderId`, el servidor genera la carpeta a partir de las relaciones
  del frontmatter, creando las carpetas que falten:
  - con proyecto: `/clientes/<slug-cliente>/<slug-proyecto>/`; el cliente sale del proyecto según core, o del primero de
    `jiku.clients`;
  - con cliente y sin proyecto: `/clientes/<slug-cliente>/general/`;
  - sin cliente ni proyecto: `/general/`.

  Los slugs salen de los nombres en core, en kebab-case sin acentos. Con varios proyectos se usa el primero de la lista.
- **Nombre de archivo opcional.** Si falta `fileName`, se genera desde el título en kebab-case `.md`, único en la
  carpeta con sufijo `-2`, `-3`, etc.
- **Sin movimientos automáticos.** Cambiar relaciones después **no mueve** el documento: la ubicación no importa para
  las personas.
- **La UI nunca muestra la ruta.** El campo `path` sigue en las respuestas para el CLI y el sync.

## 3. Relaciones

- **Comando nuevo `documents.{id}.relations.update`**, con permiso `write`:
  - payload `{ baseVersion, add?: {clients, projects, requirements, tasks}, remove?: {...} }`;
  - reescribe el bloque `jiku:` del frontmatter sin tocar el resto del contenido y crea una versión (`kind = update`) con
    un mensaje automático (por ejemplo "Proyecto asignado: EXO - Videoconferencia");
  - valida los ids contra core (`invalid_relation`) y responde `{ id, version, relations }`;
  - es idempotente: si no cambia nada, responde `success` con la versión actual.

  Es lo que usa "Asignar proyecto". Agregar un proyecto agrega también su cliente, si falta.
- **Consulta nueva `documents.suggest-relations`**, con payload `{ id }` (requiere `read`):
  - devuelve hasta 5 candidatos `{ projectId, clientId, score, reason }`, sacados de coincidencias de nombres o alias de
    proyectos y clientes de core en el título y en el contenido;
  - el título pesa más que el contenido;
  - excluye los proyectos ya vinculados;
  - `reason` es un texto corto, por ejemplo "El título menciona «Videoconferencia»".

## 4. Listados y búsqueda

- **Filtros nuevos** en `documents.list` y `documents.search`:
  - `hasProject`, `hasClient` y `hasTopic` (boolean). Con `false` implementan "Sin proyecto", "Sin cliente" y
    "Sin tema". "Sin proyecto" significa sin relación directa con proyecto, ni por requisito ni por tarea.
  - `visibility` admite `restricted` (solo admin; para los demás es `invalid_fields` o lista vacía, y se documenta cuál).
- **Origen de la coincidencia por proyecto.** Con `filter.projectId`, cada ítem trae `projectMatch`:
  - `direct`: el proyecto está en `jiku.projects`;
  - `indirect`: el documento llega por un requisito o una tarea del proyecto.

  Es la base de la marca "vinculado, sin proyecto propio" de la card del proyecto.
- **Nombres de autores.** `documents.list`, `get`, `search` y `versions.list` traen `createdByName` y `updatedByName`
  (y `authorName` en las versiones).
  - El nombre se guarda al escribir: `actor.name` (o `username`) cuando escribe la api, el `name` de `users.list` cuando
    escribe una persona por el bus, y el nombre del servicio cuando escribe un servicio.
  - Una migración completa los existentes consultando a core.
  - Alcanza para "Editado por" y para el lock: `lock.holderName`.
- **Clientes y proyectos en los ítems.** Cada ítem de `documents.list` y `search` incluye `clients` y `projects` como
  listas de ids, para mostrar "Cliente · Proyecto". Los nombres los resuelve la api con core.
- **Temas:** `topics.list` con `filter.prefix` ya existe; sirve para sugerir temas existentes (temas libres).

## 5. Sin cambios

- Historial y compactación (`docs/compactacion-historial-v1.md`).
- Adjuntos (D-3).
- Eventos: se agregan campos nuevos a `snapshot` (`visibility` ya existe), sin cambios incompatibles.
- Daemon de sync: espeja según `path`. Con el modelo nuevo, los archivos de una persona interna dejan de ser de solo
  lectura en disco, y los `restricted` solo los sincronizan los admins.

## 6. Contratos y versión

Contratos `jiku-docs-*.yaml` a la versión **1.3.0**, con todo lo anterior: visibilidad, comando y consulta nuevos, filtros,
campos, deprecaciones de ACL y errores. `meta.describe` refleja los filtros y campos nuevos. Se actualizan la
especificación v1 (secciones 3, 4, 6, 7 y 11) y el CHANGELOG.
