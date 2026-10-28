# Configuration

Everything is configured at runtime through environment variables. The image carries no
configuration. [`.env.dist`](../.env.dist) lists every variable with values for Jiku's local stack;
copy it to `.env` (ignored by git) and fill it in. In production every value changes.

A variable set to an empty string counts as unset and takes its default. A malformed boolean,
number or duration also falls back to the default, except the history-compaction thresholds, which
stop the service (a wrong threshold must not silently change what gets deleted).

## Required for `serve`

`serve` refuses to start without these:

| Variable | What goes in it |
|---|---|
| `NATS_CREDS` | path to the sentinel credentials of Jiku's bus |
| `ZITADEL_PROJECT_ID` | Jiku's roles project in Zitadel |
| `DOCS_SERVICE_USER_KEY_B64` or `DOCS_SERVICE_USER_KEY_FILE` | the machine user's Zitadel JSON key |
| `JIKU_API_USER_ID` | the `sub` of Jiku's api service user |
| `JIKU_WEB_URL` | base URL of the Jiku web |
| `JIKU_PUBLIC_URL` | base URL of share links |
| `DATABASE_URL` | PostgreSQL DSN |
| `DOCS_S3_ENDPOINT`, `DOCS_S3_BUCKET`, `DOCS_S3_ACCESS_KEY`, `DOCS_S3_SECRET_KEY` | the content bucket |
| `DOCS_SECRET_KEY` | at least 32 random bytes, base64: `openssl rand -base64 32` |

`NATS_INSTANCE` must be `dev` or `prod`.

Other subcommands check only what they use: `migrate` needs `DATABASE_URL`; `storage init` the
bucket variables; `history compact` the database and the bucket; `module deregister` the bus and
identity variables.

## Bus

| Variable | Default | Required | Meaning |
|---|---|---|---|
| `NATS_URL` | `nats://localhost:4222` | no | URL of Jiku's NATS. The local compose sets `nats://nats:4222`. |
| `NATS_CREDS` | — | **yes** | Sentinel credentials. They grant nothing alone: they trigger the auth-callout. |
| `NATS_INSTANCE` | `dev` | no | `dev` or `prod`, the same as Jiku's `NATS_INSTANCE`. First segment of every subject. |
| `DOCS_SERVICE_NAME` | `jiku-docs` | no | Prefix of the micro services (`<name>-commands`, `<name>-queries`) and `serviceName` in `JIKU_MODULES`. Does not change the event and notification subjects. Jiku's access policy authorises `jiku-docs-*` literally, so changing it is a security change in Jiku too. |

## Identity

The machine user `jiku_docs`, with roles `internal-app` and `docs` and Access Token Type JWT.

| Variable | Default | Required | Meaning |
|---|---|---|---|
| `DOCS_SERVICE_USER_KEY_B64` | — | **yes**, or the next | Zitadel JSON key, base64 (`base64 -w0 key.json`). Takes precedence over the file. |
| `DOCS_SERVICE_USER_KEY_FILE` | — | alternative | Path to the JSON key, inside the container. |
| `ZITADEL_ISSUER_URL` | `https://id.grava.io` | no | Zitadel issuer. |
| `ZITADEL_PROJECT_ID` | — | **yes** | Project whose roles the token must carry. A token without roles stops the service. |

## Jiku integration

| Variable | Default | Required | Meaning |
|---|---|---|---|
| `JIKU_API_USER_ID` | — | **yes** | `sub` of Jiku's api service user: the only caller that may send `actor` and use `shares.resolve` / `shares.attachment`. |
| `JIKU_WEB_URL` | — | **yes** | Base of stable attachment links, `{JIKU_WEB_URL}/docs/attachments/{id}`, and of links between documents, `{JIKU_WEB_URL}/docs/{id}`. Trailing `/` is trimmed. |
| `JIKU_PUBLIC_URL` | — | **yes** | Base of share links, `{JIKU_PUBLIC_URL}/p/docs/{token}`. Must match the one Jiku derives from `DOCS_PUBLIC_DOMAIN`. Locally a host other than `localhost` (`http://127.0.0.1:3100`), so no cookies are shared. |
| `CORE_USER_INCLUDES` | `roles,identityType` | no | Includables of core's `users.list` that identify a caller's class. If core exposes only `roles`, set `roles`: the identity type is then derived from the `internal-app`/`core` role. |
| `CORE_TIMEOUT` | `10s` | no | Timeout of each call to core (Go duration). |

## Database

| Variable | Default | Required | Meaning |
|---|---|---|---|
| `DATABASE_URL` | — | **yes** | PostgreSQL 16 DSN. Migrations run on start. |
| `DOCS_DATABASE_PASSWORD` | — | local compose only | Password of the compose's PostgreSQL; the compose builds `DATABASE_URL` from it. Not read by the service. |

## Content bucket

| Variable | Default | Required | Meaning |
|---|---|---|---|
| `DOCS_S3_ENDPOINT` | — | **yes** | Endpoint the service uses. Production: `https://<region>.digitaloceanspaces.com`. |
| `DOCS_S3_PUBLIC_ENDPOINT` | — | no | Host presigned URLs are signed for, when clients reach the bucket by another host than the service. |
| `DOCS_S3_REGION` | `us-east-1` | no | Region. |
| `DOCS_S3_BUCKET` | `jiku-docs-content` | **yes** | Bucket name. |
| `DOCS_S3_ACCESS_KEY` | — | **yes** | Access key. |
| `DOCS_S3_SECRET_KEY` | — | **yes** | Secret key. |
| `DOCS_S3_FORCE_PATH_STYLE` | `true` | no | `true` for MinIO; **`false` for Spaces**. |
| `DOCS_TRASH_RETENTION_DAYS` | `7` | no | Days `trash/` keeps what the service removes before its lifecycle rule expires it. At least 1. Applied by `jiku-docs storage init`: changing it requires running it again. |

## Service secret

| Variable | Default | Required | Meaning |
|---|---|---|---|
| `DOCS_SECRET_KEY` | — | **yes** | At least 32 random bytes, base64. Encrypts the share-link token kept 5 minutes for idempotent retries, and keys the HMAC of the IPs recorded on share-link accesses. Changing it makes earlier IP hashes incomparable with new ones. |

## Operation

| Variable | Default | Required | Meaning |
|---|---|---|---|
| `DOCS_HTTP_ADDR` | `:8080` | no | Address of `/healthz`, `/readyz` and `/metrics`. Also what `jiku-docs healthcheck` calls. |
| `LOG_LEVEL` | `info` | no | `debug`, `info`, `warn`, `error`. |
| `DOCS_INDEXER_ENABLED` | `true` | no | Runs the search indexer in this process. |
| `DOCS_JANITOR_ENABLED` | `true` | no | Runs the periodic tasks (lock expiry, purges, relation review, history compaction). |
| `DOCS_OUTBOX_ENABLED` | `true` | no | Runs the outbox publisher (events and notifications). |
| `DOCS_RELATION_REVIEW_HOUR_UTC` | `6` | no | UTC hour of the daily review of relations against core. |

The three switches exist to split work between replicas; with all three off a replica only serves
requests, and nothing publishes events. Leave them on unless you know why.

## History compaction

See [operations.md](operations.md#history-compaction) and
[`docs/history-compaction-v1.md`](../docs/history-compaction-v1.md). Durations accept Go syntax plus
a `d` suffix for days (`30d`, `1d12h`).

| Variable | Default | Meaning |
|---|---|---|
| `DOCS_HISTORY_COMPACTION_ENABLED` | `true` | Runs the daily compaction. `jiku-docs history compact` works regardless. |
| `DOCS_HISTORY_COMPACTION_HOUR` | `3` | Hour (0–23, in `DOCS_HISTORY_TIMEZONE`) from which the daily run starts. |
| `DOCS_HISTORY_TIMEZONE` | `America/Argentina/Buenos_Aires` | Zone of the run hour and of the calendar days of retention. |
| `DOCS_HISTORY_KEEP_ALL` | `24h` | Window in which every version is kept. |
| `DOCS_HISTORY_KEEP_BLOCKS` | `30d` | Age up to which the last version of each editing session is kept. Must be at least `KEEP_ALL`. |
| `DOCS_HISTORY_BLOCK_GAP` | `30m` | Pause that closes an editing session. |
| `DOCS_HISTORY_BATCH_SIZE` | `100` | Documents per batch. At least 1. |
| `DOCS_HISTORY_S3_DELETE_BATCH` | `1000` | Objects per batch when moving pruned versions to `trash/` (parallel copies, then one `DeleteObjects`). 1–1000. |

An invalid threshold, hour, batch size or time zone stops `serve` and `history compact` with a
message naming the variable.

## HedgeDoc importer

| Variable | Default | Meaning |
|---|---|---|
| `HEDGEDOC_MYSQL_DSN` | `root:hedgedoc@tcp(127.0.0.1:33306)/codimd?parseTime=true&charset=utf8mb4` | MariaDB holding the HedgeDoc dump. Only `jiku-docs import hedgedoc` reads it; `--mysql-dsn` overrides it. |

## Local compose only

`docker-compose.local.yml` also reads `JIKU_DEPLOY_DIR` (default `../../jiku/deploy`, where the
sentinel credentials are) and `LOCAL_UID` / `LOCAL_GID` (default `1000`, the user the service runs
as). They are not service settings.
