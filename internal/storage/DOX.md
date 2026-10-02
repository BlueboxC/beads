# DOX.md — Storage boundary

## Purpose

Own storage boundary within the fork.

## Ownership

Storage interfaces, backend selection and persistence operations.

## Local Contracts

Root DOX.md and the parent chain remain binding.

## Work Guidance

Keep Dolt engine behavior behind the existing storage boundary. Do not bypass it from CLI or other internal packages.

## Verification

Use the affected existing checks selected in engdocs/TESTING.md; verify indexed child contracts.

## Child DOX Index

- [schema/DOX.md](schema/DOX.md) — Schema and migrations.
- [dolt/DOX.md](dolt/DOX.md) — Server-backed Dolt adapter.
- [embeddeddolt/DOX.md](embeddeddolt/DOX.md) — Embedded Dolt adapter.
- [issueops/DOX.md](issueops/DOX.md) — Issue persistence operations.
- [uow/DOX.md](uow/DOX.md) — Units of work.
- [domain/DOX.md](domain/DOX.md) — Storage domain model.
