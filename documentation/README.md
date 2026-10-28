# Jiku Docs documentation

Markdown documentation for a services team, kept next to the work it describes. Jiku Docs is the
**optional documentation module of Jiku**: documents linked to clients, projects, requirements and
tasks, with versions, edit locks, attachments, full-text search, live notifications and read-only
share links. It replaces HedgeDoc, which had no API and exposed every note publicly.

This is the documentation for **using and running Jiku Docs**. The internal working documentation —
the technical specification, integration decisions, design notes for each change — lives in
[`docs/`](../docs/). The bus contracts in [`docs/apis/`](../docs/apis/) are the source of truth for
anything that crosses the bus.

| | |
|---|---|
| [features.md](features.md) | what the product does |
| [user-guide.md](user-guide.md) | how people and agents use it: the web, the client portal, the `jiku docs` CLI and the sync daemon |
| [installation.md](installation.md) | how to install it next to Jiku |
| [configuration.md](configuration.md) | every environment variable |
| [operations.md](operations.md) | running it: subcommands, health, metrics, compaction, trash, backups |
| [bus-reference.md](bus-reference.md) | the bus interface: 19 commands, 14 queries, events, errors |
| [known-limitations.md](known-limitations.md) | **read before adopting** |

## How it fits with Jiku

```
   browser                         people and agents              services
      │                          (jiku-go: CLI, sync daemon)     (internal-app role)
      ▼                                     │                            │
  web / opus-web ──HTTP──▶ api ── actor ──┐ │                            │
                                          ▼ ▼                            ▼
                         NATS + auth-callout (identity in the subject) ◀──────────┐
                              │                      │                            │
                              ▼                      ▼                            │
                            core ◀── users, ── jiku-docs ── outbox ──▶ JIKU_DOCS_EVENTS
                              │     project-     │     │                          │
                       Jiku PostgreSQL  permissions, │     └──▶ docs-notify        ▼
                                    files.*      ▼          (per person)     indexer
                                       own PostgreSQL 16 + own S3 bucket
```

| Part | What it is responsible for |
|---|---|
| `jiku-docs` | This service. Serves two NATS micro services, `jiku-docs-commands` (writes) and `jiku-docs-queries` (reads). **No business HTTP at all**: like Jiku's `core`, it answers request/reply on Jiku's bus and takes the caller's identity from the subject, which the auth-callout guarantees. |
| Jiku `api` | Reaches jiku-docs on behalf of the two frontends, attaching the person in an `actor` envelope. Also serves the public route of share links. |
| `web` / `opus-web` | The documentation screens of the internal web and the client portal (both in Jiku's repository). |
| `jiku-go` | The official client: the `jiku docs` CLI, the `jiku docs sync` daemon and the Go library. People and agents reach the bus directly with it. |
| `core` | Jiku's core. jiku-docs asks it who a caller is, which projects an external user may see, whether a related client, project, requirement or task exists, and stores attachments through its file service. It never touches Jiku's database. |
| PostgreSQL 16 | jiku-docs' own database: metadata, versions, relations, changelog, search index. |
| S3 bucket | jiku-docs' own content bucket: one object per document version. DigitalOcean Spaces in production, MinIO locally. |

Jiku discovers the module through the `JIKU_MODULES` key-value bucket on the bus: when jiku-docs is
ready it writes the `docs` key, and the api and clients ping it. Without the key, nothing about
documentation appears in Jiku; with the key but no answer, the screens show a notice and disable the
actions that need it.

## Going further

- Technical specification: [`docs/specification-v1.md`](../docs/specification-v1.md), amended by
  [`docs/integration-decisions.md`](../docs/integration-decisions.md) and
  [`docs/p1/service.md`](../docs/p1/service.md) (permissions by visibility, generated locations),
  which take precedence over it.
- History compaction: [`docs/history-compaction-v1.md`](../docs/history-compaction-v1.md).
- The sync daemon as a systemd user service: [`docs/sync-systemd-v1.md`](../docs/sync-systemd-v1.md).
- HedgeDoc migration notes: [`docs/hedgedoc-migration.md`](../docs/hedgedoc-migration.md).
- Changes per release: [`CHANGELOG.md`](../CHANGELOG.md).
- The client side in full: [jiku-go, `docs/docs-module.md`](https://github.com/gravadigital/jiku-go/blob/main/docs/docs-module.md).
