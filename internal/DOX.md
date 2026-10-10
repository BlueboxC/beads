# DOX.md — Internal implementation

## Purpose

Own internal implementation within the fork.

## Ownership

Private packages, templates, services and adapters. Storage has its own contract.

## Local Contracts

Root DOX.md and the parent chain remain binding.

- Dolt server process probes distinguish failed inspection from a confirmed non-Dolt process. Preserve a live pid/port when `ps` fails; EPERM from a liveness probe means alive. Stop refuses to signal an unverifiable or recycled PID and retains state for diagnosis.
- On macOS, a newly launched managed Dolt server is ready only after a greeting and bounded `lsof`/`ps` proof that every listed port holder is the child or its descendant. Unknown ownership waits until the configured readiness deadline and fails without publishing pid/port; a proven foreign holder retries only an ephemeral port. Failed startup kills/reaps only its launched child/group. Other platforms retain greeting-based readiness. Existing-server adoption and Stop inspection contracts remain separate.


- Jira pull preserves supported ADF block/inline structure as Markdown through the shared description converter; v2 strings and raw fallbacks remain supported. Rendering is bounded to 1 MiB input, 2 MiB output, depth 64 and 50,000 nodes/marks; exceeding a bound retains the original JSON. Unknown containers retain descendant text. Push still emits plain paragraph ADF and is not a rich-text round trip.

- HTTP event streams revalidate bearer credentials before delivery and around journal reads, including idle and backlog passes. Successful removal terminates the stream; failed token-file reloads retain the last-good token set.
- Terminal Markdown rendering removes raw controls and numeric entities that decode into controls before Glamour parsing; renderer-generated styles and printable entities remain supported.

- Telemetry remains opt-in. Its default resource collects host, process identity and runtime metadata without argv or process owner; explicit environment attributes still override/extend defaults. Command spans retain credential-scrubbed arguments in local console traces only.

- Recipe SharedPaths explicitly declare section-managed instruction files; other paths retain their existing whole-file ownership semantics.

## Work Guidance

Tracker adapters share configuration resolution: YAML-only secrets never query Dolt, while ordinary values keep storage-first/environment fallback including store errors.

Keep orchestration policy outside the issue-tracking core; follow ../engdocs/PROJECT_CHARTER.md.

Use line-local gosec annotations for intentional native directory handles or syscall buffer pointers only after checking descriptor identity or the bounded synchronous buffer contract; do not disable rule categories globally.

## Verification

Use the affected existing checks selected in engdocs/TESTING.md; verify indexed child contracts.

## Child DOX Index

- [storage/DOX.md](storage/DOX.md) — Storage boundary.
- [graphview/DOX.md](graphview/DOX.md) — Shared native offline/live graph viewer.
- [httpapi/DOX.md](httpapi/DOX.md) — HTTP transport and exclusive read-only viewer mode.
- [knowledge/DOX.md](knowledge/DOX.md) — Source-backed project knowledge and offline graph.
- [codeindex/DOX.md](codeindex/DOX.md) — Derived multilingual symbols, static references and index lifecycle.

- [activity/DOX.md](activity/DOX.md) — Bounded observed session/turn journal, distinct from reviewed knowledge.
