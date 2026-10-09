# DOX.md — Backend conformance

## Purpose

Own backend conformance within the fork.

## Ownership

Shared tests for observable backend semantics.

## Local Contracts

Root DOX.md and the parent chain remain binding.

## Work Guidance

Exercise behavior, errors and state transitions against each supported implementation. The optional `AtomicMemories` case runs only where advertised and checks generation guards, no-op writes and preservation of neighboring planes.

## Verification

CheckHumanGatePolicy checks durable/ephemeral gates, explicit versus fallback actors, force/status/type/defer/delete/edge bypasses, enabled journal rollback and atomic/best-effort batches. This fork-specific SQL qualification stays outside the portable RunRoleContracts bundle. Three adapter wirings share it; the native classic adapter case reuses a disposable UOW SQL server when Docker is unavailable.

Use the affected existing checks selected in engdocs/TESTING.md; verify indexed child contracts.

## Child DOX Index
