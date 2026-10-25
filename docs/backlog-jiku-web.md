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

## Pedidos del dueño

(Se agregan a medida que llegan.)
