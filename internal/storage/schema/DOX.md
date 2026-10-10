# DOX.md — Schema and migrations

## Purpose

Own schema and migrations within the fork.

## Ownership

Persistent SQL schema, versions and migrations.

## Local Contracts

Root DOX.md and the parent chain remain binding.

- Forward repairs 0067 and ignored/0027 recompute blocked state with separate issue/wisp target joins. Preserve the shipped predecessors and user timestamps; only parent-child edges propagate parent blockage.

- Forward migrations 0068 and ignored/0028 add clone-local journal `actor_source` with empty historical defaults. Preserve old rows and cursors; runtime replay is idempotent and fresh CLI bundles use direct DDL. This is per-mutation data, including deletes, rather than issue metadata.

## Work Guidance

Prefer existing issue metadata for extension data. Schema changes need a justified durable contract, migration and persistence coverage.

## Verification

Use the affected existing checks selected in engdocs/TESTING.md; verify indexed child contracts.

## Child DOX Index
