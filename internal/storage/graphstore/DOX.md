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
- Serialize graph writes through coordination; opaque graph tokens are distinct from local history ordinals. Preserve bounded reads, canonical bytes and permanent identity/tombstones.

## Work Guidance

Support direct embedded and ordinary SQL-server modes. Refuse unsupported proxy/adoption/recovery rather than falling back to ordinary storage. Independent graph-clone merge is unqualified.

## Verification

Run real embedded lifecycle, history, rollback, guard and reopen tests. Set BEADS_GRAPH_TEST_SERVER_PORT for isolated ordinary-server concurrency qualification.

## Child DOX Index
