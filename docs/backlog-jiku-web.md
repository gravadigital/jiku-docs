# Backlog para la web de Jiku (pendientes de jiku-docs)

Lista de trabajo acumulado para el monorepo de jiku (`web`, `opus-web`, `api`), que se ejecuta con grava-workflow (un REQ por tema, sobre la rama `feat/jiku-docs`, sin push).

Estados:
- **aprobado**: el dueño lo pidió y se puede ejecutar;
- **a confirmar**: lo propuse yo y espera el visto bueno;
- **hecho**.

| # | Tema | Origen | Estado |
| --- | --- | --- | --- |
| 1 | Pantalla de historial de versiones del documento en `web`, y campo de mensaje opcional al guardar en el editor | `docs/compactacion-historial-v1.md`, sección 5 | aprobado, va dentro de P-1 (botón de historial de la pantalla 06) |
| 2 | CSP de la ruta pública `/p/docs/*`: sumar a `img-src` el origen del storage configurado. En local las imágenes no cargan porque el storage es http | Prueba e2e en navegador | a confirmar |
| 3 | CORS de la api solo bajo `/api`: hoy `/p/docs/*` responde `Access-Control-Allow-Origin: *` | Prueba e2e por el bus. Era parte del REQ-018 descartado; se pide sin el cambio de tokens | aprobado, va en P-2 |
| 4 | Log `warn` en core por cada `files.request-upload` de jiku-docs (`resolve-actor.ts`): bajarlo a `debug` para callers de clase conector | Prueba e2e por el bus. Era parte del REQ-018 descartado | aprobado, va en P-2 |
| 5 | opus-web: un externo sin permiso sobre un proyecto ve el tablero vacío y "No hay proyectos disponibles" | Prueba e2e en navegador; no es del módulo docs | a confirmar |
| 6 | Textos de interfaz de docs propuestos sin fuente en la UX: errores de `permission_denied` al crear un link, fallo al revocar, fallo al tomar el lock y el contador "Documentación (5+)" | Informe de REQ-016 | a confirmar |
| 7 | Componente `alert` en el Design System: hoy `DocsNotice` está implementado de forma local | Informe de REQ-016 | a confirmar |
| 8 | Altas sin `id` en las rutas de la api: no se pueden reintentar sin duplicar | Informe de REQ-016 | a confirmar |
| 9 | Documentos de más de 256 KiB: no se pueden crear ni editar desde la web, que no tiene subida por URL | Informe de REQ-016 | a confirmar |
| 10 | **Bug JIKU-WEB-1:** el visor interno (`/docs/{id}`) muestra el frontmatter como texto. La UX (`docs/ux/surfaces/web/screens/documento.md:160`) dice que no se muestra, y `MarkdownViewer mode="document"` no lo quita. Propuesta: quitarlo antes de renderizar, como opus-web con `stripFrontmatter` | e2e en navegador con usuario humano | aprobado, va dentro de P-1 |
| 11 | **Bug JIKU-WEB-2:** "Crear y editar" en "Nuevo documento" deja `/docs/{id}?editar=1` con el botón Editar en spinner y nunca toma el lock. Se reprodujo 5 de 5 veces; con recarga completa funciona. Causa probable: carrera entre `onClose()` + `router.push` en `NewDocumentDialog` y el efecto de `DocumentView` (`router.replace` + `session.start`) | e2e en navegador con usuario humano | aprobado, va dentro de P-1 |
| 13 | web, detalle de requisito: la tabla de la card "Documentación" desborda la card y corta la columna Ruta a 1440 px | Relevamiento de capturas | aprobado, va dentro de P-1 |
| 14 | web, detalle de requisito: las pills de estado, tipo y prioridad se anuncian las tres como "Estado: …" en lectores de pantalla | Relevamiento de capturas | aprobado, va en P-2 |
| 15 | web, listado de tareas: una tarea muestra "Vencido hace: 32666 día(s)" (fecha inválida o nula) | Relevamiento de capturas | aprobado, va en P-2 |
| 16 | 404 de web y opus-web: es la página por defecto de Next.js, sin estilos ni navegación | Relevamiento de capturas | a confirmar |
| 17 | opus-web a 390 px: sin navegación ni "Nuevo requisito"; la vista Columnas se ignora; tocar una card abre la página en lugar del modal | Relevamiento de capturas | a confirmar |
| 18 | opus-web: el externo que abre un requisito interno ve "Error al cargar el requisito" con "Reintentar", sin header ni explicación | Relevamiento de capturas | aprobado, va en P-2 |
| 12 | La web muestra "por otra persona" en el lock cuando el que edita es el propio usuario o el servicio, porque `persons` no resuelve esos ids | e2e en navegador | aprobado, va dentro de P-1 |

## Agrupación para ejecutar

- **P-1** (jiku-docs y jiku-go primero, después REQ de la web): documentación sin carpetas, con los ítems 1, 10, 11, 12 y 13 y la card del detalle de proyecto.
- **P-2** (REQ de arreglos chicos en jiku): ítems 3, 4, 14, 15 y 18.
- **No se aplican por ahora:** 2, 5, 6, 7, 8, 9, 16 y 17.

## Pedidos del dueño

### P-1. Documentación sin carpetas: "propuesta B" (aprobado, 2026-10-26)

**Referencia:** `_research/jiku-documentacion-propuesta-b/` en jiku-docs (README, 9 pantallas en HTML en modo claro y oscuro, capturas a 1440 px, `css/jiku.css` con tokens y componentes). Las pantallas 08 (edición) y 09 (compartir) vienen de la variante A: hay que pasarlas a Cliente / Proyecto.

**Idea:** no se navega por carpetas. Todo se encuentra desde un solo buscador que va sumando filtros:
- proyecto, cliente, tema, persona;
- texto con Enter;
- "Sin proyecto", "Sin cliente" y "Sin tema".

La casilla "Solo visibles para el cliente" queda aparte del buscador. Hay vistas guardadas. Cada documento tiene cliente, proyecto (o "Sin proyecto") y temas de una lista controlada. La ruta se genera sola y solo se muestra como dato técnico.

**Pantallas:**
1. **Inicio:** vistas guardadas, "En mis proyectos activos" y "Editados recientemente", con "Cliente · Proyecto" en lugar de la ruta.
2. **Sugerencias agrupadas** del buscador, con atajos de teclado.
3. **Resultados** con fragmentos resaltados, conteo y paginado.
4. **Documentos de un proyecto:** destino de "Ver todos" desde el detalle de proyecto, con "Volver al proyecto".
5. **"Sin proyecto":** "Asignar proyecto" o "Asignar sugerido" según la ruta.
6. **Documento en lectura:** cabecera con Cliente / Proyecto y temas. A la derecha, índice, "Vinculado a", propiedades (con la ruta como "Ruta de almacenamiento") y adjuntos.
7. **"Nuevo documento":**
   - título;
   - cliente y proyecto con búsqueda;
   - temas de la lista común, con "Proponer tema nuevo";
   - visibilidad y plantilla;
   - sin carpeta, con la ruta generada a la vista.
8. **Edición** con vista previa y aviso de bloqueo.
9. **Compartir.**

**Decisiones del dueño (2026-10-26):**

1. **Alcance visual:** solo el contenido de las pantallas de Documentación. El sidebar y la barra superior quedan como hoy (sin barra superior ni búsqueda global ⌘K).
2. **Documentos de un proyecto:** son los vinculados por relaciones (frontmatter `jiku:`). La ubicación no importa.
3. **Permisos sin carpetas:**
   - **Personas internas** (`user`, `admin`): leen y escriben todos los documentos `internal` y `public`. Ninguna carpeta ni ACL limita a una persona interna; por ejemplo, cualquiera puede editar un documento de infra.
   - **Externos:** como hoy. Solo ven los documentos `public` vinculados a proyectos de sus `project-permissions`.
   - **Visibilidad nueva `restricted`:** solo `admin`, y los servicios como hoy. Se elige en el mismo selector de visibilidad del alta (Interno · Visible para el cliente · Restringido), con Interno por defecto.
   - **Garantía exigida:** un `user` o un externo **nunca** accede a un documento `restricted` por ninguna vía: `list`, `get`, `search`, `changes`, `versions`, adjuntos, notificaciones, links compartidos, sync, ni sugerencias del buscador. Tiene que estar cubierto por tests en cada una.
4. **"Asignar proyecto":** se mantiene la pantalla 05 "Sin proyecto".
   - "Asignar proyecto" solo agrega la relación, sin mover nada.
   - "Asignar sugerido" deduce el proyecto del título y del contenido, no de la carpeta.
5. **Temas libres**, con sugerencia de los existentes. No hay lista controlada ni "Proponer tema nuevo".
6. **Vistas guardadas:** se eliminan. Ni la fila "Vistas:" ni el botón "Vistas".
7. **Plantillas:** no hay. Se quita el campo "Plantilla" del alta.
8. **Ubicación:** no se muestra en ningún lado. Se quitan "Ruta de almacenamiento", la ruta generada del alta y la columna de ruta de la pantalla 05. Las pantallas 08 (edición) y 09 (compartir) las adapto yo a Cliente / Proyecto, con el estilo del resto.
9. **Sync:** el disco local sigue organizado según la ruta que genera el servidor. Está bien.

**Impacto previsto (a precisar en el diseño del REQ):**
- **web (jiku):** las 9 pantallas, el buscador con filtros como tokens, las vistas guardadas, "Ver todos" en el detalle de proyecto y los estilos de `jiku.css` aplicados a la sección.
- **jiku-docs (servicio):**
  - **Modelo de permisos:** clases y visibilidad (decisión 3) en lugar de ACL de carpetas para personas; visibilidad `restricted`.
  - **Ruta generada en el servidor:** `documents.new` sin carpeta, la ruta sale de cliente y proyecto, y nunca se expone en la UI.
  - **Filtros nuevos:** "sin proyecto", "sin cliente" y "sin tema".
  - **"Asignar proyecto":** un comando que actualiza solo las relaciones del frontmatter, con versión base.
  - **Sugerencia de proyecto** por título y contenido.
  - **Sugerencias de temas existentes:** `topics.list` ya existe.
  - **Nombres en "Editado por":** resolverlos. Hoy la web muestra "Otra persona".
  - **Contratos, especificación y migración:** de la ACL actual y de los documentos importados.
- **jiku-go:** `restricted` en el CLI. `new` sin `--folder`. Revisar `ls` y `mv` como herramientas de ruta interna.
- **api (jiku):** rutas nuevas para lo anterior y para las sugerencias del buscador. Proyectos, clientes y personas salen de core; los temas, de jiku-docs.

**Card "Documentación" del detalle de proyecto (agregado 2026-10-26).** Referencia: `docs/p1/card-documentacion-detalle-proyecto.png`.
- **Encabezado:** "Documentación" con la cantidad, un "+" (nuevo documento con el proyecto ya elegido) y "Ver todos ›".
- **"Ver todos":** lleva a la pantalla de Documentación con el filtro de proyecto activado (pantalla 04, con "Volver al proyecto").
- **Filas:**
  - ícono, título y fecha de actualización a la derecha ("—" si no hay);
  - debajo, los temas como pills, o "Sin tema";
  - un documento que aparece en el proyecto pero no tiene ese proyecto como propio (por ejemplo, vinculado por un requisito o una tarea del proyecto) lleva la marca "vinculado, sin proyecto propio". El diseño del REQ precisa la regla exacta.
- **Mismo criterio** para las cards de requisito y tarea: sin ruta, y absorbe el ítem 13.

**Estado:** definido. Se ejecuta cuando el dueño lo indique: jiku-docs y jiku-go en paralelo, y después el REQ de la web en jiku.

### Preguntas abiertas de P-1

Resueltas el 2026-10-26; ver "Decisiones del dueño" en P-1.
