# jiku-docs — repository conventions

- Everything in this repository is English: documentation, internal docs, contracts, code strings (CLI help,
  flags, bus error messages, `meta.describe` labels, logs), test script output and commit messages.
  Identifiers (error codes, subjects, field names, env vars, enum values) are not prose and do not change.
  Quotes of the Jiku web's Spanish UI keep the Spanish literal with its English meaning the first time.
- Go 1.25 (`go.mod` at `go 1.25.0`): do not upgrade dependencies that require a newer version.
- The contracts in `docs/apis/` are the source of truth for shape. A shape change goes to the contract, the
  code and `CHANGELOG.md` (`[Unreleased]`) in the same commit. `docs/integration-decisions.md` takes
  precedence over `docs/specification-v1.md`.
- The resource specs (`internal/service/specs.go`) drive validation and `meta.describe`;
  `internal/contract` checks that they match the YAML.
- Deny-by-default: unknown keys, filters, fields and includes are `invalid_fields`; what is not visible is
  `*_not_found`. Access is never granted if core does not answer (`upstream_unavailable`).
- Every write: a transaction with an event in `changelog` (outbox). Content is uploaded to S3 before the
  transaction. No content, tokens or presigned URLs in logs or events.
- Tests: `go test -race ./...` (integration with Docker; `JIKU_DOCS_SKIP_INTEGRATION=1` skips it). Synthetic
  fixtures only: the HedgeDoc dump and the credentials live in `.local/` and are never committed or copied.
- Conventional commits in English (earlier history is in Spanish and is not rewritten), one per concern, on
  `dev`. Never `git push`. Never commit `.local/`, `_research/`, `.env` or credentials.
- Before finishing: `go vet ./...`, `golangci-lint run ./...`, `go test -race ./...`, `docker build .`.

---

## Cutting a release

**Triggers:** "versiona", "versionar", "cortá una release", "nueva versión", "bump de versión",
"versiona y listo", "release", "cut a release", "new version", "version bump", "bump the version",
"release and done", or any variant, in Spanish or English. When one appears, follow this procedure in full
and in order. It is the same as the one in the jiku repo, adapted to a Go service.

### What is versioned and what is not

| Version | Where it lives | Who moves it |
| --- | --- | --- |
| **The product**, what "version it" means | The `vX.Y.Z` tag + the `## [X.Y.Z]` section of `CHANGELOG.md` | This procedure |
| The contracts (`info.version` of `docs/apis/*.yaml`) | Each YAML | The commit that changes the shape (see above), never a release |

The product number **is not written in any code file**: the binary receives it at build time
(`-ldflags "-X main.version=..."`, `ARG VERSION` in the `Dockerfile`), and `release.yml` takes it from the
tag. That is why there is no `set-version.sh` here: applying the number means writing the CHANGELOG section
and creating the tag. The policy is in [CHANGELOG.md](CHANGELOG.md#versioning-policy); this is the
step by step.

### Step 1: choose the cut point

`main` is the release branch; work comes in through `dev`.

```sh
git describe --tags --abbrev=0            # latest published version (nothing: this is the first one)
git rev-list --left-right --count main...dev
git status --porcelain
```

**The cut lands on finished work.** Never in the middle of a half-implemented request (a P-x of
`docs/jiku-web-backlog.md`, a spec in `docs/` in progress) nor with `[Unreleased]` describing something the
code does not do yet. If `dev` has open work, the cut is `main` (or the last finished commit of `dev`), and
**the user is told**, it is not resolved silently. If `origin/main` is behind `main`, report it too, but
**do not push** (see step 8).

**First release:** if `main` does not exist, it is created at the cut point (`git branch main <commit>`),
without pushing.

### Step 2: gather the real changes

The CHANGELOG entry covers **everything** that came in since the last tag. Commit subjects are not enough:
read the bodies.

```sh
git log v<latest>..HEAD --oneline
git log v<latest>..HEAD --format='%h%n%B'      # the bodies explain the why
git diff --stat v<latest>..HEAD
```

Sources, from richest to poorest:

1. `[Unreleased]` in `CHANGELOG.md`, which every shape commit already fed.
2. `docs/integration-decisions.md`, `docs/p1/service.md` and the specs in `docs/` (why and how).
3. The commit bodies.

And always review these diffs, which are the ones that produce entries nobody wrote:

```sh
git diff v<latest>..HEAD -- .env.dist                          # new or renamed variables
git diff v<latest>..HEAD -- docs/apis/                         # bus and registry contracts
git diff --stat v<latest>..HEAD -- internal/store/migrations/  # migrations
git diff v<latest>..HEAD -- Dockerfile docker-compose.local.yml .github/workflows/
git diff v<latest>..HEAD --diff-filter=D --name-only           # what was deleted
```

> **The rule that matters: every entry must be verifiable in the tree being tagged.** If the cut is from
> `main`, a change that is only in `dev` **does not go** in the entry. Check with
> `git branch --contains <sha>` before claiming anything.

### Step 3: propose the number and WAIT

| Bump | When |
| --- | --- |
| **major** | Breaks the bus contract (`docs/apis/jiku-docs-*.yaml`), the module protocol (`jiku-modules.yaml`) or requires a destructive data migration |
| **minor** | New functionality, backwards compatible |
| **patch** | Bugs and internal changes, with no effect on any contract |

Present to the user: **the proposed number, the reasoning and the list of breaking changes found**.

**Do not set the version without explicit confirmation.** If the user corrects the number, it is their
decision: take it. If the correction contradicts the table (a `minor` with contract-breaking changes, for
example), say so **in one line**, keep the breaking changes clearly visible at the top of the entry and go
on without insisting.

### Step 4: write the CHANGELOG entry

In [CHANGELOG.md](CHANGELOG.md), above the previous entry:

- Title `## [X.Y.Z] - YYYY-MM-DD` with **today's real date**.
- `## [Unreleased]` stays empty above; what it had moves under the new title.
- Keep a Changelog sections, only those that apply: `Added`, `Changed`, `Deprecated`, `Removed`, `Fixed`,
  `Security`.
- **`### Notes for existing installations`** at the end. It belongs to this repo, not to Keep a Changelog,
  and it is **mandatory** when there are: new required environment variables, renamed or removed
  variables, destructive or data migrations, roles that change in Zitadel, or bus consumers that have to
  migrate.
- In English, like the rest of the file. Bold at the start of the important entries. Say **what breaks and
  what whoever operates the installation has to do**, not only what changed.
- Never delete or rewrite entries of already published versions.

### Step 5: update the README and the documentation if needed

- **[README.md](README.md)**: if the package table, the diagram, the number of commands and queries, the
  subcommands, the requirements or something it links to changed. **It carries no version number, on
  purpose: do not add one.**
- [`documentation/`](documentation/README.md) (user documentation): if a feature, an operation, a variable
  or a known limitation changed.
- `.env.dist`: it must have every new variable, with its comment.
- `docs/apis/*.yaml` are the source of truth for the contracts: if the code and the document disagree, the
  document wins, and that is a bug to report, not something to "fix" in the YAML during a release.

### Step 6: verify the number

```sh
grep -n '^## \[X.Y.Z\] - ' CHANGELOG.md          # the same check release.yml requires
go vet ./... && golangci-lint run ./... && go test -race ./...
docker build --build-arg VERSION=X.Y.Z -t jiku-docs:release-check . \
  && docker run --rm jiku-docs:release-check version   # must print X.Y.Z
docker rmi jiku-docs:release-check
```

It runs before tagging because `release.yml` will run it anyway, and by then the tag is already in place.

### Step 7: commit

On the cut branch, with the CHANGELOG and whatever was touched in the README, `documentation/`, `.env.dist`
or `docs/`.

**Never `git add -A` or `git add .`**: `.local/`, `_research/` and `.env` live at the root and are not
committed. Stage by name:

```sh
git add CHANGELOG.md README.md .env.dist docs/<whatever applies>
git status --porcelain          # confirm there is nothing extra
git commit -m "chore(release): X.Y.Z"
```

End the message with the usual `Co-Authored-By:` line. If the cut was on `dev`, bring `main` to the release
commit (`git switch main && git merge --ff-only dev`, or create it if it is the first one).

### Step 8: local tag, and nothing else

```sh
git tag vX.Y.Z          # on main
```

> ### NEVER PUSH ANYTHING
>
> Not the branch, not the tag, not `git push --tags`, not `git push --follow-tags`, not
> `git push origin vX.Y.Z`. **Not even when it looks like the obvious next step**, nor when the user says
> "versiona y listo" (release and done).
>
> The reason is concrete: pushing the tag is what publishes `gravadigital/jiku-docs` to Docker Hub
> (`release.yml` is triggered by `push: tags: ['v*.*.*']`, with the tags `X.Y.Z`, `X.Y` and `latest`).
> That action belongs to the user, not to the agent.
>
> Finish by telling them the exact command, as information, without running it:
>
> ```sh
> git push origin main dev && git push origin vX.Y.Z
> ```

### Step 9: report

- Which version was tagged, on which branch and on which commit.
- What was left out of the release and why (typically: `dev` work from an open request).
- If `origin/<branch>` is behind, say so. If the GitHub repo does not exist yet, say that too.
- The push command, without running it.
- That `release.yml` checks the tag against the CHANGELOG again and runs vet, lint and tests before
  publishing, so a wrongly placed tag never reaches the registry.

### Hard rules

1. **Never `git push`** anything, at any step, for any reason.
2. **Never set the version without** the user's explicit confirmation.
3. **Never put a change in the entry that is not in the tree being tagged.**
4. **Never version on top of a half-implemented request** without warning first.
5. **Never touch the contracts' `info.version` in a release**: it moves with the shape change.
6. **Never rewrite entries of already published versions.**
7. If the tag already exists and needs fixing: `git tag -d vX.Y.Z` and create it again. This is safe **only
   because it is never pushed**.
