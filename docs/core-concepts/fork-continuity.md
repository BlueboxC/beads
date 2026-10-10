---
title: BlueboxC continuity fork
description: Install and use the fork's document learning, code index, native graph and session continuity in one Beads Dolt workspace
---

# BlueboxC continuity fork

Use this fork when preserving project objectives, decisions, existing modules
and source-backed solutions matters across sessions. [FORK.md](https://github.com/BlueboxC/beads/blob/codex/project-continuity/FORK.md)
identifies the upstream baseline, implemented additions and remaining limits.
All project data stays in that project's existing Beads Dolt.

## Optional Graph Preview

The fork also includes an explicit experimental graph workspace format. It adds
typed Links and retained versions over the same Dolt engine. It does not migrate
existing continuity workspaces or replace their code/knowledge/activity/viewer
commands. See [Graph Preview](/reference/graph-preview) for fresh-workspace setup,
contributor attribution and current limits. Continue using ordinary initialization
below for projects that need the full continuity workflow.

## Manual approval records

The fork adds an opt-in `gates.human.resolvers` allowlist over the existing
mutation transactions. Explicit listed actors can resolve human gates; fallback
identities and `--force` cannot waive the policy. Closure, status/type/defer/
persistence changes, deletion and removal of protected relations share the rule.
See [gates](/workflows/gates#optional-human-resolver-policy-blueboxc-fork) for setup
and batch behavior. Names are caller-asserted; your invoking system still owns
identity, configuration permissions and authorization of external operations.

## External prerequisites

Configured `external:<project>:<capability>` blocks participate in ready,
blocked, ready counts and atomic `ready --claim`. A capability is satisfied only
by a closed provider bearing `provides:<capability>`; exporting it or forcing
shipment while it remains open does not release consumers. Missing capabilities
and unavailable or moved provider paths stay blocked, including active
parent-child descendants across tasks and wisps. Limits apply after exclusion.

Reads use the existing storage factory with no migration, hooks or server
auto-start. Provider observations have a five-second cooperative lookup deadline;
local selection/claim is atomic, but independent databases are not one distributed
transaction. API embedders bind a read-only resolver with
`issueops.WithExternalResolver`; HTTP callers pass `Config.ExternalResolver`.
Without a resolver, external prerequisites remain blocked. Restart the normal
API server after changing its configured provider paths. The explicit manual
`update --claim` retains upstream's override semantics; use `ready --claim` for
blocker-aware scheduling.

## Install the fork

The upstream Homebrew/npm packages and upstream install script install upstream
Beads. To obtain these additions, build this fork's branch with the Go version
in `go.mod` and an embedded-capable C compiler toolchain:

```bash
git clone --branch codex/project-continuity https://github.com/BlueboxC/beads.git
cd beads
make install-force
bd version
bd knowledge --help
bd code --help
```

The fork branch intentionally differs from `origin/main`; `install-force` is the
existing source-install target that skips only that branch-equality check.

Follow [build dependencies](https://github.com/BlueboxC/beads/blob/codex/project-continuity/docs/getting-started/installation.md#build-dependencies-contributors-only)
for your platform. Python indexing uses an operator-owned Python 3 interpreter;
JS/TS uses operator-owned Node.js and bundled TypeScript 5.9.3; Java/C#/Rust/C++
reuse Node with bundled MIT Tree-sitter WASM parsers. The additional PHP/C, scripts, web, GraphQL, mobile, SQL and configuration grammars share that fixed runtime. XML uses the Go standard XML decoder without DTD loading or external entity resolution. Go/XML parsing needs
no project executable. The parsers do not execute project code, load project
packages or `tsconfig`, or install dependencies. Use `bd code --licenses` for
bundled parser notices. Keep a backup of an existing binary and project database
before upgrading; do not infer feature availability from the upstream version
number alone.

## Enroll a project once

Run these commands in the project you explicitly want to incorporate:

```bash
cd /path/to/project
bd init --skip-hooks
bd setup codex --check
bd knowledge scan DOX.md docs
bd code scan src tests --languages all
bd code status --json
```

Choose existing owned paths; the examples are placeholders. For an already
initialized workspace, omit `bd init`. A new monorepo module shares the existing
workspace only if it belongs under approved roots. A separate project needs its
own explicit initialization. Global `bd setup codex --global` installs reusable
agent integration, not project enrollment. Avoid installing both global and
plugin hook definitions for the same lifecycle.

`knowledge scan` with paths replaces its saved document selection; supply the
complete desired roots. `code scan` saves code roots/languages/exclusions. `all` selects the 23 languages/formats in [code-index coverage](/core-concepts/code-index); omitted languages reuse the saved
selection, with Python as the first-index default.
Directories discover new children when maintained; an exact file selection
tracks only that file. Inspect selection/status before changing either.

## Work with durable knowledge

```bash
bd knowledge list --json
bd knowledge context <module-or-issue>
bd knowledge sources DOX.md src/context.py --json
bd knowledge proposals --json
```

Read existing solutions, current sources and every applicable DOX before
proposing new work or recording a fix. Source hashes establish currency, not
that tests ran. A record's scope distinguishes recorded, inspected, tested,
installed and productive evidence. Source changes become `needs_review`;
index/catalog refresh does not renew that evidence.

For a completed problem/module, the supervising agent can explicitly record
and review a justified learning without asking the owner for routine approval.
That review must still inspect the evidence, preserve previous solutions and
leave unverified candidates pending. Keep this standing rule in user-managed
AGENTS guidance outside setup-generated blocks if desired. It is an agent
instruction, not autonomous acceptance by a hook.

## Prepare and review a session learning

First enable observed activity for this project and trust the changed handlers
through the normal Codex `/hooks` flow:

```bash
bd activity enable
bd activity list --session <session-id> --turn <turn-id> --json
```

Use an observed session/turn or specific event IDs from this workspace:

```bash
bd knowledge prepare DOX.md src/context.py   --session <session-id> --turn <turn-id> --json
# Alternatively: --activity-event <event-id> (repeat up to 16 times)
```

Preparation is read-only. The returned private packet contains current source
and DOX text, recorded hashes, existing topics/context, activity origins and an
editable `draft`. Read omitted tails when reported. Codex fills only
`draft.record`, retaining recorded scope, prepared hashes, workspace identity
and exact origins. Store packets/drafts outside source control. Submit only the
filled draft via stdin:

```bash
bd knowledge propose --file - --json < /path/to/private/draft.json
bd knowledge proposals --json
bd knowledge review <proposal-id> --decision accept --reviewer Codex   --reason 'Compared current sources with the existing solution'   --scope tested --evidence 'Describe the actual commands, results and limits'
```

The acceptance example is appropriate only after those tests actually ran.
`review` records a review; it does not run tests. The reviewer label is declared
provenance, not authentication. Rejection uses `--decision reject` with reviewer
and reason. Missing/changed sources or origins, altered workspace identity and
concurrent conflicts withhold promotion. Established topics require explicit
review/revision rather than replacement by a new draft. See
[knowledge commands](https://github.com/BlueboxC/beads/blob/codex/project-continuity/docs/core-concepts/knowledge.md) for the full record/proposal contract.

## Explore the native graph

```bash
bd graph --project --html > /path/to/private/project-graph.html
bd serve --graph-viewer --addr 127.0.0.1:18740
```

Open `http://127.0.0.1:18740/viewer`. The same viewer presents task states,
knowledge/proposals, directories, files, symbols, evidence and relationships.
Source bodies are not embedded in graph exports. Families stay separated;
dragging their header/principal node moves members together, while Shift drags
one node. Fit View changes framing; Reset View restores initial positions and
clears selection/filters. The narrow legend can collapse. Directory yellow and
module green are distinct. Filters, relationship buttons and Back support
source/symbol inspection.

The live page starts in Automatic mode unless a previous Manual choice was
saved. Automatic queries every five seconds while visible; hidden tabs pause.
Actualizar grafo works in either mode. Manual waits for the button or explicit
authentication. Refresh preserves positions, zoom, filters, selection and the
collapsed legend. Errors visibly retain the previous unverified snapshot.
Offline HTML never connects to the database and needs regeneration.

The Historial tab reads the same bounded journal and supports session/turn/type/
text filtering. Preparing a learning copies a request for Codex; downloading a
packet retains a private export. Neither action invokes a model or accepts a
proposal. The loopback viewer is read-only, does not start on boot, and does not
update indexes. See [HTTP operation](https://github.com/BlueboxC/beads/blob/codex/project-continuity/engdocs/SERVE_RUNBOOK.md) for optional
authentication and transport bounds.

## Maintain derived selections

```bash
bd maintain once --json
bd maintain watch --interval 30s
```

Stop the foreground watcher with Ctrl-C. It reads the latest saved selections,
reuses unchanged ASTs, and updates derived code/catalog generations in embedded
Dolt. Unchanged passes do not parse or write. The driver closes between bounded
passes. A changed pass makes a local derived commit; publication failures retain
the last complete snapshot for each projection. Code and catalog publication
are separate, so the next pass may need to converge them.

The watcher does not change tasks, blockers, human memories or accepted
assertions, and does not run a project runtime, test suite, model or remote sync.
It is independent of viewer polling and prime/hooks. See
[code index](https://github.com/BlueboxC/beads/blob/codex/project-continuity/docs/core-concepts/code-index.md#automatic-maintenance) for bounds and failure handling.

## Preserve privacy and evidence

Session capture requires opt-in settings and delivered supported PostToolUse,
Stop and SessionEnd hooks. It stores bounded observed metadata, payload hashes
and redacted reported handoffs; raw commands, tool responses, prompts and
transcripts are not retained. Coverage is partial and redaction is best effort.
Disabling capture preserves existing events. Reported text remains untrusted
source material, not proof that a fix works.

Keep `.beads` runtime stores, packets, exports, backups and machine configuration
private. Publishing source does not publish Dolt data. Cross-machine data sync
requires an explicit `bd dolt push`/remote configuration and a separate decision
about which project material may leave the machine.

Follow [the testing authority](https://github.com/BlueboxC/beads/blob/codex/project-continuity/engdocs/TESTING.md) to verify a change.
Static calls, current hashes and successful local tests do not by themselves
qualify installation, native compact behavior or production. Old binary rollback
must respect derived manifest versions and expanded limits; retain the prior
derived manifest as well as the binary/database backup.


## Review a change and manage derived retention

```bash
bd code impact src/context.py --depth 8 --limit 200 --json
bd code relink src/old.py src/new.py --json
bd code prune --readonly --json
```

Impact returns conservative file dependencies, witness paths, recorded learnings
and candidate tests. It does not run those tests or establish runtime coverage.
Relink emits review drafts only for a unique identical-content move with current
destination symbols and contracts; inspect and explicitly review the draft.
Earlier verification remains historical.

Prune defaults to a plan. Explicit `bd code prune --apply --json` needs upgraded
direct embedded writers and a backup. All writes/deletes are guarded in the
existing storage transaction; other adapters refuse application. Human knowledge
and Dolt history are preserved. Retention reduces current rows, not necessarily
disk use; restore a complete historical generation or rebuild after pruning.
See [code-index boundaries](/core-concepts/code-index#derived-blob-retention).

Context-hook diagnostics retain bounded private metadata beside the refresh
marker. A failed cold stdout delivery keeps the next-prompt fallback; successful
local delivery alone does not establish native context admission. See
[hook diagnostics](/integrations/codex#local-hook-diagnostics).

## Corrections and upgrade boundaries

The fork branch includes the reviewed environment-selection, migration,
terminal rendering, event-stream authorization, MCP argument and dependency
corrections described in [the fork scope](https://github.com/BlueboxC/beads/blob/codex/project-continuity/FORK.md#included-corrections).
Upstream review is independent of their inclusion here.

Present operator environment values, including empty selectors, override project
`.env` during startup and full loading; absent selectors retain their routing.
A corrupt authoritative metadata file is rejected rather than replaced. Restore
only an explicitly selected, known-valid project backup; do not reinitialize to
bypass a refusal. Keep source/binary provenance and private backups before an
upgrade. Passing local tests does not qualify every native compact cycle or a
production runtime, and source publication never publishes project Dolt data.
