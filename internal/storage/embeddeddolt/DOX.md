# DOX.md — Embedded Dolt adapter

## Purpose

Own embedded dolt adapter within the fork.

## Ownership

In-process Dolt storage and its lifecycle.

## Local Contracts

Root DOX.md and the parent chain remain binding.

- Embedded open permission failures retain their typed cause as OpenPermissionError. Logical readonly forbids Beads mutations but the driver still needs filesystem write access. Do not change engine retries/locks, relax permissions or classify errors by matching text.

## Work Guidance

Respect embedded writer and transaction constraints; use disposable stores for tests. Memories.Apply validates keys before opening a transaction, checks every expected value before mutation and atomically writes/deletes through the existing SQL helpers. Equal values skip writes; return counts only after successful commit. No engine-specific lock, schema change or retry layer.

## Verification

Use the affected existing checks selected in engdocs/TESTING.md; verify indexed child contracts.

## Child DOX Index
