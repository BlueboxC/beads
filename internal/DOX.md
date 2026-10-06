# DOX.md — Internal implementation

## Purpose

Own internal implementation within the fork.

## Ownership

Private packages, templates, services and adapters. Storage has its own contract.

## Local Contracts

Root DOX.md and the parent chain remain binding.

- HTTP event streams revalidate bearer credentials before delivery and around journal reads, including idle and backlog passes. Successful removal terminates the stream; failed token-file reloads retain the last-good token set.
- Terminal Markdown rendering removes raw controls and numeric entities that decode into controls before Glamour parsing; renderer-generated styles and printable entities remain supported.

## Work Guidance

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
