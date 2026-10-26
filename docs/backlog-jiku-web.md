# Backlog para la web de Jiku (pendientes de jiku-docs)

Lista de trabajo acumulado para el monorepo de jiku (`web`, `opus-web`, `api`), que se ejecuta con grava-workflow (un REQ por tema, sobre la rama `feat/jiku-docs`, sin push).

Estados:
- **aprobado**: el dueño lo pidió y se puede ejecutar;
- **a confirmar**: lo propuse yo y espera el visto bueno;
- **hecho**.

| # | Tema | Origen | Estado |
| --- | --- | --- | --- |
| 1 | Pantalla de historial de versiones del documento en `web`, y campo de mensaje opcional al guardar en el editor | `docs/compactacion-historial-v1.md`, sección 5 | aprobado (postergado hasta ahora) |
| 2 | CSP de la ruta pública `/p/docs/*`: sumar a `img-src` el origen del storage configurado. En local las imágenes no cargan porque el storage es http | Prueba e2e en navegador | a confirmar |
| 3 | CORS de la api solo bajo `/api`: hoy `/p/docs/*` responde `Access-Control-Allow-Origin: *` | Prueba e2e por el bus. Era parte del REQ-018 descartado; se pide sin el cambio de tokens | a confirmar |
| 4 | Log `warn` en core por cada `files.request-upload` de jiku-docs (`resolve-actor.ts`): bajarlo a `debug` para callers de clase conector | Prueba e2e por el bus. Era parte del REQ-018 descartado | a confirmar |
| 5 | opus-web: un externo sin permiso sobre un proyecto ve el tablero vacío y "No hay proyectos disponibles" | Prueba e2e en navegador; no es del módulo docs | a confirmar |
| 6 | Textos de interfaz de docs propuestos sin fuente en la UX: errores de `permission_denied` al crear un link, fallo al revocar, fallo al tomar el lock y el contador "Documentación (5+)" | Informe de REQ-016 | a confirmar |
| 7 | Componente `alert` en el Design System: hoy `DocsNotice` está implementado de forma local | Informe de REQ-016 | a confirmar |
| 8 | Altas sin `id` en las rutas de la api: no se pueden reintentar sin duplicar | Informe de REQ-016 | a confirmar |
| 9 | Documentos de más de 256 KiB: no se pueden crear ni editar desde la web, que no tiene subida por URL | Informe de REQ-016 | a confirmar |
| 10 | **Bug JIKU-WEB-1:** el visor interno (`/docs/{id}`) muestra el frontmatter como texto. La UX (`docs/ux/surfaces/web/screens/documento.md:160`) dice que no se muestra, y `MarkdownViewer mode="document"` no lo quita. Propuesta: quitarlo antes de renderizar, como opus-web con `stripFrontmatter` | e2e en navegador con usuario humano | a confirmar (recomendado) |
| 11 | **Bug JIKU-WEB-2:** "Crear y editar" en "Nuevo documento" deja `/docs/{id}?editar=1` con el botón Editar en spinner y nunca toma el lock. Se reprodujo 5 de 5 veces; con recarga completa funciona. Causa probable: carrera entre `onClose()` + `router.push` en `NewDocumentDialog` y el efecto de `DocumentView` (`router.replace` + `session.start`) | e2e en navegador con usuario humano | a confirmar (recomendado) |
| 13 | web, detalle de requisito: la tabla de la card "Documentación" desborda la card y corta la columna Ruta a 1440 px | Relevamiento de capturas | a confirmar |
| 14 | web, detalle de requisito: las pills de estado, tipo y prioridad se anuncian las tres como "Estado: …" en lectores de pantalla | Relevamiento de capturas | a confirmar |
| 15 | web, listado de tareas: una tarea muestra "Vencido hace: 32666 día(s)" (fecha inválida o nula) | Relevamiento de capturas | a confirmar |
| 16 | 404 de web y opus-web: es la página por defecto de Next.js, sin estilos ni navegación | Relevamiento de capturas | a confirmar |
| 17 | opus-web a 390 px: sin navegación ni "Nuevo requisito"; la vista Columnas se ignora; tocar una card abre la página en lugar del modal | Relevamiento de capturas | a confirmar |
| 18 | opus-web: el externo que abre un requisito interno ve "Error al cargar el requisito" con "Reintentar", sin header ni explicación | Relevamiento de capturas | a confirmar |
| 12 | La web muestra "por otra persona" en el lock cuando el que edita es el propio usuario o el servicio, porque `persons` no resuelve esos ids | e2e en navegador | a confirmar |

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

**Impacto previsto (a precisar en el diseño del REQ):**
- **web (jiku):** las 9 pantallas, el buscador con filtros como tokens, las vistas guardadas, "Ver todos" en el detalle de proyecto y los estilos de `jiku.css` aplicados a la sección.
- **jiku-docs (servicio):**
  - **Ruta generada:** a partir de cliente y proyecto, decidida en el servidor y no en la UI.
  - **Lista controlada de temas:** consulta, alta y propuesta de tema nuevo. Hoy los temas se crean al usarse.
  - **Filtros nuevos:** "sin proyecto", "sin cliente", "sin tema" y "por persona" (`updatedBy`/`createdBy` ya existen).
  - **"Asignar proyecto":** actualiza el frontmatter `jiku:` y, si corresponde, mueve el documento a la ruta generada.
  - **Sugerencia por ruta:** el proyecto que sugiere la carpeta actual.
  - **Vistas guardadas:** dónde se guardan.
  - **Nombres en "Editado por":** resolverlos. Hoy la web muestra "Otra persona".
- **api (jiku):** rutas nuevas para lo anterior y para las sugerencias del buscador. Proyectos, clientes y personas salen de core; los temas, de jiku-docs.

**A definir antes de ejecutar:** ver la sección "Preguntas abiertas de P-1" más abajo.

### Preguntas abiertas de P-1

1. **Shell nuevo:** las pantallas usan el shell "v2", con barra superior, búsqueda global ⌘K, "Cargar horas", el sidebar agrupado (Trabajo, Tiempo, Conocimiento, Organización) y el pie con las horas del día. ¿Entra en este pedido (y cambia toda la web) o solo el contenido de las pantallas de Documentación dentro del shell actual?
2. **Documentos del proyecto:** ¿cuentan los vinculados a mano (relaciones), los de su ruta, o ambos? Ya está anotado en "A definir" del README.
3. **Permisos sin carpetas:** hoy quién ve qué se decide por la carpeta (ACL). Ejemplos: `/infra/` y `/interno/` cerradas, `/importado/` solo admin. Con la ruta generada, ¿dónde va un documento?
   - con cliente y proyecto: `/clientes/<cliente>/<proyecto>/`;
   - con cliente sin proyecto: ¿`/clientes/<cliente>/general/`?
   - sin cliente: ¿`/interno/`?

   ¿Cómo se marca un documento de infra o de accesos, que hoy vive en una carpeta restringida? ¿Hace falta un "ámbito" o algo equivalente en el diálogo de alta?
4. **"Asignar proyecto":** ¿mueve el documento a la ruta del proyecto (y cambia quién lo ve) o solo agrega la relación y deja la ruta?
5. **Lista de temas:** ¿quién la administra y quién aprueba un "tema nuevo"? ¿Se aceptan en el frontmatter temas que no están en la lista (por ejemplo, desde el CLI o un agente)?
6. **Vistas guardadas:** ¿son personales, compartidas, o hay algunas fijas del sistema y otras personales?
7. **Plantillas:** ¿qué plantillas hay y dónde se definen?
8. **Edición y compartir (08 y 09):** ¿los adapto yo a Cliente / Proyecto siguiendo el estilo de las otras pantallas, o mandás una versión?
9. **CLI y daemon de sync:** el daemon espeja carpetas. Si la web deja de mostrarlas, el disco sigue organizado por la ruta generada. ¿Está bien?
