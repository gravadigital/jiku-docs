# jiku-docs

Jiku's documentation module: it stores Grava's documentation in markdown, with permissions by
visibility (`internal`, `public` for external clients, `restricted` for admins only), versions, relations to
Jiku clients, projects, requirements and tasks, full-text search, edit locks, real-time notifications,
read-only public links and attachments. It replaces HedgeDoc.

Like `core`, it has no business HTTP: it serves request/reply on Jiku's NATS bus, takes the identity from
the subject (guaranteed by the auth-callout) and publishes events to JetStream. Frontends come in through
the Jiku `api`; people, agents and services go directly to the bus (official client:
[`jiku-go`](https://github.com/gravadigital/jiku-go)).

- User documentation (features, user guide, installation, configuration, operations, bus reference, known
  limitations): [`documentation/`](documentation/README.md)
- Specification: [`docs/specification-v1.md`](docs/specification-v1.md)
- Integration decisions (they take precedence over the specification): [`docs/integration-decisions.md`](docs/integration-decisions.md)
- AsyncAPI 3.0 contracts (source of truth for shape): [`docs/apis/`](docs/apis/)
- Changes: [`CHANGELOG.md`](CHANGELOG.md)
- P-1, documentation without folders (takes precedence over the specification): [`docs/p1/service.md`](docs/p1/service.md)

**Permissions (P-1, contract 1.3.0).** They are decided by the caller's class and the document's visibility,
not by folders: `admin` and `user` read and write everything `internal` and `public`; an external person
reads what is `public` and related to their projects; `restricted` is only for `admin` and services, and for
everyone else it does not exist through any path. Folders still exist to organise the files (CLI and sync);
`folders.*` is only for admins and services, and the ACL rules, `externalBlocked` and `sharingBlocked` no
longer have any effect.

In `meta.describe` the visibilities are labelled **Internal** (`internal`), **Visible to the client**
(`public`) and **Restricted** (`restricted`). A document created without a folder gets a generated location:
`/clients/<client>/<project>/`, `/clients/<client>/general/` or `/general/`.

## Architecture

```
        Jiku api (actor) ─┐        people / agents (jiku-go)        services (internal-app)
                          ▼                    ▼                              ▼
               NATS + auth-callout ── {instance}.{userId}.jiku-docs-{commands|queries}.v1.{method}
                          │
                          ▼
   jiku-docs ── micro jiku-docs-commands (19) / jiku-docs-queries (14), own queue group
     │  ├─ caller classes and visibility, strict validation, versioning, locks
     │  ├─ users / project-permissions / entities / files.*  ──▶ core (jiku-queries, jiku-commands)
     │  ├─ own PostgreSQL 16 (metadata, changelog, es_unaccent index)
     │  └─ own S3 bucket (one version per object, large uploads, trash/)
     │
     ├─ outbox: changelog ─▶ JetStream JIKU_DOCS_EVENTS (Nats-Msg-Id = eventId)
     │                    └▶ {instance}.{userId}.docs-notify.v1.* (people with read)
     ├─ indexer: durable jiku-docs-indexer on JIKU_DOCS_EVENTS
     ├─ janitor: lock expiry, purges, daily review of relations
     └─ registration in the JIKU_MODULES KV (key docs) once ready
```

| Package | Responsibility |
| --- | --- |
| `cmd/jiku-docs` | Binary and subcommands (`serve`, `migrate`, `reindex`, `module deregister`, `storage init`, `storage restore-from-trash`, `history compact`, `import hedgedoc`, `healthcheck`) |
| `internal/app` | Process wiring: stream, micro services, outbox, indexer, janitor, registration |
| `internal/bus` | Registration of the two NATS micro services (core's `BusHost` pattern) |
| `internal/service` | Logic: 19 commands, 14 queries, permissions by visibility, generated location, relations and suggestions, events, purges, relation review, P-1 migration |
| `internal/query` | Core's read grammar: filters, `or`, ordering, keyset, `fields`, `include`, `count`, `meta.describe` |
| `internal/validate` | Deny-by-default decoding (`invalid_fields` with `{field, value, allowed}`) |
| `internal/protocol` | Response envelope, error catalogue, subject grammar, inbox, ULID |
| `internal/caller`, `internal/acl` | Caller classes; the (deprecated) folder ACL is only evaluated in the P-1 migration |
| `internal/coreapi` | Core client (on top of `jiku-go`) with the spec cache (60 s / 5 min) |
| `internal/markdown`, `internal/secrets` | Frontmatter, titles, topics, snippets, sanitized HTML, secret detector |
| `internal/store`, `internal/blob` | PostgreSQL (embedded migrations) and S3 bucket |
| `internal/outbox`, `internal/indexer`, `internal/janitor`, `internal/registry`, `internal/metrics` | Background processes and observability |
| `internal/importer/hedgedoc` | HedgeDoc importer (D-9) |
| `internal/contract`, `internal/testutil`, `test/integration` | Validation against `docs/apis/` and the integration environment |

## Requirements in Jiku (for installation)

jiku-docs does not modify Jiku. It needs **Jiku 1.7.0 or later**, the first release that carries these changes:

1. **Machine user `jiku_docs`** (Access Token Type JWT) with roles `internal-app` and `docs` (D-1).
2. **core: `users.list` must expose `roles` and `identityType`** as includables, at least for callers of
   class `connector`. Today the `users` spec does not declare them and **without them jiku-docs cannot
   classify anyone** (every person call fails with `upstream_unavailable`). jiku-docs requests
   `users.list { filter: { id }, include: ["roles", "identityType"] }`; the names are configurable with
   `CORE_USER_INCLUDES` (if core only exposes `roles`, use `CORE_USER_INCLUDES=roles`: the identity type is
   derived from the `internal-app`/`core` role, as core does).
3. **core must not purge `files` without a link** to a Jiku entity (docs attachments are never linked, D-3).
4. **auth-callout**: a `match: docs` rule above `internal-app` with the `docs.yaml` template. Besides what is
   in `connector.yaml`, jiku-docs needs exactly:

   | What for | Permission |
   | --- | --- |
   | Serving the micro services | sub `{instance}.*.jiku-docs-commands.v1.>` and `{instance}.*.jiku-docs-queries.v1.>` (queue groups `jiku-docs-commands` / `jiku-docs-queries`), sub `$SRV.>`, `allow_responses` as in `core.yaml` |
   | Events and notifications | pub `{instance}.docs-events.v1.>` and `{instance}.*.docs-notify.v1.>` |
   | Stream and indexer consumer | pub `$JS.API.INFO`, `$JS.API.STREAM.CREATE.JIKU_DOCS_EVENTS`, `$JS.API.STREAM.UPDATE.JIKU_DOCS_EVENTS`, `$JS.API.STREAM.INFO.JIKU_DOCS_EVENTS`, `$JS.API.CONSUMER.CREATE.JIKU_DOCS_EVENTS` and `.>`, `$JS.API.CONSUMER.DURABLE.CREATE.JIKU_DOCS_EVENTS.>`, `$JS.API.CONSUMER.INFO.JIKU_DOCS_EVENTS.>`, `$JS.API.CONSUMER.MSG.NEXT.JIKU_DOCS_EVENTS.jiku-docs-indexer`, `$JS.ACK.JIKU_DOCS_EVENTS.>` |
   | Module registration | pub `$JS.API.STREAM.INFO.KV_JIKU_MODULES` and `$KV.JIKU_MODULES.docs` (writing and deleting its key) |
   | Replies | sub `_INBOX.<hash(userId)>.>` (built by jiku-go) |

   Registry readers (api, connectors, people) need the permissions documented in
   [`docs/apis/jiku-modules.yaml`](docs/apis/jiku-modules.yaml).
5. **`JIKU_MODULES` KV** created by the Jiku deployment (D-6). If it does not exist, jiku-docs starts anyway
   and retries registering every 30 s.
6. **api: full `actor`** (D-11). The api sends jiku-docs the same `actor` it sends core (`buildActor`):
   `{ id, roles, name?, username?, email? }`, with the roles of the verified web token. jiku-docs classifies
   the person with `actor.roles` (it does not query `users.list`, whose role mirror may be stale) and an
   `actor` without `roles` is `invalid_fields`. An external person receives `documents.get` without
   frontmatter.

## Running locally against the Jiku stack

1. Start Jiku (`jiku/deploy/local.sh up`): it creates the `jiku-local_jiku` network, NATS + auth-callout, MinIO
   and the KV.
2. Configure:

   ```sh
   cp .env.dist .env
   # machine user key, in base64 (the JSON key lives in .local/, it is never committed)
   echo "DOCS_SERVICE_USER_KEY_B64=$(base64 -w0 .local/jiku_docs.service_user.json)" >> .env
   echo "DOCS_SECRET_KEY=$(openssl rand -base64 32)" >> .env
   ```

   Check `JIKU_API_USER_ID` (userId in `jiku/local/api-su.json`), `ZITADEL_PROJECT_ID` and `NATS_INSTANCE`
   against `jiku/deploy/.env`.
3. Start:

   ```sh
   docker compose -f docker-compose.local.yml up -d --build
   docker logs -f jiku-docs-local                       # "jiku-docs ready", "module registered"
   curl -s localhost:8090/readyz
   nats micro info jiku-docs-queries                    # with the creds of a bus user
   ```

   The compose starts its own Postgres 16 (`127.0.0.1:5442`), runs `jiku-docs storage init` (it creates
   `jiku-docs-content` without versioning in Jiku's MinIO, or suspends versioning if it was enabled, and
   applies the lifecycle rules) and the service, with `NATS_URL=nats://nats:4222` and the sentinel creds from
   `../../jiku/deploy/nats/creds/sentinel-client.creds` (change it with `JIKU_DEPLOY_DIR`).
4. Try it with jiku-go: `jiku docs status`, `jiku docs ls /`, etc.

Without Docker for the service: `go run ./cmd/jiku-docs` with the `.env` variables exported and
`DATABASE_URL` pointing at the compose's Postgres (`docker compose ... up -d docs-database docs-storage-init`).

## Environment variables

All of them are documented in [`.env.dist`](.env.dist). The required ones for `serve`:
`NATS_CREDS`, `ZITADEL_PROJECT_ID`, `DOCS_SERVICE_USER_KEY_B64` (or `_FILE`), `JIKU_API_USER_ID`,
`JIKU_WEB_URL`, `JIKU_PUBLIC_URL`, `DATABASE_URL`, `DOCS_S3_ENDPOINT`, `DOCS_S3_BUCKET`,
`DOCS_S3_ACCESS_KEY`, `DOCS_S3_SECRET_KEY` and `DOCS_SECRET_KEY` (32 bytes, base64).

In production the bucket is a dedicated DigitalOcean Space (`DOCS_S3_ENDPOINT=https://<region>.digitaloceanspaces.com`,
`DOCS_S3_FORCE_PATH_STYLE=false`), without versioning and with the lifecycle rules for `uploads/` and `trash/`
(`jiku-docs storage init` creates it if the credential allows it and applies the rules; see "Spaces" below).

## Operations

| Subcommand | What it does |
| --- | --- |
| `jiku-docs serve` (default) | Migrates (including the P-1 data migration, only once and before serving), creates/updates the stream, registers the micro services, starts the outbox, indexer and janitor, and writes the `docs` key of `JIKU_MODULES` |
| `jiku-docs migrate` | Applies migrations (`serve` does it too; safe with several replicas) |
| `jiku-docs reindex` | Rebuilds the whole search index (in process; it does not inject events into the stream) |
| `jiku-docs module deregister` | Deletes the `docs` key (uninstall). A normal shutdown does **not** delete it |
| `jiku-docs storage init` | Creates the bucket without versioning (if versioning was enabled, it suspends it and warns in the log), applies the lifecycle rules (`uploads/` at 1 day, `trash/` at `DOCS_TRASH_RETENTION_DAYS`) and reads them back |
| `jiku-docs storage restore-from-trash --key <key> [--version-row]` | Returns an object from `trash/` to its original key (it never overwrites an existing one; the trash copy stays until it expires). With `--version-row` it also puts the pruned version back into the history (see "Trash" below). Prints a JSON line with what it did |
| `jiku-docs history compact [--document <id>] [--dry-run]` | Compacts the version history now (database and bucket only, no bus). `--dry-run` prints a JSON line per document with each version, whether it is kept and why, and touches nothing. If the daily run holds the lock, it exits with an error |
| `jiku-docs import hedgedoc` | Importer (see below) |
| `jiku-docs healthcheck` | Queries `/readyz` (the image's HEALTHCHECK) |

**Operations HTTP** (`DOCS_HTTP_ADDR`, default `:8080`): `/healthz`, `/readyz` (database and NATS) and
`/metrics` (Prometheus). Business health is NATS micro's: `nats micro info jiku-docs-commands`.

**Suggested metrics and alerts**

| Metric | Alert |
| --- | --- |
| `jiku_docs_outbox_oldest_unpublished_seconds` | > 60 |
| `jiku_docs_indexer_lag_seconds` | > 300 |
| `jiku_docs_requests_total{code="upstream_unavailable"}` | sustained rate |
| `jiku_docs_request_duration_seconds`, `jiku_docs_locks_active`, `jiku_docs_notifications_total`, `jiku_docs_outbox_published_total` | — |
| `docs_history_last_success_timestamp_seconds` | more than 48 h ago (compaction fails or does not run); also `docs_history_runs_total{result="failure"}` |
| `docs_history_pruned_objects_oldest_seconds` | > 86400 (moving to the trash is not progressing); `docs_history_pruned_objects_pending` gives the volume |
| `docs_trash_objects_moved_total{source}` | — (objects moved to `trash/`: `compaction`, `orphans`, `deleted-documents`) |
| `docs_history_versions_pruned_total`, `docs_history_bytes_freed_total`, `docs_history_run_duration_seconds` | — |

**Logs**: JSON on stderr with method, caller, actor, `correlationId`, duration and `errorCode`. Never content,
tokens or presigned URLs.

**Periodic tasks** (one replica at a time, by advisory lock): lock expiry with `document.unlocked` (every
10 s), deletion of the encrypted share retry tokens (5 min), expired uploads, attachments pending for more
than 24 h, documents deleted more than 30 days ago (their objects go to `trash/`), changelog older than 30
days, orphaned objects (weekly, paginated sweep: one page of 1,000 objects per minute, with the cursor in
`service_state`; orphaned content goes to `trash/`), link accesses older than 180 days and the daily review
of relations against core (`DOCS_RELATION_REVIEW_HOUR_UTC`), which removes relations to non-existent
entities by rewriting the frontmatter in a new version signed by the service.

**History compaction** ([`docs/history-compaction-v1.md`](docs/history-compaction-v1.md)):
every save still creates a version, and once a day, from `DOCS_HISTORY_COMPACTION_HOUR` in
`DOCS_HISTORY_TIMEZONE`, the old intermediate versions are deleted: under 24 h, all are kept; up to 30 days,
the last of each editing block (same author, same `via`, pauses under 30 min); older, the last of each day.
The current version, the creation, restores, versions with a `message`, versions with `via: service` and
versions pinned by an active `pinned` link are never deleted; agent versions follow the block rule (to
protect one, save it with `--message`). It processes document by document with `FOR UPDATE` and short
transactions, and after the commit it moves the objects to `trash/` through `pruned_objects` (parallel
copies and `DeleteObjects` in batches). It processes the documents with new activity, those with a pinned
link that expired or was revoked, and those with an unprotected version that crossed the 24 h or the 30-day
threshold since their last compaction (so the previous day's session is compacted in the next run even if
nobody edits again). It runs on a single replica (`pg_try_advisory_lock`), also against a manual
`history compact`. The parameters are in `.env.dist`; `DOCS_HISTORY_COMPACTION_ENABLED=false` turns it off.

**Trash.** Every content object the service deletes goes through `trash/`: pruned versions, orphaned
content from the weekly sweep and the versions of documents purged 30 days after deletion. Moving is
`CopyObject` to `trash/<original key>` (in parallel) and then `DeleteObjects` of the originals in batches. A
`pruned_objects` row is removed only once the object is in the trash and the original no longer exists; if
the process stops between the copy and the delete, the next run picks it up. In-flight uploads (`uploads/`)
and rendered pages (`renders/`) are not content and are deleted directly. Nobody sweeps the trash: its
lifecycle rule empties it.

**Bucket and lifecycle.** `storage init` leaves the bucket without versioning and with two time-based
expiration rules: `uploads/` at 1 day and `trash/` at `DOCS_TRASH_RETENTION_DAYS` (default 7). It reads the
rules back and warns in the log if the provider did not store them. Verified against MinIO (tests
`TestStorageInit*`).

**Spaces.** Both rules are time-based expirations under a prefix, which DigitalOcean Spaces supports
("Supported for time-based expiration and removing incomplete multipart uploads",
[S3 Compatibility](https://docs.digitalocean.com/products/spaces/reference/s3-compatibility/)).
`NoncurrentVersionExpiration`, which Spaces does not confirm, is no longer needed: bucket versioning is no
longer the safety net. `DOCS_HISTORY_COMPACTION_ENABLED=true` is the safe default in production too. If the
bucket had versioning enabled (`storage init` enabled it before the trash existed), `storage init` suspends
it (`Suspended`) and warns in the log; the old versions it already kept are **not** deleted on their own:
when they are no longer needed, delete them by hand (for example `aws s3api list-object-versions` and
`delete-object --version-id` on the non-current ones, or `mc rm --recursive --versions --non-current` in
MinIO).

**Recovering a version pruned by mistake** (within `DOCS_TRASH_RETENTION_DAYS`):

1. Find the key. It is `docs/<documentId>/<version>-<sha256>.md`; in the trash it is
   `trash/docs/<documentId>/...`. For example, with whichever S3 client you use:
   `mc ls --recursive <alias>/jiku-docs-content/trash/docs/<documentId>/` (the name starts with the version
   number). If the compaction is recent, the per-document log (`--dry-run` or debug level) shows which
   versions it deleted.
2. Return it, object and history row, with a single command:

   ```sh
   jiku-docs storage restore-from-trash --key docs/<documentId>/<version>-<sha256>.md --version-row
   # {"key":"docs/...","objectRestored":true,"documentId":"...","version":3,"rowRestored":true}
   ```

   The trash copy keeps the version's row (author, `via`, date, checksum, size, title) as object metadata,
   so there is no need to write SQL by hand. The command first copies the object to its original key (if it
   already exists it does not overwrite it) and then, in a transaction, inserts the row into
   `document_versions` marked `protected` (the next compaction will not delete it again) and decrements
   `pruned_versions_count`. It refuses if the document no longer exists or is deleted (restore it first), if
   the version is not older than the current one or if that version already exists with another object. It
   is idempotent.
3. Without `--version-row` it returns only the object: useful for orphaned content or content of a purged
   document, which have no row to restore. An object returned without a row is orphaned again and the next
   sweep sends it back to the trash.

**Backup**: daily database backup (30 days); the bucket has no versioning: the safety net for what the
service deletes is `trash/`. Restoring a backup older than a compaction may leave version rows whose object
is in the trash (return it with `restore-from-trash` without `--version-row`) or has already expired (older
than `DOCS_TRASH_RETENTION_DAYS`): when restoring, check the objects and discard the rows without an object.
The index is not backed up: `reindex`.

**Several replicas**: commands and queries are spread by queue group; the outbox and the janitor elect a
leader with PostgreSQL advisory locks; the indexer shares the durable.

## Importing from HedgeDoc (D-9)

The dump never leaves `.local/` and is never committed. The importer reads a MariaDB database loaded with the
dump and creates the documents in process, like the jiku-docs service.

```sh
# 1. Temporary MariaDB with the dump (a port that does not clash with anything in Jiku)
docker run -d --name jiku-docs-hedgedoc-db -e MARIADB_ROOT_PASSWORD=hedgedoc -e MARIADB_DATABASE=codimd \
  -p 127.0.0.1:33306:3306 mariadb:10.11
docker exec -i jiku-docs-hedgedoc-db mariadb -uroot -phedgedoc codimd < .local/codimd-2026-10-24.sql

# 2. Mapping sheet to fill in (note -> folder)
go run ./cmd/jiku-docs import hedgedoc --export-mapping .local/hedgedoc-mapping.csv

# 3. Dry run: plan + report, without writing or downloading anything
#    (--check-relations also validates the relations against core over the bus, read-only)
go run ./cmd/jiku-docs import hedgedoc --mapping .local/hedgedoc-mapping.csv --dry-run

# 4. Real import (.env variables exported: database, bucket, bus)
go run ./cmd/jiku-docs import hedgedoc --mapping .local/hedgedoc-mapping.csv
```

- **Mapping sheet**: a CSV with the columns `note` (id, shortid or alias), `folder` (`/infra/servers/`) and
  the optional `fileName` and `skip`; other columns are ignored (they help with the review). A row without
  `folder`, `fileName` or `skip` does not count as mapped. Notes without a folder go to `/imported/` with
  `visibility: restricted` (P-1: only admins read them); mapped notes are `internal`, like any document.
- **Relations and topics**: the optional columns `projects`, `clients`, `requirements` and `tasks` (Jiku ids
  separated by `;` or `,`) and `topics` (separated by `;`). With any value, version 1 comes out with the
  `jiku:` block and the `topics` in the frontmatter, merged with what it already has (no duplicates; topics
  normalised like the service does). On their own they do not select a note: they apply to the notes being
  imported. If core answers `invalid_relation`, the note is retried without that relation and the report
  says which one was dropped and why (`droppedRelations`); with `--dry-run --check-relations` they show up
  before importing.
- **What is imported**: notes with content. Anonymous notes (without an owner) are skipped unless they are
  mapped in the sheet or `--include-anonymous` is passed: in the real dump there are ~10,000 and almost all
  are spam. `--only-mapped` imports only what is mapped and `--limit N` is useful for testing.
- **Content**: frontmatter with `title`, `topics` (from HedgeDoc's `tags`) and a `hedgedoc` block with id,
  shortid, alias, dates, author, last editor, permission and the original keys; the original creation and
  modification dates; the original author in the `message` of version 1.
- **Images**: URLs under `--asset-prefix` (default: the Space `grava-internal-01/docs-grava-io` and
  `docs.grava.io/uploads/`) are downloaded and uploaded as attachments through core's file service (D-3);
  the markdown ends up with `{JIKU_WEB_URL}/docs/attachments/{id}`.
- **Links between notes** (`{--hedgedoc-url}/{alias|shortid|id}`, `/s/…`, `/p/…`) are rewritten to
  `{JIKU_WEB_URL}/docs/{id}`; those that do not resolve stay as they were and are reported.
- **Idempotent**: deterministic ids (ULID with the note's date); re-importing does not duplicate. In a
  re-import, notes already imported with the same content stay `unchanged` and attachments already confirmed
  are not downloaded again; only the failed ones are retried. If the content changed (for example, the sheet
  now has relations), the note is updated with `documents.{id}.update` on the current version (`updated`,
  message "Re-imported from HedgeDoc…"), as long as the last version is the importer's; if someone edited,
  moved, renamed or deleted it in jiku-docs, it stays `exists` and is not overwritten.
- **Report** (`--report`, default `.local/hedgedoc-import-report.csv`): status per note, target path,
  attachments, unresolved links, final relations and topics, dropped relations and **possible secrets** (rule
  and line, never the value). Those notes were public in HedgeDoc: rotate those credentials before shutting
  it down.

## Development

```sh
go build ./... && go vet ./... && golangci-lint run ./...
go test -race ./...            # unit, contract and integration tests (requires Docker)
JIKU_DOCS_SKIP_INTEGRATION=1 go test ./...   # without Docker
docker build -t gravadigital/jiku-docs:local .
```

The integration tests start PostgreSQL 16 and MinIO with testcontainers (containers `jiku-docs-test-*`,
random ports), an embedded `nats-server` with JetStream and a strict core double, and validate **every**
request, response, event and notification against `docs/apis/`. The contract tests also check that the
resource specs (the ones that feed `meta.describe`) match the YAML.

`test/e2e/local-bus.sh` walks the module end to end over the real bus, with the local stack running and
jiku-go's `jiku` CLI as service, user and external-user (visibility and `restricted`, documents, generated
location, relations, permissions, locks, search, attachments, links, feed, sync and logs; `E2E_DISRUPTIVE=1`
adds module down and deregister). The variables and their defaults are in its header.

`jiku-go` is used at its published version (`v1.3.0`, from the Go proxy); no `replace` is needed.

CI (`.github/workflows/`): `ci.yml` (build, vet, lint, tests, `docker build`), `dev-images.yml` (image
`gravadigital/jiku-docs:dev` on every push to `dev`) and `release.yml` (tags `vX.Y.Z` → `X.Y.Z`, `X.Y`,
`latest`; requires the CHANGELOG section). Secrets: `DOCKERHUB_USERNAME`, `DOCKERHUB_TOKEN`.

## License

[Apache-2.0](LICENSE) © Grava Digital.
