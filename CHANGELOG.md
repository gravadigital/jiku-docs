# Changelog

Format: [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Versioning: [SemVer](https://semver.org/).

## Versioning policy

The service has a single version number, the one of the `vX.Y.Z` tag. It is not written in any code file:
the binary receives it at build time (`ARG VERSION` in the `Dockerfile`) and `jiku-docs version` prints it.
The contracts in `docs/apis/` have their own `info.version`, which moves with every shape change and not
with releases; the bus also carries the `v1` segment in the subjects, which is what lets old and new
consumers coexist during a migration.

| Bump      | When |
| --------- | ---- |
| **major** | A breaking change in the bus contract (commands, queries, events, notifications), in the module protocol, or a destructive data migration. |
| **minor** | New functionality, backwards compatible. |
| **patch** | Fixes and internal changes with no effect on any contract. |

The database schema is not versioned separately: the migrations in `internal/store/migrations/` run on
startup (`serve` or `migrate`) and are expected to be additive.

### Cutting a release

`[Unreleased]` is moved to a `## [X.Y.Z] - YYYY-MM-DD` section, committed, brought to `main` and tagged:

```sh
git tag vX.Y.Z && git push origin vX.Y.Z
```

Pushing the tag is what publishes. `release.yml` checks that the CHANGELOG has the tag's section, runs vet,
lint and the full suite, and only then publishes `gravadigital/jiku-docs` to Docker Hub with the tags
`X.Y.Z`, `X.Y` and `latest`. A tag without its section fails before anything is built. The step by step
for agents is in [CLAUDE.md](CLAUDE.md#cutting-a-release).

## [Unreleased]

### Added

- **The bus and identity settings accept the Jiku connector SDK names as fallbacks**, so the image runs
  in the connectors deploy without translating variables: `JIKU_SERVERS` (`NATS_URL`), `JIKU_CREDS`
  (`NATS_CREDS`), `JIKU_INSTANCE` (`NATS_INSTANCE`), `JIKU_KEY_FILE` (`DOCS_SERVICE_USER_KEY_FILE`),
  `JIKU_ISSUER` (`ZITADEL_ISSUER_URL`) and `JIKU_PROJECT_ID` (`ZITADEL_PROJECT_ID`). The existing names
  win when both are set, so current installations are unaffected.

## [1.1.0] - 2026-10-28

### Changed

- **The repository is now in English.** Documentation, internal docs (renamed to English file names, such as
  `docs/specification-v1.md` and `docs/integration-decisions.md`), the contracts' prose, CLI help, logs, and
  report and test-script output. Commit messages are in English from now on; the history is not rewritten.
  Identifiers do not change: error codes, subjects, field names, environment variables, enum values, CLI
  subcommand and flag names.
- **Visible to clients — text only, no shapes or codes:**
  - Bus error `message`s are now English. The error `code`s and the response envelope are the same.
  - The `meta.describe` visibility labels are now **Internal**, **Visible to the client** and
    **Restricted** (were "Interno", "Visible para el cliente" and "Restringido"). The values (`internal`,
    `public`, `restricted`) are the same.
  - Automatic version messages are now English (for example "Client assigned: …").
  - Consumers that match on a message text or on a label must match on the error `code` or the enum value
    instead.
- **Visible to clients — generated locations:** documents without an explicit folder are now placed under
  `/clients/<client>/<project>/`, `/clients/<client>/general/`, `/general/` (unchanged) and, for HedgeDoc
  notes without a mapping, `/imported/` (were `/clientes/…` and `/importado/`). A location is a path, so a
  client that hard-coded the old prefixes must use the new ones.
- **The bucket trash prefix is now `trash/`** (was `papelera/`): everything the service deletes from the
  bucket goes to `trash/<original key>`, `jiku-docs storage restore-from-trash` reads from there, and
  `jiku-docs storage init` applies the expiration rule to `trash/`.

### Added

- **User documentation** under [`documentation/`](documentation/README.md): features, user guide,
  installation, configuration, operations, bus reference and known limitations.
- **License**: Apache-2.0 ([`LICENSE`](LICENSE)) and [`NOTICE`](NOTICE).

### Fixed

- **The release pipeline publishes again.** The integration tests started MinIO from the `minio/minio`
  image, which can no longer be pulled from Docker Hub, so the 1.0.0 release run failed its tests and never
  published the image. The test S3 is now Garage (`dxflrs/garage`). Garage has no bucket versioning, so the
  test of `storage init` suspending an old bucket's versioning skips there; the rest of `storage init`
  (bucket and lifecycle rules) is still verified. The service itself is unchanged: it still talks to any
  S3-compatible provider.

### Notes for existing installations

The `v1.0.0` tag was pushed, but its release run failed before publishing, so no 1.0.0 image exists and
there are no production installations. For a local environment that ran 1.0.0:

- Run `jiku-docs storage init` again (the local compose does it on `up`): it replaces the bucket's lifecycle
  configuration, so the `papelera/` rule is dropped and the `trash/` rule is applied.
- Objects already under `papelera/` no longer expire on their own and `restore-from-trash` does not find
  them: delete them by hand (for example `mc rm --recursive --force <alias>/jiku-docs-content/papelera/`),
  or move them to `trash/` first if one of them may still be needed.
- Documents that already have a location under `/clientes/…` or `/importado/` keep it; only new generated
  locations use the English names.

## [1.0.0] - 2026-10-27

First version. Optional markdown documentation module for Jiku, replacing HedgeDoc. It serves over Jiku's NATS
bus, with no business HTTP; frontends come in through the Jiku api, and people, agents and services go directly
to the bus (official client: `jiku-go`). Contracts in `docs/apis/`: commands, queries and events at 1.3.0,
module registry at 1.0.0.

### Added

- **`jiku-docs` service in Go 1.25** with two NATS micro services and its own queue group: `jiku-docs-commands`
  (19 commands) and `jiku-docs-queries` (14 queries). Identity comes from the subject, guaranteed by the
  auth-callout; caller classes `person-internal`, `person-external`, `service` and the Jiku api with `actor`
  (`{ id, roles, name?, username?, email? }`).
- **Documents** with frontmatter, versions, edit locks and soft delete: `documents.new` (no folder: the server
  generates the location from the relations and the title), `update`, `move`, `delete`, `restore`,
  `lock`/`unlock`, large content upload through a presigned URL (`request-content-upload`), `relations.update`
  (rewrites only the `jiku:` block, idempotent) and `suggest-relations` (up to 5 suggested projects or clients
  from the title and content).
- **Reading with core's grammar**: filters (scalar, list, range, one level of `or`), ordering with `id` as the
  tie-breaker, keyset pagination with a byte budget, `fields`, `include`, `count` and `meta.describe` derived
  from the same specs that validate. Filters `hasProject`, `hasClient` and `hasTopic`; `projectMatch`
  (`direct`/`indirect`) with `filter.projectId`; items with `clients`, `projects`, `createdByName`,
  `updatedByName`; `topics.list` with counts.
- **Full-text search** in PostgreSQL (`es_unaccent`), one result per document with the best snippet.
- **Version history** (`documents.versions.list` with `kind`, `protected` and `authorName`) and **daily
  compaction**: all versions younger than 24 h are kept, the last of each editing block up to 30 days, and
  after that the last of each day. The current version, the creation, restores, versions with a message,
  service versions and versions pinned by an active link are never pruned. Error `version_pruned` when a pruned
  one is requested; `prunedVersionsCount` in `documents.get`. Command
  `jiku-docs history compact [--document <id>] [--dry-run]`.
- **Trash** in the bucket: every object the service deletes goes through `papelera/` (7 days by default) and is
  recovered with `jiku-docs storage restore-from-trash --key <key> [--version-row]`. The bucket does not use
  versioning.
- **Attachments** delegated to core's file service (`attachments.*`), with the stable link
  `{JIKU_WEB_URL}/docs/attachments/{id}`.
- **Read-only share links** (`shares.*`, `jds_` token, `latest` and `pinned` modes), sanitized HTML, a 5-minute
  idempotent retry and an access audit with the IP in an HMAC. Secret detector: `possible_secret_detected`
  warning and mandatory confirmation to share.
- **Events** through a transactional outbox to the JetStream stream `JIKU_DOCS_EVENTS` (`Nats-Msg-Id = eventId`):
  `document.*`, `attachment.*`, `share.*`, `folder.*`. Per-person **notifications** on
  `{instance}.{userId}.docs-notify.v1.*`, including `access.changed`. Change feed `documents.changes` for sync.
- **Author names** (`createdByName`, `updatedByName`, `authorName`, `lock.holderName`): those of the actor or,
  if it does not carry them, those from core's `users.list`, with an in-memory cache; if core does not answer
  they are `null` and the operation does not fail.
- **Registration in the `JIKU_MODULES` KV** (key `docs`) once ready, and `jiku-docs module deregister` to
  uninstall.
- **HedgeDoc importer** (`jiku-docs import hedgedoc`): mapping sheet to folders and relations, topics from the
  `tags`, rewriting of links between notes, attachments, `--dry-run` and `--check-relations`, a per-note report
  and idempotent re-import that does not overwrite what was edited in jiku-docs. Notes without a mapping come
  in as `restricted`.
- **Operations**: subcommands `serve`, `migrate`, `reindex`, `storage init`, `healthcheck` and `version`; JSON
  logs without content, Prometheus metrics, `/healthz`, `/readyz` and `/metrics`. Indexer (durable
  `jiku-docs-indexer`), lock expiry, purges and a daily review of relations against core.
- `gravadigital/jiku-docs` image (distroless, non-root), `docker-compose.local.yml` on top of Jiku's local
  stack, and CI, dev image and release workflows.

### Security

- **Permissions by caller class and visibility**, deny-by-default. Internal people (`admin`, `user`) read and
  write everything `internal` and `public`; an external person only reads what is `public` and related to the
  projects of their `project-permissions`. What cannot be seen answers `*_not_found`, and access is never
  granted if core does not answer (`upstream_unavailable`).
- **`restricted` visibility**: only `admin` and services. For a `user` or an external person it does not exist
  through any path (`list`, `count`, `get` with and without `version`, `search` and snippets, `changes`,
  `versions.list`, `topics.list`, `attachments.*`, `shares.*`, `docs-notify` or `suggest-relations`). Only
  `admin` or a service set or remove it. Moving a document to `restricted` revokes its links, and a
  `restricted` document does not accept links (`sharing_blocked`).
- An external person receives documents without frontmatter (it holds internal requirement and task ids) and
  cannot read earlier versions, which could date from when the document was `internal`.
- No content, tokens or presigned URLs in logs or events.

### Deprecated

- **Folder ACL**: `folders.acl.replace`, `inheritAcl`, `include: [acl]`, `externalBlocked` and
  `sharingBlocked` remain in the contract but have no effect: permissions are decided by visibility. Folders
  only organise the files (CLI and sync); `folders.*` is only for `admin` and services.

### Notes for new installations

There are no earlier installations. To install:

- **Requires a Jiku version that is not published yet**: the one on the `feat/jiku-docs` branch of the Jiku
  repo (REQ-016 to REQ-022). Core must expose `roles` and `identityType` in `users.list` to connectors and not
  purge `files` without a link; the auth-callout needs the `match: docs` rule with its template; the api and
  the web bring the documentation screens. Details in "Requisitos en Jiku" (requirements in Jiku) in the
  README.
- **Zitadel**: create the `docs` role in the project and a machine user (Access Token Type JWT) with the roles
  `internal-app` and `docs`; its JSON key goes in `DOCS_SERVICE_USER_KEY_B64` (or `_FILE`).
- **Own infrastructure**: a PostgreSQL 16 (`DATABASE_URL`; migrations run on their own at startup) and an S3
  bucket for the content (in production, a DigitalOcean Space with `DOCS_S3_FORCE_PATH_STYLE=false`). Run
  `jiku-docs storage init` once to create it and apply the lifecycle rules.
- **Required variables** for `serve`: `NATS_CREDS`, `ZITADEL_PROJECT_ID`, `DOCS_SERVICE_USER_KEY_B64` (or
  `_FILE`), `JIKU_API_USER_ID` (the `sub` of the Jiku api's service user), `JIKU_WEB_URL`, `JIKU_PUBLIC_URL`,
  `DATABASE_URL`, `DOCS_S3_ENDPOINT`, `DOCS_S3_BUCKET`, `DOCS_S3_ACCESS_KEY`, `DOCS_S3_SECRET_KEY` and
  `DOCS_SECRET_KEY` (32 bytes in base64: `openssl rand -base64 32`). All of them, with the optional ones, in
  `.env.dist`.
- The `JIKU_MODULES` KV is created by the Jiku deployment; without it jiku-docs starts anyway but the web and
  the clients do not see it installed.
- The first time jiku-docs connects to a new Jiku database, core may reject its first query while it creates
  the service user's record; startup continues and after a restart it is fine.
