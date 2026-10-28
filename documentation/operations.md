# Operations

Running a Jiku Docs deployment: what the binary does, how to watch it, and how to recover what it
removed.

## Subcommands

The image's entrypoint is the `jiku-docs` binary; the default command is `serve`.

| Subcommand | What it does |
|---|---|
| `jiku-docs serve` (default) | Runs migrations (including the one-time P-1 data migration, before serving), creates or updates the `JIKU_DOCS_EVENTS` stream, registers the two micro services, starts the outbox publisher, the indexer and the periodic tasks, and writes the `docs` key of `JIKU_MODULES` once ready |
| `jiku-docs migrate` | Applies the migrations and exits. `serve` does it too; safe with several replicas |
| `jiku-docs reindex` | Rebuilds the whole search index, in process. Injects nothing into the stream |
| `jiku-docs module deregister` | Deletes the `docs` key of `JIKU_MODULES` (uninstall). A normal shutdown does **not** |
| `jiku-docs storage init` | Prepares the content bucket: creates it, leaves it without versioning, applies and re-reads the lifecycle rules. Idempotent |
| `jiku-docs storage restore-from-trash --key <key> [--version-row]` | Brings an object back from `trash/`; with `--version-row`, also the pruned version into the history. Prints one JSON line |
| `jiku-docs history compact [--document <id>] [--dry-run]` | Compacts the version history now (database and bucket only, no bus) |
| `jiku-docs import hedgedoc [flags]` | Imports from HedgeDoc ([below](#hedgedoc-import)) |
| `jiku-docs healthcheck` | Asks the local process's `/readyz`; the image's `HEALTHCHECK` |
| `jiku-docs version` | Prints the version, set at build time |

## Health

| Endpoint (`DOCS_HTTP_ADDR`, default `:8080`) | Answers |
|---|---|
| `/healthz` | the process is alive |
| `/readyz` | 200 only when the database answers and the bus connection is up |
| `/metrics` | Prometheus metrics |

This HTTP is for operation only; the business health is that of the micro services:

```sh
nats micro info jiku-docs-commands
nats micro info jiku-docs-queries
```

## Logs

JSON on stderr, one line per request with method, caller, actor, `correlationId`, duration and
`errorCode`. Never document content, tokens or presigned URLs.

## Metrics and suggested alerts

| Metric | Alert |
|---|---|
| `jiku_docs_outbox_oldest_unpublished_seconds` | > 60 |
| `jiku_docs_indexer_lag_seconds` | > 300 |
| `jiku_docs_requests_total{code="upstream_unavailable"}` | sustained rate (core is not answering) |
| `docs_history_last_success_timestamp_seconds` | more than 48 h ago (compaction fails or does not run); also `docs_history_runs_total{result="failure"}` |
| `docs_history_pruned_objects_oldest_seconds` | > 86400 (moving to the trash is not progressing); `docs_history_pruned_objects_pending` gives the volume |
| `jiku_docs_request_duration_seconds`, `jiku_docs_locks_active`, `jiku_docs_notifications_total`, `jiku_docs_outbox_published_total` | — |
| `docs_trash_objects_moved_total{source}` | — (objects moved to `trash/`: `compaction`, `orphans`, `deleted-documents`) |
| `docs_history_versions_pruned_total`, `docs_history_bytes_freed_total`, `docs_history_run_duration_seconds` | — |

## Periodic tasks

Run by every replica's janitor, but each by **one replica at a time**, chosen with PostgreSQL
advisory locks:

| Task | Every |
|---|---|
| Expire locks (emits `document.unlocked` with reason `expired`) | 10 s |
| Delete the encrypted retry tokens of share links (older than 5 min) and share-link access records older than 180 days | 5 min |
| Remove expired content uploads | 15 min |
| Remove attachments pending for more than 24 h | 1 h |
| Purge documents deleted more than 30 days ago (their objects go to `trash/`) | 1 h |
| Delete changelog entries older than 30 days | 6 h |
| Sweep orphaned objects (one page of 1,000 objects a minute, cursor kept in the database; orphaned content goes to `trash/`) | weekly |
| Review relations against core: remove the ones to entities that no longer exist, in a new version signed by the service | daily, at `DOCS_RELATION_REVIEW_HOUR_UTC` |
| Compact the version history | daily, from `DOCS_HISTORY_COMPACTION_HOUR` |

## History compaction

Every save still creates a version. Once a day, from `DOCS_HISTORY_COMPACTION_HOUR` in
`DOCS_HISTORY_TIMEZONE`, old intermediate versions are removed: under 24 h all are kept; up to 30
days, the last of each editing session (same author, same `via`, pauses under 30 minutes); older,
the last of each day. Never removed: the current version, the first version, restores, versions
with a message, versions written by a service and versions shown by an active pinned share link.
The thresholds are configurable ([configuration.md](configuration.md#history-compaction)).

How it runs:

- Only documents that need it: new activity, a pinned link that expired or was revoked, or an
  unprotected version that crossed the 24 h or 30-day threshold since the last run (so yesterday's
  session is compacted on the next run even if nobody edits again).
- Document by document, each in a short transaction holding the document's row (`FOR UPDATE`), in
  batches of `DOCS_HISTORY_BATCH_SIZE`.
- After each commit the removed versions' objects are moved to `trash/` through the
  `pruned_objects` table: parallel copies, then `DeleteObjects` in batches. A row leaves the table
  only once its object is in the trash and the original is gone; an interrupted run is resumed by
  the next one.
- One replica at a time, also against a manual `history compact`, which then exits with an error.
- It emits no events or notifications: it never changes a document's current state.
- `DOCS_HISTORY_COMPACTION_ENABLED=false` turns the daily run off.

On demand:

```sh
jiku-docs history compact --dry-run                  # one JSON line per document: each version, kept or not, and why
jiku-docs history compact --document <documentId>    # one document, with or without new activity
jiku-docs history compact
# documents=… versionsPruned=… bytesFreed=… objectsTrashed=… objectsPending=… errors=… dryRun=… duration=…
```

## Trash and restore-from-trash

Every content object the service removes goes through `trash/`: pruned versions, orphaned content
found by the weekly sweep, and the versions of documents purged 30 days after deletion. Moving is a
copy to `trash/<original key>` followed by deleting the original. Uploads in transit (`uploads/`)
and rendered copies (`renders/`) are not content and are deleted directly. Nobody sweeps the trash:
its lifecycle rule empties it after `DOCS_TRASH_RETENTION_DAYS` (default 7).

**Recovering a version compacted by mistake** (within the retention days):

1. Find the key. Versions are stored as `docs/<documentId>/<version>-<sha256>.md`, and in the trash
   as `trash/docs/<documentId>/…`; the name starts with the version number. With any S3 client, for
   example `mc ls --recursive <alias>/jiku-docs-content/trash/docs/<documentId>/`. A dry run or the
   debug log shows which versions a run removed.
2. Bring back the object and its history row with one command:

   ```sh
   jiku-docs storage restore-from-trash --key docs/<documentId>/<version>-<sha256>.md --version-row
   # {"key":"docs/...","objectRestored":true,"documentId":"...","version":3,"rowRestored":true}
   ```

   The trash copy carries the version's row (author, `via`, date, checksum, size, title) as object
   metadata, so no SQL is needed. The command copies the object back (never over an existing one;
   the trash copy stays until it expires) and, in one transaction, re-inserts the row marked
   **protected**, so the next compaction keeps it. It refuses if the document no longer exists or is
   deleted (restore it first), if the version is not older than the current one, or if that version
   exists with another object. It is idempotent.
3. Without `--version-row` only the object comes back: for orphaned content or a purged document,
   which have no row. An object brought back without a row is an orphan again, and the next sweep
   sends it back to the trash.

## Reindex

The search index is derived from the current versions. Rebuild it after restoring a database
backup, or if search results look wrong:

```sh
jiku-docs reindex
```

It runs in process, logs progress every 200 documents and does not touch the event stream.

## Module deregister

```sh
jiku-docs module deregister
```

Deletes the `docs` key from `JIKU_MODULES`; stop the service right after. Jiku then reports the
module as not installed and hides documentation. Restarting `serve` registers it again.

## Upgrades

Pull the new image and restart. Migrations are embedded and **run on start**, before the service
answers, and are expected to be additive; several replicas can start at once. A one-time data
migration that fails stops the process from starting, so the service never answers with new rules
over unmigrated data. Read the release's section in [`CHANGELOG.md`](../CHANGELOG.md) first: a major
version means an incompatible change in a bus contract, in the module protocol or a destructive data
migration.

## Several replicas

Commands and queries are spread by queue group. The outbox publisher and the periodic tasks elect a
leader with PostgreSQL advisory locks; the indexer replicas share one durable consumer
(`jiku-docs-indexer`).

## Backups

- **Database:** a daily backup, kept 30 days, is the recommendation. It holds every piece of
  metadata, the history and the changelog.
- **Bucket:** no versioning. The safety net for what the service removes is `trash/`, for
  `DOCS_TRASH_RETENTION_DAYS`. The bucket itself should be protected by the provider's own means.
- **Index:** not backed up; `jiku-docs reindex`.

Restoring a database backup taken **before** a compaction can leave version rows whose object is in
the trash (bring it back with `restore-from-trash` **without** `--version-row`) or already expired.
After a restore, check the objects and discard the rows that have none.

## HedgeDoc import

The importer reads a MariaDB loaded with a HedgeDoc dump and creates the documents in process, as
the jiku-docs service. It needs the database, the bucket and the bus, like `serve`. Keep the dump
out of version control.

```sh
# 1. A temporary MariaDB with the dump
docker run -d --name jiku-docs-hedgedoc-db -e MARIADB_ROOT_PASSWORD=hedgedoc -e MARIADB_DATABASE=codimd \
  -p 127.0.0.1:33306:3306 mariadb:10.11
docker exec -i jiku-docs-hedgedoc-db mariadb -uroot -phedgedoc codimd < codimd-dump.sql

# 2. A mapping sheet to fill in (note -> folder)
jiku-docs import hedgedoc --export-mapping mapping.csv

# 3. Dry run: plan and report, nothing written or downloaded
#    (--check-relations also validates relations against core over the bus, read only)
jiku-docs import hedgedoc --mapping mapping.csv --dry-run

# 4. The real import
jiku-docs import hedgedoc --mapping mapping.csv
```

- **Mapping sheet:** CSV with `note` (id, shortid or alias), `folder` (`/infra/servers/`) and
  optional `fileName` and `skip`; other columns are ignored. A row with none of `folder`,
  `fileName` or `skip` does not count as mapped. Unmapped notes go to `/imported/` as
  `restricted`; mapped ones are `internal`.
- **Relations and topics:** optional columns `projects`, `clients`, `requirements`, `tasks` (Jiku
  ids separated by `;` or `,`) and `topics` (separated by `;`), merged into the first version's
  frontmatter. A relation core rejects is dropped, the note imported anyway, and the report says
  which (`droppedRelations`).
- **What is imported:** notes with content. Notes without an owner are skipped unless mapped or
  `--include-anonymous` is passed. `--only-mapped` imports only mapped notes; `--limit N` is for
  testing.
- **Content:** frontmatter with `title`, `topics` (from HedgeDoc's tags) and a `hedgedoc` block with
  the original ids, dates and author; the original author also in the first version's message.
- **Images** under `--asset-prefix` (repeatable) are downloaded (at most `--max-asset-bytes`,
  default 50 MiB) and uploaded as attachments; links between notes (`--hedgedoc-url`) are rewritten
  to `{JIKU_WEB_URL}/docs/{id}`. Unresolved ones are left as they were and reported.
- **Idempotent:** ids are deterministic, so re-importing never duplicates. Unchanged notes stay
  `unchanged`; confirmed attachments are not downloaded again. A note whose content changed is
  updated only if its last version was written by the importer; if someone edited, moved, renamed
  or deleted it in jiku-docs, it stays `exists` and is not overwritten.
- **Report** (`--report`, default `.local/hedgedoc-import-report.csv`): status per note,
  destination path, attachments, unresolved links, final relations and topics, dropped relations
  and **possible secrets** (rule and line, never the value). Notes on a public HedgeDoc were public:
  rotate those credentials before shutting it down.

The full flag list: `jiku-docs import hedgedoc --help`.
