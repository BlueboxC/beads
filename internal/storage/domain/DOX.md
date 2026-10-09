# DOX.md — Storage domain model

## Purpose

Own storage domain model within the fork.

## Ownership

Domain values and storage-facing semantics.

## Local Contracts

Root DOX.md and the parent chain remain binding.

- Domain/db mutation repositories use the shared human-gate policy before protected writes, including bulk edge deletions and expanded delete sets. UOW and direct storage must agree; no CLI-only approval guard.

## Work Guidance

Keep semantic contracts explicit and shared across backends. Empty-parent reparent requests for dotted IDs are refused before writing; preserve legacy implicit ancestry and explicit nonempty reparenting.

ConfigUseCase/GetConfigByPrefix selects literal, case-sensitive stored-key prefixes inside the caller transaction, with optional exclusion. Empty inclusion selects all config; empty exclusion removes no keys. SQL is owned by domain/db. Failed queries, scans or iteration return an error without partial config.

## Verification

Use the affected existing checks selected in engdocs/TESTING.md; verify indexed child contracts.

## Child DOX Index
