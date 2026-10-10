---
title: Graph Preview in the BlueboxC fork
description: Optional typed graph workspace with retained versions and read-only BDP access
---

# Graph Preview

Graph Preview adds typed Beads and Links, opaque revision guards, retained
snapshots and graph queries to this fork. It reuses the existing embedded/server
Dolt engines; there is no second database engine or service. Contributor source,
pins, licenses and fork adaptations are recorded in
[provenance](https://github.com/BlueboxC/beads/blob/codex/project-continuity/graphops/PROVENANCE.md).

This is an **experimental explicit workspace format**, not a migration of
ordinary Beads. Existing projects keep their tasks, knowledge, code index,
activity capture, Codex recovery and interactive viewer. Those continuity
commands are not admitted inside the experimental format. Keep ordinary Beads
for existing production project memory.

## Try a fresh workspace

Run from a new disposable project directory, never an existing `.beads`:

```sh
bd init --graph-mode link --scope-url https://example.invalid/demo/   --prefix gp --skip-hooks --skip-agents --non-interactive
bd status --graph --json
bd types --json
bd remember "Keep previous verified solutions" --id beads/policy --title "Continuity policy"
bd create "Verify the saved solution" --id beads/check
bd link beads/policy beads/check --link-type types/preview-related-v2 --id links/evidence
bd graph beads/policy --view generic --direction both --depth 2 --json
bd show beads/policy --json
bd versions beads/policy --json
```

`--scope-url` is a permanent canonical identity chosen by the operator; it is
not automatically registered on the network. Metadata also binds the workspace
real path, authority and format generation. Moving, copying, adopting or healing
a workspace is not silently supported. Fresh init refuses an existing database;
reads never run migrations. Preserve the workspace and known-compatible binary
before changing preview versions.

## Supported operations

- Memory creation, title/body search, recall, explicit-ID update and deletion of
  unreferenced Memories. Optional revision comparisons reject stale edits.
- Native Issue creation and supported scalar edits, notes append/guarded
  replacement/deliberate clear, claim/unclaim, close/reopen, defer/undefer and
  ready/blocked inspection. Native human-gate policy remains in force.
- Typed informational Links and blocking Dependencies, independent Link identity,
  incident queries, guarded unlink, common metadata and bounded ordered property
  patches. Informational Links do not imply scheduling dependencies.
- Current records, exact retained versions, comparison and local ordered version
  lists. Version tokens address snapshots; local ordinals do not.
- Bounded generic graph traversal in JSON/text. This differs from the ordinary
  fork's interactive task/knowledge/code viewer.

Use the complete output of `bd status --graph --json` for implemented capabilities
and numerical limits. Unsupported flags/commands fail explicitly before ordinary
storage opens. On this v1.3.1 fork, `--if-revision` is graph-only; it does not add
upstream's newer decimal revision CLI to ordinary workspaces.

## Guard an edit

Read the current `revision` with `bd show beads/policy --json`, then use that
opaque token:

```sh
bd remember "Keep verified fixes and their evidence" --update beads/policy   --if-revision OBSERVED_TOKEN
bd show beads/policy --version PREVIOUS_VERSION_TOKEN --json
bd versions beads/policy --json
```

A successful mutation retains the completed payload and owned Links atomically.
No-ops create no version; stale guards and failed writes leave prior data intact.
Stored actor claims are projected as `writer-supplied`, not authenticated identity.

## BDP Read

Graph Preview `bd serve` requires an explicitly configured **ordinary shared
Dolt SQL server**. It exposes bounded BDP Read records, Types and pagination through
the existing HTTP Host/auth/deadline/concurrency controls. It exposes no issue
write routes, HTTP History or aliases. Embedded Graph Preview serving is refused.
Pagination cursors are process-local and expire; a restart invalidates them.
Non-loopback binds retain the ordinary explicit authorization requirements.

Graph Preview `serve` and the ordinary `serve --graph-viewer` are exclusive modes.
The ordinary viewer continues showing ordinary project memory.

## Setup and limits

With the shown `--skip-hooks --skip-agents`, initialization installs no client
configuration. Without those flags, fresh graph setup writes managed project
guidance and a local Claude Stop reminder. `bd setup claude --check|--remove`
inspects/removes it. No global Codex hooks or persistent trust changes are made.
The reminder is not automatic semantic extraction or context recovery.

Full production Type/Memory/History contracts, external/federated Link endpoints,
proxy access, old workspace conversion, recovery and independent clone merging
remain unqualified. This fork provisions Issue history prerequisites only during
fresh graph initialization; ordinary migrations and their numbering remain intact.
