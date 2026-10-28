# Installation

Jiku Docs is installed next to an existing Jiku deployment. It does not modify Jiku: it needs a
Jiku version that knows about it, an identity, its own database and its own bucket.

## What you need

| | |
|---|---|
| **Jiku 1.7.0 or later** | the first release with the documentation module (the `feat/jiku-docs` changes): see below |
| **Zitadel access** | to create a role and a machine user in Jiku's roles project |
| **PostgreSQL 16** | a database of its own, separate from Jiku's |
| **An S3 bucket** | DigitalOcean Spaces in production, MinIO locally |
| **Docker** | the published image `gravadigital/jiku-docs` |

## 1. Requirements in Jiku

Jiku Docs talks to Jiku only over the bus. These pieces have to be in place on Jiku's side; Jiku's
own [`CHANGELOG.md`](https://github.com/gravadigital/jiku/blob/main/CHANGELOG.md) (1.7.0, "Notes
for existing installations") is the reference for that side.

1. **Zitadel role `docs`** in the roles project (`GESTION_ZITADEL_PROJECT_ID` in Jiku), and a
   **machine user** for jiku-docs (`jiku_docs`) with **Access Token Type = JWT** and **both** roles,
   `internal-app` and `docs`. Its JSON key goes in `DOCS_SERVICE_USER_KEY_B64` (or
   `DOCS_SERVICE_USER_KEY_FILE`). The default `Bearer` token type issues opaque tokens the bus
   rejects; a token without roles stops jiku-docs at startup.
2. **auth-callout rule `match: docs`, above `internal-app`**, with its template `docs.yaml`. If the
   rule sits below `internal-app`, the machine user is matched as a plain connector and the module
   cannot serve. Besides what any connector has, the template grants exactly:

   | For | Permission |
   |---|---|
   | serving the micro services | sub `{instance}.*.jiku-docs-commands.v1.>` and `{instance}.*.jiku-docs-queries.v1.>` (queue groups `jiku-docs-commands` / `jiku-docs-queries`), sub `$SRV.>`, responses as `core.yaml` |
   | events and notifications | pub `{instance}.docs-events.v1.>` and `{instance}.*.docs-notify.v1.>` |
   | its stream and the indexer's consumer | pub `$JS.API.INFO`, `$JS.API.STREAM.CREATE/UPDATE/INFO.JIKU_DOCS_EVENTS`, `$JS.API.CONSUMER.CREATE.JIKU_DOCS_EVENTS` and `.>`, `$JS.API.CONSUMER.DURABLE.CREATE.JIKU_DOCS_EVENTS.>`, `$JS.API.CONSUMER.INFO.JIKU_DOCS_EVENTS.>`, `$JS.API.CONSUMER.MSG.NEXT.JIKU_DOCS_EVENTS.jiku-docs-indexer`, `$JS.ACK.JIKU_DOCS_EVENTS.>` |
   | the module registry | pub `$JS.API.STREAM.INFO.KV_JIKU_MODULES` and `$KV.JIKU_MODULES.docs` (write and delete its own key) |
   | replies | sub `_INBOX.<hash(userId)>.>` |

   The person and connector templates are widened in the same Jiku release so that people, the api
   and connectors can reach the module and read the registry; the exact lines are in
   [`docs/apis/jiku-modules.yaml`](../docs/apis/jiku-modules.yaml). Restart the auth-callout after
   changing its templates.
3. **Core requirements.** Core must expose `roles` and `identityType` as includables of
   `users.list` to the `connector` class (without them jiku-docs cannot classify anyone and every
   call from a person over the bus fails with `upstream_unavailable`), must accept `docs` as a
   service role, and must not purge files that are not linked to a Jiku entity (attachments of
   documents never are). **Deploy core before jiku-docs connects.**
4. **The api sends the full `actor`** (`id`, `roles`, and `name`/`username`/`email` when the token
   has them) on every call it makes on behalf of a person. jiku-docs classifies that person by
   `actor.roles`.
5. **The `JIKU_MODULES` KV bucket**, created by Jiku's deployment
   (`deploy/nats/create-modules-kv.sh`, which `./local.sh up` runs). Without it jiku-docs starts
   anyway and retries its registration every 30 seconds, but Jiku sees the module as not installed.

On the first connection to a fresh Jiku database, core may reject jiku-docs' first query while it
creates the machine user's record. jiku-docs reconnects up to five times at startup to get past
this; if core still does not know it, it starts anyway and a restart settles it.

From Jiku, jiku-docs needs three values: the `sub` of the api's machine user
(`JIKU_API_USER_ID` — the only caller allowed to send `actor` and resolve share links), the web's
URL (`JIKU_WEB_URL`) and the public URL of share links (`JIKU_PUBLIC_URL`, which Jiku derives from
its `DOCS_PUBLIC_DOMAIN`; both sides must match).

## 2. PostgreSQL 16

An empty database and a user that owns it. Migrations are embedded and **run on start** (`serve`),
safe with several replicas; `jiku-docs migrate` runs them alone. The search configuration needs the
`unaccent` extension, which the migrations create. Point `DATABASE_URL` at it.

## 3. The content bucket

One bucket, private, for the versions of every document (`docs/`), large uploads in transit
(`uploads/`), copies rendered for external users (`renders/`) and the trash (`trash/`).

- **Production: DigitalOcean Spaces**, with credentials of its own:
  `DOCS_S3_ENDPOINT=https://<region>.digitaloceanspaces.com` and `DOCS_S3_FORCE_PATH_STYLE=false`.
- **Locally: the MinIO of Jiku's stack**, bucket `jiku-docs-content`, path-style.

Then prepare it once (and again whenever `DOCS_TRASH_RETENTION_DAYS` changes):

```sh
docker run --rm --env-file .env gravadigital/jiku-docs:1.1.0 storage init
```

`storage init` is idempotent. It creates the bucket if the credentials allow it, leaves it
**without versioning** (suspends it if it was enabled, and logs that old versions are not removed by
themselves), applies two time-based expiration rules — `uploads/` after 1 day and `trash/` after
`DOCS_TRASH_RETENTION_DAYS` — and reads them back, logging a warning if the provider did not keep
them.

Attachments do not use this bucket: they are stored by core's file service.

## 4. The image

`gravadigital/jiku-docs` on Docker Hub, distroless and non-root. Tags: `X.Y.Z`, `X.Y` and `latest`
for releases, `dev` for the tip of the `dev` branch. The default command is `serve`; the HTTP port
for health and metrics is `8080` (`DOCS_HTTP_ADDR`); the image's `HEALTHCHECK` runs
`jiku-docs healthcheck`.

```sh
docker run -d --name jiku-docs --env-file .env \
  -v /path/to/sentinel-client.creds:/run/secrets/nats.creds:ro \
  -e NATS_CREDS=/run/secrets/nats.creds \
  gravadigital/jiku-docs:1.1.0
```

`NATS_CREDS` are the sentinel credentials of Jiku's bus: they grant nothing by themselves, they
trigger the auth-callout, which authorises the machine user's token. Every variable is in
[configuration.md](configuration.md).

When it is up, the log says `jiku-docs ready` and then that the module was registered, and
`/readyz` answers 200. From a bus user: `nats micro info jiku-docs-queries`.

## Running locally

Against Jiku's local stack:

1. Start Jiku (`jiku/deploy/local.sh up`). It creates the `jiku-local_jiku` network, NATS with the
   auth-callout, MinIO and the `JIKU_MODULES` bucket.
2. Configure:

   ```sh
   cp .env.dist .env
   echo "DOCS_SERVICE_USER_KEY_B64=$(base64 -w0 path/to/jiku_docs.service_user.json)" >> .env
   echo "DOCS_SECRET_KEY=$(openssl rand -base64 32)" >> .env
   ```

   Check `JIKU_API_USER_ID` (the `userId` of Jiku's local api service user), `ZITADEL_PROJECT_ID`
   and `NATS_INSTANCE` against `jiku/deploy/.env`.
3. Start:

   ```sh
   docker compose -f docker-compose.local.yml up -d --build
   docker logs -f jiku-docs-local
   curl -s localhost:8090/readyz
   ```

`docker-compose.local.yml` adds, without touching Jiku: its own PostgreSQL 16 on
`127.0.0.1:5442`, a one-shot `storage init` against Jiku's MinIO, and the service, with
`NATS_URL=nats://nats:4222`, the sentinel credentials mounted from
`../../jiku/deploy/nats/creds/sentinel-client.creds` (`JIKU_DEPLOY_DIR` changes the path) and
health and metrics on `127.0.0.1:8090`. The service runs with the host's uid
(`LOCAL_UID`/`LOCAL_GID`, default 1000), because the sentinel credentials are readable only by the
user who generated them.

Then try it with jiku-go: `jiku docs status`, `jiku docs ls /`.

Without Docker for the service: start only the database and the init
(`docker compose -f docker-compose.local.yml up -d docs-database docs-storage-init`), export the
variables of `.env` with `DATABASE_URL` pointing at port 5442, and `go run ./cmd/jiku-docs`.

## Uninstalling

`jiku-docs module deregister` deletes the `docs` key from `JIKU_MODULES`; then stop the service.
Jiku stores no documentation data, so nothing is left broken. A normal shutdown does **not**
delete the key: a restart is not an uninstall.
