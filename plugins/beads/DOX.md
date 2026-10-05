# DOX.md — Beads plugin package

## Purpose

Own beads plugin package within the fork.

## Ownership

Codex and Claude plugin manifests, hooks, commands and skill resources.

## Local Contracts

Root DOX.md and the parent chain remain binding.

## Work Guidance

Keep packaged hooks and instructions consistent with the corresponding CLI version. Keep read-only context handlers separate from opt-in observed activity handlers; installation never grants hook trust.

Packaged SessionStart must include source=compact alongside startup/resume/clear, matching generated setup so automatic compact continuations receive context immediately.

## Verification

Setup tests compare packaged Codex hooks with the generated fallback and verify idempotent preservation/removal.

## Child DOX Index
