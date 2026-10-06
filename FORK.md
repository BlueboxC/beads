# BlueboxC Beads continuity fork

This fork extends [gastownhall/beads](https://github.com/gastownhall/beads) to
preserve project direction, existing modules, decisions and resolved problems
across coding sessions. It builds on upstream **v1.3.1** at
`c1c4b642ac1c08d8c828007a1c2f96e47e43ef7c` and the published bootstrap
`6fd8ea6288a1c9c5fa23dcb22a47183e779ca59c`.

The continuity extensions share Beads' existing **Dolt memory plane**, without
another graph service, vector database or hosted model. Native correctness
repairs include forward blocked-state migrations 0067 and ignored/0027; shipped
predecessors and the Dolt dependency pin remain unchanged.
Upstream attribution and the MIT license remain intact. Fork additions are
available on `codex/project-continuity`; upstream installation channels do not
contain them. See [installation and workflow](docs/core-concepts/fork-continuity.md).

## Added capabilities

| Area | Added behavior | Boundary |
| --- | --- | --- |
| Documents and DOX | Selected document catalogs, source hashes and explicit objectives, constraints, decisions, modules and solutions | A scanned document is source material; scanning does not extract or certify a learning. |
| Supervised learning | Source-backed pending proposals, explicit accept/reject reviews, immutable history and pinned activity origins | A supervising human or agent reviews evidence. Existing solutions and historical verification scopes are preserved. |
| Code structure | Incremental Python, Go, JavaScript and TypeScript AST index, imports/calls, symbols, source locations and reviewed learning links | Static references are not runtime proof. Dynamic or ambiguous references remain unresolved. |
| Change review | Conservative inverse static file dependencies, shortest witness paths, associated learnings and candidate tests; unique identical-content move drafts | Dynamic impact and edited/ambiguous moves require manual analysis; a draft never renews prior evidence. |
| Derived retention | Read-only prune plan and explicit guarded atomic cleanup in upgraded direct embedded workspaces | Human records and Dolt history remain intact; cleanup is never automatic and does not promise filesystem savings. |
| Native graphs | One interactive task/knowledge/code explorer with typed colors, grouped drag, zoom, filters, source/symbol details, back navigation and separate Fit/Reset | Families are presentation scopes, not ownership or task blockers. Offline exports are snapshots. |
| Live viewer | Read-only graph/history queries, Actualizar grafo, Manual/Automático selection, visible disconnects and preserved layout | Refresh reads saved data; it does not parse code or renew evidence. |
| Derived maintenance | Explicit `maintain once` and `maintain watch` over saved document/code selections | Only approved directory roots discover new files. Human records and task state are not rewritten. |
| Session continuity | Opt-in observed operations, reported handoffs and session markers; bounded prime recovery and compact refresh/fallback | Delivered supported hooks determine coverage. Hooks do not read transcripts or accept learned solutions. |
| Hook diagnostics | Cold-output next-prompt fallback and bounded private compact/failure metadata | Local stdout success does not prove native context admission or universal reliability. |
| Bounded reads | Literal memory-key prefix selection across server, embedded and UOW adapters; selected-symbol provenance | Reads avoid unrelated stored values while retaining the existing case-insensitive text search. |

Detailed references: [knowledge](docs/core-concepts/knowledge.md),
[code index and maintenance](docs/core-concepts/code-index.md),
[Codex hooks and activity](docs/integrations/codex.md), and
[HTTP operation](engdocs/SERVE_RUNBOOK.md).

## Public source and private operation

This branch includes the verified fork corrections independently of upstream
review. Upstream pull requests remain subject to separate owner authorization. Project databases, learned project
records, chat/session exports, machine configuration, credentials, backups and
installation evidence remain outside this repository. Source publication does
not authorize publishing `refs/dolt/data`.

Initialize each chosen project explicitly; global setup does not enroll new
projects. Activity capture, derived maintenance and the live viewer have
separate opt-in steps. Use the normal Codex hook trust flow; setup never grants
trust. Neither indexing, source currency nor an accepted learning authorizes
project execution, deployment or reopening completed work.

## Included corrections

The following native fixes adapt upstream contributors' proposed patches while
preserving this fork's existing corrections:

| Report | Included behavior | Source proposal |
| --- | --- | --- |
| [#7289](https://github.com/gastownhall/beads/issues/7289) | Retried single/batch creates regenerate IDs from the new snapshot and preserve concurrent writers. Explicit import IDs retain upsert semantics. | Fork fix; reporter's collision probe extended to tasks, wisps and batches |
| [#7037](https://github.com/gastownhall/beads/issues/7037) | Forward migrations 0067 and ignored/0027 repair false blockers using separate target joins, without modifying shipped migration bytes or user timestamps. | Fork repair following the reporter's OR-free query design |
| [#5963](https://github.com/gastownhall/beads/issues/5963) | Recall/Forget and CLI/HTTP recognize imported empty memory rows as present. New empty content remains refused. Selected-prefix reads and atomic batches remain supported. | [#5964](https://github.com/gastownhall/beads/pull/5964), adapted to preserve bounded UOW selection |
| [#7091](https://github.com/gastownhall/beads/issues/7091) | Empty-parent updates for dotted IDs are refused atomically rather than claiming a detach that legacy readers undo. Explicit nonempty reparenting and nondotted detachment remain supported. | Fork compatibility guard; full dotted-ID detachment is not introduced |
| [#7300](https://github.com/gastownhall/beads/issues/7300) | Ready readers and counts hide children of an indefinitely deferred parent. `IncludeDeferred` and unrelated relationships retain their behavior. | [#7304](https://github.com/gastownhall/beads/pull/7304), adapted to the baseline storage API |
| [#7275](https://github.com/gastownhall/beads/issues/7275) | Long or dotted lowercase read-shaped memory keys cannot overwrite a neighboring memory. Explicit keyed writes keep their semantics. | [#7278](https://github.com/gastownhall/beads/pull/7278) |
| [#7214](https://github.com/gastownhall/beads/issues/7214) | Failed process inspection preserves a live Dolt pid/port; EPERM means alive; stop does not signal an unverifiable or recycled PID. | [#7229](https://github.com/gastownhall/beads/pull/7229) |
| [#7098](https://github.com/gastownhall/beads/issues/7098), [#5972](https://github.com/gastownhall/beads/issues/5972) | Local restore rejects empty/non-backup directories before calling Dolt. Nonempty regular manifest required; full content integrity and atomic restore remain outside this preflight. | [#7192](https://github.com/gastownhall/beads/pull/7192) |
| [#6873](https://github.com/gastownhall/beads/issues/6873) | Python MCP forwards label replacement and explicit clearing without changing omitted-label behavior. | [#6877](https://github.com/gastownhall/beads/pull/6877) |
| [#7095](https://github.com/gastownhall/beads/issues/7095) | Codex hooks prime the payload workspace and scope Git authority there, including protection against selectors inherited from caller startup. | [#7119](https://github.com/gastownhall/beads/pull/7119), extended after its blocking review |

These adaptations do not merge, approve or reopen the upstream pull requests.

- Project `.env` imports only documented selector/passive connection keys. Operator
  values, including explicitly empty selectors, win during early and full loading.
  Executable overrides and credential commands remain operator-controlled.
- Database migration validates identifiers and real child directories before
  changing paths. Corrupt authoritative metadata is refused without replacement;
  recovery requires an explicit known-valid project backup.
- Terminal issue rendering removes controls before styling; default DAG titles
  stay on one row. Storage, JSON and exports retain original values.
- Established event streams recheck authorization before delivery and around
  journal reads. Successful revocation closes the stream; failed file reloads
  retain the last-good token set within the documented deadline limits.
- Python MCP comments/notes preserve literal positional text and configured actor
  identity. Option-shaped text cannot become another CLI option.
- Root/example dependencies retain the reviewed gRPC and cryptography minima;
  Python package requirements retain PyJWT >=2.15.0 and dev urllib3 >=2.8.0.

The verified local source cut passed the canonical Go baseline (102 packages),
targeted regressions, native/Windows cross-lint, documentation checks and real
Python-client round trips. Embedded Dolt has separate complete batch inventories.
Focused local-server tests reproduce concurrent data loss and migration false
blockers and verify their repairs; embedded and UOW tests verify memory presence
and parent-update parity. These results do not qualify all server/Docker paths,
native Windows, production or every
native compact cycle, and do not claim all advisory inventory warnings vanished.
Private reproduction reports, installation evidence and backups stay local.

## Remaining limits

- Autonomous semantic extraction, embedding/vector retrieval and whole-language
  type analysis are not implemented. Codex supplies and reviews semantic content.
- Journal capture is partial and redaction is best effort. Reported summaries and
  observed exit codes do not prove a repair or successful deployment.
- The live viewer and maintenance require direct embedded workspaces; they do
  not aggregate unrelated projects or provide a writable web interface.
- Unreferenced derived blobs can be pruned explicitly on upgraded direct embedded
  writers. Dolt history is retained; live payload savings do not establish disk savings.
  Server/proxied prune application and mixed older writers are not qualified.
- Compact dispatch, cold-output fallback and bounded diagnostics have handler/process
  coverage. Native context-limit-triggered midturn recovery must be qualified for
  each deployed source/binary cut; historical positive cycles do not establish
  universal reliability or explain previously missing deliveries.

## Development

Keep `main` available for upstream synchronization and scope fork work to
`codex/` branches. Use Beads for durable development tasks, the existing storage
interfaces for persistence, and [engdocs/TESTING.md](engdocs/TESTING.md) for
verification. Follow the DOX hierarchy before edits. Preserve the bundled D3
and TypeScript licenses/notices; `bd code --licenses` exposes parser attribution.
