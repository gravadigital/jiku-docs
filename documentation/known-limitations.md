# Known limitations

Things that will surprise you, documented rather than hidden. They are the current state, with the
reasoning behind each one. Read this before adopting Jiku Docs.

## It only exists inside a Jiku deployment

- **No HTTP of its own.** Jiku Docs answers only on Jiku's NATS bus. Browsers reach it through
  Jiku's api, people and agents through jiku-go. Its own HTTP port serves health and metrics only.
  Even share links are served by Jiku's api, on a public route of its own; without
  `DOCS_PUBLIC_DOMAIN` configured in Jiku there are no share links.
- **It needs Jiku 1.7.0 or later**, the first release with the module's role, auth-callout rule,
  core changes and screens. Neither side checks the other's version at runtime: an older Jiku
  simply cannot use it, and an older jiku-docs answers `invalid_fields` to what a newer Jiku sends.
- **The service names are fixed by Jiku's access policy.** The auth-callout templates authorise
  `jiku-docs-commands` and `jiku-docs-queries` literally. `DOCS_SERVICE_NAME` exists, but changing
  it is a security change in Jiku too.
- **The first start against a fresh Jiku database may need a restart.** Core may reject the
  module's first query while it creates the machine user's record; jiku-docs retries a few times
  and then starts anyway.

## Permissions depend on core

jiku-docs keeps no users. For every caller it asks core: who the caller is (people and services
over the bus), which projects an external user may see, and whether related entities exist.

- **If core does not answer, access is refused, never granted**: operations that need those answers
  fail with `upstream_unavailable`. A core outage is a documentation outage for people on the bus
  and for external users. People coming through the web are less affected, because the api sends
  their roles in `actor`, but writing relations and listing for external users still need core.
- **Answers are cached**: a caller's roles and an external user's project permissions for 60
  seconds, entity existence for 5 minutes. Revoking a client's access to a project in Jiku can take
  up to a minute to reach documentation.
- **Revoking a role in Zitadel** takes effect on the bus at the person's next authentication, as for
  any Jiku identity; through the web, at once, because the api reads the token on every request.
- **Every `internal-app` machine user reads and writes all documentation**, `restricted` included:
  jiku-docs treats any of them as a trusted service. Granting that role to a new connector grants
  this too.
- **Author names are labels.** They are stored when a version is written and not updated when a
  person changes name; if core does not know an id, the name is `null`.

## Folders still exist, but mean nothing for access

- **Folder ACL is deprecated, not removed.** `folders.{id}.acl.replace`, `inheritAcl`,
  `externalBlocked` and `sharingBlocked` are still in the contract and still stored, but decide
  nothing. They are expected to go in a next major version. Do not build on them.
- **Folder commands are for admins and services only.** Internal people see every folder but cannot
  create, rename or delete one; they write documents into any of them.
- **Locations go stale.** A document is filed by its relations when created and **never moved**
  afterwards: changing its project leaves it under the old client's folder. The web hides
  locations; the CLI and the sync daemon show them.
- **The one-time P-1 migration erred on the side of closing.** When access moved from folders to
  visibility, every document a plain `user` could not read under the old ACL became `restricted` —
  including documents opened to one specific person by id, who lost access to them.

## Content and size

- **256 KiB inline.** Content above it travels through presigned URLs. The CLI does this by itself,
  but **the Jiku web cannot create or edit a document over 256 KiB** — it has no upload by URL.
  In the client portal, such a document is fetched by the browser from the bucket, which needs a
  CORS `GET` rule on the bucket.
- **5 MiB per document, markdown only.** No other formats in the tree.
- **One editor at a time.** No simultaneous collaborative editing; the lock and the base version
  are what prevent overwrites.
- **Assigning relations rewrites the `jiku:` block.** Comments inside that block are lost, and a
  frontmatter written as a single flow-style map (`{title: x}`) is rewritten entirely.
- **The secret detector is pattern-based and does not block.** It warns and makes sharing need an
  acknowledgement; it will miss credentials in formats it does not know.

## History

- **Compaction deletes versions for good.** Intermediate versions older than a day are removed
  daily; after `DOCS_TRASH_RETENTION_DAYS` (default 7) in the trash they are gone. Only protected
  versions survive — save with a message to keep a point. An agent's versions are not protected by
  being an agent's.
- **Version numbers have gaps**, and asking for a removed one answers `version_pruned`.
- **A restore does not record, as a field, which version it came from.** The history entry is a
  version of kind `restore` whose message says "Restored from version N"; the `document.restored`
  event carries `changes.restoredFrom`, but `documents.versions.list` has no field for it.
- **External users see only the current version**, without frontmatter, and are never told the
  history has gaps.
- **Deleted documents are purged after 30 days**, with all their versions, and are then
  recoverable only from the trash within its retention.

## Storage

- **No bucket versioning.** The only safety net is the `trash/` prefix, for what the *service*
  removes. Objects deleted by anyone else with the bucket's credentials are not protected.
- **DigitalOcean Spaces lifecycle rules are taken on trust.** The two rules (`uploads/` after 1 day,
  `trash/` after the retention days) are time-based expirations under a prefix, which Spaces
  documents as supported; `storage init` was verified against MinIO. It re-reads the rules and logs
  a warning if the provider did not keep them — and **if they are missing, the trash never empties
  and keeps growing** until it is emptied by hand.
- **Changing the retention needs `storage init` again**; the variable alone changes nothing.
- **A bucket that had versioning keeps its old versions.** `storage init` suspends versioning but
  does not delete what was already stored; remove non-current versions by hand when no longer
  needed.
- **Restoring a database backup older than a compaction** can leave version rows whose object is in
  the trash or already expired; the restore has to be checked by hand ([operations.md](operations.md#backups)).

## Notifications, events and sync

- **Notifications are not durable.** They are plain NATS messages; whoever is not connected misses
  them and must catch up with the change feed.
- **The change feed and the changelog keep 30 days.** A client away for longer gets
  `invalid_cursor` and must reconcile in full.
- **`access.changed` carries no detail.** A client learns only that it should reconcile.
- **Attachments are not synced** by `jiku docs sync`; they stay as links.

## Search

- **One language configuration.** Full-text search stems with PostgreSQL's Spanish configuration
  without accents; documents in other languages are searched with Spanish rules.
- **At most 50 results, no paging**, one per document. No semantic search: `mode` other than `text`
  answers `semantic_search_not_configured`.

## Share links

- **A share link is a bearer credential.** Anyone who has it reads the document until it expires
  (at most 180 days) or is revoked. The token is shown once and cannot be retrieved.
- **The public route's rate limit is Jiku's**, per api replica and per IP; a page with many images
  spends it quickly. See Jiku's own known limitations.

## Scale

The design target is a small team: about 30 users, 20,000 documents and documents up to 5 MiB.
Notifications are computed for every person who can read a document on every save, which is cheap
at that size and has not been measured beyond it.
