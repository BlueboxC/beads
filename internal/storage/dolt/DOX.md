# DOX.md — Server-backed Dolt adapter

## Purpose

Own server-backed dolt adapter within the fork.

## Ownership

SQL-backed Dolt storage and backend-specific operations.

## Local Contracts

Root DOX.md and the parent chain remain binding.

## Work Guidance

Use shared storage contracts; verify actual durability and transaction behavior at the real backend boundary.

## Verification

The shared optional AtomicMemories case is explicitly wired and skips with a stated reason when the accessor does not advertise it; no atomic-batch capability is inferred for this adapter.

Use the affected existing checks selected in engdocs/TESTING.md; verify indexed child contracts.

## Child DOX Index
