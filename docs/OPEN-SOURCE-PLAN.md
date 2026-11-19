# Plan: publish as open source, docs site, developer guides

Three milestones, M7 to M9 in `ROADMAP.md`. M7 can start now and runs in parallel with M4.
M8 and M9 should start after M4 closes, so the reference pages do not churn while the studio
APIs are still changing. Everything builds in containers, like the rest of the project.

## What is already in place

Apache-2.0 `LICENSE` and `NOTICE`, `CONTRIBUTING.md` (DCO sign-off), `CODE_OF_CONDUCT.md`,
`SECURITY.md`, CI (format, vet, race tests with real Redis and Postgres, build), distroless
images, a `docs/` folder, a runnable demo (`docker compose up`) and a fictional test game.
A remote already exists (`origin`, GitHub). Visibility has not been checked from here.

## M7. Open source release

Exit criterion: a stranger can find the repository, trust it, run the demo in five minutes,
report a vulnerability privately, and get a signed, versioned release.

### 7.1 Pre-flight audit (before the repository is public)

Public history is permanent and indexed, so do this first.

1. **Independence check.** Search code, docs, examples, commit messages and history for any
   employer, real game or real company name (AGENTS.md independence rules). Fix by rewriting, not
   by a follow-up commit, if anything is found in history.
2. **Secret scan of the full history**, in a container: `gitleaks detect` (and `trufflehog` as a
   second opinion). Dev secrets such as `dev-secret` and the CI Postgres password are
   placeholders and stay, but each must be documented as dev only.
3. **Author identity.** All 21 commits carry a personal email address. Decided: rewrite to the
   GitHub `noreply` address before the first public push (see Decisions). Rewriting is only cheap
   now, before anyone forks.
4. **Tooling files.** `CLAUDE.md` and `.claude/` are committed. Decided: ship `AGENTS.md` as the
   contributor guide, keep `CLAUDE.md` as a pointer, drop `.claude/settings.json` (see Decisions).
5. **Dependency and license audit.** `go-licenses` report in a container. All direct
   dependencies must be Apache-2.0, MIT or BSD compatible. Commit the report to `docs/`.
6. **`make vuln` and `make lint` clean** on the exact tag to be released.

### 7.2 Repository settings (GitHub)

- Visibility public, description, topics (`mcp`, `ai-agents`, `game-development`, `gateway`,
  `oauth`, `golang`), website set to the Pages URL once M8 lands.
- Default branch `main`, protected: require PR, require the `ci` check, require linear history,
  no force push. Keep admin bypass off.
- Enable: private vulnerability reporting (matches `SECURITY.md`), secret scanning with push
  protection, Dependabot alerts and version updates (`gomod`, `docker`, `github-actions`).
- DCO enforcement: the DCO GitHub App, or a small workflow that checks `Signed-off-by`.
- Issue forms (bug, feature, security redirect), PR template with the rules checklist from
  `AGENTS.md` rule list (tests, docs, DECISIONS entry for compatibility changes), `CODEOWNERS`,
  `FUNDING` only if wanted. Labels: `good first issue`, `help wanted`, `area/*`, `milestone/*`.
- Enable Discussions (Q&A, ideas, show and tell). Keep Wiki off, the docs site is the home.

### 7.3 Workflows to add

| Workflow | Purpose |
|---|---|
| `ci.yml` (exists) | format, vet, tests, build |
| `lint.yml` | golangci-lint, on PR |
| `vuln.yml` | govulncheck on PR and weekly schedule |
| `codeql.yml` | CodeQL for Go, weekly and on PR |
| `release.yml` | on tag `v*`: build, test, publish |

Pin third party actions by commit SHA. Keep `permissions: contents: read` by default and grant
write only inside `release.yml`.

### 7.4 Releases

- **Versioning.** Semantic versioning. Start at `v0.1.0` and say plainly in the README that
  `0.x` may break manifest or protocol details, each such change with a `DECISIONS.md` entry.
  Add `CHANGELOG.md` (Keep a Changelog format), generated from conventional PR titles or written
  by hand per release.
- **Binaries.** GoReleaser run in a container: `gateway`, `gwctl`, `gwbridge` for linux, macOS
  and windows (amd64, arm64), checksums file.
- **Images.** Multi-arch `gateway` and `gwbridge` images to GHCR (`ghcr.io/moustafa-elgammal/agentgw`),
  distroless, non-root, tagged by version and `latest`.
- **Supply chain.** SBOM (syft) and keyless signatures with cosign for images and checksums.
  Document how to verify in the docs.
- **Go module.** The path `github.com/Moustafa-Elgammal/agentgw` already works for `go get` and
  pkg.go.dev. `pkg/agentsig` is the public library, so give it a package example and a `doc.go`.
  Everything else stays under `internal/`.

### 7.5 The second repository, `tinycity-mock`

The tutorials and the five-minute demo need the fictional game, so decide:

- **Decided, option A: publish it** as `tinycity-mock`, Apache-2.0, after the same audit.
  It is clearly fictional and already built as a public-safe test target. Rename the folder in the
  docs to "example game".
- **Option B:** move a trimmed copy into `agentgw/examples/` and keep the original private.
  More duplication, no second repository to maintain.

The workspace root (a meta repository holding both as submodules) stays private. It is a
convenience for the maintainer, not part of the product.

### 7.6 README for strangers

Reorder the README to: one sentence, a short GIF of an agent playing the example game through the
gateway, a five command quickstart, the status and compatibility promise, the feature list, links
to the docs site, contributing, security, license. Add badges (CI, Go reference, license,
release). Keep the long internal detail in the docs site.

## M8. GitHub Pages: project site and developer documentation

Exit criterion: `https://moustafa-elgammal.github.io/agentgw/` serves a landing page that shows
the features and a documentation site that builds from `docs/` on every merge, with link checks
green.

### 8.1 Choice of tool (decided: Starlight)

Constraints: Markdown source that stays in `docs/` and is readable on GitHub too, builds in a
container (no host installs), free hosting on GitHub Pages, search, dark mode, good code blocks.

| Option | Notes |
|---|---|
| **Astro Starlight (recommended)** | Markdown and MDX, built in search, dark mode, i18n, a splash page for the landing, active project. Node runs only inside the build container |
| MkDocs Material | Very quick, Python. Check the project's maintenance status before committing to it |
| Docusaurus | Strong versioned docs, heavier React stack |
| Hugo plus a docs theme | Fast builds, more theme assembly |

These are site tooling dependencies, kept in a separate `site/` folder with its own
`Dockerfile` target and compose service (`docker compose run --rm site build`). They never
touch the Go module. Record the choice in `DECISIONS.md`.

### 8.2 Information architecture

```text
Home (landing)
Get started
  Overview: what the gateway is and is not
  Quickstart: run the demo in five minutes
  Connect an agent (Claude Desktop through gwbridge, any MCP client)
Concepts
  How a call flows (the pipeline)
  Tiers, scopes and policy
  Players, grants and confirmations
  Security model
Guides                        (M9)
Reference
  Manifest format (generated from the validator)
  Signed request protocol (with test vectors)
  Studio and admin HTTP API (OpenAPI)
  Gateway flags and environment (generated from `--help`)
  Result and error codes
  Audit event fields
Operate
  Deploy with Compose, Postgres, Redis
  Rotate keys, upload and roll back manifests
  Observability and runbooks (grows with M6)
Project
  Roadmap, decisions, changelog, contributing, security policy
```

Existing files map across: `ARCHITECTURE`, `SECURITY-MODEL`, `PROTOCOL`, `MANIFEST`, `DECISIONS`
and `ROADMAP` become pages with front matter added. Do not fork the content: the site reads
`docs/`, so the repository and the site never disagree.

### 8.3 Landing page that shows the features

Sections, each with one short visual:

1. Hero: "Let a player's AI agent play your game, safely." A terminal recording (asciinema or a
   GIF) of the demo, and the two buttons Get started and GitHub.
2. How it works: one diagram (agent, gateway, studio backend) with the pipeline order.
3. Feature cards: tools from a manifest, per tier policy, daily caps and cooldowns, signed backend
   calls, player confirmations, OAuth linking, kill switches, audit with hashed arguments,
   hot-swappable manifests and key rotation, MCP over streamable HTTP.
4. Safety by design: the six design rules in plain words.
5. Integration levels: manifest only, signed endpoint, relay (marked planned).
6. Code strip: a ten line manifest, the signed request, the agent's tool call.
7. Status and roadmap, with the compatibility promise.

Diagrams are SVG or Mermaid in the repository, never screenshots of slides, so they diff.

### 8.4 Generated reference (so it cannot drift)

- Manifest reference from the Go validator rules (`gwctl docs manifest`), or from a JSON Schema
  generated once and tested against the example manifests.
- Studio and admin API as an OpenAPI 3.1 file in `docs/openapi/`, rendered in the site. A test
  checks every route mounted in `cmd/gateway` appears in the spec.
- Flags page generated from `gateway --help` during the docs build.
- Error code table generated from the codes the gateway emits, checked by a test.

### 8.5 Pages deployment and quality gates

- Workflow `pages.yml`: build in the container, upload the artifact, deploy with
  `actions/upload-pages-artifact` and `actions/deploy-pages`. Run on `main` and on `docs/**`
  changes. A PR build runs without deploying.
- Set the site `base` to `/agentgw/` (or a custom domain later, with a `CNAME`).
- Gates in CI: build succeeds, internal and external link check, spell check on prose,
  every code block marked as a manifest is validated with `gwctl validate`, markdown lint.
- Basics: sitemap, `robots.txt`, social preview image, per-page edit link to GitHub, search,
  copy button on code blocks, dark and light themes, mobile layout, accessibility check.
- Later: versioned docs per release once `v1.0` is near. Optional custom domain.

## M9. Developer guides, tutorials and best practices

Exit criterion: a developer who has never seen the project can onboard a toy game end to end by
following the guides alone, and every command in them is run by CI.

### 9.1 Getting started tutorials (each 10 to 20 minutes, runnable)

1. **Play the example game with an agent.** `docker compose up`, connect Claude Desktop through
   `gwbridge`, ask the agent to build, and watch the audit and the caps work.
2. **Describe your game's actions.** Write a manifest from scratch for a small REST API:
   tiers, params, caps, `confirm`. Validate with `gwctl`.
3. **Make your backend accept signed calls.** Verify the signature, check the timestamp, enforce
   the idempotency key. Snippets in Go (import `agentsig`), Python, TypeScript and C#, each
   tested against the published test vectors.
4. **Link a player.** Mint a link ticket from your backend, the consent page, coach or operator
   scope, and the grants screen in the game's own UI.
5. **Ask the player before big actions.** Mark a tool `confirm`, build the approve and deny UI
   with the studio confirmation endpoints.
6. **Go live.** Postgres and Redis, the admin key, upload a manifest, roll back, rotate keys,
   run behind a proxy (`--client-ip-header`), health and kill switches.
7. **Test your integration.** Run the conformance suite against your gateway and backend.

### 9.2 Best practices (short, opinionated, each with the why)

- Designing tools: few, coarse tools beat many tiny ones. Names a model can guess. Descriptions
  written for the agent, with units and limits. Return small, structured results.
- Tiers: start in coach (read only), add operator tools one at a time, keep sensitive disabled.
- Caps and cooldowns: cap by amount of what can be lost, not by call count. Examples for
  currency, inventory, social actions.
- Confirmation: which actions need it (irreversible, expensive, affect other players) and how
  to show the player the studio's own description, never the agent's text.
- Idempotent backends: honor the idempotency key, return the same result for a repeat.
- Untrusted data: backend text that reaches the agent is data, never instructions. Do not echo
  other players' chat into tool results.
- Keys: separate player and admin keys, rotation schedule, secrets in a manager, never in a
  manifest or the repository.
- Operations: what to alert on, how to read the audit log, using kill switches in an incident.
- Rollouts: ship a manifest to a staging tenant, compare behavior, roll back fast.
- Threat model walk-through for studios, linking to `SECURITY-MODEL.md`.

### 9.3 Examples and recipes

- `examples/` gains three small, tested projects: a manifest-only integration, a Go backend with
  `agentsig`, a Python backend. Each has its own README and a compose file.
- A cookbook: rate limit tuning, per-tier caps, a "shop" tool with confirmation, handling backend
  errors, multi-step plans an agent can follow.
- An FAQ and a troubleshooting page keyed by error code (`scope_insufficient`,
  `confirmation_required`, `outcome_unknown`, and so on).

### 9.4 Keeping guides true

- Every tutorial's commands live in a script under `docs/tutorials/` that CI runs against the
  compose stack. A failing guide fails the build.
- Each guide states the version it was verified against and links to the changelog.
- A docs checklist in the PR template: if behavior changes, update the guide or the reference.

## Sequence and rough effort

| Step | Depends on | Size |
|---|---|---|
| 7.1 audit, 7.5 decision on the mock | none | small, do first |
| 7.2 settings, 7.3 workflows | 7.1 | small |
| 7.4 release automation, 7.6 README | 7.1 | medium |
| Make the repository public, tag `v0.1.0` | 7.1 to 7.4 | the launch |
| 8.1 tool decision, site skeleton, `pages.yml` | none (can start early) | medium |
| 8.2 to 8.4 content, landing, generated reference | M4 closed | medium to large |
| 9.1 tutorials 1 to 3 | skeleton | medium |
| 9.1 tutorials 4 to 7 | M4 and M3 OIDC decision | medium |
| 9.2 to 9.4 | tutorials | medium |

## Decisions

Decided by the maintainer:

1. **Author identity: rewrite.** Before the first public push, rewrite history so every commit
   uses the GitHub `noreply` address (`<id>+<user>@users.noreply.github.com`), with
   `git filter-repo --mailmap` in a container. Steps: make a full backup clone first, rewrite in
   a fresh clone, verify with `git log --format='%an <%ae>' | sort -u` that only the noreply
   address remains, check that every commit still has its `Signed-off-by` line (the sign-off
   contains the email too, so the mailmap must also rewrite the commit messages with
   `--replace-message`), then force push once while the repository is still private. Set
   `git config user.email` to the noreply address and turn on "Keep my email addresses private"
   and "Block command line pushes that expose my email" in GitHub settings so it cannot recur.
   The same applies to `tinycity-mock`. Do this in 7.1, before anything else is published.
2. **`tinycity-mock`: publish as a second repository**, Apache-2.0, after the same audit.
3. **Docs tool: Astro Starlight.** Recorded in `DECISIONS.md` when the site skeleton lands.
4. **Contributor notes: ship them, cleaned.** `AGENTS.md` becomes the tool-neutral contributor
   guide (Docker first, pipeline order, change rules). `CLAUDE.md` stays as a one line pointer.
   The independence rules are reworded to "no third-party or proprietary material". Drop
   `.claude/settings.json` and the stale `m1-spike` command. Keep `check` only if wanted.
5. **Domain: the default `github.io` URL for now**, a custom domain can come later with a
   `CNAME`. (Assumed from the answer; say so if a custom domain is wanted at launch.)
6. **Funding: none for now.** No `FUNDING.yml`.

Still needed before 7.1 can run: the numeric GitHub user id for the `noreply` address (shown at
`https://api.github.com/users/<username>`, or in GitHub settings under Emails).
