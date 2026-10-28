# jiku-docs: technical specification v1

Oct 24, 2026 · @Lautaro Alvarez

## 1. Summary, scope and requirements

`jiku-docs` is a new service of the Jiku ecosystem that stores Grava's documentation in markdown, with per-folder permissions, relations to Jiku entities, full-text search and real-time sync. It replaces the current HedgeDoc instance, which has no API and exposes every document publicly.

It follows the same pattern as `core`: it has no HTTP of its own, it serves request/reply on the Jiku NATS bus, takes the identity from the subject (guaranteed by the auth-callout) and publishes events to JetStream. Frontends reach it through the Jiku `api`; people (sync daemon, agents) and services reach the bus directly.

### Scope of v1

- Hierarchical folders with inheritable permissions.
- Versioned markdown documents, with frontmatter that relates them to Jiku clients, projects, requirements and tasks.
- Attachments in S3 linked from the markdown.
- Indexed catalog: full-text search, filterable by metadata.
- Editing sessions with lock and version control, for agent-assisted work.
- Real-time per-person notifications and a change feed for reconciliation.
- Public read-only links to share a document with third parties, with expiry and revocation.
- Documentation commands and sync daemon inside jiku-go, used by people and agents.
- Import from HedgeDoc.

### Out of scope for v1

- Simultaneous collaborative editing (CRDT). The model is one editor and many live readers; the architecture allows adding it later.
- Formats other than markdown in the document tree.
- Secret storage. Documents describe access, but credentials live in a secrets manager.
- Semantic search (embeddings). Search is full-text; the contract already provides for the `semantic` and `hybrid` modes so they can be added without incompatible changes.
- WebDAV adapter.

### Functional requirements

| Id | Requirement |
| --- | --- |
| RF-01 | Every operation is authenticated by the Jiku identity (Zitadel via auth-callout), except reading through a share link |
| RF-02 | Read, write and administration permissions per folder, inherited by subfolders |
| RF-03 | A document lives in a folder and can be related to several clients, projects, requirements and tasks |
| RF-04 | Cross-cutting documents (infrastructure, internal, best practices) coexist with client documents |
| RF-05 | Only `.md` files are accepted in the document tree |
| RF-06 | Attachments in S3, linked from the markdown, with the document's permissions |
| RF-07 | Version history with author, date and origin (person, agent or service) |
| RF-08 | An agent can open, edit and save a document on behalf of the person it works with |
| RF-09 | Whoever has a document open or synced receives the changes within seconds |
| RF-10 | Search by content, project, client, topic, date and author, respecting permissions |
| RF-11 | External clients can see public documents of the projects they already see in Jiku |
| RF-12 | Jiku can list the documents related to a project, requirement or task |
| RF-13 | Whoever administers a folder can generate a public read-only link to a document, with mandatory expiry, and revoke it at any time |
| RF-14 | The module is optional: Jiku discovers whether it is installed and available, and enables or disables each documentation feature in the api, the web and the connectors without configuration changes |

### Non-functional requirements

| Id | Requirement |
| --- | --- |
| RNF-01 | p95 latency of simple queries under 300 ms; text search under 500 ms |
| RNF-02 | A change notification reaches connected clients in under 3 s |
| RNF-03 | No confirmed change is lost: outbox for events and a reconcilable change feed |
| RNF-04 | Commands safe to retry (idempotency by id and base version) |
| RNF-05 | Deny-by-default on permissions, filter fields and payload keys, like the rest of Jiku |
| RNF-06 | Target scale: 30 users, 20,000 documents, documents of up to 5 MiB |
| RNF-07 | AsyncAPI contracts as the source of truth, validated in tests |

## 2. Architecture

`jiku-docs` is a Go process with two NATS micro services (`jiku-docs-commands` and `jiku-docs-queries`), its own PostgreSQL database and two S3 buckets. It does not access the Jiku database: everything it needs from Jiku it queries from `core` through `jiku-queries`.

```
   web / opus-web            jiku (CLI and sync)          Jiku services
        │ HTTP              (person with own token)      (internal-app role)
        ▼                             │                           │
   Jiku api ───── actor ──┐           │                           │
                          ▼           ▼                           ▼
                 NATS + auth-callout (identity in the subject) ◀─────────────┐
                          │                       │                          │
                          ▼                       ▼                          │
                        core               jiku-docs ── outbox ──▶ JIKU_DOCS_EVENTS
                          │             │      │      │                    │
                    Jiku PostgreSQL     │      │      └─▶ docs-notify        ▼
                                        ▼      ▼          (per person)    indexer
                              Postgres docs   S3 docs / S3 attachments
```

### Components

| Component | Responsibility | Technology |
| --- | --- | --- |
| `jiku-docs` (service) | Commands and queries, authorization, versioning, locks, markdown and frontmatter validation, outbox | Go, NATS micro |
| Outbox publisher | Reads the committed changelog and publishes to `JIKU_DOCS_EVENTS` and to the `docs-notify` of each authorized person | Go, same binary |
| Indexer | Consumes `JIKU_DOCS_EVENTS`, splits the text into chunks and maintains the catalog | Go, same binary or separate process |
| Postgres docs | Tree, ACL, documents, versions, relations, changelog, index (full-text) | PostgreSQL 16 |
| S3 docs | Content of each version of each document | Bucket without versioning; whatever the service deletes goes through `trash/` |
| S3 attachments | Attachment bytes | Separate bucket, no public access |
| `jiku-go` | Existing library and CLI; they add the `jiku docs` group, the `jiku docs sync` daemon and the `modules` and `modules/docs` packages | Go |
| Jiku api | HTTP routes for the frontends, module discovery and attachment redirection | Existing, extended |

### Design decisions

1. **Storage is the source of truth for content; Postgres for metadata.** Each version is an immutable object in S3 until compaction removes it (history retention policy: [`history-compaction-v1.md`](history-compaction-v1.md)). The index is derived and can be regenerated.
2. **The frontmatter is the source of truth for relations, topics and visibility.** It is parsed on every write. This way a document edited locally can be related to a project without using another tool.
3. **The subject sets the identity.** `jiku-docs` does not validate tokens; it trusts the auth-callout, just like `core`.
4. **Commands are idempotent.** Client-generated ids (ULID) and a mandatory base version on modifications.
5. **Events are never lost.** They are written to the changelog in the same transaction as the command and published from there (outbox).
6. **Two event channels.** Full stream for services only; permission-filtered notifications for people.
7. **One editor, many readers.** Per-session lock with expiry; the rest see the changes live.

8) **The module is optional.** Jiku stores no documentation data and discovers the module through a registry on the bus; without it, Jiku works the same (section 13).

## 3. Domain model

> **P-1 (contract 1.3.0, `docs/p1/service.md`):** the folder no longer defines who sees a document: its `visibility` (`internal`, `public`, `restricted`) does, together with the caller class (section 4). The location only organises the files for the CLI and the sync daemon; if `documents.new` does not carry it, the server generates it from the relations. Where this section talks about folders deciding access, P-1 prevails.

Each document has a **location** (the folder, which organises the files) and **relations** (what it is about, which never grant access). This allows cross-cutting documents without duplicating them.

### Entities

| Entity | Description | Identifier |
| --- | --- | --- |
| Folder | Tree node. It has access rules, decides whether it inherits the parent's and whether it allows external clients (`externalBlocked`) and share links (`sharingBlocked`) | ULID |
| Document | Markdown file inside a folder, with a current version and metadata derived from the frontmatter | ULID |
| Version | Immutable content of a document at a point in time, with author and origin | `documentId` + sequential number |
| Relation | Link between a document and a Jiku client, project, requirement or task | Jiku numeric id + type |
| Topic | Normalised free-form tag (`infra`, `access`, `meetings`) | Slug |
| Attachment | Binary file in S3, belonging to a document | ULID |
| Editing session | Temporary lock on a document by a person, optionally assisted by an agent | `sessionId` (ULID) |
| Share link | Bearer credential that grants anonymous read access to a single document, with expiry | ULID (the secret token is stored only as a hash) |

### Recommended folder tree

```
/internal/               processes, administration, team
/infra/                  servers, access, runbooks
/best-practices/         standards, Grava Workflow
/clients/<client>/<project>/
    meetings/            meeting summaries
    technical/           architecture, decisions, integrations
```

The tree is organised by who should see, not by topic. A document about the staging server lives in `/infra/` and is related to the projects that use it; when searching for "documents of project X" it shows up together with those in `/clients/<client>/X/`, as long as the person can read `/infra/`.

### Visibility

Each document has a `visibility`: `internal` (default), `public` or `restricted` (P-1).

- `internal`: every internal person (`admin`, `user`) reads and writes it.
- `public`: external people with permission on any of its projects also read it (section 4). Any internal person can set it.
- `restricted`: only `admin` and services. For a `user` or an external person it does not exist through any path. Only an `admin` (or a service) sets or removes it, in `new`, `update`, `restore` and `relations.update`; a `user` gets `permission_denied`. It does not allow share links, and moving a document to `restricted` revokes the active ones.

### Document format

UTF-8, LF line endings (the service normalises CRLF), maximum size 5 MiB. File name: `^[^/\\]{1,200}\.md$`, unique per folder, case-insensitive.

The YAML frontmatter is optional:

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

Parsing rules:

- `title`: if missing, the first `#` heading is used; if there is none either, the file name without extension.
- `visibility`: `internal`, `public` or `restricted`. Any other value is a `frontmatter_invalid` error.
- `topics`: list of strings; normalised to kebab-case without accents. Topics are created when used.
- `jiku`: strict block. It only accepts `clients`, `projects`, `requirements` and `tasks`, with lists of numeric ids. Each id is validated against `core`; one that does not exist or is not visible to the writer is an `invalid_relation` error.
- Other top-level keys are kept in the content and ignored.

Since the frontmatter is part of the content, changing relations, topics or visibility is a normal document update and creates a version.

### Attachment links

Attachments are linked with a stable Jiku web URL (D-5: `{JIKU_WEB_URL}/docs/attachments/{id}`, which the web serves with its session and forwards to the `api`), never with the S3 URL:

```markdown
![Network diagram]({JIKU_WEB_URL}/docs/attachments/01JBX7Q2M4N8P6R3T5V9W1Y0ZA)
```

The `api` resolves that route by checking permissions and redirects to a short-lived presigned URL (section 9).

## 4. Identity and authorization

`jiku-docs` does not validate tokens: the subject's `user-id` is the identity, and the auth-callout guarantees that nobody publishes under someone else's id. On top of that identity, the service decides permissions with the caller class, the document's visibility and data it queries from `core`.

> **P-1 (contract 1.3.0, `docs/p1/service.md`):** the folder ACL is **without effect** (it is kept in the database and in the contract as deprecated), as are `externalBlocked` and `sharingBlocked`:

| Visibility | `admin` | `user` | `external-user` | service |
| --- | --- | --- | --- | --- |
| `internal` | reads and writes | reads and writes | never | everything |
| `public` | reads and writes | reads and writes | reads, if related to a project in their `project-permissions` | everything |
| `restricted` | reads and writes | never | never | everything |

`folders.*` (commands) is only for `admin` and services; internal people see every folder and write documents in any of them. `unlock` with `force` is only for `admin`. Any internal person creates and revokes links for `internal` and `public` documents. The P-1 data migration (on startup, only once) moved to `restricted` every document that a `user` could not read with the ACL in force and to `internal` the `public` ones under `externalBlocked`. The subsections "Folder ACL" and "External clients" describe the previous model.

### Caller classes

| Class | How it is recognised | Effective identity | Read | Write |
| --- | --- | --- | --- | --- |
| `person-internal` | `users.identity_type = person` and an internal role | The subject's `user-id` | `internal` and `public`; `restricted` only `admin` (P-1) | Same as read |
| `person-external` | `users.identity_type = person` and a client role | The subject's `user-id` | Only `public` documents related to projects visible in `project-permissions` | Never |
| `api` | The `user-id` is the one of the Jiku `api` | The person declared in `actor` (`actor.id`) | According to the class given by `actor.roles` | According to the class given by `actor.roles` |
| `service` | `identity_type = service` with the `internal-app` role | The subject's `user-id` | Everything, without trimming | Everything; attributed to the service |

Mapping of Zitadel roles to classes: `admin` and `user` are `person-internal`; `external-user` is `person-external`; `internal-app` is `service`. Any other role (`core`, `bus-observer`) has no access to `jiku-docs`.

`actor` rules, the same as `core`'s:

- Only the `api` can send the `actor` key. If any other caller sends it: `invalid_fields`.
- If the `api` does not send `actor`, the call is treated as coming from the `api` service.
- The `actor` is the same envelope the `api` sends to `core`: `{ id, roles, name?, username?, email? }`, with `roles` taken from the already verified web token (D-11). An `actor` without `roles` or with unknown keys is `invalid_fields`.
- The person's class comes from `actor.roles` (`admin`/`user` → `person-internal`, `external-user` → `person-external`, anything else → `caller_not_authorized`) and the per-role ACL rules use those same roles; for the `api` with `actor`, `users.list` is not queried. Authorship is always `actor.id`. The `project-permissions` of an external person are still requested from `core`.
- Authorship fields in the payload do not exist: the author is always the effective identity.

### Data queried from core

| Data | Query | Cache |
| --- | --- | --- |
| Roles and `identity_type` of the caller (people over the bus and services; not the `api` with `actor`) | `users.list` with `filter.id` | 60 s per user |
| Projects visible to an external person | `project-permissions.list` with `filter.userId` | 60 s per user |
| Existence of entities in relations | `clients.list`, `projects.list`, `requirements.list`, `tasks.list` with `filter.id` | 5 min |

If `core` does not respond, the operations that depend on that data fail with `upstream_unavailable` (access is never granted by default). Cached data keeps being used until it expires.

Since `core` does not publish client or project events, a daily task checks the relations of every document against `core` and removes those pointing to entities that no longer exist, recording it in the document's history.

A user without a row in `users` (because of the mirroring problem described in the connector guide, §3.4) receives `unknown_caller`. That problem does not affect people who come in through the web: the `api` sends their fresh roles in `actor` (D-11).

### Folder ACL

Each rule is: folder + principal + permission.

| Field | Values |
| --- | --- |
| `principalType` | `role` (a Zitadel role, as it appears in `users.roles`) or `user` (a `user-id`) |
| `principal` | The role or the `user-id` |
| `permission` | `read`, `write` (includes read) or `manage` (includes write, allows changing the ACL, moving folders and publishing) |

Effective permission of a person on a folder:

1. The folder's rules that match the person (by `user-id` or by any of their roles) are collected.
2. If the folder has `inheritAcl = true`, the effective rules of the parent folder are added, recursively.
3. The effective permission is the highest one found. Without rules, there is no access.

There are no deny rules: to restrict a subfolder, set `inheritAcl = false` and define its explicit rules. The root folder has `inheritAcl = false` and a `manage` rule for the admin role.

### Visibility rules on reads

- A document or folder the person cannot read answers `*_not_found`, without distinguishing "does not exist" from "not visible", just like `core`.
- A write on something that can be read but not written answers `permission_denied`.
- The results of `list`, `search` and `changes` are filtered by permissions before paginating.

### External clients

An external user sees a document if all three conditions hold: `visibility = public`, the document has a relation with a project (or a requirement or task of a project) that appears in their `project-permissions`, and the document's folder does not have the `externalBlocked = true` flag. That flag allows closing whole folders (for example `/infra/`) to clients even if someone publishes a document by mistake. The external person reads only the current version: earlier ones may predate its publication. They do not see the frontmatter either (it has internal ids of requirements and tasks): see `documents.get` in section 7.

### Share links

A share link is a bearer credential: whoever has the link reads the document without identifying themselves. That is why it is the only read that does not go through the bus; it is resolved by the Jiku `api` on a public HTTP route (section 9.8).

It coexists with access by clients with an account, but serves another purpose. For clients who have a user in opus-web, the right thing is `visibility: public` related to their projects: it is tied to their identity and is revoked together with their access. The link is for one-off cases: someone without an account, a supplier, a document that is sent once.

| Rule | Definition |
| --- | --- |
| Who creates | Any internal person who reads the document (P-1); a `restricted` document does not allow links (`sharing_blocked`) |
| Who revokes | Any internal person who reads the document (P-1) |
| Blocked folders | Deprecated since P-1: `sharingBlocked` has no effect. Replaced by `restricted` |
| Token | 256 random bits in base64url with the `jds_` prefix (lets secret scanners detect it). Returned only once; only its SHA-256 is stored |
| Mode | `latest` always shows the current version; `pinned` shows a fixed version |
| Expiry | Mandatory. Default 30 days, maximum 180 |
| Attachments | Included by default; can be disabled per link. Only attachments of the shared document are served |
| What is shown | Only the body rendered to sanitised HTML. The frontmatter is always removed (it contains Jiku ids and internal topics). Links to other documents are shown as text |
| Invalidation | Revoking, expiring, deleting the document or moving it to `restricted` cuts the link on the next access. Deleting or restricting the document revokes its links (`revokeReason`: `document_deleted` or `restricted`): restoring it or setting it back to `internal` does not reactivate them |
| Secrets | If the secret detector flagged the document, `shares.new` requires `acknowledgeSecretWarning: true` |
| Audit | Each link records creator, date, access counter and last access. Each access is stored with a hashed IP and a truncated user agent |

A link that is invalid for any reason answers the same (`share_not_found`, and 404 on the public route), so as not to reveal whether it existed, expired or was revoked.

## 5. Bus protocol

The protocol copies `core`'s conventions (subject grammar, response shape, filters and pagination) so that whoever already integrated Jiku does not have to learn anything new. There are three differences: idempotency, outbox and content transfer.

### Subjects

```
Commands:       {instance}.{user-id}.jiku-docs-commands.v1.{method}
                prod.281234567890123456.jiku-docs-commands.v1.documents.01JBX7Q2M4.update
Queries:        {instance}.{user-id}.jiku-docs-queries.v1.{method}
                prod.281234567890123456.jiku-docs-queries.v1.documents.search
Events:         {instance}.docs-events.v1.{entity}.{action}
                prod.docs-events.v1.document.updated
Notifications:  {instance}.{user-id}.docs-notify.v1.{entity}.{action}
                prod.281234567890123456.docs-notify.v1.document.updated
```

| Segment | Value |
| --- | --- |
| `instance` | `dev` / `prod`, same as Jiku's `NATS_INSTANCE` |
| `user-id` | The `sub` of the caller's token, raw |
| `version` | `v1`. Commands and queries are versioned together; events and notifications, separately |
| `method` | In commands, ids go in the subject (`documents.{id}.update`). In queries, in the payload |

ULID ids are valid as subject tokens (26 uppercase alphanumeric characters).

### Caller connection

Same as with `core`: sentinel credentials, Zitadel token with hot refresh and an `inboxPrefix` derived from the `user-id` (`_INBOX.` + first 16 characters of `lowercase(base32_without_padding(sha256(userId)))`). Without `inboxPrefix` replies do not arrive and the symptom is a timeout.

### Response

```jsonc
{ "status": "success", "data": { ... } }
{ "status": "failure", "errorCode": "version_conflict", "errorMessage": "...", "errorDetails": { "currentVersion": 8 } }
```

Always decide by `errorCode`; `errorMessage` is human-readable English text for the interface.

### Error catalog

| Code | Meaning | Retry |
| --- | --- | --- |
| `invalid_fields` | Unknown key, field not declared in filter, sort or fields, or `actor` sent by a caller that is not the `api` | No: caller bug |
| `invalid_cursor` | `filter` or `sort` changed between pages | Restart pagination |
| `document_not_found` / `folder_not_found` / `attachment_not_found` | Does not exist or is not visible | No |
| `permission_denied` | Can be read but not written or administered | No |
| `version_conflict` | `baseVersion` differs from the current one. `errorDetails.currentVersion` | Re-read, merge and retry |
| `version_pruned` | Requesting (`documents.get` with `version`), restoring (`restore`) or pinning (`shares.new` with `mode: pinned`) a version that history compaction deleted. `errorDetails`: `requestedVersion`, `previousAvailable` and `nextAvailable` (the closest kept ones, or `null`). A `person-external` never receives it: for them any version other than the current one is `document_not_found` without `errorDetails`. See [`history-compaction-v1.md`](history-compaction-v1.md) | Request one of the kept versions |
| `document_locked` | Another session holds the lock. `errorDetails`: `holder`, `via`, `expiresAt` | Wait or ask for release |
| `lock_not_held` | The session does not hold the lock it tries to renew or release | Take it again |
| `already_exists` | A client `id` already used with different content, or a repeated file name in the folder | No |
| `file_name_invalid` | Name that does not end in `.md` or has forbidden characters | No |
| `frontmatter_invalid` | Malformed YAML or invalid value. `errorDetails`: `line`, `field`, `reason` | No |
| `invalid_relation` | Jiku id that does not exist or is not visible. `errorDetails`: `entityType`, `entityId` | No |
| `content_too_large` | More than 5 MiB, or more than 256 KiB inline | Use upload by URL |
| `checksum_mismatch` | The declared SHA-256 does not match the content | Recompute |
| `upload_not_found` | `contentUploadId` expired or nonexistent | Request another URL |
| `folder_not_empty` | Attempt to delete a folder with content | No |
| `attachment_type_not_allowed` / `attachment_too_large` | Attachment policy | No |
| `query_timeout` | The query exceeded the database time limit | Narrow the filter |
| `unknown_caller` / `caller_not_authorized` | No row in `users` or a role without permission | Reconnect |
| `upstream_unavailable` | `core` did not answer a required query | Retry with backoff |
| `internal_error` | Anything else | Retry with backoff |

The catalog may grow within `v1`: an unknown code is treated as a generic error.

Share link errors:

| Code | Meaning | Retry |
| --- | --- | --- |
| `share_not_found` | Link nonexistent, revoked, expired, of a deleted document or of a blocked folder (not distinguished) | No |
| `sharing_blocked` | The folder or an ancestor has `sharingBlocked` | No |
| `share_expiry_invalid` | `expiresAt` in the past or beyond the maximum (180 days) | Fix the date |
| `secret_warning_not_acknowledged` | The document has a possible-secret warning and `acknowledgeSecretWarning: true` was not sent | Review and confirm |

Module configuration errors (section 13):

| Code | Meaning | Retry |
| --- | --- | --- |
| `semantic_search_not_configured` | `documents.search` with `mode` `semantic` or `hybrid` (not available in v1) | Use `mode: text` |
| `attachments_not_configured` | Attachment operation without a configured bucket | No |

### Timeouts and limits

| Parameter | Value |
| --- | --- |
| Recommended timeout for commands | 5 s |
| Recommended timeout for queries | 10 s (the database cuts at 8 s with `query_timeout`) |
| Inline content in commands and responses | Up to 256 KiB |
| Maximum document size | 5 MiB |
| Presigned URLs | Expire after 300 s |
| `page.limit` | Default 50, maximum 200 |

Above 256 KiB the content travels through S3: the `documents.request-content-upload` command returns an upload URL and a `contentUploadId`, and `documents.get` returns `contentUrl` instead of `content`.

### Idempotency

Unlike `core`, every `jiku-docs` command can be retried after a timeout:

| Command | Mechanism |
| --- | --- |
| Creations (`folders.new`, `documents.new`) | The client generates the `id` (ULID). Repeating with the same `id` and the same content returns the existing resource; with different content, `already_exists` |
| Document modifications | `baseVersion` is mandatory. If the content sent is identical to the current version (same SHA-256), it answers `success` with that version even if `baseVersion` is stale |
| Locks | Taking a lock the same session already holds renews it |
| Deletions | Deleting something already deleted answers `success` |

### Filters, sort and pagination

Same grammar as `jiku-queries`: `filter` (with the `gte` and `lte` operators and a single level of `or`), `sort` (with `-` for descending; `id` is always appended), `page` with a keyset cursor, `fields`, `include` and `count`. The absence of `page.cursor` is the only end signal. `meta.describe` returns the fields, filters, sorts and enums of each resource.

## 6. Commands (`jiku-docs-commands`)

19 commands (P-1 adds `documents.{id}.relations.update`). All answer `{ status, data }`; on creations and modifications `data` carries at least `id` and, for documents, `version`. The details of each payload are in the `jiku-docs-commands.yaml` contract; the schemas reject unknown keys.

### Folders

| Method | Payload | Response | Permission | Specific errors |
| --- | --- | --- | --- | --- |
| `folders.new` | `id`, `parentId`, `name`, `inheritAcl?` (default `true`), `externalBlocked?`, sharingBlocked? (the three deprecated, without effect) | `{ id, path }` | `admin` or service | `folder_not_found`, `already_exists`, `permission_denied` |
| `folders.{id}.edit` | `name?`, `parentId?` (move), `inheritAcl?`, `externalBlocked?`, sharingBlocked? | `{ id, path }` | `admin` or service | `folder_not_found`, `already_exists`, `invalid_fields` (move into itself) |
| `folders.{id}.delete` | empty | `{ id }` | `admin` or service | `folder_not_empty` |
| `folders.{id}.acl.replace` | `rules: [{ principalType, principal, permission }]` (max. 100). Deprecated: the rules have no effect | `{ id, rules }` | `admin` or service | `invalid_fields` |

### Documents

| Method | Payload | Response | Permission | Specific errors |
| --- | --- | --- | --- | --- |
| `documents.new` | `id`, `folderId?`, `fileName?`, `content` or `contentUploadId`, `checksum`, `via?`, `message?` | `{ id, version: 1, path, folderId, fileName }` | Internal person or service; `restricted` only `admin` | `folder_not_found`, `permission_denied`, `already_exists`, `file_name_invalid`, `frontmatter_invalid`, `invalid_relation`, `content_too_large`, `checksum_mismatch`, `upload_not_found` |
| `documents.{id}.update` | `baseVersion`, `content` or `contentUploadId`, `checksum`, `sessionId?`, `via?`, `message?` | `{ id, version }` | `write` | `version_conflict`, `document_locked`, `frontmatter_invalid`, `invalid_relation`, `content_too_large`, `checksum_mismatch` |
| `documents.{id}.move` | `baseVersion`, `folderId?`, `fileName?` | `{ id, version, path }` | `write` on source and destination | `version_conflict`, `document_locked`, `already_exists`, `file_name_invalid` |
| `documents.{id}.delete` | `baseVersion` | `{ id }` | `write` | `version_conflict`, `document_locked` |
| `documents.{id}.restore` | `fromVersion`, `baseVersion` | `{ id, version }` | `write` | `version_conflict`, `document_not_found` (nonexistent version) |
| `documents.{id}.lock` | `sessionId`, `via` (`human` / `agent`), `ttlSeconds?` (30 to 300, default 120) | `{ id, sessionId, expiresAt }` | `write` | `document_locked` |
| `documents.{id}.unlock` | `sessionId`, `force?` | `{ id }` | `write`; `force` only `admin` or service | `lock_not_held` |
| `documents.{id}.relations.update` | `baseVersion`, `add?`, `remove?` (`{ clients, projects, requirements, tasks }`), `sessionId?` | `{ id, version, relations }` | `write` | `version_conflict`, `document_locked`, `invalid_relation`, `invalid_fields` |
| `documents.request-content-upload` | `checksum`, `size` | `{ contentUploadId, uploadUrl, expiresIn }` | Any internal person or service | `content_too_large` |

Notes:

- **`via`** records the origin of the change: `human`, `agent` or `service`. For services `service` is forced. It does not affect permissions; it is traceability.
- **`delete`** is logical: the document is no longer listed, its versions are kept and it can be restored with `documents.{id}.restore` for 30 days; after that it is permanently removed together with its versions.
- **`restore`** creates a new version with the content of `fromVersion`; it never rewrites history. On a deleted document, it reactivates it.
- **Lock:** if a document has a lock, writes from other sessions fail with `document_locked`. Without a lock, any person with `write` can write, always with `baseVersion`. The lock is renewed by repeating `lock` with the same `sessionId` before `expiresAt`.
- **Generated location (P-1):** without `folderId`, the server creates (if missing) and uses `/clients/<client>/<project>/` (the client comes from the project according to core or from the first of `jiku.clients`; without a client, `no-client`), `/clients/<client>/general/` or `/general/`, with kebab-case slugs without accents of the core names and the first project or client as listed in the frontmatter. Without `fileName`, the title in kebab-case `.md`, unique in the folder (`-2`, `-3`…). Changing relations later does not move the document.
- **`relations.update` (P-1):** rewrites only the `jiku:` block of the frontmatter ((current ∪ `add`) ∖ `remove`; adding a project adds its client if missing) and creates an `update` version with an automatic message ("Project assigned: EXO - Videoconferencia"). Without changes it answers `success` with the current version. It does not carry `via`: the origin comes from the caller class.
- **Names (P-1):** each write stores the author's name (`actor.name` or `username` with the api, the `name` from `users.list` over the bus, the service name for a service), which queries return as `createdByName`, `updatedByName`, `authorName` and `lock.holderName`. If the `actor` arrives without `name` or `username` (the web token does not carry them), the name comes from core's `users.list`. On read, a stored `null` name is filled in from core without writing it to the database. It is a label and not an access decision: with core down it stays `null` and the operation continues (in-memory cache of 10 min, 1 min for ids core does not know, 30 s without querying after a failure, 2 s wait at most).
- **Content by URL:** the client requests `request-content-upload`, does a `PUT` of the content to `uploadUrl` and then sends `contentUploadId` in `new` or `update`. The service verifies the SHA-256 when consuming it. A `contentUploadId` is used only once.

### Attachments

| Method | Payload | Response | Permission | Specific errors |
| --- | --- | --- | --- | --- |
| `attachments.request-upload` | `id`, `documentId`, `fileName`, `mimeType`, `fileSize`, `checksum` | `{ id, uploadUrl, expiresIn, link }` | `write` on the document | `attachment_type_not_allowed`, `attachment_too_large`, `already_exists` |
| `attachments.{id}.confirm` | empty | `{ id, status: "ready" }` | The same caller that requested the upload | `upload_not_found`, `checksum_mismatch` |
| `attachments.{id}.request-download` | empty | `{ id, downloadUrl, expiresIn }` | `read` on the document | `attachment_not_found` |
| `attachments.{id}.delete` | empty | `{ id }` | `write` on the document | `attachment_not_found` |

`link` is the stable URL to paste into the markdown (`{JIKU_WEB_URL}/docs/attachments/{id}`, D-5). An attachment stays in `pending` status until `confirm`; pending ones older than 24 h are deleted. The type and size policy (default: images, PDF, spreadsheets, archives; 50 MiB) is configured in the service.

### Share links

| Method | Payload | Response | Permission | Specific errors |
| --- | --- | --- | --- | --- |
| `shares.new` | `id`, `documentId`, `mode` (`latest` / `pinned`), `version?` (mandatory with `pinned`), `expiresAt`, `includeAttachments?` (default `true`), `note?`, `acknowledgeSecretWarning?` | `{ id, token, url, expiresAt }` | Internal person who reads the document (not `restricted`) | `document_not_found`, `permission_denied`, `sharing_blocked`, `share_expiry_invalid`, `secret_warning_not_acknowledged`, `already_exists` |
| `shares.{id}.revoke` | empty | `{ id, revokedAt }` | Internal person who reads the document | `share_not_found`, `permission_denied` |

`token` and `url` appear only in the `shares.new` response; they cannot be queried again. If the link is lost, it is revoked and another one is created. Retrying `shares.new` with the same `id` after a timeout returns the same link only within 5 minutes (the service keeps the token encrypted for that time to cover this case); after that it answers `already_exists` without a token.

## 7. Queries (`jiku-docs-queries`)

14 queries (P-1 adds `documents.suggest-relations`), with the same grammar as `jiku-queries`. All filter by permissions before returning or paginating; a `restricted` document does not exist for a `user` or for an external person in any of them (not in counts, snippets, feed or topics).

| Method | Purpose | Main parameters |
| --- | --- | --- |
| `meta.describe` | Describes resources, fields, filters, sorts and enums | none |
| `folders.list` | Browse the tree | `filter`: `parentId`, `pathPrefix`, `id[]`; `include`: `effectivePermission` |
| `folders.get` | Folder detail | `id`; `include`: `acl` (deprecated; only `admin` and services), `effectivePermission` (`write` for `user`, `manage` for `admin`) |
| `documents.list` | Filterable catalog | see filters below |
| `documents.get` | Document with its content | `id`, `version?`; `include`: `content`, `relations`, `topics`, `lock`, `attachments`. Carries `prunedVersionsCount` (not to external people); a pruned `version` answers `version_pruned` |
| `documents.versions.list` | History (with gaps in the numbering because of compaction) | `documentId`, `author`, `via`, `createdAt`, `kind`, `protected`; sorted by `version`. Each version carries `kind` (`create`, `update`, `restore`) and `protected` |
| `documents.search` | Full-text search | `query`, `mode`, `filter`, `limit` |
| `documents.changes` | Change feed for reconciliation | `sinceSeq`, `limit`, `filter.pathPrefix?` |
| `topics.list` | Topics in use, with document count | `filter.prefix?` |
| `attachments.list` | Attachments of a document | `filter.documentId` |
| `documents.suggest-relations` | Candidates for "Asignar sugerido" (assign suggested) (P-1) | `id`. Returns up to 5 `{ projectId, clientId, score, reason }` based on names, parts of the name or code of projects and names of clients from core found in the title (weighs more) and the content; excludes projects already related; a client-only candidate carries `projectId: null`. An external person gets `permission_denied` |

### `documents.list` filters

| Filter | Type | Notes |
| --- | --- | --- |
| `id` | ULID or list | Fetch several by id |
| `folderId` | ULID | Only that folder |
| `pathPrefix` | string | That folder and its descendants (`/clients/acme/`) |
| `clientId`, `projectId`, `requirementId`, `taskId` | integer or list | By relation with Jiku. `projectId` includes documents related to requirements or tasks of that project |
| `topic` | string or list | Exact slug match |
| `visibility` | `internal` / `public` / `restricted` | `restricted` requested by someone who cannot read it: empty list |
| `hasProject`, `hasClient`, `hasTopic` | boolean | P-1. `false`: "Sin proyecto" (no project; neither direct nor through a requirement or task), "Sin cliente" (no client; without `jiku.clients`), "Sin tema" (no topic) |
| `createdBy`, `updatedBy` | user-id or list | Author of the first or the last version |
| `createdAt`, `updatedAt` | range (`gte`, `lte`) | ISO 8601 UTC |
| `fileName` | string | Prefix, case-insensitive |
| `deleted` | boolean | Default `false` |

Allowed sort: `updatedAt`, `createdAt`, `title`, `path`. Base fields: `id`, `title`, `path`, `folderId`, `version`, `visibility`, `updatedAt`, `updatedBy`, `updatedByName`, `clients`, `projects` (P-1; `createdByName` is requested in `fields`). With `filter.projectId` each item carries `projectMatch` (`direct` if the project is in `jiku.projects`, `indirect` if it comes through a requirement or a task); the same in `documents.search`, whose items also carry `clients`, `projects`, `createdByName`, `updatedByName` and `visibility`. For an external person, `clients` is empty and `projects` only contains theirs. `documents.list` never returns content; that is what `documents.get` is for.

### `documents.get`

```jsonc
// request
{ "id": "01JBX7Q2M4N8P6R3T5V9W1Y0ZA", "include": ["content", "relations", "lock"] }

// response
{ "status": "success", "data": {
  "id": "01JBX7Q2M4N8P6R3T5V9W1Y0ZA", "title": "Access to the staging server",
  "path": "/infra/servers/staging.md", "version": 7, "checksum": "sha256:9f2c…",
  "visibility": "internal", "updatedAt": "2026-10-24T13:02:11.123Z", "updatedBy": "281234567890123456",
  "content": "---\ntitle: …",          // or "contentUrl" if it exceeds 256 KiB
  "relations": { "clients": [4], "projects": [12, 31], "requirements": [812], "tasks": [] },
  "lock": { "sessionId": "01JC…", "holder": "281234567890123456", "via": "agent", "expiresAt": "…" }
} }
```

For a `person-external` (over the bus or through the `api` with `actor`), `content` and `contentUrl` carry the document **without frontmatter**, and the `checksum` and `sizeBytes` of that response are those of that body, not those of the stored file (D-11). If the body exceeds 256 KiB, a derived copy is stored under `renders/` (the purge deletes it after 24 h) and `contentUrl` points to it. `documents.versions.list` does the same with the only version an external person sees. In `documents.list`, `documents.changes` and the events, `checksum` identifies the stored version (full file): an external person uses it to detect changes, not to verify the received content. The `documents.search` snippets never include frontmatter.

### `documents.search`

```jsonc
{
  "query": "\"customs integration\" -test",    // quotes for phrases, OR, and -word to exclude
  "mode": "text",                              // only value in v1; semantic or hybrid → semantic_search_not_configured
  "filter": { "clientId": 4, "updatedAt": { "gte": "2026-06-01T00:00:00Z" } },
  "limit": 20                                   // max. 50
}
```

Response: list of chunks with `documentId`, `version`, `title`, `path`, `headingPath` (the headings that contain the chunk), `snippet` (up to 500 characters), `score`, `updatedAt` and `updatedBy`. It accepts the same filters as `documents.list`. It does not paginate: it returns the best `limit` results.

### `documents.changes`

Feed ordered by a global, monotonic sequence number (`seq`). It is what the daemon uses when reconnecting.

```jsonc
// request
{ "sinceSeq": 18340, "limit": 500, "filter": { "pathPrefix": "/clients/" } }

// response
{ "status": "success", "data": {
  "items": [ { "seq": 18341, "type": "document.updated", "documentId": "01JB…", "version": 8, "path": "/clients/acme/pzt/meetings/2026-10-24.md" } ],
  "nextSeq": 18341, "hasMore": false
} }
```

It includes only changes to documents the person can read at the time of the query. Permissions may change after an event, so a document that stopped being visible does not show up as deleted: the daemon detects it with the full reconciliation (section 10). The feed keeps 30 days; an older `sinceSeq` answers `invalid_cursor` and forces a full reconciliation.

### Share link queries

| Method | Who | Parameters | Returns |
| --- | --- | --- | --- |
| `shares.list` | `manage` on the document's folder | `filter`: `documentId`, `status` (`active` / `revoked` / `expired`), `createdBy` | Links with `id`, `mode`, `version`, `expiresAt`, `revokedAt`, `createdBy`, `accessCount`, `lastAccessAt`, `note`. Never the token |
| `shares.resolve` | Only the `api` (without `actor`) | `token` | `{ shareId, documentId, title, version, html, attachments: [{ id, fileName, mimeType }] }` or `share_not_found` |
| `shares.attachment` | Only the `api` (without `actor`) | `token`, `attachmentId` | `{ downloadUrl, expiresIn }` or `share_not_found` |

`shares.resolve` and `shares.attachment` answer `caller_not_authorized` to any caller that is not the `api`. `html` arrives sanitised, without frontmatter and with attachment links already rewritten to the public route; if it exceeds 256 KiB, `htmlUrl` (presigned, 300 s) comes instead. Each successful `resolve` increments the access counter and records the access.

## 8. Events, notifications and change feed

Each confirmed change is written to the changelog in the same transaction as the command. Three things come out of there: the full event for services, the filtered notification for people and the `documents.changes` feed. No confirmed change can be left without an event.

### Outbox

```
command → validates → transaction { writes metadata + inserts changelog(seq) } → COMMIT → replies
                                                  │
                    outbox publisher ◀───────────┘  (LISTEN/NOTIFY + sweep every 5 s)
                         │
                         ├─▶ JetStream JIKU_DOCS_EVENTS   (Nats-Msg-Id = eventId: dedupe)
                         ├─▶ docs-notify of each authorized person
                         └─▶ sets published_at in the changelog
```

The content is uploaded to S3 before opening the transaction, with a key that includes the checksum. If the transaction fails an orphan object is left, which a cleanup process deletes; there is never metadata pointing to a nonexistent object.

### `JIKU_DOCS_EVENTS` stream

| Parameter | Value |
| --- | --- |
| Subjects | `{instance}.docs-events.v1.>` |
| Retention | `limits`, `max_age` 30 days |
| Delivery | At-least-once; deduplicate by `eventId` |
| Duplicates on publish | 2 min deduplication window with `Nats-Msg-Id` |
| Consumers | Services only: indexer, Jiku and authorized connectors. People have no permission on the stream |

### Event catalog (15)

| Event | Emitted by |
| --- | --- |
| `document.created` | `documents.new` |
| `document.updated` | `documents.{id}.update` |
| `document.moved` | `documents.{id}.move` |
| `document.deleted` | `documents.{id}.delete` |
| `document.restored` | `documents.{id}.restore` |
| `document.locked` | `documents.{id}.lock` (when taken, not when renewed) |
| `document.unlocked` | `documents.{id}.unlock` or lock expiry |
| `folder.created` | `folders.new` |
| `folder.updated` | `folders.{id}.edit` |
| `folder.deleted` | `folders.{id}.delete` |
| `folder.acl.changed` | `folders.{id}.acl.replace` |
| `attachment.created` | `attachments.{id}.confirm` |
| `attachment.deleted` | `attachments.{id}.delete` |
| `share.created` | `shares.new`. Never carries the token |
| `share.revoked` | `shares.{id}.revoke`, and the automatic revocation when the document is deleted (`changes.reason`: `manual` / `document_deleted`) |

`share.*` events only go to the stream; they do not generate per-person notifications. Accesses to a link do not generate events: they stay in the access log and in the `shares.list` counters.

### Event envelope

```jsonc
{
  "eventId": "01JC…",                       // ULID, deduplication key
  "seq": 18341,                             // position in the changelog
  "type": "document.updated",
  "version": "v1",
  "occurredAt": "2026-10-24T13:02:11.123Z",
  "correlationId": "…",
  "actor": { "id": "281234567890123456", "name": "Lautaro", "via": "agent" },
  "entity": { "type": "document", "id": "01JB…", "folderId": "01JA…", "path": "/infra/servers/staging.md" },
  "snapshot": { "title": "…", "version": 8, "checksum": "sha256:…", "visibility": "internal",
                "relations": { "projects": [12, 31] }, "topics": ["infra"] },
  "changes": { "version": { "from": 7, "to": 8 }, "relations": { "added": { "projects": [31] } } }
}
```

The envelope never carries the document content: whoever needs it requests it with `documents.get`. `snapshot` is the metadata state after the change.

### Per-person notifications

> **P-1:** they are computed with the visibility model: a `restricted` document notifies only the admins. When a document moves to `restricted`, each `user` receives `access.changed` (without detail); when it stops being `public`, the same goes for the external people with permission on any of its projects. The sync daemon reconciles with that signal.

Subject: `{instance}.{user-id}.docs-notify.v1.{entity}.{action}`. They are plain NATS messages (no JetStream): if the person is not connected, they are lost, and the `documents.changes` feed covers the gap.

- Same envelope as the event, without `changes.relations` or `snapshot.relations` (they could reveal projects the person does not see).
- They are sent to every person with `read` on the document at that moment, whether connected or not, as computed by the outbox publisher. At the target scale the cost is negligible and there is no need to track presence.
- `folder.acl.changed` is notified to every internal person as `access.changed`, without detail: it is the signal for the daemon to reconcile.

### Consuming the stream from a service

Same as `JIKU_EVENTS`: own durable with a unique name, explicit ack, `filter_subject` with the version (`{instance}.docs-events.v1.>`), deduplication persisted by `eventId`, and ignoring the events caused by the service itself (`actor.id`).

## 9. Main flows

Eight walkthroughs cover the v1 use cases. In all of them, the identity comes from the subject and each write generates a version, changelog entry and event.

### 9.1 Working on a document with an agent

1. The person asks the agent to work on a topic. The agent runs `jiku docs search` or `jiku docs query documents.list` and picks the document.
2. The agent runs `jiku docs get <id> -o json` and keeps `version` as the base version.
3. If the document has a lock from another session (reported by `get`), the agent says who is editing it and offers to work read-only.
4. For a long edit, it takes the lock with `jiku docs lock <id> --session <ulid> --ttl 300` and renews it by repeating the command before it expires.
5. Each change the person accepts is saved with `jiku docs put <id> <file> --base-version <n> --session <ulid> --message "..."`, with `JIKU_VIA=agent` in the environment. The response carries the new version, which becomes the base.
6. Each save triggers `document.updated`: the web and the daemons of whoever can read it update the document within seconds.
7. When done, the agent runs `jiku docs unlock <id> --session <ulid>`. If it does not, the lock expires on its own.

If `version_conflict` arrives in step 5 (someone wrote without a lock, for example from an offline daemon that reconnected), the agent re-reads with jiku docs get, shows the difference and proposes a merge before retrying.

### 9.2 Remote change to a synced file

1. Another person saves a new version.
2. The outbox publisher sends `docs-notify.v1.document.updated` to each connected person with `read`.
3. The daemon receives the notification, compares the version with the one it has recorded and, if it is higher, calls `documents.get`.
4. If the local file has no pending changes, it replaces it atomically (writes to a temporary file and renames). The editor reloads it.
5. If the local file has unuploaded changes, it saves the remote version as `<name>.remoto-v<version>.md` ("remote"; a jiku-go literal) next to the original and notifies the user.
6. The daemon updates its local `lastSeq` with the event's `seq`.

### 9.3 Local change uploaded by the daemon

1. The daemon detects the change on disk (fsnotify) and waits 2 s without new changes (debounce).
2. It computes the SHA-256. If it matches the recorded version, it does nothing.
3. It calls `documents.{id}.update` with `baseVersion` = recorded version and `via: human`.
4. On `success`, it records the new version. On `version_conflict`, it downloads the remote version as a copy and notifies. On `document_locked`, it shows who is editing and retries when `document.unlocked` arrives.
5. A new `.md` file in a synced folder is created with `documents.new` (id generated by the daemon). A file with another extension is ignored and reported.

### 9.4 Reconnection and reconciliation

1. On startup or reconnection, the daemon calls `documents.changes` with its `lastSeq` and applies each change as in 9.2.
2. If it receives `invalid_cursor` (more time passed than the feed retention) or `access.changed`, it does a full reconciliation: paginated `documents.list` with `fields: [id, path, version, checksum]` and compares with its local record.
3. Local documents that no longer appear in the listing are moved to a `.jiku-docs/sin-acceso/` ("no access"; a jiku-go literal) folder and reported, instead of being deleted.
4. In addition, the daemon does a full reconciliation every 24 h.

### 9.5 Uploading and viewing an attachment

1. The client calls `attachments.request-upload` with its own `id` and the file data.
2. It does a `PUT` of the file to `uploadUrl`, directly to S3.
3. It calls `attachments.{id}.confirm`; the service verifies that the object exists and that the checksum matches.
4. It inserts `link` into the markdown and saves the document with `update`.
5. When the link is opened (`{JIKU_WEB_URL}/docs/attachments/{id}`, D-5), the web forwards it with the person's session to the `api` (`GET /api/docs/attachments/{id}`), which authenticates it, calls `attachments.{id}.request-download` as `actor` and answers 302 to the presigned URL.

### 9.6 Documents of a project in Jiku

1. A person opens a project in the Jiku web.
2. The `api` calls `documents.list` with `filter.projectId` and `actor`.
3. `jiku-docs` returns the documents related to the project, its requirements or its tasks that this person can read, including the cross-cutting ones in `/infra/` or `/best-practices/`.
4. For an external client in opus-web, the same call returns only the `public` ones (section 4).

### 9.7 Import from HedgeDoc

1. Export the HedgeDoc notes in markdown with their metadata (title, author, dates).
2. A team member assigns each note a destination folder in a mapping sheet; unassigned ones go to `/imported/` with access for administration only.
3. A script creates the documents with `documents.new` as a service, adds the frontmatter with the original date and keeps the original author in the `message` field of the first version.
4. Before shutting HedgeDoc down, review the notes with sensitive data: until now they were public, so any credential that shows up must be rotated.

### 9.8 Sharing a document through a public link

1. From the web (or the agent, with the person's confirmation) the document, the mode (`latest` or `pinned`), the expiry and whether attachments are included are chosen.
2. The `api` calls `shares.new` with `actor`. `jiku-docs` checks `manage` and that the folder does not have `sharingBlocked`, generates the token, stores its hash and answers `token` and `url`.
3. The web shows the link only once to copy it, with the expiry date.
4. The recipient opens `https://{JIKU_PUBLIC_URL}/p/docs/{token}`. The `api` serves the route without a session, applies per-IP rate limiting and calls `shares.resolve` as a service.
5. If the link is valid, the `api` answers a read-only page with the received `html` and the headers `X-Robots-Tag: noindex`, `Referrer-Policy: no-referrer`, `Cache-Control: private, no-store` and a CSP that does not allow scripts.
6. The images and attachments of the page point to `/p/docs/{token}/attachments/{id}`; the `api` calls `shares.attachment` and answers 302 to a short-lived presigned URL.
7. If the link is invalid for any reason, the `api` answers 404 with a generic page.
8. To cut access, someone with `manage` (or the creator) calls `shares.{id}.revoke`; the next access already answers 404.

## 10. Clients: jiku-go

The official client of the module is [`jiku-go`](https://github.com/gravadigital/jiku-go), the binary and library that already exists to talk to Jiku over NATS. There is no separate daemon or MCP server: people and agents use the same CLI, and Go services the same library. `jiku-go` already handles the inbox prefix, the token with refresh, the subjects and validation against `meta.describe`; the module reuses all of that.

### 10.1 Library

| Package | What it adds |
| --- | --- |
| `modules` | Reads `JIKU_MODULES`, does the ping and returns the status (`not_installed`, `incompatible`, `unavailable`, `available`) and the `serviceName`. Opt-in, like `events` |
| `modules/docs` | `jiku-docs` client on the same `jiku.Client`: the generic operations (`List`, `Get`, `Iterate`, `Contract`) pointed at the service from the registry, plus helpers for whatever has its own steps (`Search`, `GetContent`, `Put`, `New`, `Lock`, `Attach`, `Share`) |

Design notes:

- The package is called `modules/docs` because `docs/` is already the repository's documentation folder.
- The service name comes from the registry (`serviceName`); nothing is compiled with `jiku-docs`.
- Since `jiku-docs` answers `meta.describe` with the same shape as `core`, the contract as data, the upfront filter validation and cursor pagination work unchanged.
- The module's error codes are added as constants (`CodeVersionConflict`, `CodeDocumentLocked`, `CodeShareNotFound`, etc.), to use with `IsCode`.
- The `jiku-docs` service itself uses the library to query `core` with its machine user.

### 10.2 CLI: `jiku docs` group

Two levels. The generic ones are the commands that already exist for `core`, pointed at the module:

| Command | What it does |
| --- | --- |
| `jiku docs status` | Module status and `serviceName` |
| `jiku docs describe [resource]` | The module's contract, from its `meta.describe` |
| `jiku docs query <method>` | Any query, with `--filter`, `--sort`, `--fields`, `--include`, `--all` and upfront validation |
| `jiku docs cmd <method> <json>` | Any command, with the id in the method |

The ergonomic ones hide the steps an agent would get wrong by hand:

| Command | What it does |
| --- | --- |
| `jiku docs ls [path]` | Folders and documents of a path |
| `jiku docs search <text>` | `documents.search`, with `--mode` and `--filter` |
| `jiku docs get <id or path>` | Raw markdown to stdout; version, checksum and lock to stderr. `-o json` returns everything together; `--version N` an earlier version |
| `jiku docs put <id> <file>` | `documents.{id}.update` with mandatory `--base-version`, optional `--session` and `--message` |
| `jiku docs new <file> --folder <path or id>` | `documents.new` |
| `jiku docs mv`, `rm`, `restore` | Move, delete and restore, always with `--base-version` |
| `jiku docs history <id>` | Version history |
| `jiku docs lock <id>`, `unlock <id>` | Editing session with `--session` and `--ttl` |
| `jiku docs attach <id> <file>` | Requests the upload, uploads the file, confirms and prints the link for the markdown |
| `jiku docs share <id> --expires 30d` | Creates a share link (`--pinned`, `--no-attachments`, `--note`) and prints the URL, which cannot be queried again |
| `jiku docs shares <id>`, `revoke <shareId>` | Lists and revokes links |
| `jiku docs edit <id>` | For people: takes the lock, opens `$EDITOR`, saves and releases, renewing the lock while the editor is open |
| `jiku docs sync [folder]` | The sync daemon (10.4) |

What every ergonomic command does without anyone asking: generate the ULID for creations (retrying is safe), compute the SHA-256, use the upload by URL when the content exceeds 256 KiB, accept a path (`/infra/servers/staging.md`) where an id goes and send `via` according to `JIKU_VIA` (10.5).

```
jiku docs search "customs integration" --filter clientId=4
jiku docs get /infra/servers/staging.md > staging.md
jiku docs put 01JBX7Q2M4N8P6R3T5V9W1Y0ZA staging.md --base-version 7 --message "New SSH port"
jiku docs share 01JBX7Q2M4N8P6R3T5V9W1Y0ZA --expires 15d --note "Hosting provider"
```

If the module is not installed or does not respond, any `jiku docs` exits with a non-zero code and a message that distinguishes the two cases.

### 10.3 Changes in the rest of jiku-go

| Command | Change |
| --- | --- |
| `jiku doctor` | One more line for the module: not installed, incompatible, down or available, with the ping response time |
| `jiku batch` | Optional `service` field on each line (`core` by default, `docs`), to mix queries of both in a single connection |
| `jiku raw` | Unchanged: `--service jiku-docs-queries` already works |

`jiku-go`'s semantic compatibility covers the new command and flag names from the version that incorporates them.

### 10.4 `jiku docs sync` daemon

It runs in the foreground or as a system service (systemd or launchd) and mirrors into a local folder the documents the person can read.

| Aspect | Definition |
| --- | --- |
| Login | `jiku-go`'s: `jiku login` (device flow) for people, key file for unattended use |
| Local folder | `~/JikuDocs/` by default; it mirrors the tree with the same paths |
| Scope | Everything readable, or a selection of folders configured by the person |
| Local state | SQLite in `~/JikuDocs/.jiku-docs/state.db`: `documentId`, path, version, checksum, `lastSeq` |
| Local permissions | Files in folders with only `read` are read-only on disk |
| Real time | Subscription to `{instance}.{user-id}.docs-notify.v1.>` |
| Reconciliation | `documents.changes` on reconnect; full on `access.changed`, `invalid_cursor` or every 24 h |
| Conflicts | Never overwrites local changes: saves the remote version as a copy and notifies |
| Ignored | Non-`.md` files, hidden files and editor temporary files (`.swp`, `~`, `.#*`) |
| Visible status | Log on stderr and `jiku docs sync status` (latest changes and pending conflicts). A graphical interface is out of v1 |

Attachments are not synced: they stay as links that open in the browser.

### 10.5 Agents

Agents use the CLI with the contract they already have at hand; the `jiku docs` commands are added to it. There is no MCP server in v1.

- **Authorship.** The CLI runs with the person's token (`jiku login`), so whatever an agent writes is attributed to that person. To record that an agent did it, its environment sets `JIKU_VIA=agent`; without the variable, the CLI sends `via: human`.
- **Sessions.** Each CLI invocation is a separate process, so an agent does not keep a lock renewed in the background. The real protection is `--base-version`; for long edits, the agent takes the lock with `--ttl 300` and renews it by repeating `jiku docs lock` with the same `--session`.
- **Rules in the agent's contract.** Search before creating, to avoid duplicates. Cite document and version when answering with information from the documentation. Do not save changes the person did not accept. Do not run `jiku docs share` or `revoke` without explicit confirmation: the CLI cannot enforce that gate, it depends on these instructions.
- **Agents without a shell.** If one shows up (for example, an assistant in the browser), a `jiku mcp` subcommand can expose the same library operations as MCP tools, with per-tool approval in the client.

### 10.6 WebDAV adapter (optional)

A separate process that exposes the tree over WebDAV for whoever wants to mount a drive or use standard tools. It translates each operation into bus commands and queries (`PROPFIND` to `folders.list` and `documents.list`, `GET` to `documents.get`, `PUT` to `documents.new` or `update` using the ETag as base version, `LOCK` to `documents.{id}.lock`). Since native WebDAV clients do not support OIDC, it authenticates with personal tokens issued from the Jiku web. It is not part of v1.

## 11. Data model and indexing

Own PostgreSQL database (`jiku_docs`), separate from Jiku's. The service uses a user with write access; the indexer can use the same one or one restricted to the index tables.

### Tables

> **P-1 (migration `0005`):** `documents.visibility` accepts `restricted`; columns for names stored on write (`documents.created_by_name`, `documents.updated_by_name`, `document_versions.author_name`, `document_locks.holder_name`); `shares.revoke_reason` accepts `restricted`. The data migration (visibility and names) runs in Go on startup and is recorded in `service_state` (`migration:p1-visibility`, `migration:p1-names`).

```sql
CREATE TABLE folders (
  id               text PRIMARY KEY,             -- ULID
  parent_id        text REFERENCES folders(id),
  name             text NOT NULL,
  path             text NOT NULL UNIQUE,         -- materialised: '/clients/acme/pzt/'
  inherit_acl      boolean NOT NULL DEFAULT true,
  external_blocked boolean NOT NULL DEFAULT false,
  sharing_blocked  boolean NOT NULL DEFAULT false,  -- effective = OR with the ancestors
  created_by       text NOT NULL,
  created_at       timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX folders_name_uq ON folders (parent_id, lower(name));

CREATE TABLE folder_rules (
  folder_id      text NOT NULL REFERENCES folders(id) ON DELETE CASCADE,
  principal_type text NOT NULL CHECK (principal_type IN ('role','user')),
  principal      text NOT NULL,
  permission     text NOT NULL CHECK (permission IN ('read','write','manage')),
  PRIMARY KEY (folder_id, principal_type, principal)
);

CREATE TABLE documents (
  id              text PRIMARY KEY,              -- ULID generated by the client
  folder_id       text NOT NULL REFERENCES folders(id),
  file_name       text NOT NULL,
  title           text NOT NULL,
  visibility      text NOT NULL DEFAULT 'internal' CHECK (visibility IN ('internal','public','restricted')),  -- P-1
  current_version integer NOT NULL,
  checksum        text NOT NULL,                 -- 'sha256:<hex>' of the current version
  size_bytes      integer NOT NULL,
  created_by      text NOT NULL,
  created_at      timestamptz NOT NULL,
  updated_by      text NOT NULL,
  updated_at      timestamptz NOT NULL,
  deleted_at      timestamptz
);
CREATE UNIQUE INDEX documents_name_uq ON documents (folder_id, lower(file_name)) WHERE deleted_at IS NULL;

CREATE TABLE document_versions (
  document_id text NOT NULL REFERENCES documents(id),
  version     integer NOT NULL,
  storage_key text NOT NULL,                     -- 'docs/<documentId>/<version>-<sha256>.md'
  checksum    text NOT NULL,
  size_bytes  integer NOT NULL,
  author      text NOT NULL,
  via         text NOT NULL CHECK (via IN ('human','agent','service')),
  session_id  text,
  message     text,
  created_at  timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (document_id, version)
);

CREATE TABLE document_relations (
  document_id text NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
  entity_type text NOT NULL CHECK (entity_type IN ('client','project','requirement','task')),
  entity_id   integer NOT NULL,
  project_id  integer,                           -- resolved project, to filter by project
  PRIMARY KEY (document_id, entity_type, entity_id)
);

CREATE TABLE document_topics (
  document_id text NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
  topic       text NOT NULL,
  PRIMARY KEY (document_id, topic)
);

CREATE TABLE document_locks (
  document_id text PRIMARY KEY REFERENCES documents(id),
  session_id  text NOT NULL,
  holder      text NOT NULL,
  via         text NOT NULL,
  expires_at  timestamptz NOT NULL
);

CREATE TABLE attachments (
  id          text PRIMARY KEY,
  document_id text NOT NULL REFERENCES documents(id),
  file_name   text NOT NULL,
  mime_type   text NOT NULL,
  size_bytes  bigint NOT NULL,
  checksum    text NOT NULL,
  storage_key text NOT NULL,
  status      text NOT NULL CHECK (status IN ('pending','ready','deleted')),
  uploaded_by text NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE content_uploads (
  id          text PRIMARY KEY,
  checksum    text NOT NULL,
  size_bytes  integer NOT NULL,
  storage_key text NOT NULL,
  requested_by text NOT NULL,
  expires_at  timestamptz NOT NULL,
  consumed_at timestamptz
);

CREATE TABLE changelog (
  seq          bigserial PRIMARY KEY,
  event_id     text NOT NULL UNIQUE,            -- ULID
  type         text NOT NULL,
  entity_type  text NOT NULL,
  entity_id    text NOT NULL,
  folder_id    text,
  payload      jsonb NOT NULL,                  -- full event envelope
  occurred_at  timestamptz NOT NULL DEFAULT now(),
  published_at timestamptz
);
CREATE INDEX changelog_unpublished ON changelog (seq) WHERE published_at IS NULL;
```

`project_id` in `document_relations` is resolved on save (for requirements and tasks `core` is queried) and lets `filter.projectId` include documents related to requirements or tasks of the project.

Share links:

```sql
CREATE TABLE shares (
  id                  text PRIMARY KEY,           -- ULID generated by the client
  document_id         text NOT NULL REFERENCES documents(id),
  token_hash          bytea NOT NULL UNIQUE,      -- sha256(token); the token is never stored in clear
  token_retry_cipher  bytea,                      -- encrypted token for retries; deleted after 5 min
  mode                text NOT NULL CHECK (mode IN ('latest','pinned')),
  version             integer,                    -- mandatory with mode = 'pinned'
  include_attachments boolean NOT NULL DEFAULT true,
  note                text,
  created_by          text NOT NULL,
  created_at          timestamptz NOT NULL DEFAULT now(),
  expires_at          timestamptz NOT NULL,
  revoked_at          timestamptz,
  revoked_by          text,
  revoke_reason       text CHECK (revoke_reason IN ('manual','document_deleted','restricted')),
  access_count        bigint NOT NULL DEFAULT 0,
  last_access_at      timestamptz,
  CHECK (mode = 'latest' OR version IS NOT NULL)
);
CREATE INDEX shares_document ON shares (document_id);

CREATE TABLE share_accesses (
  share_id      text NOT NULL REFERENCES shares(id) ON DELETE CASCADE,
  accessed_at   timestamptz NOT NULL DEFAULT now(),
  kind          text NOT NULL CHECK (kind IN ('document','attachment')),
  attachment_id text,
  ip_hash       bytea NOT NULL,                   -- HMAC of the IP with a service key
  user_agent    text                              -- truncated to 200 characters
);
CREATE INDEX share_accesses_share ON share_accesses (share_id, accessed_at DESC);
```

`share_accesses` records are kept for 180 days.

History compaction (migration `0003`, [`history-compaction-v1.md`](history-compaction-v1.md), section 4):

```sql
ALTER TABLE document_versions
  ADD COLUMN kind text NOT NULL DEFAULT 'update' CHECK (kind IN ('create','update','restore')),
  ADD COLUMN protected boolean NOT NULL DEFAULT false;   -- create, restore, with message or via service

ALTER TABLE documents
  ADD COLUMN last_compacted_at timestamptz,
  ADD COLUMN pruned_versions_count integer NOT NULL DEFAULT 0;
CREATE INDEX documents_pending_compaction ON documents (id)
  WHERE deleted_at IS NULL AND (last_compacted_at IS NULL OR updated_at > last_compacted_at);

CREATE TABLE pruned_objects (                            -- objects to delete from S3 after the commit
  storage_key text PRIMARY KEY,
  document_id text NOT NULL,
  version     integer NOT NULL,
  pruned_at   timestamptz NOT NULL DEFAULT now(),
  version_row jsonb          -- migration 0004: the deleted row, which travels as metadata to the trash
);
```

Existing data is marked when migrating: version 1 is `create`, those with the fixed message `Restaurado desde la versión N` ("Restored from version N", the literal the service wrote when this migration was introduced; it now writes it in English) are `restore`, and creations, restorations, versions with a message and versions with `via` `service` are protected.

### Storage

| Bucket | Key | Notes |
| --- | --- | --- |
| `jiku-docs-content` | `docs/<documentId>/<version>-<sha256>.md` | Immutable objects until compaction deletes the version, the orphan sweep finds them without a row, or the document is purged 30 days after deletion; in those three cases the object goes through the trash |
| `jiku-docs-content` | `trash/<original key>` | Trash: every content object the service deletes is copied here and then the original is deleted. A lifecycle rule expires it after `DOCS_TRASH_RETENTION_DAYS` (default 7). `jiku-docs storage restore-from-trash` brings an object back (and, if it comes from compaction, its version row) |
| `jiku-docs-content` | `uploads/<contentUploadId>` | Large content in transit; lifecycle rule at 24 h; deleted directly, without the trash |
| `jiku-docs-attachments` | `att/<attachmentId>` | No public access; presigned URLs only |

The content bucket does not use versioning: the safety net for what the service deletes is the trash, with two time-based expiration rules (`uploads/` and `trash/`), which DigitalOcean Spaces supports; `NoncurrentVersionExpiration` is not needed. `jiku-docs storage init` creates the bucket, suspends versioning if it was enabled (old versions that already existed are not deleted on their own), applies the two rules and reads them back.

Only `jiku-docs` has credentials for the buckets, just like `core` with its own.

### Indexing

Search is full-text with PostgreSQL, without additional services.

```sql
CREATE EXTENSION IF NOT EXISTS unaccent;
CREATE TEXT SEARCH CONFIGURATION es_unaccent (COPY = spanish);
ALTER TEXT SEARCH CONFIGURATION es_unaccent
  ALTER MAPPING FOR hword, hword_part, word WITH unaccent, spanish_stem;

CREATE TABLE index_chunks (
  document_id  text NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
  version      integer NOT NULL,
  chunk_no     integer NOT NULL,
  heading_path text[] NOT NULL,
  body         text NOT NULL,
  tsv          tsvector NOT NULL,   -- title and headings with weight A, body with weight B (es_unaccent)
  PRIMARY KEY (document_id, chunk_no)
);
CREATE INDEX index_chunks_tsv ON index_chunks USING gin (tsv);
```

Indexer process for each `document.created`, `updated`, `moved`, `restored` or `deleted`:

1. If the document was deleted, it removes its chunks and stops.
2. It reads the version indicated in the event (if a newer one is already indexed, it discards the event).
3. It strips the frontmatter from the indexable text and splits by headings; sections of more than \~800 words are cut by paragraphs.
4. It computes the `tsvector` of each chunk (document title and headings with weight A, body with weight B) and replaces the document's chunks in one transaction.

Search: the query is interpreted with `websearch_to_tsquery('es_unaccent', ...)`, which supports quoted phrases, `OR` and exclusion with `-`. Results are ranked by `ts_rank_cd`, filtered by permissions and metadata with a `JOIN` to the folders readable by the person (computed once per query) and the `snippet` is built with `ts_headline`. With `es_unaccent` (Spanish stemming, since the documentation is written in Spanish), the variants of a word ("aduana", "aduanas") match and accents do not matter.

The index is fully regenerated with a maintenance command that walks `documents` and enqueues a synthetic event per document. If semantic search is added later, a vector column is added to this table plus an embeddings model, without changing the contracts.

## 12. Changes in Jiku, security and operations

Integrating `jiku-docs` requires security changes in the Jiku repository (role, templates and auth-callout rules) and new routes in the `api`. In `core` only the new role is registered; nobody touches its database.

### Auth-callout changes

| Change | File | Detail |
| --- | --- | --- |
| `docs-service` role | `rules.yaml` + new template `templates/docs-service.yaml` | Subscribe to `{instance}.*.jiku-docs-commands.v1.>` and `.jiku-docs-queries.v1.>` (with queue group); publish on `{instance}.{own-id}.jiku-queries.v1.>`; publish on `{instance}.docs-events.v1.>` and `{instance}.*.docs-notify.v1.>`; manage the `JIKU_DOCS_EVENTS` stream; consume `JIKU_EVENTS` (optional) |
| Person templates | `templates/<person>.yaml` | Publish on `{instance}.{own-id}.jiku-docs-commands.v1.>` and `.jiku-docs-queries.v1.>`; subscribe to `{instance}.{own-id}.docs-notify.v1.>` |
| Connector template | `templates/connector.yaml` | Publish on the docs command and query subjects; consume `JIKU_DOCS_EVENTS` with its own durable |
| `api` template | whichever applies | Publish on the docs command and query subjects |

### Changes in core

| Change | File |
| --- | --- |
| `docs-service` entry in `ROLE_METHODS` with `queries: ALL` and `commands: []` | `core/src/authorize-caller.ts` |
| `docs-service` in `SERVICE_ROLES` | `core/src/events/auth/identity-type.ts` |

### Changes in the api and the frontends

| Change | Detail |
| --- | --- |
| Document proxy routes | `GET /docs/documents`, `GET /docs/documents/{id}`, `GET /docs/search`, etc., which translate to `jiku-docs` queries and commands with `actor` |
| Attachment redirection | `GET /docs/attachments/{id}`: authenticates, calls `attachments.{id}.request-download` and answers 302 |
| Share link routes | `POST /docs/documents/{id}/shares`, `GET /docs/documents/{id}/shares`, `DELETE /docs/shares/{id}`, with `actor` |
| Public route | `GET /p/docs/{token}` and `GET /p/docs/{token}/attachments/{id}`, without a session: they call `shares.resolve` / `shares.attachment` without `actor`, with per-IP rate limiting (60 requests per minute per IP) and the headers of section 9.8. Served on a host of their own (`JIKU_PUBLIC_URL`), separate from the application's, so they do not share cookies |
| Documents panel | On the project, requirement and task pages of `web`; in `opus-web`, only `public` documents |
| Viewer and editor | Markdown viewer in `web`; editing with lock and base version, same as the CLI; "Compartir" (share) button with the document's link management |

### Service creation

1. Machine user in Zitadel with Access Token Type JWT and the `docs-service` role.
2. JSON key given to the service; hot token refresh.
3. Sentinel creds, NATS URL and `NATS_INSTANCE`.
4. Idempotent creation of the `JIKU_DOCS_EVENTS` stream and of the KV on startup.
5. Handling of `unknown_caller` on startup as a reconnection signal (connector guide, §3.4).

### Security

- Deny-by-default: without a rule there is no access; unknown keys in payloads are an error.
- No document content in logs or events; only ids, versions, `correlationId` and `errorCode`.
- Presigned URLs of 300 s; never stored or sent in events.
- Buckets without public access, encryption at rest and credentials only in `jiku-docs`.
- Per-person notifications do not reveal relations with projects the person does not see.
- Documents must not contain secrets. A credential pattern detector (private keys, tokens with known formats) flags the document and warns the person in the command response, without blocking the save.
- Revoking a person's access in Zitadel takes effect on the bus at their next authentication; for an immediate cut, a reconnection is forced, as with any Jiku identity.

### Observability

| Signal | Detail |
| --- | --- |
| Logs | Structured JSON: method, caller, `correlationId`, duration, `errorCode` |
| Metrics | Latency and errors per method; age of the unpublished changelog; indexer lag; active locks; notification fan-out |
| Alerts | Unpublished changelog older than 60 s; indexer lag over 5 min; rate of `upstream_unavailable` |
| History compaction | Log per run (start and end, documents, versions deleted, bytes freed, duration, errors); per-document log only with `--dry-run` or debug level. Metrics `docs_history_versions_pruned_total`, `docs_history_bytes_freed_total`, `docs_history_run_duration_seconds`, `docs_history_pruned_objects_pending`, plus `docs_history_runs_total{result}`, `docs_history_last_success_timestamp_seconds` and `docs_history_pruned_objects_oldest_seconds`; `docs_trash_objects_moved_total{source}` counts the objects moved to the trash (`compaction`, `orphans`, `deleted-documents`). Alerts: a run that fails or does not run in 48 h; `pruned_objects` with rows older than 24 h. See [`history-compaction-v1.md`](history-compaction-v1.md), section 6 |
| Health | NATS micro endpoints (`nats micro info jiku-docs-commands`) |

### Backup

Daily backup of `jiku_docs` with 30-day retention. The content bucket has no versioning: whatever the service deletes stays in `trash/` for `DOCS_TRASH_RETENTION_DAYS` (default 7) and is recovered with `jiku-docs storage restore-from-trash --key <key> [--version-row]` (README, "Recovering a version pruned by mistake"). The index is not backed up; it is regenerated. With history compaction, restoring a backup taken before a compaction can leave version rows whose object is in the trash (it is brought back with `restore-from-trash`) or already expired: the restore process must verify the objects, bring back those still in the trash and discard the rows without an object.

## 13. Optional module and autodiscovery

`jiku-docs` is an optional module: Jiku works the same without it and, when it is installed, discovers it by reading a key on the bus. The first version is deliberately minimal: knowing whether the module is there, what its service is called in order to connect, and whether it responds. Jiku stores no documentation data, so installing or uninstalling the module leaves nothing broken.

### Registry

The base Jiku deployment creates a `JIKU_MODULES` KV bucket (idempotent script, like the events stream), even if there are no modules. On startup, once it can serve, `jiku-docs` writes its key once:

```jsonc
// key: docs
{
  "module": "docs",
  "serviceName": "jiku-docs",
  "protocol": "v1",
  "registeredAt": "2026-10-24T12:00:00.000Z"
}
```

| Field | Purpose |
| --- | --- |
| `module` | Short name of the module; it is the key in the KV |
| `serviceName` | Prefix of the micro services and subjects. By convention, the module serves `{serviceName}-commands` and `{serviceName}-queries` |
| `protocol` | Version of the contracts. Allows telling an incompatibility from an outage |
| `registeredAt` | Informational, for operations |

A normal shutdown does not delete the key (a restart is not an uninstallation). To uninstall: `jiku-docs module deregister` deletes the key and then the service is stopped.

### Status

The `api` reads the key on startup and watches it. To know whether the module responds it uses the standard NATS micro ping (`$SRV.PING.{serviceName}-queries`) every 30 s and every time a call fails with `no responders`.

| Status | Condition | api | Interface |
| --- | --- | --- | --- |
| `not_installed` | The key does not exist | `/docs/*` and `/p/docs/*` routes answer 404 `module_not_installed` | Nothing about documentation appears |
| `incompatible` | `protocol` different from the one the `api` supports | Same as `not_installed`, plus an error log | Same as `not_installed` |
| `unavailable` | Key present, ping without response | Routes answer 503 `module_unavailable` | Menu and panels visible with a notice; actions disabled |
| `available` | Key present, ping with response | Routes enabled | Everything enabled |

The web and opus-web query `GET /modules` (authenticated) on login, every 60 s and on receiving `module_unavailable`. It answers `[{ module, status }]`.

### Configuration-dependent features

They are not announced in the registry. If a module feature is not configured, the command or query answers a specific error and the interface shows it:

| Feature | If not configured |
| --- | --- |
| Semantic or hybrid search (not included in v1) | `documents.search` with `mode` other than `text` answers `semantic_search_not_configured` |
| Attachments (no bucket) | `attachments.*` answers `attachments_not_configured` |
| Share links (no `JIKU_PUBLIC_URL` in the `api`) | The `api` answers 404 on `/p/docs/*` and the web hides "Compartir" (share) |

If later the interface needs to know this in advance, an optional field is added to the registry without breaking `v1`.

### Who uses the registry

| Part | Behavior |
| --- | --- |
| `api` | KV watch, ping, `GET /modules` and route responses according to the status |
| Connectors and agents | Read the key on startup; they only expose documentation tools if it exists and the ping responds |
| Sync daemon | If the module does not respond, it stays offline and reconciles when it comes back; if the key disappears, it stops syncing and notifies, without deleting files |
| `core` | Unchanged |

### Auth-callout permissions

| Role | Permission |
| --- | --- |
| `docs-service` | Write and read only the `docs` key of the `JIKU_MODULES` KV |
| `api` | Read and watch the KV; publish `$SRV.PING.jiku-docs-queries` |
| Connectors and people | Read and watch the KV |

Reading and watching a KV implies permissions on `$JS.API.DIRECT.GET.KV_JIKU_MODULES.>` and to create ephemeral consumers on `KV_JIKU_MODULES`; writing a key implies publishing on `$KV.JIKU_MODULES.<key>`.

### Contract

`jiku-modules.yaml` describes the bucket and the key schema. It belongs to the Jiku repository, because every module will use it. In the `api`'s HTTP contract, the `/docs/*`, `/p/docs/*` and `/modules` routes are marked with `x-jiku-module: docs` and document `module_not_installed` and `module_unavailable`.

## 14. Contracts and references

The four AsyncAPI 3.0 contracts are delivered together with this document and prevail over it on any difference of shape, just like in Jiku:

| File | Content |
| --- | --- |
| `jiku-docs-commands.yaml` | 19 commands with payloads, responses, required permission (`x-permission`) and possible errors (`x-error-codes`) |
| `jiku-docs-queries.yaml` | 14 queries with filters, sorts, fields, includes and paginated responses |
| `jiku-docs-events.yaml` | `JIKU_DOCS_EVENTS` stream, `docs-notify` notifications and their envelopes |

The fourth contract, `jiku-modules.yaml`, describes the module registry (section 13): the `JIKU_MODULES` bucket, the key schema and the service naming convention. It goes in `docs/apis/` of the Jiku repository, because all modules share it.

It is suggested to place them in `docs/apis/` of the `jiku-docs` repository and validate them in CI, like those of `core`.

### References

- [Version history compaction](history-compaction-v1.md): history retention policy; it prevails over this document where it contradicts it.

- [Guide to building a Jiku connector (v1.6.0)](https://github.com/gravadigital/jiku/tree/v1.6.0)
- [core contracts: `docs/apis/`](https://github.com/gravadigital/jiku/tree/v1.6.0/docs/apis)
- [Auth-callout policy](https://github.com/gravadigital/jiku/blob/v1.6.0/deploy/nats/auth-callout/README.md)
- [Reference implementation of subjects and inbox](https://github.com/gravadigital/jiku/blob/v1.6.0/packages/nats-protocol/src/index.ts)
- [Reference event consumer](https://github.com/gravadigital/jiku/blob/v1.6.0/core/tests/tools/events-test-consumer.ts)
