# jiku-docs: compactación del historial de versiones

Oct 24, 2026 · @Lautaro Alvarez · revisado 2026-10-25 contra la implementación y con las decisiones del dueño (ver sección 8)

## 1. Resumen

Cada guardado de un documento crea una versión nueva, y con autoguardado eso genera miles de versiones por día. Se mantiene ese comportamiento, porque sostiene la actualización en tiempo real, y se agrega un proceso diario que compacta el historial viejo según una política de retención.

Este documento complementa la especificación técnica de `jiku-docs` v1. Donde contradice algo de ella (en particular, la afirmación de que todas las versiones se conservan), manda este documento.

### Problema

VS Code, Obsidian y el daemon `jiku docs sync` guardan cada pocos segundos mientras alguien escribe. Con 10 personas editando 4 horas por día y un guardado cada 10 segundos, el sistema acumula unas 14.000 versiones diarias (alrededor de 140 MB por día con documentos de 10 KB). El costo de almacenamiento es bajo, pero el historial se vuelve ilegible, la tabla de versiones crece sin techo y restaurar algo significa buscar entre miles de entradas casi idénticas.

### Decisión

- **Escritura y tiempo real sin cambios.** Cada guardado sigue creando una versión, sigue incrementando `version` y sigue disparando `document.updated`. El control de concurrencia con `baseVersion` no cambia.
- **Compactación diaria.** Un proceso del servicio borra versiones intermedias según su antigüedad, conservando siempre las que tienen valor (sección 2).
- **Cambios de contrato mínimos.** El historial puede tener huecos en la numeración y hay un código de error nuevo, `version_pruned`.

Con el escenario de arriba, de las \~14.000 versiones diarias sobreviven unas pocas decenas después del primer día, y cada entrada del historial viejo corresponde a una sesión de trabajo de alguien.

## 2. Política de retención

Las versiones se adelgazan por tramos de antigüedad; las versiones protegidas y la versión actual no se borran nunca.

### Tramos

| Antigüedad de la versión | Qué se conserva |
| --- | --- |
| Menos de 24 h | Todas |
| De 24 h a 30 días | La última versión de cada bloque de edición |
| Más de 30 días | La última versión de cada día calendario |
| Cualquiera | Las versiones protegidas y la versión actual |

La antigüedad se mide sobre `created_at` de la versión, en el momento en que corre la compactación. Los días calendario se calculan en la zona horaria configurada (default `America/Argentina/Buenos_Aires`).

### Bloque de edición

Un bloque de edición es una secuencia de versiones consecutivas de un documento que cumplen las tres condiciones:

1. Tienen el mismo `author`.
2. Tienen el mismo `via` (`human`, `agent` o `service`).
3. Entre cada versión y la siguiente pasaron menos de 30 minutos.

El bloque termina cuando cambia el autor, cambia el origen o hay una pausa de 30 minutos o más. De cada bloque se conserva la última versión, que representa el estado del documento al terminar esa sesión de trabajo.

Ejemplo: Ana guarda 200 versiones entre las 10:00 y las 11:40, Bruno guarda 50 entre las 11:45 y las 12:10, y Ana vuelve a guardar 30 entre las 15:00 y las 15:20. Después del primer día quedan tres versiones: la de Ana a las 11:40, la de Bruno a las 12:10 y la de Ana a las 15:20. Después de 30 días queda solo la de las 15:20 (la última del día), salvo que alguna esté protegida.

### Versiones protegidas

Una versión protegida no se borra en ningún tramo.

| Versión | Motivo |
| --- | --- |
| La versión actual del documento | Es el estado vigente |
| La primera versión (`kind = create`) | Registra el origen del documento |
| Versiones con `message` | Quien escribe un mensaje marca un punto importante ("versión enviada al cliente") |
| Versiones con `via` `service` | Son cambios automáticos (importación, revisión de relaciones) que conviene poder auditar uno por uno |
| Restauraciones (`kind = restore`) | Registran que alguien volvió atrás |
| Versiones fijadas por un link compartido activo (`mode = pinned`, sin revocar ni vencer) | El link tiene que seguir mostrando esa versión |

Las versiones con `via` `agent` **no** están protegidas por su origen: siguen la regla de bloques como las humanas. Como un cambio de `via` cierra el bloque, cada sesión de un agente conserva su última versión y nunca se mezcla con la edición de una persona. Si una versión de agente lleva `message`, queda protegida por esa regla. Así un agente que guarda seguido (por ejemplo con `jiku docs sync` y `JIKU_VIA=agent`) no vuelve a llenar el historial de versiones imborrables.

La protección por link compartido es dinámica: cuando el link vence o se revoca, la versión vuelve a ser candidata en la siguiente compactación. Las demás protecciones son permanentes.

Como las versiones con `message` nunca se compactan, para marcar un punto importante basta con guardar con mensaje, por ejemplo `jiku docs put <id> <archivo> --base-version <n> --message "Versión enviada al cliente"`.

### Lo que no cambia

- Los documentos borrados se eliminan definitivamente a los 30 días con todas sus versiones, como antes. La compactación no los procesa.
- El índice de búsqueda solo contiene la versión actual, así que no se ve afectado.
- El changelog y el feed `documents.changes` conservan sus registros; un evento viejo puede mencionar una versión que ya fue compactada.

## 3. Proceso de compactación

Un proceso del propio servicio `jiku-docs` corre una vez por día, procesa documento por documento en transacciones cortas y borra los objetos de S3 recién después de confirmar el borrado en la base.

### Ejecución

- Corre una vez por día a partir de la hora configurada (`DOCS_HISTORY_COMPACTION_HOUR`, default 3) en la zona configurada, con el mismo mecanismo que las demás tareas periódicas del servicio (`JobDue` sobre `service_state`). Si hay varias réplicas del servicio, solo una ejecuta: toma un advisory lock de PostgreSQL (`pg_try_advisory_lock`) y las demás lo saltean.
- Procesa los documentos que cumplen alguna de estas condiciones desde su última compactación (los que nunca se compactaron, siempre):
  - **Actividad nueva:** `documents.updated_at > documents.last_compacted_at`.
  - **Link fijado que venció o se revocó:** un link compartido con `mode = pinned` cuyo `expires_at` o `revoked_at` es posterior a `last_compacted_at`.
  - **Cruce de umbral:** alguna versión no protegida (y que no es la actual) cruzó el umbral de 24 h o el de 30 días entre la última compactación y ahora. Sin esta regla, la sesión de edición del día anterior a la corrida (todas sus versiones tienen menos de 24 h al compactar) quedaría entera para siempre si nadie vuelve a editar el documento; con ella, se compacta en la corrida siguiente al cruce de 24 h (caso 24).
- Un documento sin actividad nueva cuyas versiones no cruzaron ningún umbral no se procesa (caso 11).
- Recorre los documentos en lotes (default 100), ordenados por `id`, para poder retomar donde quedó si se corta.
- También se puede ejecutar a demanda con un comando de mantenimiento: `jiku-docs history compact [--document <id>] [--dry-run]`. Con `--dry-run` informa qué borraría sin borrar nada.

### Algoritmo por documento

1. Abrir transacción y bloquear la fila del documento con `SELECT ... FROM documents WHERE id = $1 FOR UPDATE`.
2. Leer sus versiones ordenadas por `version` y el conjunto de versiones fijadas por links activos.
3. Marcar como conservadas las versiones protegidas (sección 2).
4. Agrupar todas las versiones del documento en bloques de edición. Para las versiones del tramo de 24 h a 30 días, conservar la última de cada bloque.
5. Para las versiones de más de 30 días, partir de las que dejó la regla de bloques, agruparlas por día calendario y conservar la última de cada día.
6. Marcar como conservadas todas las versiones de menos de 24 h.
7. Borrar las filas de `document_versions` no conservadas, guardando su `storage_key` en `pruned_objects`.
8. Actualizar `last_compacted_at` y `pruned_versions_count` del documento, y confirmar la transacción.

Los bloques se calculan sobre todas las versiones del documento, no solo las del tramo, para que un bloque que cruza el límite de 24 h no se parta en dos. La regla diaria se aplica sobre lo que dejó la regla de bloques, así que el resultado es como máximo una versión por día más las protegidas.

El proceso es idempotente: correrlo dos veces seguidas no borra nada más, porque las versiones que conserva cumplen las mismas reglas en la segunda pasada.

### Borrado en S3

El borrado de objetos ocurre fuera de la transacción, en dos pasos:

1. Después del commit, el proceso toma las filas de `pruned_objects` y borra los objetos en S3 en lotes (`DeleteObjects`, hasta 1.000 claves por llamada).
2. Cada fila se elimina de `pruned_objects` cuando su objeto se borró o ya no existía.

Si el proceso se corta entre el commit y el borrado, las filas quedan en `pruned_objects` y la siguiente ejecución las retoma. Nunca queda una fila de versión apuntando a un objeto borrado; en el peor caso queda un objeto sin fila por un tiempo.

El barrido general de huérfanos (`PurgeOrphans`), que hoy lista todo `docs/` y consulta la base por cada objeto, no escala con este volumen y se superpone con `pruned_objects`. Pasa a ser semanal y paginado con un cursor persistido en `service_state`; el borrado de versiones compactadas lo hace siempre `pruned_objects`.

### Concurrencia con las escrituras

- La compactación nunca borra la versión actual, así que no interfiere con `baseVersion` ni con los locks de edición.
- El `FOR UPDATE` sobre el documento serializa la compactación con `update`, `restore` y `shares.new` del mismo documento. Esas operaciones esperan unos milisegundos como máximo, porque la transacción de compactación de un documento no hace llamadas externas.
- Si un `restore` o un `shares.new` con `mode: pinned` pide una versión que la compactación acaba de borrar, responde `version_pruned`.

### Eventos

La compactación no emite eventos ni notificaciones: no cambia el estado vigente de ningún documento. Queda registrada en logs y métricas (sección 6).

## 4. Cambios en el modelo de datos y storage

Se agregan dos columnas a `document_versions`, dos a `documents` y una tabla para el borrado diferido en S3.

### Migración

```sql
-- Origen de cada versión, para proteger creaciones y restauraciones.
-- `documents.move` no crea versión (solo emite document.moved), así que no hay kind 'move'.
ALTER TABLE document_versions
  ADD COLUMN kind text NOT NULL DEFAULT 'update'
    CHECK (kind IN ('create','update','restore'));

-- Protección permanente, calculada al insertar (create, restore, message, via service)
ALTER TABLE document_versions
  ADD COLUMN protected boolean NOT NULL DEFAULT false;

-- Datos existentes
UPDATE document_versions SET kind = 'create' WHERE version = 1;
UPDATE document_versions SET kind = 'restore' WHERE message LIKE 'Restaurado desde la versión %';
UPDATE document_versions SET protected = true
  WHERE kind IN ('create','restore') OR message IS NOT NULL OR via = 'service';

-- Control de la compactación por documento
ALTER TABLE documents
  ADD COLUMN last_compacted_at timestamptz,
  ADD COLUMN pruned_versions_count integer NOT NULL DEFAULT 0;

CREATE INDEX documents_pending_compaction
  ON documents (id)
  WHERE deleted_at IS NULL AND (last_compacted_at IS NULL OR updated_at > last_compacted_at);

-- Objetos de S3 pendientes de borrar después del commit
CREATE TABLE pruned_objects (
  storage_key text PRIMARY KEY,
  document_id text NOT NULL,
  version     integer NOT NULL,
  pruned_at   timestamptz NOT NULL DEFAULT now()
);
```

Las restauraciones existentes se identifican por el mensaje fijo que escribe el servicio al restaurar (`Restaurado desde la versión N`).

### Reglas al escribir

| Comando | `kind` | `protected` |
| --- | --- | --- |
| `documents.new` | `create` | `true` |
| `documents.{id}.update` | `update` | `true` si trae `message` o si `via` es `service` |
| `documents.{id}.restore` | `restore` | `true` |

La protección por link compartido fijado no se guarda en la columna: se calcula en cada compactación a partir de `shares` (activos, `mode = 'pinned'`).

### Storage

- Los objetos de versiones compactadas se borran de `jiku-docs-content` (sección 3).
- El versionado del bucket se mantiene como red de seguridad, con una regla de ciclo de vida que expira las versiones no actuales de objetos a los 7 días. **A verificar:** que DigitalOcean Spaces soporte `NoncurrentVersionExpiration` (MinIO, en local, lo soporta). Si no lo soporta, definir otra red de seguridad antes de activar la compactación en producción. Estado al 2026-10-25: la documentación pública de Spaces no lo confirma ni lo descarta (ver README, sección "Bucket y ciclo de vida"); `jiku-docs storage init` aplica la regla, la relee y avisa si el proveedor no la guardó. Así, un borrado por error de la compactación se puede recuperar durante una semana, y el espacio se libera de verdad pasado ese plazo.
- El respaldo diario de la base no cambia. Restaurar un backup anterior a una compactación deja filas apuntando a objetos que pueden haberse borrado hace más de 7 días; el proceso de restauración debe verificar los objetos y descartar las filas sin objeto.

## 5. Cambios en contratos y en jiku-go

Los cambios son compatibles dentro de `v1`: un campo nuevo en las respuestas y un código de error nuevo, que los clientes ya deben tolerar según las reglas del protocolo.

### Código de error nuevo

| Código | Cuándo | `errorDetails` |
| --- | --- | --- |
| `version_pruned` | Se pide, restaura o fija una versión que fue compactada | `requestedVersion`, `previousAvailable` y `nextAvailable` (las versiones conservadas más cercanas, o `null`) |

Una versión que nunca existió (mayor que la actual) sigue respondiendo `document_not_found`, como hoy.

Un caller `person-external` **nunca** recibe `version_pruned`: para él cualquier versión que no sea la actual sigue siendo `document_not_found`, sin `errorDetails`, para no revelar el historial.

### `jiku-docs-queries.yaml`

| Consulta | Cambio |
| --- | --- |
| `documents.get` con `version` | Puede responder `version_pruned` (nunca a externos) |
| `documents.versions.list` | La numeración puede tener huecos. Se agrega `kind` (`create`, `update`, `move`, `restore`) y `protected` al esquema `Version`, y `kind` y `protected` como filtros |
| `documents.get` (include) | El documento agrega `prunedVersionsCount`: cuántas versiones se compactaron |

### `jiku-docs-commands.yaml`

| Comando | Cambio |
| --- | --- |
| `documents.{id}.restore` | Si `fromVersion` fue compactada, responde `version_pruned` |
| `shares.new` con `mode: pinned` | Si `version` fue compactada, responde `version_pruned` |

### `jiku-docs-events.yaml`

Sin cambios. La compactación no emite eventos.

### Especificación técnica v1

Actualizar en la especificación:

- Sección 2, decisión 1: "Cada versión es un objeto inmutable en S3" pasa a "Cada versión es un objeto inmutable en S3 hasta que la compactación la elimina".
- Sección 5, catálogo de errores: agregar `version_pruned`.
- Sección 11, tablas y storage: agregar la migración de la sección 4 de este documento.
- Sección 12, observabilidad: agregar las señales de la sección 6.
- Agregar una referencia a este documento como política de retención del historial.

### jiku-go

| Elemento | Cambio |
| --- | --- |
| Constante de error | `CodeVersionPruned` |
| `jiku docs history <id>` | Muestra `kind` y una marca en las versiones protegidas. Al final, una línea con la cantidad de versiones compactadas. Flag `--protected` para listar solo las protegidas |
| `jiku docs get <id> --version N` | Ante `version_pruned`, informa las versiones conservadas más cercanas y sale con código distinto de cero |
| `jiku docs restore` | Mismo manejo de `version_pruned` |
| `jiku docs put --message` | Sin cambios de comportamiento; la ayuda del comando explica que guardar con mensaje protege la versión para siempre |
| Skill de agentes | Agregar la regla: al terminar un trabajo importante, guardar con `--message` para que la versión quede protegida (las versiones de agente sin mensaje se compactan por bloques) |

### Interfaz web (pendiente, fuera de esta etapa)

Se implementa después, como REQ de jiku por grava-workflow. Esta etapa cubre solo el servicio y jiku-go. Hoy la web no tiene pantalla de historial.

El historial de un documento muestra las versiones conservadas con su autor, fecha y tipo, distingue las protegidas e indica cuántas versiones intermedias se compactaron. Al guardar, ofrece un campo opcional de mensaje con la aclaración de que esa versión queda protegida.

## 6. Configuración y observabilidad

Todos los umbrales de la política son parámetros del servicio, con los valores de este documento como default.

### Parámetros

| Variable | Default | Descripción |
| --- | --- | --- |
| `DOCS_HISTORY_COMPACTION_ENABLED` | `true` | Activa el proceso diario |
| `DOCS_HISTORY_COMPACTION_HOUR` | `3` | Hora del día (en `DOCS_HISTORY_TIMEZONE`) a partir de la cual corre la ejecución diaria |
| `DOCS_HISTORY_TIMEZONE` | `America/Argentina/Buenos_Aires` | Zona para la hora de ejecución y los días calendario |
| `DOCS_HISTORY_KEEP_ALL` | `24h` | Ventana en la que se conservan todas las versiones |
| `DOCS_HISTORY_KEEP_BLOCKS` | `30d` | Hasta qué antigüedad se conserva una versión por bloque de edición |
| `DOCS_HISTORY_BLOCK_GAP` | `30m` | Pausa que cierra un bloque de edición |
| `DOCS_HISTORY_BATCH_SIZE` | `100` | Documentos por lote |
| `DOCS_HISTORY_S3_DELETE_BATCH` | `1000` | Claves por llamada a `DeleteObjects` |

### Observabilidad

| Señal | Detalle |
| --- | --- |
| Log por ejecución | Inicio y fin, documentos procesados, versiones borradas, bytes liberados, duración, errores |
| Log por documento | Solo con `--dry-run` o nivel debug: versiones conservadas y borradas con el motivo |
| Métricas | `docs_history_versions_pruned_total`, `docs_history_bytes_freed_total`, `docs_history_run_duration_seconds`, `docs_history_pruned_objects_pending` (filas en `pruned_objects`) |
| Alertas | Una ejecución que falla o no corre en 48 h; `pruned_objects` con filas de más de 24 h (el borrado en S3 no avanza) |

## 7. Casos de prueba

Los casos cubren cada regla de la política, los bordes entre tramos y la seguridad del borrado. Todos se pueden escribir con fechas de versión fijadas a mano y el proceso ejecutado a demanda.

| # | Escenario | Resultado esperado |
| --- | --- | --- |
| 1 | 300 versiones de un mismo autor en 2 horas, todas de hace menos de 24 h | No se borra ninguna |
| 2 | El mismo caso, con las versiones de hace 3 días | Queda solo la última del bloque y la actual |
| 3 | Ana edita, Bruno edita, Ana vuelve a editar el mismo día, hace 3 días | Quedan tres versiones, la última de cada bloque |
| 4 | Un autor con una pausa de 31 minutos entre dos tandas, hace 3 días | Quedan dos versiones, una por bloque |
| 5 | El mismo autor alterna `via: human` y `via: agent` sin pausas, hace 3 días | Cada cambio de `via` cierra el bloque; queda la última versión de cada bloque, humano o agente |
| 6 | Varios bloques en un mismo día, hace 40 días | Queda la última versión de ese día |
| 7 | Un bloque que empieza hace 25 h y termina hace 23 h | El bloque se evalúa completo; no se borra ninguna versión del bloque que tenga menos de 24 h |
| 8 | Versión intermedia con `message`, hace 60 días | Se conserva |
| 9 | Restauración hace 60 días | Se conserva |
| 10 | Versión fijada por un link activo, hace 60 días | Se conserva; tras revocar el link, la siguiente compactación la borra si no está protegida por otro motivo |
| 11 | Documento sin cambios desde la última compactación | No se procesa |
| 12 | Ejecutar la compactación dos veces seguidas | La segunda no borra nada |
| 13 | El proceso se corta después del commit y antes de borrar en S3 | Las filas quedan en `pruned_objects`; la siguiente ejecución borra los objetos |
| 14 | Dos réplicas inician la compactación a la vez | Solo una ejecuta (advisory lock) |
| 15 | `documents.get` con una versión compactada | `version_pruned` con las versiones conservadas más cercanas |
| 16 | `restore` desde una versión compactada | `version_pruned`; no se crea versión nueva |
| 17 | `shares.new` con `mode: pinned` sobre una versión compactada | `version_pruned` |
| 18 | Un `update` llega mientras se compacta el mismo documento | El `update` espera el commit de la compactación y se aplica normalmente; la versión actual nunca se borra |
| 19 | `--dry-run` | Informa lo que borraría y no modifica filas ni objetos |
| 20 | Documento borrado lógicamente | La compactación no lo procesa |
| 21 | Un externo pide con `documents.get` una versión compactada de un documento público | `document_not_found`, sin `errorDetails` |
| 22 | Versión de agente con `message`, hace 60 días | Se conserva |
| 23 | Versión de `service` sin mensaje, hace 60 días | Se conserva |
| 24 | Sesión del día anterior sin actividad posterior | Se compacta en la corrida siguiente al cruce de 24 h; las corridas siguientes no lo procesan hasta otro cruce |

## 8. Revisión contra la implementación (2026-10-25)

Cambios acordados respecto del borrador del 2026-10-24, y decisiones del dueño del mismo día:

1. Las versiones de `via: agent` siguen la regla de bloques; solo `service` queda protegido por origen.
2. Un externo nunca recibe `version_pruned`.
3. No existe `kind = move`: `documents.move` no crea versión.
4. Las restauraciones existentes se marcan por el mensaje fijo del servicio.
5. `PurgeOrphans` pasa a semanal y paginado; el borrado de versiones compactadas lo hace `pruned_objects`.
6. Verificar el soporte de `NoncurrentVersionExpiration` en DigitalOcean Spaces.
7. Programación con hora del día (`DOCS_HISTORY_COMPACTION_HOUR`), como el resto de las tareas del servicio, en lugar de cron.
8. Pendiente para otra etapa: la pantalla de historial y el campo de mensaje en la web (REQ en jiku).
9. Fuera de alcance, anotado: el volumen de eventos, changelog y notificaciones por guardado no cambia con la compactación.
10. Criterio de selección confirmado: además de los documentos con actividad nueva o con un link fijado que venció o se revocó, se procesan los que tienen alguna versión no protegida que cruzó el umbral de 24 h o el de 30 días desde la última compactación (sección 3, caso 24).
