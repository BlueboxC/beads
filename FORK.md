# BlueboxC Beads continuity fork

This fork extends [gastownhall/beads](https://github.com/gastownhall/beads) to
preserve project direction, existing modules, decisions and resolved problems
across coding sessions. It builds on upstream **v1.3.1** at
`c1c4b642ac1c08d8c828007a1c2f96e47e43ef7c` and the published bootstrap
`6fd8ea6288a1c9c5fa23dcb22a47183e779ca59c`.

The extensions share Beads' existing **Dolt memory plane**. No second graph
service, vector database, schema migration or hosted model is introduced.
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

This public branch starts from already-published history. Operator-only
vulnerability patches are not included. Project databases, learned project
records, chat/session exports, machine configuration, credentials, backups and
installation evidence remain outside this repository. Source publication does
not authorize publishing `refs/dolt/data`.

Initialize each chosen project explicitly; global setup does not enroll new
projects. Activity capture, derived maintenance and the live viewer have
separate opt-in steps. Use the normal Codex hook trust flow; setup never grants
trust. Neither indexing, source currency nor an accepted learning authorizes
project execution, deployment or reopening completed work.

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
