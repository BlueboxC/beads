# DOX.md — User documentation

## Purpose

Own user documentation within the fork.

## Ownership

Installation, concepts, architecture, workflows and recovery guidance.

## Local Contracts

Root DOX.md and the parent chain remain binding.

## Work Guidance

Document shipped behavior and distinguish fork goals from implemented capabilities. Human gate guidance owns opt-in resolver setup, protected mutations, batch behavior and caller-controlled identity/admin limits; do not describe manual resolution as authenticated approval. Audit examples use stored EventType literals; sync onboarding separates fresh `init --remote` from existing workspaces, preserves local data and describes embedded as the default. Do not advertise removed CLI modes.
Code-index guidance owns conservative impact, bounded fragmented file persistence, explicit rename-review drafts and opt-in atomic pruning; distinguish live-row payload savings from unchanged Dolt history and filesystem size.
Observability guidance distinguishes default process metadata from operator-supplied attributes and local command-span arguments; telemetry stays opt-in.
Fork behavior and onboarding: core-concepts/fork-continuity.md. Detailed commands: core-concepts/knowledge.md and core-concepts/code-index.md. Generated CLI pages and upstream installation channels describe the upstream release. DOX contracts stay outside Mintlify navigation.

## Verification

Use the affected existing checks selected in engdocs/TESTING.md; verify indexed child contracts.

## Child DOX Index

- [cli-reference/DOX.md](cli-reference/DOX.md) — Command reference.
- [integrations/DOX.md](integrations/DOX.md) — Integration documentation.
