# DOX.md — Storage boundary

## Purpose

Own storage boundary within the fork.

## Ownership

Storage interfaces, backend selection and persistence operations.

## Local Contracts

Root DOX.md and the parent chain remain binding.

- Context-aware proxy discovery retains authenticated adoption, upstream matching and owned-child cleanup. Cancellation stops polling/readiness and does not signal an adopted process; existing identity probes retain their 500 ms bound. The legacy API remains a background-context wrapper.

- Empty-parent updates on dotted IDs fail before any patch mutation; legacy implicit ancestry remains supported. Nonempty reparenting and detachment of nondotted IDs retain their contracts. Do not silently remove the explicit edge and claim detachment.
- History compaction dates the squashed base at the boundary and retains each replayed commit’s committer/date and separate author metadata. Snapshot metadata before mutations, restore exact caller session overrides on success/error and surface restoration failures. This preserves markers inside retained history; it does not reconstruct discarded history or repair previously re-dated stores.
- Ready-work readers withhold children connected by `parent-child` when the parent has deferred status or a future `defer_until`. `IncludeDeferred` lifts this selection filter; non-parent relations do not inherit it. Preserve parity between shared issueops and domain/db readers without rewriting stored task state.

- External blocks are evaluated from a caller-bound read-only resolver before ready limits/counts/atomic selection. Unresolved prerequisites and active parent-child descendants stay blocked across issue/wisp planes; do not materialize remote status into the local is_blocked cache.

## Work Guidance

Memory namespace selectors preserve exact prefix semantics across adapters. All three adapters select namespaces in SQL before transferring values; the unit-of-work adapter uses ConfigUseCase/repository within its existing read transaction. Case-insensitive memory search remains in Go.

Embedded Memories also offers optional guarded atomic batches. Expected values, equal-value skips and all writes/deletes share one existing transaction; a mismatch or failed write rolls back the batch. Other adapters do not advertise this optional capability or qualify pruning.

Dolt server fixtures suppress asynchronous usage-event flushing before spawning the backend; shutdown waits retain their process contract and temporary configuration homes must not have detached writers.

Proxy fixtures that do not require a chosen data port bind port zero directly; retain the explicit occupied-port refusal check and use the published actual endpoint when dialing.

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
