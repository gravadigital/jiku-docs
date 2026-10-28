# jiku-docs — convenciones del repo

- Go 1.25 (`go.mod` en `go 1.25.0`): no subir dependencias que exijan una versión mayor.
- Los contratos de `docs/apis/` son la fuente de verdad de forma. Un cambio de forma va al contrato, al
  código y al `CHANGELOG.md` (`[Unreleased]`) en el mismo commit. `docs/integration-decisions.md` manda
  sobre `docs/specification-v1.md`.
- Las fichas de recursos (`internal/service/specs.go`) alimentan la validación y `meta.describe`;
  `internal/contract` verifica que coincidan con el YAML.
- Deny-by-default: claves, filtros, campos e includes desconocidos son `invalid_fields`; lo no visible es
  `*_not_found`. Nunca se concede acceso si core no responde (`upstream_unavailable`).
- Toda escritura: transacción con evento en `changelog` (outbox). El contenido se sube a S3 antes de la
  transacción. Nada de contenido, tokens ni URLs prefirmadas en logs o eventos.
- Tests: `go test -race ./...` (integración con Docker; `JIKU_DOCS_SKIP_INTEGRATION=1` la saltea). Solo
  fixtures sintéticos: el dump de HedgeDoc y las credenciales viven en `.local/` y nunca se commitean ni se
  copian.
- Commits convencionales en español, uno por preocupación, en `dev`. Nunca `git push`. Nunca commitear
  `.local/`, `_research/`, `.env` ni credenciales.
- Antes de cerrar: `go vet ./...`, `golangci-lint run ./...`, `go test -race ./...`, `docker build .`.

---

## Cortar una release

**Disparadores:** "versiona", "versionar", "cortá una release", "nueva versión", "bump de versión",
"versiona y listo", "release", "cut a release", "new version", "version bump" o cualquier variante.
Cuando aparece uno, seguir este procedimiento completo y en orden. Es el mismo que el del repo de jiku,
adaptado a un servicio Go.

### Qué se versiona y qué no

| Versión | Dónde vive | Quién la mueve |
| --- | --- | --- |
| **El producto**, lo que "versiona" significa | El tag `vX.Y.Z` + la sección `## [X.Y.Z]` de `CHANGELOG.md` | Este procedimiento |
| Los contratos (`info.version` de `docs/apis/*.yaml`) | Cada YAML | El commit que cambia la forma (ver arriba), nunca una release |

El número del producto **no está escrito en ningún archivo de código**: el binario lo recibe al compilar
(`-ldflags "-X main.version=..."`, `ARG VERSION` del `Dockerfile`), y `release.yml` lo toma del tag. Por
eso acá no hay un `set-version.sh`: aplicar el número es escribir la sección del CHANGELOG y poner el
tag. La política está en [CHANGELOG.md](CHANGELOG.md#política-de-versionado); esto es el paso a paso.

### Paso 1: elegir el punto de corte

`main` es la rama de releases; el trabajo entra por `dev`.

```sh
git describe --tags --abbrev=0            # última versión publicada (nada: es la primera)
git rev-list --left-right --count main...dev
git status --porcelain
```

**El corte cae sobre trabajo cerrado.** Nunca en medio de un pedido a medio implementar (un P-x de
`docs/jiku-web-backlog.md`, una spec de `docs/` en curso) ni con `[Unreleased]` describiendo algo que el
código todavía no hace. Si `dev` tiene trabajo abierto, el corte es `main` (o el último commit cerrado de
`dev`), y **se le dice al usuario**, no se resuelve en silencio. Si `origin/main` está atrás de `main`, se
informa también, pero **no se pushea** (ver paso 8).

**Primera release:** si `main` no existe, se crea en el punto de corte (`git branch main <commit>`), sin
pushear.

### Paso 2: juntar los cambios reales

La entrada del CHANGELOG cubre **todo** lo que entró desde el último tag. Los asuntos de los commits no
alcanzan: hay que leer los cuerpos.

```sh
git log v<última>..HEAD --oneline
git log v<última>..HEAD --format='%h%n%B'      # los cuerpos explican el porqué
git diff --stat v<última>..HEAD
```

Fuentes, de la más rica a la más pobre:

1. `[Unreleased]` del `CHANGELOG.md`, que cada commit de forma ya alimentó.
2. `docs/integration-decisions.md`, `docs/p1/service.md` y las specs de `docs/` (por qué y cómo).
3. Los cuerpos de los commits.

Y siempre revisar estos diffs, que son los que generan entradas que nadie escribió:

```sh
git diff v<última>..HEAD -- .env.dist                          # variables nuevas o renombradas
git diff v<última>..HEAD -- docs/apis/                         # contratos del bus y del registro
git diff --stat v<última>..HEAD -- internal/store/migrations/  # migraciones
git diff v<última>..HEAD -- Dockerfile docker-compose.local.yml .github/workflows/
git diff v<última>..HEAD --diff-filter=D --name-only           # lo que se borró
```

> **La regla que importa: toda entrada tiene que poder verificarse en el árbol que se taguea.** Si se
> corta desde `main`, un cambio que solo está en `dev` **no va** en la entrada. Verificar con
> `git branch --contains <sha>` antes de afirmar nada.

### Paso 3: proponer el número y ESPERAR

| Bump | Cuándo |
| --- | --- |
| **major** | Rompe el contrato del bus (`docs/apis/jiku-docs-*.yaml`), el protocolo de módulos (`jiku-modules.yaml`) o exige una migración de datos destructiva |
| **minor** | Funcionalidad nueva, compatible hacia atrás |
| **patch** | Bugs y cambios internos, sin efecto en ningún contrato |

Presentarle al usuario: **el número propuesto, el razonamiento y la lista de cambios incompatibles
encontrados**.

**No fijar la versión sin confirmación explícita.** Si el usuario corrige el número, es su decisión: se
toma. Si la corrección contradice la tabla (un `minor` con cambios que rompen contrato, por ejemplo), se
dice **en una línea**, se dejan los cambios incompatibles bien visibles arriba de la entrada y se sigue
sin insistir.

### Paso 4: escribir la entrada del CHANGELOG

En [CHANGELOG.md](CHANGELOG.md), arriba de la entrada anterior:

- Título `## [X.Y.Z] - AAAA-MM-DD` con **la fecha real de hoy**.
- `## [Unreleased]` queda vacío arriba; lo que tenía pasa debajo del título nuevo.
- Secciones de Keep a Changelog, solo las que correspondan: `Added`, `Changed`, `Deprecated`, `Removed`,
  `Fixed`, `Security`.
- **`### Notas para instalaciones existentes`** al final. Es de este repo, no de Keep a Changelog, y es
  **obligatoria** cuando hay: variables de entorno nuevas obligatorias, variables renombradas o quitadas,
  migraciones destructivas o de datos, roles que cambian en Zitadel, o consumidores del bus que tienen que
  migrar.
- En español, como el resto del archivo. Negrita al principio de las entradas importantes. Decir **qué se
  rompe y qué tiene que hacer quien opera la instalación**, no solo qué cambió.
- Nunca borrar ni reescribir entradas de versiones ya publicadas.

### Paso 5: actualizar el README y la documentación si hace falta

- **[README.md](README.md)**: si cambió la tabla de paquetes, el diagrama, la cantidad de comandos y
  consultas, los subcomandos, los requisitos o algo que enlaza. **No lleva número de versión, a propósito:
  no agregarlo.**
- `.env.dist`: que tenga toda variable nueva, con su comentario.
- `docs/apis/*.yaml` son la fuente de verdad de los contratos: si el código y el documento no coinciden,
  manda el documento, y eso es un bug para reportar, no algo para "arreglar" en el YAML durante una
  release.

### Paso 6: verificar el número

```sh
grep -n '^## \[X.Y.Z\] - ' CHANGELOG.md          # lo mismo que exige release.yml
go vet ./... && golangci-lint run ./... && go test -race ./...
docker build --build-arg VERSION=X.Y.Z -t jiku-docs:release-check . \
  && docker run --rm jiku-docs:release-check version   # tiene que imprimir X.Y.Z
docker rmi jiku-docs:release-check
```

Se corre antes de taguear porque `release.yml` lo va a correr igual, y para entonces el tag ya está puesto.

### Paso 7: commit

En la rama de corte, con el CHANGELOG y lo que se haya tocado del README, `.env.dist` o `docs/`.

**Nunca `git add -A` ni `git add .`**: en la raíz viven `.local/`, `_research/` y `.env`, que no se
commitean. Stage por nombre:

```sh
git add CHANGELOG.md README.md .env.dist docs/<lo que corresponda>
git status --porcelain          # confirmar que no hay nada de más
git commit -m "chore(release): X.Y.Z"
```

Cerrar el mensaje con la línea `Co-Authored-By:` de siempre. Si el corte fue en `dev`, llevar `main` al
commit de la release (`git switch main && git merge --ff-only dev`, o crearla si es la primera).

### Paso 8: tag local, y nada más

```sh
git tag vX.Y.Z          # sobre main
```

> ### NUNCA PUSHEAR NADA
>
> Ni la rama, ni el tag, ni `git push --tags`, ni `git push --follow-tags`, ni `git push origin vX.Y.Z`.
> **Ni siquiera cuando parece el paso obvio que sigue**, ni cuando el usuario dice "versiona y listo".
>
> El motivo es concreto: pushear el tag es lo que publica `gravadigital/jiku-docs` en Docker Hub
> (`release.yml` se dispara con `push: tags: ['v*.*.*']`, con los tags `X.Y.Z`, `X.Y` y `latest`). Esa
> acción es del usuario, no del agente.
>
> Terminar diciéndole el comando exacto, como información, sin ejecutarlo:
>
> ```sh
> git push origin main dev && git push origin vX.Y.Z
> ```

### Paso 9: informe

- Qué versión se tagueó, en qué rama y sobre qué commit.
- Qué quedó afuera de la release y por qué (típicamente: trabajo de `dev` de un pedido abierto).
- Si `origin/<rama>` está atrás, decirlo. Si el repo de GitHub todavía no existe, también.
- El comando de push, sin ejecutarlo.
- Que `release.yml` vuelve a verificar el tag contra el CHANGELOG y corre vet, lint y tests antes de
  publicar, así que un tag mal puesto nunca llega al registro.

### Reglas duras

1. **Nunca `git push`** de nada, en ningún paso, por ningún motivo.
2. **Nunca fijar la versión sin** confirmación explícita del usuario.
3. **Nunca poner en la entrada un cambio que no esté en el árbol que se taguea.**
4. **Nunca versionar sobre un pedido a medio implementar** sin avisarlo antes.
5. **Nunca tocar `info.version` de los contratos en una release**: se mueve con el cambio de forma.
6. **Nunca reescribir entradas de versiones ya publicadas.**
7. Si el tag ya existe y hay que corregirlo: `git tag -d vX.Y.Z` y crearlo de nuevo. Es seguro **solo
   porque nunca se pushea**.
