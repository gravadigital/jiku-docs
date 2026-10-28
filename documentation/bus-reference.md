# Bus reference

Jiku Docs has no HTTP interface for business: everything is request/reply on Jiku's NATS bus, with
the same conventions as Jiku's `core` (subject grammar, reply envelope, filters and paging). Whoever
has integrated core has nothing new to learn.

This page is an overview. **The contracts are the source of truth** for every payload, field,
permission and error:

| Contract | Content |
|---|---|
| [`docs/apis/jiku-docs-commands.yaml`](../docs/apis/jiku-docs-commands.yaml) | the 19 commands, with payloads, replies, permission (`x-permission`) and error codes (`x-error-codes`) |
| [`docs/apis/jiku-docs-queries.yaml`](../docs/apis/jiku-docs-queries.yaml) | the 14 queries, with filters, sorts, fields, includes and paginated replies |
| [`docs/apis/jiku-docs-events.yaml`](../docs/apis/jiku-docs-events.yaml) | the `JIKU_DOCS_EVENTS` stream, the `docs-notify` notifications and their envelopes |
| [`docs/apis/jiku-modules.yaml`](../docs/apis/jiku-modules.yaml) | the `JIKU_MODULES` registry and its key |

All four are AsyncAPI 3.0; the first three are at version 1.3.0. The integration tests validate
every request, reply, event and notification against them. At runtime, `meta.describe` is the
authoritative description of fields, filters, sorts and enums. The simplest client is
[jiku-go](https://github.com/gravadigital/jiku-go), which already handles everything below.

## Two micro services

| Service | Plane | Queue group |
|---|---|---|
| `jiku-docs-commands` | writes | `jiku-docs-commands` |
| `jiku-docs-queries` | reads | `jiku-docs-queries` |

The prefix comes from `DOCS_SERVICE_NAME` and is published as `serviceName` in the `docs` key of
`JIKU_MODULES`, with `protocol: v1`. Clients find the service names there and check that the module
answers with the standard NATS micro ping, `$SRV.PING.jiku-docs-queries`.

## Subjects

```
Commands:      {instance}.{userId}.jiku-docs-commands.v1.{method}
               prod.281234567890123456.jiku-docs-commands.v1.documents.01JBX7Q2M4N8P6R3T5V9W1Y0ZA.update
Queries:       {instance}.{userId}.jiku-docs-queries.v1.{method}
               prod.281234567890123456.jiku-docs-queries.v1.documents.search
Events:        {instance}.docs-events.v1.{entity}.{action}
Notifications: {instance}.{userId}.docs-notify.v1.{entity}.{action}
```

| Segment | Value |
|---|---|
| `instance` | `dev` or `prod`, like Jiku's `NATS_INSTANCE` |
| `userId` | the raw `sub` of the caller's token. **This is the identity**: the auth-callout guarantees nobody publishes under someone else's id, and jiku-docs validates no tokens itself |
| `v1` | commands and queries are versioned together; events and notifications separately |
| `method` | in commands, ids go in the subject (`documents.{id}.update`); in queries, in the payload |

Event and notification subjects and the stream name are fixed: they do not follow `serviceName`.

Callers connect as with core: the sentinel credentials, a Zitadel token refreshed in place, and an
inbox prefix derived from the user id (`_INBOX.` plus the first 16 characters of
`lowercase(base32_without_padding(sha256(userId)))`). Without the prefix replies never arrive and the
symptom is a timeout.

## Caller classes and the `actor` envelope

| Class | Recognised by | Effective identity | Reads | Writes |
|---|---|---|---|---|
| person, internal | role `admin` or `user` | the subject's `userId` | `internal` and `public`; `restricted` only `admin` | same as reads |
| person, external | role `external-user` | the subject's `userId` | `public` documents related to projects in their `project-permissions` | never (`caller_not_authorized`) |
| `api` | the `userId` is `JIKU_API_USER_ID` | the person in `actor.id` | by the class `actor.roles` gives | by the class `actor.roles` gives |
| service | identity type service with role `internal-app` | the subject's `userId` | everything | everything, attributed to the service |

Any other role (`core`, `bus-observer`) has no access. A person who is both external and admin is
external: the most restrictive class wins. For people and services over the bus, the class comes
from core's `users.list` (cached 60 s); a user core does not know gets `unknown_caller`.

Only Jiku's api may send `actor`, the same envelope it sends to core:

```jsonc
{ "actor": { "id": "281234567890123456", "roles": ["user"], "name": "…", "username": "…", "email": "…" } }
```

- `id` and `roles` are required; any other key, a missing `roles`, or `actor` from any other caller
  is `invalid_fields`.
- The person's class comes from `actor.roles` (`admin`/`user` internal, `external-user` external,
  anything else `caller_not_authorized`); `users.list` is not consulted for it. External users'
  project permissions are still asked to core.
- Authorship is always `actor.id`. Payloads have no author fields.
- The api without `actor` is treated as a service. `shares.resolve` and `shares.attachment` answer
  only to the api **without** `actor`.

## Replies

```jsonc
{ "status": "success", "data": { … } }
{ "status": "failure", "errorCode": "version_conflict", "errorMessage": "…", "errorDetails": { "currentVersion": 8 } }
```

Decide by `errorCode`, never by `errorMessage`. Writes may add `warnings` (for example
`possible_secret_detected`), which do not mean failure.

Every schema rejects unknown keys; an unknown filter, sort, field or include is `invalid_fields`
with `{field, value, allowed}`. Anything the caller may not see answers `*_not_found`, never
distinguishable from "does not exist".

**Every command is safe to retry:** creations take a client-generated ULID `id` (same id and same
content returns the existing resource; different content, `already_exists`); document changes take
a mandatory `baseVersion` (identical content to the current version succeeds with that version);
taking a lock the session already holds renews it; deleting what is already deleted succeeds.

| Limit | Value |
|---|---|
| Suggested timeout for commands / queries | 5 s / 10 s (the database stops a query at 8 s with `query_timeout`) |
| Inline content in commands and replies | 256 KiB; above it, through presigned URLs |
| Document size | 5 MiB |
| Presigned URLs | 300 s |
| `page.limit` | default 50, maximum 200 (higher is cut to 200) |

## Commands (`jiku-docs-commands`, 19)

| Method | Does |
|---|---|
| `folders.new` | creates a folder (admin or service) |
| `folders.{id}.edit` | renames or moves a folder (admin or service) |
| `folders.{id}.delete` | deletes an empty folder (admin or service) |
| `folders.{id}.acl.replace` | replaces a folder's ACL rules — **deprecated, no effect** (admin or service) |
| `documents.new` | creates a document; folder and file name optional (generated location) |
| `documents.{id}.update` | saves a new version, with `baseVersion` |
| `documents.{id}.move` | changes folder or file name; creates no version |
| `documents.{id}.delete` | logical delete, restorable for 30 days |
| `documents.{id}.restore` | new version from an older one; undeletes a deleted document |
| `documents.{id}.lock` | takes or renews the edit lock (`ttlSeconds` 30–300, default 120) |
| `documents.{id}.unlock` | releases the lock; `force` only admin or service |
| `documents.{id}.relations.update` | adds or removes relations, rewriting only the `jiku:` block |
| `documents.request-content-upload` | presigned URL to upload content over 256 KiB |
| `attachments.request-upload` | presigned URL to upload an attachment (through core's file service) |
| `attachments.{id}.confirm` | confirms an upload after checking its SHA-256 |
| `attachments.{id}.request-download` | presigned download URL |
| `attachments.{id}.delete` | logical delete of an attachment |
| `shares.new` | creates a share link; the token and URL are returned only here |
| `shares.{id}.revoke` | revokes a share link |

## Queries (`jiku-docs-queries`, 14)

| Method | Does |
|---|---|
| `meta.describe` | resources, fields, filters, sorts, includes and enums |
| `folders.list` | the folder tree (internal people see all; external users none) |
| `folders.get` | one folder |
| `documents.list` | the filterable catalogue; never content |
| `documents.get` | one document with content, relations, topics, lock, attachments; `version` for an older one |
| `documents.versions.list` | the kept versions, with `kind`, `protected` and `authorName` |
| `documents.search` | full-text search, one hit per document with its best snippet |
| `documents.changes` | the change feed since a `seq`, for reconciling (30 days) |
| `documents.suggest-relations` | up to 5 candidate projects or clients for a document |
| `topics.list` | topics in use, with counts |
| `attachments.list` | a document's attachments |
| `shares.list` | a document's share links, never the token |
| `shares.resolve` | renders a shared document from its token (only the api, without `actor`) |
| `shares.attachment` | download URL for an attachment of a shared document (only the api, without `actor`) |

The read grammar is core's: `filter` (scalars, lists, `gte`/`lte` ranges, one level of `or`),
`sort` (`-` for descending, `id` always appended as tie-breaker), keyset paging with `page.cursor`
(its absence is the only end signal), `fields`, `include` and `count`.

## Events (`JIKU_DOCS_EVENTS`, 15)

Written to the changelog in the same transaction as the change and published by the outbox to the
stream, with `Nats-Msg-Id = eventId`.

| Stream | Value |
|---|---|
| Subjects | `{instance}.docs-events.v1.>` |
| Retention | limits, `max_age` 30 days |
| Delivery | at least once; deduplicate by `eventId` (publish dedup window 2 min) |
| Consumers | services only, each with its own durable, explicit ack, ignoring their own events (`actor.id`) |

| Event | Emitted by |
|---|---|
| `document.created` | `documents.new` |
| `document.updated` | `documents.{id}.update`, `relations.update`, the relation review |
| `document.moved` | `documents.{id}.move` |
| `document.deleted` | `documents.{id}.delete` |
| `document.restored` | `documents.{id}.restore` (`changes.restoredFrom`) |
| `document.locked` | taking a lock (not renewing it) |
| `document.unlocked` | `unlock` or expiry (`changes.reason`: `released`, `forced`, `expired`) |
| `folder.created` / `folder.updated` / `folder.deleted` | folder commands; also folders created for a generated location |
| `folder.acl.changed` | `folders.{id}.acl.replace` |
| `attachment.created` / `attachment.deleted` | `attachments.{id}.confirm` / `delete` |
| `share.created` | `shares.new` (never the token) |
| `share.revoked` | `shares.{id}.revoke` and automatic revocation (`changes.reason`: `manual`, `document_deleted`, `restricted`) |

The envelope carries `eventId`, `seq`, `type`, `version`, `occurredAt`, `correlationId`, `actor`
(`{ id, name, via }`), `entity`, `snapshot` (metadata after the change) and `changes`. Never document
content or presigned URLs. History compaction emits no events.

## Notifications (`docs-notify`)

`{instance}.{userId}.docs-notify.v1.{entity}.{action}`, plain NATS (no persistence): a person who is
not connected misses them, and `documents.changes` fills the gap.

- Sent to every person who can read the document at that moment; for a `restricted` document,
  admins only. External users receive none.
- The same envelope as the event, without `snapshot.relations` or `changes.relations`.
- Entities: `document`, `attachment` and `access`. `share.*` events are not notified.
- `access.changed` carries no detail: it tells a client to reconcile in full. It is sent to internal
  people on `folder.acl.changed` and `folder.updated`, to each `user` when a document becomes
  `restricted`, and to the external users concerned when a document stops being `public`.

## Error codes

The catalogue may grow within `v1`: treat an unknown code as a generic error.

| Code | Meaning | Retry |
|---|---|---|
| `invalid_fields` | unknown key, filter, sort, field or include; `actor` from a caller that is not the api | no: caller bug |
| `invalid_cursor` | `filter` or `sort` changed between pages; or a `sinceSeq` older than the feed keeps | restart paging / reconcile in full |
| `document_not_found` / `folder_not_found` / `attachment_not_found` | does not exist or is not visible | no |
| `permission_denied` | can read but not do this | no |
| `version_conflict` | `baseVersion` is not the current one (`errorDetails.currentVersion`) | re-read, merge, retry |
| `version_pruned` | the requested, restored or pinned version was compacted (`requestedVersion`, `previousAvailable`, `nextAvailable`); never sent to external users | ask for a kept version |
| `document_locked` | another session holds the lock (`holder`, `holderName`, `via`, `expiresAt`) | wait or ask |
| `lock_not_held` | the session does not hold the lock it renews or releases | take it again |
| `already_exists` | client `id` reused with other content, or file name taken in the folder | no |
| `file_name_invalid` | not ending in `.md` or with forbidden characters | no |
| `frontmatter_invalid` | malformed YAML or invalid value (`line`, `field`, `reason`) | no |
| `invalid_relation` | Jiku id that does not exist (`entityType`, `entityId`) | no |
| `content_too_large` | over 5 MiB, or over 256 KiB inline | upload by URL |
| `checksum_mismatch` | declared SHA-256 does not match | recompute |
| `upload_not_found` | `contentUploadId` expired, used or unknown | request another |
| `folder_not_empty` | deleting a folder with content | no |
| `attachment_type_not_allowed` / `attachment_too_large` | core's attachment policy | no |
| `share_not_found` | link unknown, revoked, expired, of a deleted or restricted document (indistinguishable) | no |
| `sharing_blocked` | the document is `restricted` | no |
| `share_expiry_invalid` | `expiresAt` in the past or beyond 180 days | fix the date |
| `secret_warning_not_acknowledged` | the document is flagged and `acknowledgeSecretWarning: true` was not sent | review and confirm |
| `semantic_search_not_configured` | `documents.search` with `mode` other than `text` | use `text` |
| `attachments_not_configured` | kept for compatibility; not emitted in v1 | — |
| `query_timeout` | the query exceeded the database's time | narrow the filter |
| `unknown_caller` / `caller_not_authorized` | no user record in core, or a role without access | reconnect / no |
| `upstream_unavailable` | core did not answer a query that access depends on | retry with backoff |
| `internal_error` | anything else | retry with backoff |
