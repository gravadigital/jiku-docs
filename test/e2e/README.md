# Local e2e

Two runs against the local stack once it is up (Jiku with `deploy/local.sh up` and jiku-docs with
`docker compose -f docker-compose.local.yml up -d`):

- `local-bus.sh`: jiku-docs **over the bus**, with the `jiku` CLI from jiku-go.
- `web/`: the integration in the internal web and in opus-web, with Playwright (see `web/README.md`).

Both use the same three identities, and none of them is a test machine user:

| identity   | who                                                 | how it authenticates                       |
| ---------- | --------------------------------------------------- | ------------------------------------------ |
| `docs`     | the machine user of the jiku-docs service (`internal-app` + `docs`; class `service`: reads and writes everything) | key file `.local/jiku_docs.service_user.json` |
| `user`     | a person with the `user` role (`WEB_USER_*`)        | device flow, token in its `JIKU_CONFIG_DIR` |
| `external` | a person with the `external-user` role (`WEB_EXTERNAL_*`) | device flow, token in its `JIKU_CONFIG_DIR` |

P-1 model (contract 1.3.0): permissions are decided by the visibility, not by the folders. `docs`
does the bootstrap and what is admin-only (`folders.*`, `unlock --force`, creating `restricted`,
test share links). There is no `admin` person: the service tests it, since it reads and writes
`restricted` like an admin and is recorded as the author with `via: service`. `local-bus.sh` checks
that a `restricted` document does not exist for `human-user` or `human-external` through any path
the CLI reaches (get with and without a version, history, list and the `visibility` filter, search,
topics, attachments, share links, suggest, changes, sync), that a share link created before the
restriction gives 404, and the generated location, `relate`, `suggest`,
`hasProject`/`hasClient`/`hasTopic` and `projectMatch`.

## Preparing the persons (device flow, once)

The credentials of the persons go in `.local/test-users.env` (never versioned):
`WEB_USER_LOGIN`, `WEB_USER_PASSWORD`, `WEB_EXTERNAL_LOGIN`, `WEB_EXTERNAL_PASSWORD`. The initial
Zitadel password must have been changed (log in once by hand).

Each person needs its own `JIKU_CONFIG_DIR` with a device-flow token. Do not use the config or the
tokens in `~/.config/jiku/` of whoever runs the test: from there only `zitadel.client_id` (the
native app with Device Code) and `zitadel.project_id` are **read**.

Automatic (Playwright completes the approval with the credentials of the `.env`):

```sh
cd test/e2e/web && npm install
node device-login.mjs USER     ../../../.local/cli/human-user
node device-login.mjs EXTERNAL ../../../.local/cli/human-external
```

By hand, for each person:

```sh
JIKU_CONFIG_DIR=.local/cli/human-user JIKU_SERVERS=nats://localhost:4222 JIKU_INSTANCE=dev \
JIKU_CREDS=../../jiku/deploy/nats/creds/sentinel-client.creds JIKU_ISSUER=https://id.grava.io \
JIKU_CLIENT_ID=<zitadel.client_id> JIKU_PROJECT_ID=<zitadel.project_id> \
  ../../jiku-go/bin/jiku login          # prints a URL and a code on stderr: approve as that person
```

and verify with `jiku whoami` and `jiku doctor` in the same environment. The first request of a
person may lose the race with its own authentication event (core does not have its `users` row
yet): retry. The token lasts ~20 h and the CLI refreshes it by itself with the refresh token.

## local-bus.sh

```sh
test/e2e/local-bus.sh                       # full run
E2E_DISRUPTIVE=1 test/e2e/local-bus.sh      # also: docker stop/start and deregister of the module
```

By default it takes the service key from `.local/jiku_docs.service_user.json` and the config dirs
of the persons from `.local/cli/human-user` and `.local/cli/human-external`
(`E2E_DOCS_KEY`, `E2E_USER_CONFIG_DIR`, `E2E_EXTERNAL_CONFIG_DIR`; the rest of the variables are in
the header of the script). The user ids come from `jiku whoami`.

- Everything it creates lives under `/e2e-<timestamp>/`, except one document with a generated
  location (under `/clients/<client>/<project>/`). Since folders no longer hide anything, at the end
  the service deletes (logically) the documents of the run (`E2E_KEEP_VISIBLE=1` keeps them).
  jiku-docs does not delete folders.
- The external person is a real person who may already have `project-permissions` on the project
  the script picks: if so, they are removed during the run and given back at the end (also if the
  script is interrupted).
- Scenario `8b` (history compaction) compacts on demand inside the container and checks that the
  pruned objects ended up in `trash/` of the bucket (with the `mc` of Jiku's MinIO,
  `E2E_STORAGE_CONTAINER`, default `jiku-local-storage`) and that `jiku-docs storage
  restore-from-trash --version-row` brings a version back.
- Exits with 0 if everything passed.
