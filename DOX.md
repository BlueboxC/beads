# DOX.md — Beads fork

## Purpose

Own the BlueboxC Beads fork and its development contracts for persistent project context.

## Ownership

Root owns fork policy, public API packages, license, build/version files, small packaging assets and the Child DOX Index. Indexed children own their subtrees. FORK.md describes the public baseline, implemented extensions and verified corrections; docs/core-concepts/fork-continuity.md owns fork onboarding and capability limits.

## Local Contracts

- Read this file and every indexed DOX.md along the target path before edits. A closer contract cannot weaken this framework.
- Keep the DOX hierarchy current after meaningful changes; update affected indexes and remove stale text.
- Preserve upstream attribution and the MIT license. Keep origin pointed at BlueboxC/beads and upstream at gastownhall/beads.
- Memory Recall and Forget report row existence independently of its value; imported empty memories are found and can be deleted. Empty new content remains rejected.
- Public memory lists support literal case-sensitive user-key prefix inclusion/exclusion, combined with the existing case-insensitive search; empty selectors preserve full-plane behavior. The optional AtomicMemories capability guards and mutates derived generations in one storage transaction; basic Memories callers remain compatible.
- Use bd for tasks, dependencies and durable facts; record evidence before closing work. Keep private project records out of this public fork.

- Public journal rows expose optional actor resolution provenance, not authenticated identity or authority. `journalops.WithActorSource` binds it to a caller actor; absent historical provenance stays absent. Existing issue/audit identity strings and SDK operation signatures remain compatible.

## Work Guidance

- Prefer the simplest complete implementation and existing extension points. Avoid speculative configuration and dependencies.
- The owner authorizes publishing the verified fork corrections and extensions to BlueboxC/beads. Pull request creation/reopening remains suspended until separately authorized; keep project data, installation records and private evidence out of public commits.
- Include every verified correction in this fork and its local installed build without waiting for upstream review. Keep private disclosure and publication approval separate from local fixes.
- The fork objective is continuity: recover project direction, existing modules, verified fixes and their evidence. Use one Beads product and its existing Dolt storage boundary.
- Native baseline is upstream v1.3.1 at c1c4b642ac1c08d8c828007a1c2f96e47e43ef7c. Verified corrections preserve its baseline API; continuity extensions reuse the existing Dolt memory boundary. Keep main available for upstream synchronization and publish fork work on codex/project-continuity.
- Root and example Go modules require gRPC >=1.83.2 and golang.org/x/crypto >=0.56.0; keep the root Dolt pin unchanged and align example locks with the root effective dependency graph.
- Follow AGENTS.md, AGENT_INSTRUCTIONS.md and engdocs/TESTING.md. Keep manual experiments in disposable directories.

## Verification

- Use the checks selected by engdocs/TESTING.md, including git diff --check. Report unavailable tooling instead of claiming a check passed.
- Verify each Child DOX Index points to an existing file; keep every DOX file reachable from this root.

## Child DOX Index

- [cmd/DOX.md](cmd/DOX.md) — Executable entry points.
- [internal/DOX.md](internal/DOX.md) — Internal implementation.
- [backend/DOX.md](backend/DOX.md) — Public backend contracts.
- [docs/DOX.md](docs/DOX.md) — User documentation.
- [engdocs/DOX.md](engdocs/DOX.md) — Engineering guidance.
- [scripts/DOX.md](scripts/DOX.md) — Development and release scripts.
- [integrations/DOX.md](integrations/DOX.md) — External interfaces.
- [plugins/DOX.md](plugins/DOX.md) — Agent plugins.
- [examples/DOX.md](examples/DOX.md) — Usage examples.
- [test/DOX.md](test/DOX.md) — Go contract checks.
- [tests/DOX.md](tests/DOX.md) — Regression and external tests.
- [release-gates/DOX.md](release-gates/DOX.md) — Release qualification.
- [npm-package/DOX.md](npm-package/DOX.md) — npm distribution.
- [.github/DOX.md](.github/DOX.md) — GitHub automation.
- [.beads/DOX.md](.beads/DOX.md) — Project tracking assets.
