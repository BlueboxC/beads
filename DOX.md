# DOX.md — Beads fork

## Purpose

Own the BlueboxC Beads fork and its development contracts for persistent project context.

## Ownership

Root owns fork policy, public API packages, license, build/version files, small packaging assets and the Child DOX Index. Indexed children own their subtrees. FORK.md describes the public baseline and implemented extensions; docs/core-concepts/fork-continuity.md owns fork onboarding and capability limits.

## Local Contracts

- Read this file and every indexed DOX.md along the target path before edits. A closer contract cannot weaken this framework.
- Keep the DOX hierarchy current after meaningful changes; update affected indexes and remove stale text.
- Preserve upstream attribution and the MIT license. Keep origin pointed at BlueboxC/beads and upstream at gastownhall/beads.
- Public memory lists support literal case-sensitive user-key prefix inclusion/exclusion, combined with the existing case-insensitive search; empty selectors preserve full-plane behavior.
- Use bd for tasks, dependencies and durable facts; record evidence before closing work. Keep private project records out of this public fork.

## Work Guidance

- Prefer the simplest complete implementation and existing extension points. Avoid speculative configuration and dependencies.
- The fork objective is continuity: recover project direction, existing modules, verified fixes and their evidence. Use one Beads product and its existing Dolt storage boundary.
- Public baseline is upstream v1.3.1 at c1c4b642ac1c08d8c828007a1c2f96e47e43ef7c, plus the published bootstrap. Continuity extensions reuse the existing Dolt memory boundary. Keep operator configuration, project databases and undisclosed vulnerability patches outside public commits.
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
