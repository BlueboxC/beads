# DOX.md — Observed Codex activity

## Purpose

Preserve project continuity through observed operations and reported turn handoffs.

## Ownership

Bounded event envelopes, retry identities, redaction, queries and recovery projection. CLI owns lifecycle dispatch and existing-only Dolt access.

## Local Contracts

- Use @activity/ keys in the existing memory plane; no new schema, database, daemon or model service.
- Capture requires explicit project-local enablement and delivered supported Codex hook identities. Disabled, readonly, malformed or foreign-workspace inputs withhold writes.
- Store tool identity, reported paths, payload hashes and structured exit/error observations, never raw tool input/output, transcript contents or user prompts. Hashes identify observations and do not validate a solution.
- Store bounded redacted assistant handoffs with reported scope; recover them as quoted untrusted data. No automatic task transitions, knowledge proposals, assertion acceptance or evidence renewal.
- Retry identity excludes observation time; identical retries preserve the original event. Existing unrelated entries remain unchanged. Append separate content identities for different observations.
- Event reads validate content identity and timestamps. Supervised proposals may pin up to 16 canonical event references (ID, session, turn and exact envelope SHA-256); missing or changed origins require explicit review and never renew themselves. References remain reported provenance, not verified evidence.
- Limited reads retain only the newest requested events, ordered by observation time descending then ID ascending. Total/invalid counts and session/turn filters still cover the full plane. Recovery selects three turn handoffs without retaining or sorting all events; no history is deleted.
- Limit input to 8 MiB, serialized events to 12 KiB, reported paths to 32 and recovery to three handoffs. Reads expose invalid-envelope counts; disabled capture retains prior records.

## Work Guidance

Use metadata already delivered by Codex. Never parse unstable transcripts, infer shell writes or execute project commands. Privacy filtering is bounded and best effort; journals remain private local project material.

## Verification

Package tests cover identity, serialization limits, redaction, honest outcomes, retry preservation and bounded recovery. The opt-in activity process test verifies real Dolt persistence, nested workspace selection, two-project isolation, readonly/disabled capture and no initialization outside a workspace.

## Child DOX Index
