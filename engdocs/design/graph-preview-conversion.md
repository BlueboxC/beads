# Existing-workspace conversion to Graph Preview

Status: implementation design, not an available conversion command.
Reviewed against the fork at `df356b1b09158d07baa9bcbfe4c8ffffa41d5b33`.
The current format supports fresh initialization only; `init --graph-mode link`
must keep refusing an existing workspace. Work state and qualification belong
in Beads, not this document.

## Preservation contract

Use the existing Dolt database. Adopt existing Issue rows rather than replaying
`create`, so IDs, fields, dates, closed state, labels, metadata, leases and native
events remain unchanged. Preserve all user tables, schema migration numbers,
branches, commits and the working set. Comments remain native comments; do not
turn an old comment or observed session event into a verified solution.

Promote every non-derived keyed memory, including assertions, proposals and
reviews, to one canonical retained Memory. Preserve the exact UTF-8 key and
body, including an imported empty body. Store the existing private continuity
pointer and matching `forkContinuityKey` metadata atomically. Preserve the
original review status, source hashes, evidence and validity. Adoption supplies
conversion provenance; it must not impersonate the original author or renew an
old tested/installed claim. Keep code, document catalogs and observed activity
in their existing derived config plane.

Issue graph history starts with a snapshot of the adopted current state at the
conversion boundary. Preserve earlier native snapshots, events and Dolt history;
do not synthesize opaque graph revisions for old commits or claim earlier
states are available through `bd versions`. State the two history scopes in
the conversion receipt and user documentation.

Preserve dependency rows and their original semantics. `parent-child` affects
scheduling and closure, so it cannot become an informational Related Link.
`discovered-from` and `related` must retain their distinct type, direction,
identity, metadata, thread and attribution. Unknown types, external/wisp
endpoints, missing endpoints, invalid encodings or unsupported native fields
produce a named refusal before mutation. No silent coercion or dropped rows.

## Compatibility work before conversion

Extend native dependency projection to the selected ordinary types, keeping
ordinary ready/blocked/closure behavior. Retain the row as authoritative and
register the corresponding immutable local Type descriptors; owned snapshots,
incident queries, retained versions and unlink must agree. Do not duplicate an
ordinary dependency as an independently editable generic Link.

The current complete-inventory cap is 1,000 live Resources. Closed Issues and
supervised review/proposal Memories still count. Qualify a bounded 2,048-Resource
inventory for the initial conversion pilot while keeping the 16 MiB current-read
byte ceiling, SQL size checks and atomic postconditions. Measure a representative
2,048-Resource fixture and test 2,049 refusal, rollback and oversized-state
reduction. If the measured cost is unacceptable, retain the current cap and
implement paged selection before admitting conversion. Do not remove closed
work or raise the byte budget to make a project fit. This remains an operational
preview limit, not production scalability.

Keep the 65,536-row / 64 MiB continuity budget. An explicit existing code-prune
plan can remove only unreferenced content-addressed blobs from the current
working set, retaining the complete published manifest, file fragments and
Dolt history. Validate every blob hash and the current full index. Perform any
prune first on a recoverable isolated candidate, with upgraded writers and a
current-manifest guard; no automatic maintenance prune. Activity records and
human knowledge are not candidates for deletion.

## Preparation and isolated rehearsal

1. Acquire a consistent readonly inventory per project: source format/binary,
   Dolt branch/HEAD, working set, schema, issue fields/types, dependency types and
   endpoints, comments/events, human-memory hashes, current index/manifest,
   catalog/activity counts, retained history and projected acquisition budgets.
   Raw payloads and project identities remain private. A changed HEAD or working
   set invalidates the apply plan; readonly acquisition never authorizes apply.
2. Verify space for a full Dolt-native checkpoint and an isolated candidate.
   JSONL export alone is insufficient: it omits full table, branch and history
   state. Preserve workspace metadata/selector and the known-compatible binary.
   A filesystem copy requires quiescent writers and a supported capture method;
   verify restoration, not just backup-file existence.
3. Build an identity/adoption manifest bound to those exact fingerprints, the
   final canonical workspace path, operator-selected permanent scope URL and
   new authority. Never silently reuse a copy's path/authority as live identity.
   The private receipt maps each existing ID/key/dependency to its canonical
   resource and records projected size plus any explicit candidate-only prune.
4. Use the isolated candidate for explicit schema adoption and seeding retained
   current snapshots. Ordinary Issue bodies and existing migration numbers stay
   intact. Canonical bodies, pointers, catalogs and their first versions publish
   consistently. Workspace format metadata is activated only after database
   persistence; a crash must restore the original selector or resume the exact
   checked candidate, never present a half-converted workspace as valid.
5. Compare native rows and exact human envelopes, every relation/type/endpoint,
   all old branch/commit references and retained history. Verify the complete
   current index and catalog, ordinary scheduling, code/knowledge queries,
   supervised review, prime recovery, viewer refresh and lifecycle isolation.
   Historical source validity and production authority are unchanged.
6. Inject interruption/conflict/invalid-data/limit failures and exercise rollback
   through a fresh process with the preserved old binary. Repeated apply must
   identify the completed candidate or refuse, never allocate duplicate Memories
   or links. Do not overwrite writes accepted after conversion with an old
   backup; restoration needs a separate reconciliation boundary.

## Activation boundary

Only a qualified explicit conversion operation may activate an existing project.
Before activation, quiesce ordinary/graph writers, lifecycle hooks and viewer
reads; recheck all expected fingerprints, checkpoint integrity, free space and
final identity. Refuse drift instead of refreshing the plan silently. Keep the
original recoverable until fresh-process verification succeeds. Conversion does
not execute project code, reindex sources, accept learning proposals, configure
global Codex hooks, publish Dolt data or authorize project runtime/production.

No live project is converted by publishing this design. First support embedded
workspaces; shared-server adoption, moving converted workspaces and independent
clone merging need their own qualification.
