# DOX.md — Beads CLI

## Purpose

Own beads cli within the fork.

## Ownership

Commands, flags, help, output and CLI fixtures.

## Local Contracts

Root DOX.md and the parent chain remain binding.

- Project `.env` loads only explicit selector and passive Dolt connection keys; executable settings come from the operator environment. Existing values, including empty values, win in both early selection and full loading. Absent selectors retain dotenv routing.
- Legacy embedded database migration validates identifiers and real child directories before any rename or metadata write.
- Malformed metadata.json blocks backend-selecting diagnostics, ordinary init and data commands. Preserve the selector and restore a known-valid project backup before retrying; never infer a fallback database from a parse failure. Store-free version remains available.
- Sanitize stored issue text before terminal measurement, styling or truncation. The default DAG also displays title newlines/tabs as spaces within one row; preserve original values in storage, JSON and exports.
- `knowledge` uses the existing memory role for document provenance and explicit assertions. Prime projects bounded knowledge alongside plain memories; the four context hooks remain read-only and use the same recovery path. Workspace-relative sources follow the selected Beads root, including nested invocations. Task, knowledge and code HTML graphs use the shared native viewer with typed colors, drag/zoom and readable node evidence. `code scan` explicitly updates derived Python/Go/JS/TS AST through the same memory role; query/status detect staleness. Prime/hooks only project a bounded snapshot summary and do not run parsers or refresh code. Knowledge symbol links require a current named symbol and bind its file/DOX.

- `graph --project` composes a read-only overview of local tasks (all states), explicit knowledge and all indexed code directories. It preserves file/symbol locations, hashes, validity and typed aggregate syntax counts in the same offline snapshot. It never follows foreign workspace routes, scans code, renews assertions or infers task blockers. Direct workspace connection only; `--max-rows` applies to the complete task selection.

- `serve --graph-viewer` selects only the discovered local workspace and exposes the native live overview at `/viewer`. Skip persistent store initialization; each bounded strict-readonly `graph --project --json` query uses this executable, a pinned BEADS_DIR/database with empty path selectors that block dotenv rerouting and no shell, then closes its store. Reject --db, --database and --global; do not run a parser, renew assertions, execute project hooks or publish write APIs. Embedded Dolt is supported in this read-only mode while the normal API refusal remains. Stop with the normal server signals; no automatic startup installation.

- `maintain once|watch` is an explicitly started embedded-workspace extension. Its scoped factory open uses the existing driver and memory role, releases Dolt after each bounded child pass and performs one local derived commit only on changes; skip root import/template/hook/backup/export/push housekeeping. Refuse server/shared/proxied mode and database/global overrides. Reuse the latest saved roots/languages/exclusions; only directory roots discover new files. Missing roots remain selected for later branch/file return. Prepare all derivations before publication; derivation failures retain both previous projections, while each publication retains its last complete snapshot independently; back off to five minutes and stop on normal signals. Never write human assertions/tasks or start from prime/hooks/viewer/boot.

- Agent lifecycle hook subprocesses require a successful memory-plane read before emitting context. Ordinary prime retains advisory diagnostics; hook reads fail without emitting a diagnostic as context. Healthy empty planes, custom workflows and quoted diagnostic text remain valid. Codex consumes a pending session/workspace refresh only after writing nonempty complete context successfully; SessionStart satisfies it too. Failed reads or delivery retain the next-prompt refresh; a failed cold SessionStart queues it too. No storage-specific retry or lockfile repair.

- The four Codex context hooks record their latest local attempt per event/session/workspace plus bounded completed compact/failure attempts in the existing private marker cache. Start/completion, categorical prime/output results, duration, sanitized source/trigger/turn UUID, directory-match flag and context byte count/hash support diagnosis without retaining raw text. Atomic advisory cache writes never change hook output, pending-refresh behavior or the read-only memory plane. Local stdout success is not native context admission; missing files are not proof of omitted dispatch.

- Large document catalogs publish bounded content-addressed parts before a single manifest. Implicit scan and maintenance refuse a corrupt/incomplete saved catalog rather than defaulting to a new selection; explicit scan roots can replace it. Human evidence remains unchanged.

- `knowledge sources` reads bounded provenance without storing it. `knowledge propose` requires inspected hashes including DOX; `proposals` reports lifecycle/validity/history. `review` is the explicit accept/reject write with reviewer, reason and acceptance scope/evidence; it never rebinds or replaces existing assertions. All use the existing direct/proxied memory role; mutators enforce readonly. Native project/knowledge graphs include proposals and provenance/review edges separately from blockers.

- `knowledge prepare <sources> --session/--activity-event` returns an ephemeral readonly packet: latest 16 selected events with visible omissions, explicit current source/DOX text (64 KiB total, 16 KiB per file, tails reported), existing topic IDs and bounded recovery context. Its editable draft pins the workspace and exact origins/hashes; propose revalidates them and refuses a new draft for an established topic. Codex supplies semantic content, candidates stay pending and hooks/HTTP routes never submit them.
- `knowledge propose --activity-event <id>` explicitly pins up to 16 events from the selected workspace, preserving immutable IDs and observed session/turn provenance. Missing, corrupt or changed origins withhold new acceptance without replacing prior accepted assertions; events alone do not supply source/DOX hashes or verified scope.
- `graph --project` includes a bounded activity snapshot for the native Historial tab through the existing readonly query; no second database, transcript import, model or write API.
- `activity` keeps opt-in observed Codex operations and reported turn/session endings under @activity/ in the existing embedded memory plane. `codex-activity` handles PostToolUse, Stop and SessionEnd independently of the four context hooks. No initialization, raw tool payload/transcript storage, task changes, assertion promotion, remote sync or project execution. Configuration writes honor readonly; capture becomes a no-op under readonly or disabled/missing settings. Bounded recovery quotes up to three reported handoffs as untrusted data.

- code impact is readonly and reports conservative inverse file dependencies, witness paths, candidate tests and established learning/issue references. code relink emits review drafts without updating assertions. code prune defaults to a readonly plan; --apply enforces readonly guards, embedded mode and the optional atomic memory capability. Never enable cleanup from hooks/watchers; retain full Dolt history and refuse mixed older writers.
- SessionStart queues recovery before writing context; failure to write stdout retains next-prompt fallback, while marker-cache failure cannot prevent healthy context delivery. Diagnostics retain the latest event plus at most sixteen completed compact/failure attempts per session/workspace, containing metadata only; native admission remains a separate claim.

## Work Guidance

Keep existing CLI primitives and machine-readable output stable. Follow AGENTS.md and ../../engdocs/TESTING.md.

Memory diagnostics expose literal key-prefix inclusion/exclusion without changing default enumeration. Knowledge reads exclude derived code namespaces; symbol-link validation reads only selected indexed files and applicable provenance, retaining the proposal/review guards.

## Verification

Use the affected existing checks selected in engdocs/TESTING.md; verify indexed child contracts.

## Child DOX Index

- [setup/DOX.md](setup/DOX.md) — Agent setup.
- [doctor/DOX.md](doctor/DOX.md) — Diagnostics.
- [protocol/DOX.md](protocol/DOX.md) — CLI protocol.
