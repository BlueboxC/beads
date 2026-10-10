# DOX.md — Source-backed knowledge

## Purpose

Preserve project direction and explicit learnings with source provenance in the existing Beads memory plane.

## Ownership

Document catalogs, supervised proposal/review ledger, assertion validation, worktree hashes, DOX ancestry, bounded context and offline knowledge graph export. CLI wiring belongs to cmd/bd.

## Local Contracts

- Use memoryops; do not add another database, schema, daemon or model call.
- Catalogs contain source hashes and headings. Explicit opt-in maintenance can rebuild only that catalog; retain missing saved roots for later return, compare complete scans before publication and skip unchanged writes. Catalogs over 60 KiB use UTF-8 parts (at most 60 KiB each) addressed by SHA-256 plus one final version-2 manifest, with an 8 MiB total bound; incomplete/corrupt generations withhold the whole catalog and refuse implicit refresh/maintenance. Read version-1 catalogs unchanged. Retain old parts for concurrent readers and rollback; restoring the previous catalog value is required before downgrading a fragmented workspace. Directory roots discover only bounded Markdown/text selections; explicit symlink roots are refused. Assertions are explicitly reviewed records, never inferred from task closure or document contents.
- Bind source content and applicable ancestor DOX files during explicit record writes. Refresh only reports validity; it never renews evidence hashes. Hashes, DOX ancestry and read errors are reused only within one Refresh call; each later call reads sources again.
- Source reads stay within the selected workspace. Limit catalogs and explicit root selections to 256 each, assertions to 512, text files to 16 MiB and assertion JSON to 24 KiB. Context injection remains bounded independently of catalog size. Assertions may explicitly link up to 16 current indexed symbol IDs from any of the 23 supported languages/formats; their file and DOX hashes bind the learning. Reserved code-index keys are decoded separately.
- Evidence scope remains recorded, inspected, tested, installed or productive as explicitly supplied. Matching hashes do not prove execution or production qualification.
- Graph edges describe provenance and knowledge relations; they are independent of task blockers. HTML exports are private, offline snapshots rendered by the shared native graphview viewer; knowledge colors identify types, not task status.

- Supervised proposals bind expected SHA-256 for every inspected source/DOX, use recorded scope and immutable content IDs. Accept/reject requires an explicit reviewer label and reason; acceptance stores its assertion and review atomically in one memory key. Equal retries skip writes; conflicting concurrent reviews or duplicate accepted topic IDs withhold promotion. Existing assertions remain authoritative. Replacement candidates preserve prior pending/rejected history; they never revise accepted assertions. Limit 512 proposals, 1024 reviews and 32 KiB per envelope; malformed or excess entries withhold ambiguous promotions. Reviewer labels are provenance, not authentication. Source matching is independent of proposal status.
- Proposals may carry up to 16 explicitly selected workspace-local activity references. Their identity binds the exact original envelopes; missing/changed origins block new acceptance alongside source/DOX drift. Existing source-backed accepted assertions survive later journal loss; immutable proposal/review lineage retains its reported origin. Proposals without origins retain their existing IDs and format.
- Learning preparation is an ephemeral readonly projection of explicitly selected current source/DOX bytes, existing topics/context and at most 16 journal origins. Source text is limited to 64 KiB total and 16 KiB per file with visible tails; journal omissions are counted. Drafts bind the canonical local workspace plus exact origin/source snapshots. Submission rejects foreign/changed origins and duplicate established topics; sources revalidate through the proposal ledger. Codex supplies content and explicit review selects evidence scope.
- Learning reads metadata and observed activity while excluding all current/historical code blobs. Existing topics, catalog fragments and origin envelopes remain available; namespace filtering never promotes, renews or rewrites a proposal.
- Prime/hooks summarize proposal counts separately, never inject pending solutions as accepted facts, extract, review, rebind, execute sources or call models. Maintenance writes only derived catalogs/code. Codex reads selected docs/DOX/code and verification evidence, compares existing knowledge, then submits proposals for explicit review.

## Work Guidance

Keep semantic extraction in the supervising agent. Preserve original assertions when source catalogs are rebuilt; require explicit review before rebinding changed sources.

## Verification

- Source change, deletion and introduced DOX contracts require review without upgrading evidence.
- Catalog persistence tests cover TEXT limits, UTF-8 boundaries, no-op/legacy reads, interrupted part/manifest writes, corrupt parts and bounded preparation; real Dolt processes cover large catalogs and unchanged assertion evidence.
- Catalog discovery stays bounded and cannot read outside the workspace.
- Context stays bounded; graph export escapes document strings and needs no network.
- Proposal tests cover idempotent single-entry promotion, missing/stale DOX hashes, preserved rejection/supersession, conflicting concurrent reviews/topics, established assertion protection and bounded malformed input. Real-Dolt CLI process tests separately verify persistence, nested workspace recovery, readonly guards, project isolation and hook projection.

## Child DOX Index
