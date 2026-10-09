# DOX.md — Issue persistence operations

## Purpose

Own issue persistence operations within the fork.

## Ownership

Issue lifecycle queries and mutations.

## Local Contracts

Root DOX.md and the parent chain remain binding.

- Journal actor provenance shares the mutation transaction and cursor. Only an exact context actor match inherits the resolution source; other named actors are `provided`, actorless rows remain empty. Historical provenance is never inferred from a read.

## Work Guidance

Preserve issue semantics across supported storage adapters and shared conformance tests.

## Verification

Use the affected existing checks selected in engdocs/TESTING.md; verify indexed child contracts.

## Child DOX Index
