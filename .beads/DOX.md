# DOX.md — Project tracking assets

## Purpose

Own project tracking assets within the fork.

## Ownership

Tracked workflow formulas and local Beads project state.

## Local Contracts

Root DOX.md and the parent chain remain binding.

- Sign upstream issue follow-up comments only with the GitHub profile name (BlueboxC).
- Automatic Codex activity capture is explicitly enabled for this existing project through `bd activity enable`. The three separate PostToolUse/Stop/SessionEnd handlers require normal native hook trust; enabling the setting does not trust them. Other projects remain opt-in. Journal entries stay in @activity/ in the same local Dolt and preserve session/turn identities, bounded observed tool metadata and reported handoffs. No raw tool payloads, user prompts or transcript contents are stored; no historical chats are imported.
- Consult `bd activity list --session <id> --turn <id>` alongside `bd prime`, current DOX and established evidence before resuming. Reported handoffs and exit-code observations never authorize runtime, close tasks, accept proposals or renew solutions. Missing/unsupported/interrupted events may be absent. `bd activity disable` stops future capture and retains history; it does not toggle the four read-only context hooks.

## Work Guidance

Keep runtime databases, operator state and backups out of Git. Use bd for project tasks and durable facts; publishing Dolt data needs an explicitly configured remote.

Initialize an operator clone of this fork with --role=maintainer so tasks stay in its own database. Keep contributor auto-routing disabled for this single-database setup.

## Verification

- Activity qualification uses two disposable embedded workspaces for retries, nested discovery, readonly/disabled no-op and project isolation. Installed settings and fresh reads do not prove real native hook execution; retain the native activation/runtime gate separately.



## Child DOX Index
