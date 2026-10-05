# DOX.md — Agent setup

## Purpose

Own agent setup within the fork.

## Ownership

Client configuration, skills, hooks and managed instruction sections.

## Local Contracts

Root DOX.md and the parent chain remain binding.

## Work Guidance

Preserve unrelated user configuration and make repeat setup idempotent. Global and project paths must remain distinct.

Install four read-only context handlers and three separate opt-in activity handlers. Preserve existing definitions and unrelated handlers; new/changed definitions require normal Codex trust. Project activity enablement never grants hook trust.

SessionStart matches startup, resume, clear and compact. Native manual/automatic compaction delivers context before the next model request; successful delivery consumes the pending refresh, retaining next-prompt recovery only as a fallback.

## Verification

Use the affected existing checks selected in engdocs/TESTING.md; verify indexed child contracts.

## Child DOX Index
