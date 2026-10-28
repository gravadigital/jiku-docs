# What Jiku Docs does

One store of markdown documents for the whole team, linked to the work in Jiku, with a narrow,
explicit window for clients.

## Documents

A **document** is a markdown file (`.md`, UTF-8, at most 5 MiB) with an optional YAML frontmatter.
The frontmatter is part of the content and is the source of truth for the document's title,
visibility, topics and relations:

```markdown
---
title: Access to the staging server
visibility: internal
topics: [infra, access]
jiku:
  clients: [4]
  projects: [12, 31]
  requirements: [812]
  tasks: []
---

# Access to the staging server
...
```

- `title`: if missing, the first `#` heading; if there is none, the file name without extension.
- `visibility`: `internal` (default), `public` or `restricted` — see [Who sees what](#who-sees-what).
- `topics`: free tags, normalised to kebab-case without accents. A topic exists as soon as a
  document uses it.
- `jiku`: a strict block. Only `clients`, `projects`, `requirements` and `tasks`, as lists of Jiku
  ids. Every id is checked against core; one that does not exist is `invalid_relation`.
- Other top-level keys are kept in the content and ignored.

Because the frontmatter is content, changing a relation, a topic or the visibility is an ordinary
save and creates a version.

Deleting a document is **logical**: it stops being listed, keeps its versions and can be restored
for **30 days**. After that it is purged with all its versions (their objects go through the
[trash](operations.md#trash-and-restore-from-trash) first).

## Relations

A document can be related to any number of Jiku **clients, projects, requirements and tasks**.
Relations never grant access; they say what the document is about. A document related to a
requirement or a task of a project appears among that project's documents too, marked as an
**indirect** match (the item carries `projectMatch: indirect`; `direct` when the project is in the
document's own `jiku.projects`).

- **Assigning a relation** (`documents.{id}.relations.update`) rewrites only the `jiku:` block of
  the frontmatter and saves a new version with an automatic message ("Project assigned: …").
  Adding a project adds its client if missing. A change that changes nothing succeeds with the
  current version.
- **Suggested relations** (`documents.suggest-relations`): up to five candidate projects or
  clients, found by matching the names, name parts and codes of projects and the names of clients
  from core against the title (which weighs more) and the content. Projects already related are
  left out. Nothing is changed until someone assigns one.
- **Daily review.** Core publishes no events for clients or projects, so once a day jiku-docs
  checks every document's relations against core and removes the ones that point to entities that
  no longer exist, in a new version written by the service.

## Generated location

Documents are filed in **folders**, but folders only organise the files for the CLI and the sync
daemon — they decide nothing about access, and the web never shows them. When a document is
created without a folder, the server generates its location from the relations in the frontmatter,
creating the folders it needs:

| The document has | It is filed in |
|---|---|
| a project | `/clients/<client>/<project>/` (the client comes from the project in core, or from the first `jiku.clients`) |
| a client, no project | `/clients/<client>/general/` |
| neither | `/general/` |

Names are slugs of the names in core (kebab-case, no accents); with several projects or clients,
the first one as written in the frontmatter is used. Without a file name, the title in kebab-case
plus `.md` is used, made unique in the folder with `-2`, `-3`, …. **Changing relations later never
moves the document.** Documents imported from HedgeDoc without a mapping go to `/imported/`.

## Topics

Free tags in the frontmatter. `topics.list` returns the topics in use with the number of documents
each — counting only documents the caller can read — and filters by prefix, which is what the web
uses to suggest existing topics while typing.

## Who sees what

Access is decided by the **caller's class** and the document's **visibility**, never by folders.

| Visibility (web label) | `admin` | `user` | `external-user` | service (`internal-app`) |
|---|---|---|---|---|
| `internal` — **Internal** (default) | reads, writes | reads, writes | never | everything |
| `public` — **Visible to the client** | reads, writes | reads, writes | reads, if related to a project they have permission on in Jiku | everything |
| `restricted` — **Restricted** | reads, writes | **never** | **never** | everything |

- **What you cannot see does not exist.** A restricted document does not exist for a `user` or an
  external user by any route: lists and their counts, `get` with or without a version, search and
  its snippets, the change feed, version history, topic counts, attachments, share links,
  notifications and relation suggestions. Asking for it by id answers `document_not_found`, exactly
  as for a document that never existed.
- **Only an admin (or a service) sets or lifts `restricted`.** Since visibility lives in the
  frontmatter, the rule is applied on every create, update, restore and relation change; a `user`
  who tries gets `permission_denied`.
- **External users** read only `public` documents related to their projects, only the current
  version, and **without the frontmatter** (it carries internal requirement and task ids). In
  relation lists they see only their own projects and no clients, requirements or tasks. They never
  write, never see folders and receive no notifications.
- **Services** (any machine user with the `internal-app` role, including Jiku's api when it calls
  without an `actor`) read and write everything.

Folder ACL rules, `inheritAcl`, `externalBlocked` and `sharingBlocked` are still accepted by the
contract but have **no effect** (deprecated). Folder commands are for admins and services only.

## Search

Full-text search in PostgreSQL with a Spanish configuration without accents (`es_unaccent`):
"aduana" and "aduanas" match, accents do not matter. The query accepts quoted phrases, `OR` and
`-word` to exclude. Results are **one per document**, with its best snippet (up to 500 characters)
and the headings that contain it, ranked by relevance; at most 50, without paging. Search accepts
the same filters as listing.

Listing (`documents.list`) filters by client, project (directly or through a requirement or task),
requirement, task, topic, visibility, author, dates, folder, path prefix and file name, plus
"without project", "without client" and "without topic" (`hasProject`, `hasClient`, `hasTopic` set
to `false`). Each item carries its `clients`, `projects` and the names of who created and last
edited it.

Only the current version of each document is indexed. The index is derived and can be rebuilt with
`jiku-docs reindex`.

## Versions and history compaction

Every save creates a new version, numbered in sequence, with author, author name, date, origin
(`via`: `human`, `agent` or `service`) and an optional message. Saves need the **base version** the
content was made from; if someone saved after it, the answer is `version_conflict` instead of a lost
change. Saving content identical to the current version succeeds without a new version.
**Restoring** creates a new version with the content of an older one; history is never rewritten.

Editors that save every few seconds would produce thousands of versions a day, so once a day the
history is **compacted**:

| Age of the version | What is kept |
|---|---|
| under 24 h | every version |
| 24 h to 30 days | the last version of each editing session (same author, same `via`, no pause of 30 minutes) |
| over 30 days | the last version of each calendar day |
| any | protected versions and the current one |

**Protected** versions are never removed: the first version, every restore, every version saved
with a message, every version written by a service, and any version shown by an active pinned
share link. An agent's versions follow the same session rule as a person's. To keep a point
forever, save it with a message.

Version numbers can therefore have gaps. Asking for a compacted version answers `version_pruned`
with the nearest kept versions. Details in [operations.md](operations.md#history-compaction).

## Locks

One editor, many readers. A person or agent can take a **lock** on a document for an editing
session (30 to 300 seconds, default 120, renewed by taking it again with the same session). While a
lock is held, saves from other sessions answer `document_locked`, naming the holder. Without a lock
anyone who can write may still save — always with a base version. Locks expire on their own; an
admin can force one open.

## Attachments

Files attached to a document, stored through **core's file service** (jiku-docs keeps no attachment
bucket). Allowed types and sizes are core's policy. An attachment is uploaded to a presigned URL,
confirmed (jiku-docs checks the SHA-256), and linked from the markdown with a stable link,
`{JIKU_WEB_URL}/docs/attachments/{id}`, which checks permissions every time it is opened and is
never a storage URL. Unconfirmed uploads are removed after 24 hours.

## Share links

A **share link** lets anyone who has it read one document without an account, until it expires or
is revoked. It is meant for a supplier or a one-off recipient; for clients with an account,
`public` visibility related to their project is the better tool, because it follows their access.

- Any internal person who can read the document creates and revokes links. A `restricted` document
  takes no links, and making a document restricted revokes its links.
- The token (`jds_` prefix, 256 random bits) and its URL, `{JIKU_PUBLIC_URL}/p/docs/{token}`, are
  shown **once**; only a hash is stored. A retry of the same creation within 5 minutes returns the
  same link.
- Expiry is mandatory: default 30 days, at most 180.
- Mode `latest` always shows the current version; `pinned` shows a fixed one.
- Attachments of the shared document are included unless turned off.
- The page shows only the body, rendered to sanitised HTML, never the frontmatter.
- Each access is counted and recorded with the IP as an HMAC and the user agent truncated.
- Any invalid link — revoked, expired, deleted document, restricted — answers the same
  `share_not_found` (404 on the public route).

A **secret detector** checks every save for credential patterns. It does not block the save: the
reply carries the warning `possible_secret_detected` and the document is flagged, so sharing it
then needs an explicit acknowledgement.

## Notifications and events

Every confirmed change is written to a changelog **in the same transaction** as the change
(outbox), so no confirmed change is left without an event. From there:

- **Events** go to the JetStream stream `JIKU_DOCS_EVENTS`, for services only (Jiku, connectors,
  jiku-docs' own indexer): document, folder, attachment and share events, deduplicated by
  `eventId`, kept 30 days. They never carry document content or presigned URLs.
- **Notifications** go to each person who can read the document, on
  `{instance}.{userId}.docs-notify.v1.*`, without relation details. A restricted document notifies
  only admins. When a document stops being readable for someone, they get `access.changed`, with no
  detail, as a signal to resynchronise.
- **The change feed** (`documents.changes`) lets a client catch up on what it missed, filtered by
  what it can read; it keeps 30 days.

## Sync

The `jiku docs sync` daemon of jiku-go mirrors every document a person can read into a local
folder, with the same paths, and uploads local edits back. Live notifications bring remote changes
in seconds; the change feed covers disconnections; conflicts never overwrite local work. See
[user-guide.md](user-guide.md#the-sync-daemon).

## HedgeDoc import

`jiku-docs import hedgedoc` reads a MariaDB loaded with a HedgeDoc dump and creates the documents
as the service, with:

- a mapping sheet (CSV) that assigns each note a folder, file name, relations and topics;
- the original author in the first version's message and the original dates;
- images downloaded and uploaded as attachments, links between notes rewritten to the new documents;
- a dry run, an optional check of the relations against core, and a per-note report that lists
  possible secrets (rule and line, never the value);
- idempotent re-imports that never overwrite a note someone edited in jiku-docs.

Unmapped notes go to `/imported/` as `restricted`. See [operations.md](operations.md#hedgedoc-import).

## What Jiku Docs does not do

- **No simultaneous collaborative editing.** One editor holds the lock; others read live.
- **Markdown only.** No other formats in the document tree.
- **No semantic search.** `mode` other than `text` answers `semantic_search_not_configured`.
- **No secret storage.** Documents describe access; credentials belong in a secret manager.
- **No HTTP of its own** and no user management: identity comes from Jiku's identity provider
  through the bus.
