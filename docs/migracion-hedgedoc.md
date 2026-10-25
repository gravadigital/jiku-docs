# Migración desde HedgeDoc: definiciones, estado y preguntas abiertas

Estado al 2026-10-25. Documento de trabajo: reúne lo definido hasta ahora y lo que falta decidir antes de la
migración real. No contiene contenido de notas; los datos (dump, planillas, informes) viven en `.local/`, que
no se versiona.

## 1. Origen

| Dato | Valor |
| --- | --- |
| Instancia | HedgeDoc 1.10 (`docs.grava.io`), dockerizado en una VM. Deploy: repo `herramientas-externas/codimd` |
| Base | MariaDB `codimd`. Contenido de cada nota en la tabla `Notes`; autores en `Users` |
| Imágenes | Space de DigitalOcean `grava-internal-01`, carpeta `docs-grava-io`, **con acceso público** (`CMD_S3_PUBLIC_FILES=true`) |
| Acceso | Anónimo habilitado (`CMD_ALLOW_ANONYMOUS=true`): hasta hoy todas las notas fueron públicas |
| Export usado | `mysqldump` completo del 2026-10-24 (`.local/codimd-2026-10-24.sql`, ~377 MB) |

Comando para generar el export en la VM:

```sh
docker exec codimd-database sh -c 'mysqldump -u root -p"$MYSQL_ROOT_PASSWORD" codimd' > codimd-$(date +%F).sql
```

## 2. Volumen

| Conjunto | Notas |
| --- | --- |
| Notas totales en el dump | ~12.000 |
| Notas anónimas (sin dueño) | ~10.290; la muestra revisada es casi toda spam de casinos. Quedan fuera salvo `--include-anonymous` |
| Notas de usuarios (alcance actual) | 1.709 |
| Imágenes referenciadas por esas notas | 1.880 (1 da 404 en el Space de origen) |
| Notas con posibles credenciales | 104 |
| Enlaces entre notas sin resolver | 13 |

## 3. Cómo funciona el importador (hoy)

Subcomando `jiku-docs import hedgedoc` (código en `internal/importer/hedgedoc`; uso en el README).

1. Lee una MariaDB cargada con el dump (`HEDGEDOC_MYSQL_DSN`).
2. `--export-mapping <csv>` genera la planilla base, con una fila por nota.
3. Una planilla de mapeo decide, nota por nota:
   - `folder`: carpeta destino;
   - `fileName`: nombre de archivo;
   - `skip`: no importar la nota.

   Una fila sin `folder`, `fileName` ni `skip` se ignora. Las notas sin asignar van a `/importado/`, que solo ve admin.
4. Crea cada documento como **servicio** con `documents.new`:
   - frontmatter con el título y la fecha original (`createdAt`);
   - el autor original en el `message` de la versión 1;
   - ids deterministas, así que reimportar no duplica.
5. Baja cada imagen del Space público y la sube como adjunto a través de `files.*` de core (decisión D-3).
6. Reescribe los enlaces:
   - imágenes a `{JIKU_WEB_URL}/docs/attachments/{id}`;
   - enlaces entre notas a `{JIKU_WEB_URL}/docs/{id}`.
7. Corre el detector de secretos. El informe en `.local/` lista regla y línea, nunca el valor.
8. Tiene `--dry-run`. Una reimportación saltea los adjuntos ya subidos y marca `unchanged` las notas sin cambios.

**En curso (código, sin reimportar):** columnas `projects`, `clients`, `requirements`, `tasks` y `topics` en la planilla. El importador las escribe en el bloque `jiku:` y en `topics` del frontmatter.

Una relación con un id inexistente en Jiku se descarta y queda en el informe. Sin esto, **ninguna nota importada aparece en la card "Documentación" de la ficha de proyecto, requisito o tarea**: las relaciones salen solo del frontmatter (especificación, sección 3).

## 4. Árbol de carpetas y permisos usados en la prueba local

El árbol se organiza por **quién debe ver**, no por tema (especificación, sección 3).

| Carpeta | Permisos | Marcas |
| --- | --- | --- |
| `/infra/` | `admin: manage`, `user: read`; sin herencia | `externalBlocked`, `sharingBlocked` |
| `/interno/` | `user: write` | `sharingBlocked` |
| `/buenas-practicas/` | `user: write` | — |
| `/clientes/<cliente>/<proyecto>/{reuniones,tecnico}/` | `user: write` (heredado de `/clientes/`) | — |
| `/importado/` | solo `admin: manage`; sin herencia | — |

El importador crea las carpetas que faltan **heredando los permisos del padre**. Por eso hay que crear antes la estructura raíz con sus permisos.

## 5. Clasificación usada en la primera pasada

Reglas por palabras clave sobre el título (o el primer encabezado), aplicadas en este orden:

1. Temas sensibles (sueldos, despidos, costos) → `/importado/`.
2. Comunicación, marca y web → `/interno/comunicacion/`.
3. Infra (accesos, servidores, runbooks) → `/infra/...`, aunque sea de un cliente.
4. Grava Workflow → `/buenas-practicas/grava-workflow/`.
5. Cliente o proyecto de Jiku nombrado en el título → `/clientes/<cliente>/<proyecto>/{reuniones|tecnico}/`.
6. Proyectos internos → `/interno/proyectos/<proyecto>/`.
7. Plantillas, estándares y guías → `/buenas-practicas/`.
8. Equipo, administración, procesos, comercial → su carpeta en `/interno/`.
9. Sin coincidencia por título: el proyecto que más aparece en el contenido.
10. Resto → `/importado/`.

Las notas con posible secreto nunca van a carpetas de clientes: van a `/infra/` o `/importado/`.

Resultado de esa pasada:

| Destino | Notas | Confianza alta | media | baja |
| --- | ---: | ---: | ---: | ---: |
| `/clientes/` (31 clientes) | 861 | 706 | 78 | 77 |
| `/interno/` | 403 | 252 | 139 | 12 |
| `/importado/` | 193 | 0 | 9 | 184 |
| `/infra/` | 175 | 65 | 97 | 13 |
| `/buenas-practicas/` | 77 | 7 | 68 | 2 |
| **Total** | **1.709** | **1.030** | **391** | **288** |

Hay 39 notas con `sugerir_skip` (casi vacías o solo imágenes) que no se marcaron como `skip`.

## 6. Prueba local realizada

La importación local terminó sin errores: 1.709 notas en 9 min, 1.879 de 1.880 imágenes, y los conteos coinciden con la planilla en las 118 carpetas.

Verificado:
- autor y fecha originales;
- enlaces reescritos;
- búsqueda respetando permisos;
- reimportación idempotente.

**Lo que salió mal:** sin relaciones con Jiku (ver sección 3).

## 7. Riesgos y tareas previas a la migración real

- **Rotar credenciales:** hay que rotar las credenciales de las 104 notas marcadas. Fueron públicas en HedgeDoc. El informe del dry-run indica la nota, la regla y la línea.
- **Imágenes públicas:** siguen accesibles en el Space mientras no se cierre. Decidir si se cierra o se borra después de migrar.
- **Apagado de HedgeDoc:** definir fecha, si queda en solo lectura un tiempo y si se redirigen las URLs viejas (`docs.grava.io/<id>`) a los documentos nuevos.
- **Clientes sin cargar en Jiku:** 26 de los 31 clientes de la planilla no existen en Jiku. En Jiku solo están EXO, Verifarma, Chiatib, Tesisler y Grava. Sus notas no se pueden vincular hasta que existan.

## 8. Preguntas abiertas

1. **Clasificación:**
   - **Opción A:** un agente lee cada nota y propone carpeta, proyecto, cliente y temas, con una justificación. Más preciso, pero más lento y costoso.
   - **Opción B:** corregir las reglas por palabras clave.
   - En los dos casos, ¿quién revisa las 679 notas de confianza media o baja?
2. **Clientes que no están en Jiku:** ¿se cargan en Jiku antes de migrar, o esas notas quedan solo por carpeta, sin vínculo?
3. **Estructura de carpetas:** ¿se mantiene el árbol de la sección 4? ¿Qué subcarpetas por proyecto (`reuniones`, `tecnico`, otras)?
4. **Descarte:**
   - ¿Qué se hace con las 39 notas casi vacías?
   - ¿Y con las ~10.290 anónimas (spam)? Una opción es ignorarlas y borrarlas con HedgeDoc.
5. **Nombres de archivo:** ¿el título de la nota en kebab-case, o se conserva el título original?
6. **Visibilidad:** ¿alguna nota migrada debe quedar `public` para clientes externos, o todas `internal` y se publica después a mano?
7. **Notas sensibles:** ¿qué se hace con las 6 notas de RRHH y sueldos que hoy van a `/importado/`?
8. **Propuestas comerciales:** ¿las que quedaron en `/clientes/.../tecnico/` deben verlas los equipos de proyecto?
9. **Autoría:** ¿alcanza con dejar el autor original en el mensaje de la versión 1? ¿O hay que mapear usuarios de HedgeDoc a usuarios de Jiku?
10. **Los 13 enlaces sin resolver** entre notas: ¿se dejan como están o se corrigen a mano?
11. **Procedimiento:** ¿la migración real va como servicio a la instancia productiva en un solo corte, o por tandas (un cliente por vez) con revisión de cada equipo?

## 9. Archivos (en `.local/`, no versionados)

| Archivo | Qué es |
| --- | --- |
| `codimd-2026-10-24.sql` | Dump de HedgeDoc |
| `hedgedoc-mapping.csv` | Planilla de mapeo con las columnas de revisión (`confianza`, `motivo`, `posible_secreto`, sugerencias de proyecto y cliente) |
| `hedgedoc-mapping-resumen.md` | Conteos, reglas y la lista de notas con posible secreto |
| `hedgedoc-import-*-report.csv` | Informes de dry-run, importación y reimportación |
