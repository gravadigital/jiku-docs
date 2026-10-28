# Backlog for the Jiku web (jiku-docs pending items)

List of accumulated work for the jiku monorepo (`web`, `opus-web`, `api`), executed with grava-workflow (one REQ per topic, on the `feat/jiku-docs` branch, no push).

The Jiku web interface is in Spanish: its texts are quoted literally, with their English meaning the first time.

States:
- **approved**: the owner asked for it and it can be executed;
- **to confirm**: I proposed it and it awaits approval;
- **done**.

| # | Topic | Origin | State |
| --- | --- | --- | --- |
| 1 | Document version history screen in `web`, and an optional message field when saving in the editor | `docs/history-compaction-v1.md`, section 5 | approved, goes inside P-1 (history button of screen 06) |
| 2 | CSP of the public route `/p/docs/*`: add the origin of the configured storage to `img-src`. Locally images do not load because the storage is http | End-to-end test in the browser | to confirm |
| 3 | API CORS only under `/api`: today `/p/docs/*` responds `Access-Control-Allow-Origin: *` | End-to-end test over the bus. It was part of the discarded REQ-018; it is requested without the token change | approved, goes in P-2 |
| 4 | `warn` log in core for each `files.request-upload` from jiku-docs (`resolve-actor.ts`): lower it to `debug` for connector-class callers | End-to-end test over the bus. It was part of the discarded REQ-018 | approved, goes in P-2 |
| 5 | opus-web: an external user without permission on a project sees the empty board and "No hay proyectos disponibles" (no projects available) | End-to-end test in the browser; not part of the docs module | to confirm |
| 6 | Docs interface texts proposed without a source in the UX: `permission_denied` errors when creating a link, failure to revoke, failure to take the lock, and the "Documentación (5+)" (documentation (5+)) counter | REQ-016 report | to confirm |
| 7 | `alert` component in the Design System: today `DocsNotice` is implemented locally | REQ-016 report | to confirm |
| 8 | Creations without an `id` in the API routes: they cannot be retried without duplicating | REQ-016 report | to confirm |
| 9 | Documents larger than 256 KiB: they cannot be created or edited from the web, which has no upload by URL | REQ-016 report | to confirm |
| 10 | **Bug JIKU-WEB-1:** the internal viewer (`/docs/{id}`) shows the frontmatter as text. The UX (`docs/ux/surfaces/web/screens/documento.md:160`) says it is not shown, and `MarkdownViewer mode="document"` does not strip it. Proposal: strip it before rendering, like opus-web with `stripFrontmatter` | End-to-end in the browser with a human user | approved, goes inside P-1 |
| 11 | **Bug JIKU-WEB-2:** "Crear y editar" (create and edit) in "Nuevo documento" (new document) leaves `/docs/{id}?editar=1` with the "Editar" (edit) button spinning and never takes the lock. Reproduced 5 out of 5 times; with a full reload it works. Probable cause: a race between `onClose()` + `router.push` in `NewDocumentDialog` and the effect of `DocumentView` (`router.replace` + `session.start`) | End-to-end in the browser with a human user | approved, goes inside P-1 |
| 13 | web, requirement detail: the table of the "Documentación" card overflows the card and cuts the "Ruta" (path) column at 1440 px | Screenshot survey | approved, goes inside P-1 |
| 14 | web, requirement detail: the status, type and priority pills are all three announced as "Estado: …" (status: …) by screen readers | Screenshot survey | approved, goes in P-2 |
| 15 | web, task list: a task shows "Vencido hace: 32666 día(s)" (overdue by: 32666 day(s)) (invalid or null date) | Screenshot survey | approved, goes in P-2 |
| 16 | 404 of web and opus-web: it is the default Next.js page, without styles or navigation | Screenshot survey | to confirm |
| 17 | opus-web at 390 px: no navigation and no "Nuevo requisito" (new requirement); the "Columnas" (columns) view is ignored; tapping a card opens the page instead of the modal | Screenshot survey | to confirm |
| 18 | opus-web: an external user who opens an internal requirement sees "Error al cargar el requisito" (error loading the requirement) with "Reintentar" (retry), without a header or an explanation | Screenshot survey | approved, goes in P-2 |
| 12 | The web shows "por otra persona" (by another person) on the lock when the one editing is the user themselves or the service, because `persons` does not resolve those ids | End-to-end in the browser | approved, goes inside P-1 |

## Grouping for execution

- **P-1** (jiku-docs and jiku-go first, then the web REQ): documentation without folders, with items 1, 10, 11, 12 and 13 and the project detail card.
- **P-2** (REQ of small fixes in jiku): items 3, 4, 14, 15 and 18.
- **Not applied for now:** 2, 5, 6, 7, 8, 9, 16 and 17.

## Owner's requests

### P-1. Documentation without folders: "propuesta B" (proposal B) (approved, 2026-10-26)

**Reference:** `_research/jiku-documentacion-propuesta-b/` in jiku-docs (README, 9 screens in HTML in light and dark mode, screenshots at 1440 px, `css/jiku.css` with tokens and components). Screens 08 (editing) and 09 (sharing) come from variant A: they have to be moved to Client / Project.

**Idea:** there is no navigation by folders. Everything is found from a single search box that keeps adding filters:
- project, client, topic, person;
- text with Enter;
- "Sin proyecto" (no project), "Sin cliente" (no client) and "Sin tema" (no topic).

The "Solo visibles para el cliente" (only visible to the client) checkbox stays apart from the search box. There are saved views. Each document has a client, a project (or "Sin proyecto") and topics from a controlled list. The path is generated automatically and is only shown as technical data.

**Screens:**
1. **Home:** saved views, "En mis proyectos activos" (in my active projects) and "Editados recientemente" (recently edited), with "Cliente · Proyecto" (client · project) instead of the path.
2. **Grouped suggestions** of the search box, with keyboard shortcuts.
3. **Results** with highlighted snippets, count and pagination.
4. **Documents of a project:** destination of "Ver todos" (see all) from the project detail, with "Volver al proyecto" (back to the project).
5. **"Sin proyecto":** "Asignar proyecto" (assign project) or "Asignar sugerido" (assign suggested) depending on the path.
6. **Document in reading mode:** header with Client / Project and topics. On the right, table of contents, "Vinculado a" (related to), properties (with the path as "Ruta de almacenamiento", storage path) and attachments.
7. **"Nuevo documento"** (new document):
   - title;
   - client and project with search;
   - topics from the common list, with "Proponer tema nuevo" (propose a new topic);
   - visibility and template;
   - no folder, with the generated path in view.
8. **Editing** with preview and lock notice.
9. **Sharing.**

**Owner's decisions (2026-10-26):**

1. **Visual scope:** only the content of the Documentation screens. The sidebar and the top bar stay as today (no top bar and no global ⌘K search).
2. **Documents of a project:** they are the ones related through relations (`jiku:` frontmatter). The location does not matter.
3. **Permissions without folders:**
   - **Internal people** (`user`, `admin`): they read and write all `internal` and `public` documents. No folder or ACL limits an internal person; for example, anyone can edit an infra document.
   - **External users:** as today. They only see the `public` documents related to projects of their `project-permissions`.
   - **New `restricted` visibility:** only `admin`, and services as today. It is chosen in the same visibility selector of the creation form (Internal · Visible to the client · Restricted), with Internal by default.
   - **Required guarantee:** a `user` or an external user **never** accesses a `restricted` document by any path: `list`, `get`, `search`, `changes`, `versions`, attachments, notifications, share links, sync, or search box suggestions. It has to be covered by tests in each one.
4. **"Asignar proyecto":** screen 05 "Sin proyecto" is kept.
   - "Asignar proyecto" only adds the relation, without moving anything.
   - "Asignar sugerido" infers the project from the title and the content, not from the folder.
5. **Free topics**, with suggestions of the existing ones. There is no controlled list and no "Proponer tema nuevo".
6. **Saved views:** removed. Neither the "Vistas:" (views) row nor the "Vistas" button.
7. **Templates:** there are none. The "Plantilla" (template) field is removed from the creation form.
8. **Location:** it is not shown anywhere. "Ruta de almacenamiento", the generated path of the creation form and the path column of screen 05 are removed. I adapt screens 08 (editing) and 09 (sharing) to Client / Project myself, with the style of the rest.
9. **Sync:** the local disk stays organised according to the path the server generates. That is fine.

**Expected impact (to be detailed in the REQ design):**
- **web (jiku):** the 9 screens, the search box with filters as tokens, the saved views, "Ver todos" in the project detail and the `jiku.css` styles applied to the section.
- **jiku-docs (service):**
  - **Permission model:** classes and visibility (decision 3) instead of folder ACLs for people; `restricted` visibility.
  - **Path generated on the server:** `documents.new` without a folder, the path comes from client and project, and it is never exposed in the UI.
  - **New filters:** "no project", "no client" and "no topic".
  - **"Asignar proyecto":** a command that updates only the frontmatter relations, with a base version.
  - **Project suggestion** by title and content.
  - **Suggestions of existing topics:** `topics.list` already exists.
  - **Names in "Editado por"** (edited by): resolve them. Today the web shows "Otra persona" (another person).
  - **Contracts, specification and migration:** of the current ACL and of the imported documents.
- **jiku-go:** `restricted` in the CLI. `new` without `--folder`. Review `ls` and `mv` as internal path tools.
- **api (jiku):** new routes for the above and for the search box suggestions. Projects, clients and people come from core; topics, from jiku-docs.

**"Documentación" card of the project detail (added 2026-10-26).** Reference: `docs/p1/project-detail-documentation-card.png`.
- **Header:** "Documentación" with the count, a "+" (new document with the project already chosen) and "Ver todos ›".
- **"Ver todos":** leads to the Documentation screen with the project filter enabled (screen 04, with "Volver al proyecto").
- **Rows:**
  - icon, title and update date on the right ("—" if there is none);
  - below, the topics as pills, or "Sin tema";
  - a document that appears in the project but does not have that project as its own (for example, related through a requirement or a task of the project) carries the mark "vinculado, sin proyecto propio" (related, without its own project). The REQ design specifies the exact rule.
- **Same criterion** for the requirement and task cards: no path, and it absorbs item 13.

**State:** defined. It is executed when the owner says so: jiku-docs and jiku-go in parallel, and then the web REQ in jiku.

### P-1 open questions

Resolved on 2026-10-26; see "Owner's decisions" in P-1.
