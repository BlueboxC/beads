# DOX.md — Server-backed Dolt adapter

## Purpose

Own server-backed dolt adapter within the fork.

## Ownership

SQL-backed Dolt storage and backend-specific operations.

## Local Contracts

Root DOX.md and the parent chain remain binding.

- Retried single and batch creates restore caller-requested IDs before every transaction attempt. Generated IDs are checked against the new snapshot; explicit import IDs retain upsert semantics. All-wisp batches follow the same rule without Dolt history.

- `ApplySchemaMigrations` includes migrations performed by this store's writable open exactly once, so explicit shared-server migration reports the applied count and reaches CLI version reconciliation. Read-only/gateway opens and consent gates keep their existing behavior.

## Work Guidance

Metadata field conversion delegates to shared issueops parsing; YAML numeric types, enum fallback and adapter-specific warning/error modes remain unchanged.

Use shared storage contracts; verify actual durability and transaction behavior at the real backend boundary.

## Verification

The shared optional AtomicMemories case is explicitly wired and skips with a stated reason when the accessor does not advertise it; no atomic-batch capability is inferred for this adapter.

Use the affected existing checks selected in engdocs/TESTING.md; verify indexed child contracts.

## Child DOX Index
