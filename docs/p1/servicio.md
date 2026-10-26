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
  - Si el `actor` de la api no trae `name` ni `username` (el token de la web no los trae, JIKU-WEB-5), el nombre se busca
    en `users.list` de core antes de escribir.
  - Al leer, un nombre `null` guardado se completa desde core si lo conoce, sin escribirlo en la base. Así se ven los
    documentos escritos antes del arreglo o mientras core no respondía.
  - El nombre no es una decisión de acceso: si core no responde queda `null` y la operación sigue (no hay
    `upstream_unavailable`). Caché en memoria: 10 min por nombre, 1 min para un id que core no conoce, 30 s sin
    consultar después de un fallo, 2 s de espera como máximo.
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

## Notas de implementación

Decisiones tomadas al implementar (2026-10-26) en casos que este documento no cubría. Criterio: nunca exponer un
`restricted`; ante la duda, lo más cerrado.

**Permisos**

- **Rol `admin`:** es `person-internal` con `admin` en sus roles (`users.roles` por el bus, `actor.roles` con la api).
  Una persona con `external-user` y `admin` sigue siendo externa (gana la clase más restrictiva, como en core).
- **Carpetas, consultas:** `folders.list` y `folders.get` siguen abiertas a las personas internas, que ven **todas** las
  carpetas (`effectivePermission`: `write` para `user`, `manage` para `admin` y servicios). El daemon de sync de una
  persona las necesita para saber dónde puede crear archivos. Los externos siguen sin ver carpetas. Solo los comandos
  `folders.*` quedan para `admin` y servicios. `include: [acl]` es solo para `admin` y servicios.
- **Escritura en carpetas:** una persona interna escribe documentos en cualquier carpeta (`documents.new` con `folderId`,
  `documents.{id}.move`). Ya no hay `permission_denied` por carpeta.
- **Links:** `shares.list` lo ve cualquier persona interna que lea el documento; un externo recibe `permission_denied`
  (nunca administra links). `shares.{id}.revoke` lo hace cualquier persona interna que lea el documento; sobre un link de
  un documento que no lee responde `share_not_found`. `shares.resolve` y `shares.attachment` responden `share_not_found`
  si el documento es `restricted`, aunque el link siga activo (defensa en profundidad: pasar a `restricted` ya los revoca).
- **`documents.new` con un `id` existente:** solo devuelve el documento (reintento idempotente) si quien reintenta lo
  puede leer; si no, `already_exists` sin datos.
- **`restricted` como filtro:** quien no lo lee recibe lista vacía (nunca `invalid_fields`).
- **Relaciones para externos:** en `clients` y `projects` de cada ítem, y en `include: [relations]`, un externo recibe
  solo los proyectos de sus `project-permissions` y ninguna lista de clientes, requisitos ni tareas (son ids internos,
  como en D-11). Antes de 1.3.0 `include: [relations]` devolvía todo a un externo.

**Ubicación generada**

- Un proyecto sin cliente en core y sin `jiku.clients` va a `/clientes/sin-cliente/<proyecto>/`.
- "El primero de la lista" es el primero **tal como está escrito** en el frontmatter (las relaciones se guardan
  ordenadas, pero el servicio recuerda el primero de `jiku.projects` y de `jiku.clients`).
- Un nombre que no está en core (catálogo cacheado 5 min) usa `proyecto-<id>` o `cliente-<id>`; un slug vacío, lo mismo.
- Las carpetas generadas se crean heredando (como el servicio en nombre de quien escribe: `createdBy` es esa persona) y
  emiten `folder.created`. Creaciones concurrentes de la misma carpeta se resuelven por el índice único de rutas.
- Sin `fileName` y sin título (ni `title`, ni `#`), el nombre es `documento.md` y el título, `documento`.
- La respuesta de `documents.new` trae además `folderId` y `fileName`.

**`relations.update`**

- Payload `{ baseVersion, add?, remove?, sessionId? }`. **Sin `via`**: el origen sale de la clase del caller (`human`
  para una persona, `service` para un servicio). `sessionId` permite asignar mientras se edita con el lock propio.
- `add` y `remove` vacíos (o ausentes) son `invalid_fields`; un id en los dos también. Máximo 100 ids por tipo.
- El cliente de un proyecto agregado se agrega salvo que esté en `remove.clients`. Quitar un proyecto no quita su
  cliente.
- La versión es `kind: update` con `message` automático (por lo tanto protegida de la compactación): "Cliente asignado:
  Acmé S.A.; Proyecto asignado: EXO - Videoconferencia". Con un nombre desconocido usa `#<id>`.
- Reescribe solo las líneas del bloque `jiku:`; los comentarios dentro de ese bloque se pierden. Un frontmatter escrito
  como un solo mapa en estilo flujo (`{title: x}`) se reescribe entero.

**`suggest-relations`**

- Respuesta `{ items: [{ projectId, clientId, score, reason }] }`, hasta 5, `score` entre 0 y 1 (2 decimales).
- **`projectId` es `null` en un candidato solo de cliente** (nunca `0`): aparece cuando el texto nombra a un cliente
  y ningún proyecto de ese cliente coincidió. `clientId` es `null` si el proyecto no tiene cliente.
- Términos de un proyecto: el nombre, sus partes separadas por ` - `, `:`, `/`, `|`, y el código. Se ignoran términos de
  menos de 3 letras, solo numéricos o genéricos (`proyecto`, `general`, `interno`, `cliente`, `sistema`, `app`, `web`,
  `soporte`, `mantenimiento`, `desarrollo`). Título: 0,7; cada mención en el contenido: 0,06 (hasta 5); mención del
  cliente del proyecto: +0,1 en el título o +0,05 en el contenido. Coincidencia por palabra completa, sin acentos ni
  mayúsculas.
- Requiere leer el documento (no incluye borrados). Un externo recibe `permission_denied` (aprendería nombres de
  proyectos que no ve).

**Filtros y nombres**

- `hasClient` mira solo `jiku.clients` (no deduce el cliente de un proyecto). `hasProject: true` es el complemento
  exacto de "Sin proyecto" (directo, por requisito o por tarea).
- `projectMatch` se agrega con `projectId` en el filtro de primer nivel o dentro de `or`.
- Los ítems de `documents.search` traen además `createdBy`, `createdByName`, `updatedByName` y `visibility`.
- `document_locked` trae en `errorDetails` `holder`, `holderName`, `via` y `expiresAt`.
- El nombre de un servicio es el de su fila en `users` de core; el de la api sin `actor`, `api`; el de jiku-docs
  mismo, `jiku-docs`. Los nombres se guardan al escribir y no se actualizan si la persona cambia de nombre.
- Labels de `meta.describe` para `visibility`: Interno, Visible para el cliente, Restringido.

**Notificaciones**

- Cuando un documento pasa a `restricted`, cada `user` recibe `access.changed` sin detalle; cuando deja de ser `public`
  (a `internal` o `restricted`), lo mismo para cada externo con permiso sobre alguno de los proyectos del documento.
  Los admins no la reciben (siguen leyéndolo y reciben el `document.updated`).

**Migración de datos**

- Corre en `serve`, después de las migraciones SQL y **antes** de registrar los servicios micro: el servicio nunca
  atiende con el modelo nuevo sobre datos sin migrar. Si falla, el proceso no arranca.
- Se aplica **una sola vez** (marca `migration:p1-visibility` en `service_state`): un cambio posterior de un admin no se
  deshace al reiniciar. Es idempotente ante un corte: primero fija la columna `visibility` y revoca los links de todos
  los documentos en una transacción y guarda el plan; después escribe las versiones una por una y saltea las que ya
  dicen la visibilidad nueva.
- "Puede leer una persona con rol `user`" se evalúa con un principal que solo tiene el rol `user`: una regla para un
  `user-id` puntual no cuenta, y ese documento pasa a `restricted` (esa persona pierde el acceso; es el lado seguro).
  Un documento en una carpeta borrada también pasa a `restricted`. Se migran también los documentos borrados.
- La versión de la migración la escribe el servicio jiku-docs (`via: service`, protegida), con `message`
  "Migración P-1: visibilidad restricted (…)" o "… internal (…)", y actualiza `updatedAt`/`updatedBy` del documento. El
  `document.updated` lleva `changes.visibility` desde lo que decía el frontmatter.
- No respeta locks (es una corrección del servicio); quien tenía el lock recibe `version_conflict` en el siguiente guardado.
- Un frontmatter que no se puede editar deja el documento con la columna ya cambiada (lo seguro) y un aviso en el log.
- Los nombres de autor existentes se completan en segundo plano desde `users.list` de core (marca
  `migration:p1-names`), reintentando cada minuto si core no responde. Un id que core no conoce queda con nombre `null`.

**Importador de HedgeDoc**

- Las notas sin mapeo (las de `/importado/`) se importan con `visibility: restricted`; las mapeadas a otra carpeta,
  `internal`. La carpeta ya no decide nada.

**Para la web y la api (pendientes)**

- Resolver con core los nombres de `clients` y `projects` de cada ítem.
- "Editado por" y el lock: usar `updatedByName` y `lock.holderName` (pueden ser `null` en filas viejas de ids que core no
  conoce).
- Ante `access.changed` no hay detalle: recargar la lista.
- El selector de visibilidad del alta: Interno (default) · Visible para el cliente · Restringido (este último solo para
  admin; la api puede ocultarlo a un `user`, el servicio igual responde `permission_denied`).
