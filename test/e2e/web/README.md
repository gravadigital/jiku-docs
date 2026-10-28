# Browser e2e: web and opus-web

Runs with Playwright (Chromium) through the jiku-docs integration in the internal web and in
opus-web, with real human users of the hosted Zitadel, against the local stack once it is up. It
covers the P-1 model (documentation without folders, contract 1.3.0): search box with filters as
tokens, creation without a folder, editing by fields, history, sharing, "Sin proyecto" (no
project), project, requirement and task cards, and that a `restricted` document does not exist for
a `user` or for an external person.

The web UI is in Spanish (it lives in another repo): the selectors and assertions on its texts
("Ver todos", "Asignar proyecto"…) stay in Spanish on purpose.

## Running

```sh
cd test/e2e/web
npm install                       # playwright; uses the Chromium in ~/.cache/ms-playwright
npx playwright install chromium   # only if it is missing
E2E_CLI_DOCS="/path/to/as.sh docs" node run.mjs --down      # everything, with the module down
node run.mjs --only opus --down                              # opus-web only, with the module down
node run.mjs --only web --steps W1,W3,W4                      # debug single scenarios
```

- `--only opus|public|web`: one suite. `--down`: includes the module-down scenario (it does
  `docker stop`/`docker start` of `jiku-docs-local`; it always brings it back up). `--headed`.
  `--steps`: only those ids (the ones that depend on another scenario fail; W1/O1 do the login; W4,
  W5, W6, W11, W12 and W13 use the document W3 creates). `--keep`: does not delete the data of the
  run.
- Credentials: `.local/test-users.env` of the repo (or `$TEST_USERS_ENV`) with `WEB_USER_LOGIN`,
  `WEB_USER_PASSWORD` (role `user`) and `WEB_EXTERNAL_LOGIN`, `WEB_EXTERNAL_PASSWORD`
  (`external-user`). The script never prints them and masks them in the screenshots.
- `E2E_CLI_DOCS` (formerly `E2E_CLI_ADMIN`, still accepted): command that runs `jiku` as the
  machine user of the jiku-docs **service** (`internal-app` + `docs`, class `service`: reads and
  writes everything). It prepares data, changes ACLs, creates and revokes share links and plays
  "another person" in the conflicts (recorded as the author with `via: service`). No `admin`
  person is needed. Without it, the steps that need it end up "not tested". A minimal wrapper:

  ```sh
  #!/usr/bin/env bash
  # as.sh docs <jiku args...>
  export JIKU_SERVERS=nats://localhost:4222 JIKU_INSTANCE=dev JIKU_ISSUER=https://id.grava.io \
    JIKU_CREDS=<jiku>/deploy/nats/creds/sentinel-client.creds JIKU_PROJECT_ID=<zitadel.project_id> \
    JIKU_CONFIG_DIR=<own dir> JIKU_KEY_FILE=<jiku-docs>/.local/jiku_docs.service_user.json
  shift; exec <jiku-go>/bin/jiku "$@"
  ```

  The web persons log in with user and password (they do not need a CLI token). To use the same
  persons from the CLI (e.g. `test/e2e/local-bus.sh`), `device-login.mjs` obtains their
  device-flow token; see `../README.md`.
- Output: screenshots and `results.md` in `.local/e2e-web/<date>/` (or `$E2E_OUT`). Do not keep
  screenshots outside `.local/` or a temporary directory.
- URLs: `E2E_WEB_URL` (3000), `E2E_OPUS_URL` (3001), `E2E_PUBLIC_URL` (`http://127.0.0.1:3100`).
- Exit code: 0 all ok, 1 some failure, 2 no failures but with "not tested" scenarios.
  "jiku bug" status: the scenario found a known Jiku bug (`KNOWN` list in `run.mjs`, with the
  exact symptom that is checked) and tested the rest; it does not count as a failure unless
  `E2E_STRICT=1`. When the bug is fixed, the scenario goes back to "ok" by itself.
- The web talks to the api through Next **Server Actions** (POST to the page itself with the
  `next-action` header), not with browser `fetch`: the scenarios look at the UI, the body of those
  POSTs and the state in jiku-docs (through the service CLI), not `/api/docs/*` on the network.
  Only `/docs/attachments/{id}` and the keepalive `unlock` are browser HTTP routes.
- Links in the content open in another tab; a relative link to another document (`/docs/{id}`)
  is left without `href` (the viewer only keeps http(s), mailto and attachments).
- If Zitadel asks to change the initial password of a user, the suite does not change it: it
  marks its part as "not tested". Log in once by hand, change it and update the `.env`.
- O10 removes and gives back the external person's `project-permissions` on the project (SQL in
  `jiku-local-database`, or `$E2E_JIKU_DB_CONTAINER`). The (Zitadel) id comes from
  `user.zitadelId` of the session, or from `E2E_EXTERNAL_USER_ID`.
- Data of the run: at the start, the service (`E2E_CLI_DOCS`) creates without a folder a
  `restricted` document related to the project, to requirements 54 and 15 and to the task (with
  its own topic, word and attachment) and two `internal` ones without a project (one names the
  project in its title, for "Asignar sugerido" (assign suggested)). Everything the suite creates,
  also from the UI, carries the prefix `e2e-p1 ` and a unique mark per run, and is deleted at the
  end (`docs rm`, recoverable) unless `--keep`.
- The known Jiku bugs go in `KNOWN` (`JIKU-WEB-1` to `JIKU-WEB-5`); the scenario that finds them
  ends up "jiku bug" with the detail and keeps testing the rest.
- At 390 px the web has horizontal scroll because of the general layout (outside the scope of
  P-1): W15 reports it as an observation, without failing.

## Data it expects

The data `test/e2e/local-bus.sh` leaves with the local stack: project 43 `EXO - Videoconferencia`
(client 1 `EXO`; requirements 54 internal and 15 public, task 2716), the documents and attachments
whose ids are in `D` at the top of `run.mjs` (overridable with `E2E_*` variables; after the P-1
migration `Servidor de staging` became `internal`), more than 20 "Sin proyecto" (no project)
documents (for pagination) and notes imported from HedgeDoc (W15): the note with an image
`Opus / Arquitectura del sistema` and the link `Versión 1` of `Wecerty.md`. Also:

- The external person must have `project-permissions` on project 43
  (`user_project_permissions` in the Jiku database).
- The row of each human in core's `users` must have their roles. Core only mirrors it on the
  auth-callout event (NATS) or when the api sends a **command** on their behalf; a human who only
  browsed the web never updates it and jiku-docs answers `caller_not_authorized`. To force the
  mirror, any action that is a command is enough (e.g. subscribing to a requirement).
