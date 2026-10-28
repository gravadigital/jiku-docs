# Migration from HedgeDoc: definitions, status and open questions

Status as of 2026-10-25. Working document: it gathers what has been defined so far and what remains to be decided before
the real migration. It contains no note content; the data (dump, sheets, reports) live in `.local/`, which
is not versioned.

## 1. Source

| Item | Value |
| --- | --- |
| Instance | HedgeDoc 1.10 (`docs.grava.io`), dockerised on a VM. Deploy: repo `herramientas-externas/codimd` |
| Database | MariaDB `codimd`. Content of each note in the `Notes` table; authors in `Users` |
| Images | DigitalOcean Space `grava-internal-01`, folder `docs-grava-io`, **with public access** (`CMD_S3_PUBLIC_FILES=true`) |
| Access | Anonymous enabled (`CMD_ALLOW_ANONYMOUS=true`): until today all notes were public |
| Export used | Full `mysqldump` of 2026-10-24 (`.local/codimd-2026-10-24.sql`, ~377 MB) |

Command to generate the export on the VM:

```sh
docker exec codimd-database sh -c 'mysqldump -u root -p"$MYSQL_ROOT_PASSWORD" codimd' > codimd-$(date +%F).sql
```

## 2. Volume

| Set | Notes |
| --- | --- |
| Total notes in the dump | ~12,000 |
| Anonymous notes (no owner) | ~10,290; the sample reviewed is almost all casino spam. Left out unless `--include-anonymous` |
| User notes (current scope) | 1,709 |
| Images referenced by those notes | 1,880 (1 returns 404 in the source Space) |
| Notes with possible credentials | 104 |
| Unresolved links between notes | 13 |

## 3. How the importer works (today)

Subcommand `jiku-docs import hedgedoc` (code in `internal/importer/hedgedoc`; usage in the README).

1. It reads a MariaDB loaded with the dump (`HEDGEDOC_MYSQL_DSN`).
2. `--export-mapping <csv>` generates the base sheet, with one row per note.
3. A mapping sheet decides, note by note:
   - `folder`: destination folder;
   - `fileName`: file name;
   - `skip`: do not import the note.

   A row without `folder`, `fileName` or `skip` is ignored. Unassigned notes go to `/imported/`, which only admin sees.
4. It creates each document as a **service** with `documents.new`:
   - frontmatter with the title and the original date (`createdAt`);
   - the original author in the `message` of version 1;
   - deterministic ids, so reimporting does not duplicate.
5. It downloads each image from the public Space and uploads it as an attachment through core's `files.*` (decision D-3).
6. It rewrites the links:
   - images to `{JIKU_WEB_URL}/docs/attachments/{id}`;
   - links between notes to `{JIKU_WEB_URL}/docs/{id}`.
7. It runs the secret detector. The report in `.local/` lists rule and line, never the value.
8. It has `--dry-run`. A reimport skips the attachments already uploaded and marks unchanged notes as `unchanged`.

**Relations and topics (commit `e32cb09`; not yet reimported locally).**

- **New sheet columns**, after `skip`:
  - `projects`, `clients`, `requirements` and `tasks`: ids separated by `;` or `,`;
  - `topics`: topics separated by `;`.
- **Where they are written:** the importer puts them into the `jiku:` block and into `topics` of the version 1 frontmatter. Without relations, **no imported note appears in the "Documentación" (documentation) card** of the project, requirement or task page: relations come only from the frontmatter (specification, section 3). The local import of 2026-10-24 was done before this change, so it has none.
- **Non-existent ids:** a relation with an id that does not exist in Jiku is dropped and the note is imported anyway. The drop is recorded in the `droppedRelations` column of the report.
- **Prior validation:** `--dry-run --check-relations` validates the ids against core without writing anything.
- **Reimport:** a note with new content is updated (`updated`) only if the last version was written by the importer. If someone edited it in jiku-docs, it stays `exists` and is not overwritten.

Dry-run with the current sheet: 1,709 notes in total.

| Coverage | Notes |
| --- | --- |
| With a project | 1,175 |
| With a client | 931 (the client was inferred from the prefix of the project name, because almost no Jiku project has a `client_id`) |
| With topics | 1,409 |
| With requirements or tasks | none |
| Dropped relations | 0 |

The previous sheet was kept in `.local/hedgedoc-mapping.prev.csv`.

## 4. Folder tree and permissions used in the local test

The tree is organised by **who should see**, not by topic (specification, section 3).

| Folder | Permissions | Flags |
| --- | --- | --- |
| `/infra/` | `admin: manage`, `user: read`; no inheritance | `externalBlocked`, `sharingBlocked` |
| `/internal/` | `user: write` | `sharingBlocked` |
| `/best-practices/` | `user: write` | — |
| `/clients/<client>/<project>/{meetings,technical}/` | `user: write` (inherited from `/clients/`) | — |
| `/imported/` | only `admin: manage`; no inheritance | — |

The importer creates the missing folders **inheriting the parent's permissions**. That is why the root structure has to be created beforehand with its permissions.

(The folder names are given in English, as the tree will be created for the real migration; the local test and the mapping sheet in `.local/` used the Spanish equivalents: `/interno/`, `/buenas-practicas/`, `/clientes/`, `reuniones`, `tecnico`, `/importado/`.)

## 5. Classification used in the first pass

Keyword rules on the title (or the first heading), applied in this order:

1. Sensitive topics (salaries, layoffs, costs) → `/imported/`.
2. Communication, brand and web → `/internal/communication/`.
3. Infra (access, servers, runbooks) → `/infra/...`, even if it belongs to a client.
4. Grava Workflow → `/best-practices/grava-workflow/`.
5. Jiku client or project named in the title → `/clients/<client>/<project>/{meetings|technical}/`.
6. Internal projects → `/internal/projects/<project>/`.
7. Templates, standards and guides → `/best-practices/`.
8. Team, administration, processes, sales → their folder in `/internal/`.
9. No match by title: the project that appears most in the content.
10. Rest → `/imported/`.

Notes with a possible secret never go to client folders: they go to `/infra/` or `/imported/`.

Result of that pass:

| Destination | Notes | High confidence | medium | low |
| --- | ---: | ---: | ---: | ---: |
| `/clients/` (31 clients) | 861 | 706 | 78 | 77 |
| `/internal/` | 403 | 252 | 139 | 12 |
| `/imported/` | 193 | 0 | 9 | 184 |
| `/infra/` | 175 | 65 | 97 | 13 |
| `/best-practices/` | 77 | 7 | 68 | 2 |
| **Total** | **1,709** | **1,030** | **391** | **288** |

There are 39 notes with `sugerir_skip` (suggest skip; almost empty or only images) that were not marked as `skip`.

## 6. Local test performed

The local import finished without errors: 1,709 notes in 9 min, 1,879 of 1,880 images, and the counts match the sheet in the 118 folders.

Verified:
- original author and date;
- rewritten links;
- search respecting permissions;
- idempotent reimport.

**What went wrong:** no relations with Jiku (see section 3).

## 7. Risks and tasks before the real migration

- **Rotate credentials:** the credentials of the 104 flagged notes have to be rotated. They were public in HedgeDoc. The dry-run report indicates the note, the rule and the line.
- **Public images:** they remain accessible in the Space as long as it is not closed. Decide whether it is closed or deleted after migrating.
- **HedgeDoc shutdown:** define the date, whether it stays read-only for a while, and whether the old URLs (`docs.grava.io/<id>`) are redirected to the new documents.
- **Clients not loaded in Jiku:** 26 of the 31 clients in the sheet do not exist in Jiku. Only EXO, Verifarma, Chiatib, Tesisler and Grava are in Jiku. Their notes cannot be related until they exist.

## 8. Open questions

1. **Classification:**
   - **Option A:** an agent reads each note and proposes folder, project, client and topics, with a justification. More precise, but slower and more costly.
   - **Option B:** fix the keyword rules.
   - In both cases, who reviews the 679 notes of medium or low confidence?
2. **Clients not in Jiku:** are they loaded into Jiku before migrating, or do those notes stay by folder only, without a relation?
3. **Folder structure:** is the tree of section 4 kept? Which subfolders per project (`meetings`, `technical`, others)?
4. **Discarding:**
   - What is done with the 39 almost empty notes?
   - And with the ~10,290 anonymous ones (spam)? One option is to ignore them and delete them along with HedgeDoc.
5. **File names:** the note title in kebab-case, or is the original title kept?
6. **Visibility:** should any migrated note be `public` for external clients, or all `internal` and published later by hand?
7. **Sensitive notes:** what is done with the 6 HR and salary notes that today go to `/imported/`?
8. **Sales proposals:** should the project teams see the ones that ended up in `/clients/.../technical/`?
9. **Authorship:** is it enough to leave the original author in the message of version 1? Or do HedgeDoc users have to be mapped to Jiku users?
10. **The 13 unresolved links** between notes: are they left as they are or fixed by hand?
11. **Procedure:** does the real migration go as a service to the production instance in a single cutover, or in batches (one client at a time) with a review by each team?

## 9. Files (in `.local/`, not versioned)

| File | What it is |
| --- | --- |
| `codimd-2026-10-24.sql` | HedgeDoc dump |
| `hedgedoc-mapping.csv` | Mapping sheet with the review columns (`confianza` — confidence, `motivo` — reason, `posible_secreto` — possible secret, project and client suggestions) |
| `hedgedoc-mapping-resumen.md` | Counts, rules and the list of notes with a possible secret |
| `hedgedoc-import-*-report.csv` | Dry-run, import and reimport reports |
