# DOX.md — Graph Preview storage

## Purpose

Own opt-in Graph Preview persistence over existing Dolt engines.

## Ownership

Graph Preview storage.

## Local Contracts

Root and parent DOX contracts remain binding.

- Init creates a fresh database only. OpenExisting validates exact binding and format without DDL or migrations.
- Native Issue writes retain their existing policy. The graph adapter records once after final mutation, maps the retained native snapshot and owned Links atomically, and preserves no-op/rollback behavior.
- Native history prerequisites are fresh-graph-only DDL, outside ordinary migration numbering. Ordinary writers remain unchanged.
- ContinuityMemories reuses the keyed memory role: human records/proposals/reviews have canonical retained Memory bodies with exact forkContinuityKey metadata and key-to-path pointers, never duplicate full config bodies. Catalog/code/activity rows remain derived or observed data in the same config plane. Guarded batches, pointer publication, retained snapshots and writer coordination share one transaction. Preserve incident-Link deletion refusal, permanent tombstones, UTF-8 and complete-read budgets; corruption is an error. Native Memory deletion removes its managed pointer; native metadata edits cannot rebind that key. Equal values do not mint graph history.
- ReadContinuity composes current graph records and continuity projections in one read transaction. Existing workspace conversion, public restoration and independent clone merge remain unsupported.
- Serialize graph writes through coordination; opaque graph tokens are distinct from local history ordinals. Preserve bounded reads, canonical bytes and permanent identity/tombstones.

## Work Guidance

Support direct embedded and ordinary SQL-server modes. Refuse unsupported proxy/adoption/recovery rather than falling back to ordinary storage. Independent graph-clone merge is unqualified.

## Verification

Run real embedded lifecycle, history, rollback, guard and reopen tests. Set BEADS_GRAPH_TEST_SERVER_PORT for isolated ordinary-server concurrency qualification.

## Child DOX Index
