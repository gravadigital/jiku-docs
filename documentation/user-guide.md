# User guide

Four ways in, one set of documents and one set of rules: the Jiku web for the team, the client
portal for clients, the `jiku docs` CLI and sync daemon for people at a terminal, and the same CLI
for AI agents. Who may read or write what is the same everywhere — see
[features.md](features.md#who-sees-what).

The screens live in Jiku's repository and their interface is in **Spanish**. Labels are quoted
below as they appear, with their English meaning.

## In the Jiku web

Documentation appears only when the module is installed. If it is installed but not answering,
the screens stay visible with a notice — "La documentación no está disponible en este momento"
(documentation is not available right now) — and the actions that need it are disabled.

### Documentation cards

The project, requirement and task detail pages carry a **"Documentación"** (Documentation) card
listing the related documents, with their topics as pills or "Sin tema" (no topic). From the card:

- **"Nuevo documento"** (new document) creates one already related to that project, requirement
  or task.
- **"Ver todos"** (see all) opens the documentation page filtered by that project.
- A document that reaches a project only through one of its requirements or tasks is marked as such.

### The documentation page

`/docs` — **"Documentación"** — has no folders. Everything is found from one search bar,
"Buscá texto o escribí un proyecto, cliente, tema o persona" (search text or type a project,
client, topic or person), which adds filters as tokens:

- a project, client, topic or person (a person means "created or last edited by");
- free text, with Enter;
- **"Sin proyecto"**, **"Sin cliente"**, **"Sin tema"** (without project, client, topic), to find
  what nobody has classified yet.

Without a search, the page shows **"En mis proyectos activos"** (in my active projects) and
**"Editados recientemente"** (recently edited). Results show "Cliente · Proyecto" (client ·
project), highlighted snippets and can be sorted by **"Relevancia"** (relevance) or **"Más
recientes"** (most recent).

### Reading a document

The document page shows the rendered markdown with **"En este documento"** (table of contents), and
at the side **"Vinculado a"** (related to: client, project, requirement, task), **"Propiedades"**
(properties: visibility, version) and **"Adjuntos"** (attachments). The buttons are **"Editar"**
(edit), **"Compartir"** (share) and **"Historial de versiones"** (version history).

### Creating a document

**"Nuevo documento"** asks for **"Título"** (title), **"Cliente"** and **"Proyecto"** (searchable),
**"Temas"** (topics, suggesting existing ones) and **"Visibilidad"** (visibility):

| Label | Meaning | Value |
|---|---|---|
| "Interno" | Internal — "Solo el equipo." (the team only). Default | `internal` |
| "Visible para el cliente" | Visible to the client — "Llega al portal del cliente del proyecto." (reaches the project's client portal) | `public` |
| "Restringido" | Restricted — admins only | `restricted` |

Only an admin can choose "Restringido"; anyone else sees "Solo la conducción puede restringir un
documento." (only management can restrict a document). There is no folder to choose: the location
is generated from the client and project ([features.md](features.md#generated-location)).
**"Crear y editar"** (create and edit) creates it and opens the editor.

### Editing

**"Editar"** takes the document's lock and opens a markdown editor with a preview. The lock is
renewed while the editor is open and released when it closes; if someone else holds it, the page
says who is editing ("Otra persona lo está editando", someone else is editing it; "Un agente", an
agent). **"Guardar"** (save) accepts an optional **"Mensaje de la versión (opcional)"** (version
message): a version saved with a message is protected from history compaction.
**"Descartar cambios"** discards the changes. If the document changed meanwhile, the editor says
so ("El documento cambió") instead of overwriting.

Documents over 256 KiB cannot be saved from the web ("El documento supera los 256 KiB que se pueden
guardar desde acá"); use the CLI.

### Relations without a project

A document "Sin proyecto" offers **"Asignar proyecto"** (assign project), which only adds the
relation, and **"Asignar sugerido"** (assign suggested), which proposes a project deduced from the
title and content. Neither moves the document.

### History

**"Historial de versiones"** lists the versions kept, with author, date and kind — "Creación"
(creation), "Edición" (edit), "Restauración" (restore) — marking **"Actual"** (current) and
**"Protegida"** (protected: never removed by compaction). **"Ver"** shows a version and
**"Restaurar"** restores it as a new version.

### Sharing

**"Compartir"** creates a read-only link for someone without an account: **"Vence en"** (expires
in), **"Incluir adjuntos"** or **"Solo el texto"** (include attachments, or text only), **"Nota
(opcional)"** (note) and **"Crear link"** (create link). The link is shown **once** — **"Copiar"**
(copy) it then. **"Links de este documento"** (this document's links) lists them, and
**"Revocar"** (revoke) cuts one off. If the document was flagged as possibly holding a secret, the
dialog asks to confirm with **"Compartir igual"** (share anyway). If share links are not configured
in the installation, the dialog says so.

## In the client portal (opus-web)

A client user sees, read-only, the documents marked **"Visible para el cliente"** (`public`) that
are related to projects they have permission on, current version only and without the frontmatter.
Links to other documents appear as plain text, since they point to the internal web. If a document
does not exist or is not shared with them, the portal says "Este documento no existe o no está
compartido con vos." (this document does not exist or is not shared with you), which is distinct
from the notice for the module being down.

## From a terminal: `jiku docs`

The CLI of [jiku-go](https://github.com/gravadigital/jiku-go) speaks to the module with the same
login and connection it uses for core (`jiku login`). Its full reference is
[`docs/docs-module.md`](https://github.com/gravadigital/jiku-go/blob/main/docs/docs-module.md);
the main commands:

| Command | What it does |
|---|---|
| `jiku docs status` | whether the module is `not_installed`, `incompatible`, `unavailable` or `available` |
| `jiku docs search <text>` | full-text search, with `--filter` (e.g. `projectId=12`) |
| `jiku docs query <method>` / `jiku docs cmd <method> <json>` | any query or command, with the module's own contract |
| `jiku docs describe [resource]` | the module's `meta.describe` |
| `jiku docs ls [path]` | how files are kept, folder by folder |
| `jiku docs get <id or path>` | the markdown to stdout, version and lock to stderr; `--version N`, `-o json` |
| `jiku docs new <file>` | creates a document; `--project`, `--client`, `--topic` write the frontmatter; without `--folder` the location is generated |
| `jiku docs put <id> <file> --base-version N` | saves a new version; `--message` protects it, `--session` uses a lock |
| `jiku docs relate`, `jiku docs suggest` | assign or remove relations; suggest projects and clients |
| `jiku docs mv`, `rm`, `restore` | move, delete, restore — always with `--base-version` |
| `jiku docs history <id>` | the kept versions; `--protected` for the ones kept forever |
| `jiku docs lock`, `unlock` | edit session with `--session` and `--ttl` |
| `jiku docs edit <id>` | for people: lock, `$EDITOR`, save and release |
| `jiku docs attach <id> <file>` | uploads, confirms and prints the link for the markdown |
| `jiku docs share <id> --expires 30d`, `shares`, `revoke` | share links; the URL is printed once |

Every command takes an id or a path. **`--base-version` is required on every change**: on
`version_conflict`, re-read, merge and retry with the new base. Creating is safe to retry with the
printed `--id`. Content above 256 KiB travels through a presigned URL automatically.

```bash
jiku docs search "integración con aduana" --filter clientId=4
jiku docs get /general/meeting-notes.md > notes.md
jiku docs put /general/meeting-notes.md notes.md --base-version 7 --message "Sent to the client"
jiku docs share /general/meeting-notes.md --expires 15d --note "Hosting provider"
```

## The sync daemon

`jiku docs sync` keeps a local folder (default `~/JikuDocs`) equal to the documents you can read,
with the same paths, and uploads your edits back:

- remote changes arrive in seconds through notifications and replace the file atomically;
- a local file quiet for 2 seconds is uploaded on top of the version it was made from;
- a conflict never overwrites: the remote version is written beside yours as a copy to merge;
- a new `.md` in a known folder becomes a document; deleting a file does not delete the document;
- a document you can no longer read is moved aside, never deleted;
- offline, edits wait on disk and are uploaded when the module answers again.

On Linux it runs as a systemd user service installed by the jiku-go package:

```bash
jiku login
systemctl --user enable --now jiku-docs-sync
jiku docs sync status
journalctl --user -u jiku-docs-sync -f
```

Configuration (`~/.config/jiku/docs-sync.yaml`: `root`, `include`, `exclude`, `debounce`,
`notifications`, `log_level`), exit codes and troubleshooting are in jiku-go's
[`docs/docs-module.md`](https://github.com/gravadigital/jiku-go/blob/main/docs/docs-module.md#the-sync-daemon).
The design is in [`docs/sync-systemd-v1.md`](../docs/sync-systemd-v1.md).

## For AI agents

Agents use the same CLI, with the token of the person they work for: what an agent writes is
written in that person's name. To record that an agent made the change, set:

```bash
export JIKU_VIA=agent
```

Without it the CLI sends `via: human`. `via` is traceability, not permission, and it shapes history
compaction: a change of `via` closes an editing session, so an agent's saves never merge with a
person's.

Rules for agents:

- **Search before creating**, to avoid duplicates.
- **Read with `jiku docs get <id> -o json`** and keep `version` as the base; cite the document and
  version when answering from it.
- **For long edits take a lock** with `jiku docs lock <id> --ttl 300` and renew it by repeating the
  command with the same `--session` before it expires; each CLI call is a separate process, so
  nothing renews it in the background. Release it with `unlock` at the end.
- **Do not save changes the person did not accept.** When a piece of work is done, save with
  `--message` so the version is protected; an agent's versions without a message are compacted.
- **Never run `jiku docs share` or `revoke` without the person's explicit confirmation.** The CLI
  cannot enforce this; it depends on the agent's instructions.
- A write may come back with the warning `possible_secret_detected`: documents are not the place
  for credentials.
