# DOX.md — Storage domain model

## Purpose

Own storage domain model within the fork.

## Ownership

Domain values and storage-facing semantics.

## Local Contracts

Root DOX.md and the parent chain remain binding.

## Work Guidance

Keep semantic contracts explicit and shared across backends.

ConfigUseCase/GetConfigByPrefix selects literal, case-sensitive stored-key prefixes inside the caller transaction, with optional exclusion. Empty inclusion selects all config; empty exclusion removes no keys. SQL is owned by domain/db. Failed queries, scans or iteration return an error without partial config.

## Verification

Use the affected existing checks selected in engdocs/TESTING.md; verify indexed child contracts.

## Child DOX Index
