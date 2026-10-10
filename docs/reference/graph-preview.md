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
activity capture, Codex recovery and interactive viewer. The same continuity commands also work inside the experimental format through
a storage adapter. Existing projects are preserved; conversion is still an
explicit future operation, not an initialization flag. See the [conversion design](https://github.com/BlueboxC/beads/blob/codex/project-continuity/engdocs/design/graph-preview-conversion.md) for preservation and qualification gates.

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
- Bounded generic graph traversal in JSON/text, plus the fork's interactive
  task/knowledge/code viewer through `graph --project --html` and
  `serve --graph-viewer`.
- Document/DOX catalogs, supervised proposals and reviews, 23-language code
  indexing and conservative impact queries, bounded prime/Codex recovery.
- Opt-in observed Codex activity and derived maintenance in embedded workspaces.
  Shared-server activity and maintenance remain refused.

Use the complete output of `bd status --graph --json` for implemented capabilities
and numerical limits. Unsupported flags/commands fail explicitly before ordinary
storage opens. On this v1.3.1 fork, `--if-revision` is graph-only; it does not add
upstream's newer decimal revision CLI to ordinary workspaces.

## Integrated continuity

After fresh graph initialization, use the existing fork workflows:

```sh
bd knowledge scan DOX.md docs --json
bd knowledge sources docs/solution.md --json
# Prepare a proposal with those exact inspected source/DOX hashes.
bd knowledge propose --file=proposal.json --json
bd knowledge review PROPOSAL_ID --decision accept --reviewer BlueboxC \
  --reason "Sources and verification inspected" --scope tested \
  --evidence "Name the actual passing verification" --json
bd code scan src --languages all --json
bd code impact src/module.go --json
bd prime --memories-only
bd activity enable --json
bd maintain once --json
bd serve --graph-viewer --addr 127.0.0.1:18740
```

Review claims must name actual evidence; this example does not qualify your
project. Codex hooks use the existing installed integration and explicit event
workspace; initialization does not add global hooks or trust. `activity enable`
requires the previously installed lifecycle hooks to receive events.

Human assertions, proposals and reviews are canonical Graph Preview Memories.
Their body stores the unchanged fork envelope, while `forkContinuityKey` metadata
and a config pointer connect the existing keyed API to that one body. Updates
retain prior versions, deletes retain tombstones/history, and failures roll back
pointer/payload/history together. Informational Links still refuse implicit
cascading deletion. Managed key metadata cannot be rebound by a native edit.

Derived code/document catalogs and observed session events remain existing
config rows in the **same Dolt database**, outside canonical Memory retention.
They are not assertions and do not consume one preview Resource per symbol.
The project viewer overlays provenance/code relations with canonical Beads and
Links from one transaction; overlay edges do not acquire scheduling authority.
Explicit native informational Links can connect canonical solution Memories to
Issues without turning code symbols into tasks. Accepted solutions show their
canonical record/version in node evidence. Plain native graph Memories also
participate in bounded recovery, as untrusted memory content.

The complete canonical inventory is limited to 1,000 live Resources and 16 MiB
of current acquisition bytes. Keyed continuity projections have a separate
65,536-row / 64 MiB acquisition budget, including keys and row overhead.
All graph writers check these limits before commit and roll back payloads,
pointers, revisions and coordination together on rejection. Lists check sizes
in SQL before transferring values; they never silently truncate a generation.
A workspace left oversized by an older or external writer may be reduced in
steps, with no dimension growing until all limits fit. Retained history stays
intact and older snapshots are not charged to current reads. These limits are
not process-RAM or disk-history ceilings and do not establish production scalability
or upstream public Type/Memory/History compatibility.

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
write routes, HTTP History or aliases. Embedded BDP Read serving is refused; the separate read-only interactive viewer
supports embedded workspaces.
Pagination cursors are process-local and expire; a restart invalidates them.
Non-loopback binds retain the ordinary explicit authorization requirements.

`serve` and `serve --graph-viewer` are exclusive modes in both formats. The
viewer releases embedded Dolt between bounded queries, supports manual/automatic
refresh, and does not run parsers or renew evidence.

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
