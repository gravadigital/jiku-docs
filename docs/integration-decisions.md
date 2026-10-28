# jiku-docs: integration decisions (addendum to the v1 specification)

Date: 2026-10-24. This document **overrides `specification-v1.md`** wherever they contradict each other. The
contracts in `docs/apis/` remain the source of truth for shape; where a decision here changes a shape, the
`jiku-docs` repo updates the contract and records it in its CHANGELOG.

Repos involved (local paths):

| Repo | Path | Working branch |
| --- | --- | --- |
| jiku-docs (new service, Go) | `/home/lautaro/Documentos/proyectos/interno/gestor/conectores/jiku-docs` | `dev` |
| jiku (monorepo, grava-workflow) | `/home/lautaro/Documentos/proyectos/interno/gestor/jiku` | `feat/jiku-docs` (from `dev`) |
| jiku-go (library + CLI, **public**) | `/home/lautaro/Documentos/proyectos/interno/gestor/jiku-go` | `feat/jiku-docs` (from `dev`) |

Nobody runs `git push`. Credentials live in `jiku-docs/.local/` (ignored) and are never committed or copied
to another repo.

## D-1. Service identity: `docs` role + `internal-app`

- The machine user `jiku_docs` (userId `396604660191854594`) has **two roles** in Zitadel: `internal-app`
  and `docs`.
- **core does not change because of this.** For core it is just another `internal-app`: class `connector` (no
  row trimming), `identity_type = service`, `queries: ALL`, `commands: ALL`. The `docs` role is unknown to core
  and is ignored (verify that an unknown role in `users.roles` does not break `authorize-caller`, `caller-class`
  or `identity-type`; if it does, it is fixed in jiku with a test).
- **auth-callout:** new rule `match: docs` **above** `internal-app` in `rules.yaml`, with a new template
  `templates/docs.yaml` = the permissions of `connector.yaml` (to call core) **plus** the server ones:
  - sub `{instance}.*.jiku-docs-commands.v1.>` and `{instance}.*.jiku-docs-queries.v1.>` (queue group), `$SRV.>`;
  - `response` as in `core.yaml`;
  - pub `{instance}.docs-events.v1.>` and `{instance}.*.docs-notify.v1.>`;
  - manage the `JIKU_DOCS_EVENTS` stream (`$JS.API.STREAM.*.JIKU_DOCS_EVENTS`, etc.);
  - write and read only the `docs` key of `KV_JIKU_MODULES`.
  Verify how the callout matches with several roles (first rule that matches any of the roles).
- Where the specification says `docs-service`, read `docs`.

## D-2. Who can talk to jiku-docs

- `connector.yaml` (`internal-app`: the api and any connector) adds: pub on
  `{instance}.{user_id}.jiku-docs-commands.v1.>` and `.jiku-docs-queries.v1.>`, consume `JIKU_DOCS_EVENTS` with
  its own durable, read/watch `KV_JIKU_MODULES` and `$SRV.PING.>` (or `$SRV.PING.jiku-docs-queries`). Accepted:
  any `internal-app` is class `service` in jiku-docs and reads everything.
- `person-internal.yaml`: pub docs commands and queries under its `user_id`, sub
  `{instance}.{user_id}.docs-notify.v1.>`, read/watch the KV.
- `person-external.yaml`: only docs queries under its `user_id`, read/watch the KV. No notify.
- Exact KV read/watch permissions that jiku-go needs (verified by its implementation):
  `$JS.API.STREAM.INFO.KV_JIKU_MODULES`, `$JS.API.DIRECT.GET.KV_JIKU_MODULES.$KV.JIKU_MODULES.>`,
  `$JS.API.CONSUMER.CREATE.KV_JIKU_MODULES` and `$JS.API.CONSUMER.CREATE.KV_JIKU_MODULES.>`,
  `$JS.FC.KV_JIKU_MODULES.>` (pub) and `$SRV.PING.jiku-docs-queries` (request). They go in `connector.yaml`,
  `person-internal.yaml`, `person-external.yaml` and `docs.yaml`.
- **The api is identified by `user-id`**: jiku-docs receives `JIKU_API_USER_ID` (local: `390014149763006466`,
  the `userId` of `jiku/local/api-su.json`). Only that caller can send `actor` and use `shares.resolve` /
  `shares.attachment`.

## D-3. Attachments: core's file service (there is no attachments bucket in jiku-docs)

- jiku-docs has **no** attachments bucket or credentials. The jiku-docs `attachments.*` commands keep their shape
  in the contract, but internally they delegate to core, acting as the `jiku_docs` service (without `actor`):
  - `attachments.request-upload` → checks `write` on the document → `files.request-upload` in core → stores
    `attachments(id, document_id, core_file_id, …, status=pending)` → returns core's `uploadUrl`.
  - `attachments.{id}.confirm` → asks core for `files.{fileId}.request-download`, downloads the object and checks
    the declared SHA-256 → `ready`. (core does not verify bytes.)
  - `attachments.{id}.request-download` / `shares.attachment` → checks permissions in jiku-docs →
    `files.{fileId}.request-download` in core → returns the presigned URL.
  - `attachments.{id}.delete` → logical, in jiku-docs.
- Type and size policy: core's (`system_settings`); jiku-docs does not duplicate the allowlist and translates
  core's errors to `attachment_type_not_allowed` / `attachment_too_large`. Core down → `upstream_unavailable`.
- The `attachments.storage_key` column becomes `core_file_id`. `attachments_not_configured` is not emitted in v1
  (it stays in the catalog for compatibility).
- **To verify in jiku:** that core does not purge `files` not linked to a Jiku entity (the docs ones are never
  linked to requirements/tasks). If such a purge exists or the policy prevents it, the jiku REQ includes the
  minimal adjustment in core (e.g. marking the file as owned by an external service).

## D-4. Content storage: own S3 (DigitalOcean Spaces in prod, MinIO locally)

- A single bucket owned by jiku-docs for versioned content and large uploads
  (`docs/<documentId>/<version>-<sha256>.md`, `uploads/<contentUploadId>`). In prod it is a DigitalOcean Space
  with its own credentials; locally, the `jiku-docs-content` bucket in the **MinIO of the jiku stack**
  (`http://172.28.0.1:9000` from containers / `http://localhost:9000` from the host, `jiku-local` /
  `jiku-local-secret`, path-style). The jiku-docs compose creates the bucket idempotently.

## D-5. Attachment links and public route

- The api validates Bearer tokens; the browser does not talk to it directly. That is why the **stable link** pasted
  into the markdown is relative to the web: `{JIKU_WEB_URL}/docs/attachments/{id}`. The web serves it with a route
  handler (session → api `GET /api/docs/attachments/{id}` with Bearer → forwards the 302), just like
  `web/src/app/api/attachments/[id]/download/route.ts`. The web and opus-web viewers recognise that pattern (and
  the relative path `/docs/attachments/{id}`) and rewrite it to their own proxy. jiku-docs builds the `link` with
  `JIKU_WEB_URL`.
- Share links: `url = {JIKU_PUBLIC_URL}/p/docs/{token}`; jiku-docs receives `JIKU_PUBLIC_URL`. The public route is
  served by the api without a session (section 9.8 of the specification). Locally,
  `JIKU_PUBLIC_URL=http://127.0.0.1:3100` (a host different from `localhost`, no shared cookies). If in some
  deployment the api is not public, it is documented how to expose only `/p/docs/*`.

## D-6. Module registry

- jiku creates the `JIKU_MODULES` KV idempotently (a script like `create-events-stream.sh`, invoked by
  `deploy/local.sh up`) and documents it in `docs/apis/jiku-modules.yaml` (copied from this repo).
- jiku-docs only writes its `docs` key on startup; `jiku-docs module deregister` deletes it.
- jiku-docs creates its `JIKU_DOCS_EVENTS` stream on startup (idempotent).

## D-7. Local deployment of jiku-docs

- jiku-docs has its own `docker-compose.local.yml` (its own Postgres 16 + the service + an init that creates the
  bucket) attached to the external network of the jiku stack (`jiku-local_jiku`, verify the real name). It does
  not modify the jiku compose.
- Variables (final names are decided by the jiku-docs repo, documented in its `.env.dist`): NATS instance
  (`dev`), NATS URL, sentinel creds (`jiku/deploy/nats/creds/sentinel-client.creds`), machine user key in base64
  (`DOCS_SERVICE_USER_KEY_B64`), Zitadel issuer and project id (those in `jiku/deploy/.env`),
  `JIKU_API_USER_ID`, `JIKU_WEB_URL`, `JIKU_PUBLIC_URL`, content S3, Postgres DSN.
- jiku-docs CI: tests, Docker image build and publication to Docker Hub (`gravadigital/jiku-docs`), with the
  same scheme as `jiku/.github/workflows/{ci,dev-images,release}.yml`. The GitHub repo does not exist yet: it is
  left ready and validated locally (tests + `docker build`).

## D-8. Test users (keys in `jiku-docs/.local/`)

| File | Role | userId |
| --- | --- | --- |
| `jiku_docs.service_user.json` | `internal-app` + `docs` | `396604660191854594` |
| `testing_admin.service_user.json` | `admin` | `396554146460729346` |
| `testing_user.service_user.json` | `user` | `396554027241832450` |
| `testing_external_user.service_user.json` | `external-user` | `396554218317545474` |

## D-9. Import from HedgeDoc

- MariaDB dump in `jiku-docs/.local/codimd-2026-10-24.sql` (HedgeDoc 1.10, tables `Notes`, `Users`, …). The
  images are in a public Space (`grava-internal-01/docs-grava-io`) and are downloaded by URL.
- The importer lives in jiku-docs (`jiku-docs import hedgedoc` or a Go script): it reads a local MariaDB database
  loaded with the dump, applies a mapping sheet (CSV note → folder; unassigned → `/imported/`, admin only),
  creates the documents with `documents.new` as a service, uploads the images as attachments (D-3), rewrites the
  links and emits a report of notes with possible secrets. The dump is never committed or copied outside
  `.local/`.

## D-10. Caller roles and identity from core (follow-up in jiku)

- jiku-docs classifies the caller with `users.list {filter:{id}, include:["roles","identityType"]}`. Today
  `core/src/queries/users/users-spec.ts` deliberately excludes `roles` and `identityType` (CA-7: an external
  person could enumerate who is an admin).
- Decision: core declares them **includable only for the `connector` class** (roles `internal-app`, and therefore
  `docs`). For internal and external people they still do not exist (`invalid_fields`), so CA-7 holds. No filter
  and no sort, only `include`. A test that covers both sides.
- It goes as follow-up REQ-017 in jiku. Links between documents use the route the web already serves,
  `{JIKU_WEB_URL}/docs/{id}`; the HedgeDoc importer rewrites them to that format.

## D-11. The api sends the full `actor` and jiku-docs uses its roles

- Problem (end-to-end browser test): core updates a person's roles in `users` only when that person runs a core
  command or connects directly to NATS. A web user who only reads is left with stale roles or no row, and
  jiku-docs answers `caller_not_authorized` / `unknown_caller`.
- Decision: the api sends jiku-docs the same `actor` it sends to core (`buildActor`):
  `{ id, roles, name?, username?, email? }`, with `roles` taken from the already verified web token. Only the api
  can send `actor` (it is identified by `JIKU_API_USER_ID`). When the caller is the api and the `actor` carries
  `roles`, jiku-docs classifies the person with those roles (`admin`/`user` → `person-internal`, `external-user` →
  `person-external`, anything else → no access) and does not query `users.list` for that data. The
  `project-permissions` of an external person are still queried from core.
- Contract: the `Actor` schema of the three jiku-docs contracts becomes `{ id (req), roles (req, array of string),
  name?, username?, email? }`, `additionalProperties: false`.
  In `jiku-docs-events.yaml` the `Actor` schema is a different one: it describes the author of an event
  (`{ id, name, via }`), not what the api sends, and it does not change; the input `actor` only exists in commands
  and queries.
- The api does **not** accept machine user tokens (owner's decision, 2026-10-25): HTTP tests are done with human
  users through the browser.
- Also: a `person-external` receives the content of `documents.get` **without frontmatter** (it contains internal
  ids of requirements and tasks); opus-web shows links to documents (`/docs/{id}`) as text instead of linking to
  the internal web.
