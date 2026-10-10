# DOX.md — Issue persistence operations

## Purpose

Own issue persistence operations within the fork.

## Ownership

Issue lifecycle queries and mutations.

## Local Contracts

Root DOX.md and the parent chain remain binding.

- Journal actor provenance shares the mutation transaction and cursor. Only an exact context actor match inherits the resolution source; other named actors are `provided`, actorless rows remain empty. Historical provenance is never inferred from a read.

- Optional `gates.human.resolvers` is a transaction-local caller-asserted allowlist. Missing key is compatible; empty/malformed policy refuses protected operations. Explicit flag/env/named API actors only; force and disabled journaling never waive it. Share gate close/update/delete/edge/persistence checks with domain/db. This is no human authentication or external execution authority; imports/config/SQL/sync/restore remain caller-controlled administrative access.

- External blocker evaluation is shared with domain/db. Absent/unreadable providers fail closed; required-table and resolver failures propagate. Traverse active parent-child descendants with a visited set, preserving external references or an inherited witness in blocked output.

- The retained Issue recorder is transaction-scoped and called only by Graph Preview adapters after a final accepted mutation. Its revision bookkeeping preserves the accepted postimage's row_lock/updated_at and does not duplicate the mutation's journal event. Ordinary native writers and existing journal/security policies remain unchanged; graph-only schema prerequisites live in graphstore.

## Work Guidance

Preserve issue semantics across supported storage adapters and shared conformance tests.

## Verification

Use the affected existing checks selected in engdocs/TESTING.md; verify indexed child contracts.

## Child DOX Index
