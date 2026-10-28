# jiku-docs: version history compaction

Oct 24, 2026 · @Lautaro Alvarez · reviewed 2026-10-25 against the implementation and with the owner's decisions (see section 8)

## 1. Summary

Every save of a document creates a new version, and with autosave that produces thousands of versions per day. That behaviour is kept, because it supports real-time updates, and a daily process is added that compacts the old history according to a retention policy.

This document complements the `jiku-docs` v1 technical specification. Where it contradicts something in it (in particular, the statement that all versions are kept), this document prevails.

### Problem

VS Code, Obsidian and the `jiku docs sync` daemon save every few seconds while someone is writing. With 10 people editing 4 hours a day and one save every 10 seconds, the system accumulates about 14,000 versions per day (around 140 MB per day with 10 KB documents). The storage cost is low, but the history becomes unreadable, the versions table grows without a ceiling, and restoring something means searching through thousands of nearly identical entries.

### Decision

- **Writes and real time unchanged.** Every save still creates a version, still increments `version` and still fires `document.updated`. Concurrency control with `baseVersion` does not change.
- **Daily compaction.** A process of the service deletes intermediate versions according to their age, always keeping the ones that have value (section 2).
- **Minimal contract changes.** The history can have gaps in its numbering and there is a new error code, `version_pruned`.

With the scenario above, of the \~14,000 daily versions a few dozen survive after the first day, and each entry of the old history corresponds to someone's work session.

## 2. Retention policy

Versions are thinned out by age bands; protected versions and the current version are never deleted.

### Bands

| Age of the version | What is kept |
| --- | --- |
| Less than 24 h | All |
| From 24 h to 30 days | The last version of each editing block |
| More than 30 days | The last version of each calendar day |
| Any | Protected versions and the current version |

Age is measured on the version's `created_at`, at the time the compaction runs. Calendar days are computed in the configured time zone (default `America/Argentina/Buenos_Aires`).

### Editing block

An editing block is a sequence of consecutive versions of a document that meet all three conditions:

1. They have the same `author`.
2. They have the same `via` (`human`, `agent` or `service`).
3. Less than 30 minutes passed between each version and the next.

The block ends when the author changes, the origin changes, or there is a pause of 30 minutes or more. Of each block the last version is kept, which represents the state of the document at the end of that work session.

Example: Ana saves 200 versions between 10:00 and 11:40, Bruno saves 50 between 11:45 and 12:10, and Ana saves 30 more between 15:00 and 15:20. After the first day three versions remain: Ana's at 11:40, Bruno's at 12:10 and Ana's at 15:20. After 30 days only the 15:20 one remains (the last of the day), unless one of them is protected.

### Protected versions

A protected version is not deleted in any band.

| Version | Reason |
| --- | --- |
| The current version of the document | It is the current state |
| The first version (`kind = create`) | Records the origin of the document |
| Versions with `message` | Whoever writes a message marks an important point ("version sent to the client") |
| Versions with `via` `service` | They are automatic changes (import, relation review) that are worth being able to audit one by one |
| Restores (`kind = restore`) | Record that someone went back |
| Versions pinned by an active share link (`mode = pinned`, not revoked or expired) | The link has to keep showing that version |

Versions with `via` `agent` are **not** protected by their origin: they follow the block rule like human ones. Since a change of `via` closes the block, each agent session keeps its last version and never mixes with a person's editing. If an agent version carries a `message`, it is protected by that rule. This way an agent that saves often (for example with `jiku docs sync` and `JIKU_VIA=agent`) does not fill the history with undeletable versions again.

Protection by share link is dynamic: when the link expires or is revoked, the version becomes a candidate again in the next compaction. The other protections are permanent.

Since versions with `message` are never compacted, to mark an important point it is enough to save with a message, for example `jiku docs put <id> <file> --base-version <n> --message "Version sent to the client"`.

### What does not change

- Deleted documents are permanently removed after 30 days with all their versions, as before (their objects go through the trash, section 4). The compaction does not process them.
- The search index only contains the current version, so it is not affected.
- The changelog and the `documents.changes` feed keep their records; an old event may mention a version that has already been compacted.

## 3. Compaction process

A process of the `jiku-docs` service itself runs once a day, processes document by document in short transactions, and moves the S3 objects to the trash only after the deletion has been committed in the database.

### Execution

- It runs once a day from the configured hour (`DOCS_HISTORY_COMPACTION_HOUR`, default 3) in the configured zone, with the same mechanism as the other periodic tasks of the service (`JobDue` on `service_state`). If there are several replicas of the service, only one runs: it takes a PostgreSQL advisory lock (`pg_try_advisory_lock`) and the others skip it.
- It processes the documents that meet any of these conditions since their last compaction (those never compacted, always):
  - **New activity:** `documents.updated_at > documents.last_compacted_at`.
  - **Pinned link that expired or was revoked:** a share link with `mode = pinned` whose `expires_at` or `revoked_at` is later than `last_compacted_at`.
  - **Threshold crossing:** some unprotected version (that is not the current one) crossed the 24 h or the 30-day threshold between the last compaction and now. Without this rule, the editing session of the day before the run (all its versions are less than 24 h old when compacting) would remain whole forever if nobody edits the document again; with it, it is compacted in the run following the 24 h crossing (case 24).
- A document without new activity whose versions did not cross any threshold is not processed (case 11).
- It walks the documents in batches (default 100), ordered by `id`, so it can resume where it left off if interrupted.
- It can also be run on demand with a maintenance command: `jiku-docs history compact [--document <id>] [--dry-run]`. With `--dry-run` it reports what it would delete without deleting anything.

### Algorithm per document

1. Open a transaction and lock the document row with `SELECT ... FROM documents WHERE id = $1 FOR UPDATE`.
2. Read its versions ordered by `version` and the set of versions pinned by active links.
3. Mark the protected versions as kept (section 2).
4. Group all the versions of the document into editing blocks. For the versions in the 24 h to 30 days band, keep the last one of each block.
5. For versions older than 30 days, start from those left by the block rule, group them by calendar day and keep the last one of each day.
6. Mark all versions younger than 24 h as kept.
7. Delete the `document_versions` rows not kept, saving their `storage_key` and the deleted row (`version_row`) in `pruned_objects`.
8. Update the document's `last_compacted_at` and `pruned_versions_count`, and commit the transaction.

Blocks are computed over all the versions of the document, not only those of the band, so that a block crossing the 24 h boundary is not split in two. The daily rule is applied over what the block rule left, so the result is at most one version per day plus the protected ones.

The process is idempotent: running it twice in a row deletes nothing more, because the versions it keeps satisfy the same rules on the second pass.

### Deletion in S3: trash

No content object is deleted directly: it first goes through the trash (`trash/<original key>`, section 4). That step happens outside the transaction:

1. After the commit, the process takes the `pruned_objects` rows in batches (`DOCS_HISTORY_S3_DELETE_BATCH`, up to 1,000) and copies each object to `trash/<original key>` (`CopyObject`, several copies in parallel). The copy carries the deleted version's row as metadata, so it can be returned to the history.
2. It deletes the copied originals with one `DeleteObjects` call per batch.
3. Each row is removed from `pruned_objects` only when its object is in the trash and the original no longer exists. If the original no longer existed, it counts as done.

If the process is interrupted between the commit and the copy, or between the copy and the deletion of the original, the rows remain in `pruned_objects` and the next run picks them up: copying again over an existing copy is harmless (same content). A version row never ends up pointing to a deleted object; in the worst case there is an object without a row for a while, or the same version under the original key and in the trash.

The general orphan sweep (`PurgeOrphans`), which today lists all of `docs/` and queries the database for each object, does not scale with this volume and overlaps with `pruned_objects`. It becomes weekly and paginated with a cursor persisted in `service_state`; moving compacted versions to the trash is always done by `pruned_objects`. Orphan content found by the sweep also goes to the trash, as do the versions of a document purged 30 days after deletion.

### Concurrency with writes

- The compaction never deletes the current version, so it does not interfere with `baseVersion` or with editing locks.
- The `FOR UPDATE` on the document serialises the compaction with `update`, `restore` and `shares.new` of the same document. Those operations wait a few milliseconds at most, because a document's compaction transaction makes no external calls.
- If a `restore` or a `shares.new` with `mode: pinned` requests a version that the compaction has just deleted, it responds `version_pruned`.

### Events

The compaction emits no events or notifications: it does not change the current state of any document. It is recorded in logs and metrics (section 6).

## 4. Data model and storage changes

Two columns are added to `document_versions`, two to `documents`, and one table for the deferred move to the trash.

### Migration

```sql
-- Origin of each version, to protect creations and restores.
-- `documents.move` does not create a version (it only emits document.moved), so there is no kind 'move'.
ALTER TABLE document_versions
  ADD COLUMN kind text NOT NULL DEFAULT 'update'
    CHECK (kind IN ('create','update','restore'));

-- Permanent protection, computed on insert (create, restore, message, via service)
ALTER TABLE document_versions
  ADD COLUMN protected boolean NOT NULL DEFAULT false;

-- Existing data
UPDATE document_versions SET kind = 'create' WHERE version = 1;
UPDATE document_versions SET kind = 'restore' WHERE message LIKE 'Restaurado desde la versión %';
UPDATE document_versions SET protected = true
  WHERE kind IN ('create','restore') OR message IS NOT NULL OR via = 'service';

-- Compaction control per document
ALTER TABLE documents
  ADD COLUMN last_compacted_at timestamptz,
  ADD COLUMN pruned_versions_count integer NOT NULL DEFAULT 0;

CREATE INDEX documents_pending_compaction
  ON documents (id)
  WHERE deleted_at IS NULL AND (last_compacted_at IS NULL OR updated_at > last_compacted_at);

-- S3 objects pending the move to the trash after the commit
CREATE TABLE pruned_objects (
  storage_key text PRIMARY KEY,
  document_id text NOT NULL,
  version     integer NOT NULL,
  pruned_at   timestamptz NOT NULL DEFAULT now()
);

-- Migration 0004: the deleted row, which the copy in the trash carries as metadata
ALTER TABLE pruned_objects ADD COLUMN version_row jsonb;
```

Existing restores are identified by the fixed message the service wrote when restoring at the time the migration was applied (`Restaurado desde la versión N`, "restored from version N"; the `LIKE` above matches that historical literal).

### Rules on write

| Command | `kind` | `protected` |
| --- | --- | --- |
| `documents.new` | `create` | `true` |
| `documents.{id}.update` | `update` | `true` if it carries `message` or if `via` is `service` |
| `documents.{id}.restore` | `restore` | `true` |

Protection by a pinned share link is not stored in the column: it is computed on each compaction from `shares` (active, `mode = 'pinned'`).

### Storage

- **No versioning.** The `jiku-docs-content` bucket does not use versioning. The safety net is a trash of our own, because DigitalOcean Spaces does not confirm support for `NoncurrentVersionExpiration`. `jiku-docs storage init` does not enable versioning; on a bucket that already has it enabled, it suspends it (`Suspended`) and warns in the log that the old versions that already existed are not deleted automatically (they have to be deleted by hand when no longer needed).
- **Trash.** Every deletion of content objects done by the service goes through the `trash/` prefix: the compaction (via `pruned_objects`, section 3), the orphan sweep (`PurgeOrphans`) and the permanent purge of deleted documents after 30 days. Moving is `CopyObject` to `trash/<original key>` and then `DeleteObjects` of the originals, in batches. In-transit uploads (`uploads/`) and rendered pages (`renders/`) are not content and are deleted directly.
- **Lifecycle.** Two time-based expiration rules, which Spaces supports: `uploads/` at 1 day and `trash/` at `DOCS_TRASH_RETENTION_DAYS` (default 7). `jiku-docs storage init` applies them, reads them back and warns if the provider did not store them.
- **Recovery.** `jiku-docs storage restore-from-trash --key <original key>` returns an object from the trash (it never overwrites an existing one). With `--version-row` it also reinserts the row in `document_versions` with the data the copy carries as metadata, marked `protected` so the next compaction does not delete it again, and decrements `pruned_versions_count`. The procedure is in the README ("Recovering a version compacted by mistake").
- This way, a mistaken deletion by the compaction can be recovered during `DOCS_TRASH_RETENTION_DAYS`, and the space is actually freed after that period.
- The daily database backup does not change. Restoring a backup taken before a compaction leaves rows pointing to objects that may be in the trash or may have expired; the restore procedure must verify the objects, return those still in the trash and discard the rows without an object.

## 5. Changes to contracts and jiku-go

The changes are compatible within `v1`: a new field in responses and a new error code, which clients must already tolerate according to the protocol rules.

### New error code

| Code | When | `errorDetails` |
| --- | --- | --- |
| `version_pruned` | A version that was compacted is requested, restored or pinned | `requestedVersion`, `previousAvailable` and `nextAvailable` (the closest kept versions, or `null`) |

A version that never existed (greater than the current one) still responds `document_not_found`, as today.

A `person-external` caller **never** receives `version_pruned`: for them any version other than the current one is still `document_not_found`, without `errorDetails`, so as not to reveal the history.

### `jiku-docs-queries.yaml`

| Query | Change |
| --- | --- |
| `documents.get` with `version` | May respond `version_pruned` (never to external users) |
| `documents.versions.list` | The numbering may have gaps. `kind` (`create`, `update`, `move`, `restore`) and `protected` are added to the `Version` schema, and `kind` and `protected` as filters |
| `documents.get` (include) | The document adds `prunedVersionsCount`: how many versions were compacted |

### `jiku-docs-commands.yaml`

| Command | Change |
| --- | --- |
| `documents.{id}.restore` | If `fromVersion` was compacted, it responds `version_pruned` |
| `shares.new` with `mode: pinned` | If `version` was compacted, it responds `version_pruned` |

### `jiku-docs-events.yaml`

No changes. The compaction emits no events.

### Technical specification v1

Update in the specification:

- Section 2, decision 1: "Each version is an immutable object in S3" becomes "Each version is an immutable object in S3 until the compaction removes it".
- Section 5, error catalogue: add `version_pruned`.
- Section 11, tables and storage: add the migration from section 4 of this document.
- Section 12, observability: add the signals from section 6.
- Add a reference to this document as the history retention policy.

### jiku-go

| Element | Change |
| --- | --- |
| Error constant | `CodeVersionPruned` |
| `jiku docs history <id>` | Shows `kind` and a mark on protected versions. At the end, a line with the number of compacted versions. `--protected` flag to list only the protected ones |
| `jiku docs get <id> --version N` | On `version_pruned`, reports the closest kept versions and exits with a non-zero code |
| `jiku docs restore` | Same handling of `version_pruned` |
| `jiku docs put --message` | No behaviour changes; the command help explains that saving with a message protects the version forever |
| Agent skill | Add the rule: when finishing an important piece of work, save with `--message` so the version is protected (agent versions without a message are compacted by blocks) |

### Web interface (pending, outside this stage)

It is implemented later, as a jiku REQ through grava-workflow. This stage covers only the service and jiku-go. Today the web has no history screen.

A document's history shows the kept versions with their author, date and type, distinguishes the protected ones and indicates how many intermediate versions were compacted. When saving, it offers an optional message field with the note that that version will be protected.

## 6. Configuration and observability

All thresholds of the policy are service parameters, with the values of this document as defaults.

### Parameters

| Variable | Default | Description |
| --- | --- | --- |
| `DOCS_HISTORY_COMPACTION_ENABLED` | `true` | Enables the daily process |
| `DOCS_HISTORY_COMPACTION_HOUR` | `3` | Hour of the day (in `DOCS_HISTORY_TIMEZONE`) from which the daily run starts |
| `DOCS_HISTORY_TIMEZONE` | `America/Argentina/Buenos_Aires` | Zone for the run hour and the calendar days |
| `DOCS_HISTORY_KEEP_ALL` | `24h` | Window in which all versions are kept |
| `DOCS_HISTORY_KEEP_BLOCKS` | `30d` | Up to what age one version per editing block is kept |
| `DOCS_HISTORY_BLOCK_GAP` | `30m` | Pause that closes an editing block |
| `DOCS_HISTORY_BATCH_SIZE` | `100` | Documents per batch |
| `DOCS_HISTORY_S3_DELETE_BATCH` | `1000` | Objects per batch when moving to the trash (parallel copies and one `DeleteObjects` call for the originals) |
| `DOCS_TRASH_RETENTION_DAYS` | `7` | Days that `trash/` keeps what the service deletes (lifecycle rule of `storage init`) |

### Observability

| Signal | Detail |
| --- | --- |
| Log per run | Start and end, documents processed, versions deleted, bytes freed, objects moved to the trash, duration, errors |
| Log per document | Only with `--dry-run` or debug level: versions kept and deleted with the reason |
| Metrics | `docs_history_versions_pruned_total`, `docs_history_bytes_freed_total`, `docs_history_run_duration_seconds`, `docs_history_pruned_objects_pending` (rows in `pruned_objects`), `docs_trash_objects_moved_total{source}` (objects moved to the trash by the compaction, the orphan sweep and the purge of deleted documents) |
| Alerts | A run that fails or does not run in 48 h; `pruned_objects` with rows older than 24 h (the move to the trash is not progressing) |

## 7. Test cases

The cases cover each rule of the policy, the edges between bands and the safety of the deletion. All of them can be written with version dates set by hand and the process run on demand.

| # | Scenario | Expected result |
| --- | --- | --- |
| 1 | 300 versions by the same author in 2 hours, all less than 24 h old | None is deleted |
| 2 | The same case, with the versions from 3 days ago | Only the last of the block and the current one remain |
| 3 | Ana edits, Bruno edits, Ana edits again on the same day, 3 days ago | Three versions remain, the last of each block |
| 4 | One author with a 31-minute pause between two batches, 3 days ago | Two versions remain, one per block |
| 5 | The same author alternates `via: human` and `via: agent` without pauses, 3 days ago | Each change of `via` closes the block; the last version of each block remains, human or agent |
| 6 | Several blocks on the same day, 40 days ago | The last version of that day remains |
| 7 | A block that starts 25 h ago and ends 23 h ago | The block is evaluated whole; no version of the block younger than 24 h is deleted |
| 8 | Intermediate version with `message`, 60 days ago | Kept |
| 9 | Restore 60 days ago | Kept |
| 10 | Version pinned by an active link, 60 days ago | Kept; after revoking the link, the next compaction deletes it if it is not protected for another reason |
| 11 | Document unchanged since the last compaction | Not processed |
| 12 | Running the compaction twice in a row | The second one deletes nothing |
| 13 | The process is interrupted after the commit, between the copy to the trash and the deletion of the original | The rows remain in `pruned_objects`; the next run finishes moving the objects. An original that no longer exists counts as done |
| 14 | Two replicas start the compaction at the same time | Only one runs (advisory lock) |
| 15 | `documents.get` with a compacted version | `version_pruned` with the closest kept versions |
| 16 | `restore` from a compacted version | `version_pruned`; no new version is created |
| 17 | `shares.new` with `mode: pinned` on a compacted version | `version_pruned` |
| 18 | An `update` arrives while the same document is being compacted | The `update` waits for the compaction's commit and is applied normally; the current version is never deleted |
| 19 | `--dry-run` | Reports what it would delete and modifies no rows or objects |
| 20 | Logically deleted document | The compaction does not process it |
| 21 | An external user requests with `documents.get` a compacted version of a public document | `document_not_found`, without `errorDetails` |
| 22 | Agent version with `message`, 60 days ago | Kept |
| 23 | `service` version without a message, 60 days ago | Kept |
| 24 | Session from the previous day with no later activity | Compacted in the run following the 24 h crossing; the following runs do not process it until another crossing |
| 25 | Compaction of an old block | The objects of the deleted versions are in `trash/` and none of them in `docs/` |
| 26 | `storage restore-from-trash --version-row` on a compacted version | The object and the (protected) row come back; the next compaction keeps it |

## 8. Review against the implementation (2026-10-25)

Changes agreed with respect to the 2026-10-24 draft, and the owner's decisions of the same day:

1. `via: agent` versions follow the block rule; only `service` is protected by origin.
2. An external user never receives `version_pruned`.
3. There is no `kind = move`: `documents.move` does not create a version.
4. Existing restores are marked by the service's fixed message.
5. `PurgeOrphans` becomes weekly and paginated; deleting compacted versions is done by `pruned_objects`.
6. ~~Verify support for `NoncurrentVersionExpiration` in DigitalOcean Spaces.~~ Decided: Spaces does not confirm it, so the bucket stops using versioning and the safety net is the `trash/` trash with time-based expiration (`DOCS_TRASH_RETENTION_DAYS`, default 7), which Spaces does support. The compaction, `PurgeOrphans` and the purge of deleted documents go through the trash. `storage init` suspends versioning if it was enabled. Recovery with `jiku-docs storage restore-from-trash` (section 4). The compaction is enabled by default in production too.
7. Scheduling by hour of the day (`DOCS_HISTORY_COMPACTION_HOUR`), like the rest of the service's tasks, instead of cron.
8. Pending for another stage: the history screen and the message field in the web (REQ in jiku).
9. Out of scope, noted: the volume of events, changelog and notifications per save does not change with the compaction.
10. Selection criterion confirmed: besides the documents with new activity or with a pinned link that expired or was revoked, those with some unprotected version that crossed the 24 h or the 30-day threshold since the last compaction are processed (section 3, case 24).
