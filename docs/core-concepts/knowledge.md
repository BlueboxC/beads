---
title: Source-backed knowledge (BlueboxC fork)
description: Preserve project direction, decisions and learned solutions in the existing Beads database
---

`bd knowledge` is a local fork extension. It uses the existing Dolt memory
plane, with no schema migration, extra database, model calls or daemon.

```bash
bd knowledge scan DOX.md docs/plans
bd knowledge list --json
bd knowledge context context-pack
bd knowledge graph --html > /private/tmp/project-knowledge.html
```

Scan registers Markdown and UTF-8 text documents, hashes, headings and ancestor
DOX contracts. It does **not** infer decisions or mark a solution verified.
Paths are relative to the selected Beads workspace, including when called from
a nested module. Repeating scan without paths refreshes the saved selection.
Each explicit selection replaces the source catalog, retaining every assertion.

Catalogs up to 60 KiB retain the original single-value format. Larger catalogs
store UTF-8 fragments of at most 60 KiB in the same memory plane and publish one
version-2 manifest last. SHA-256 checks and an 8 MiB total bound reject missing,
corrupt or oversized data as a whole; implicit scan and maintenance refuse that
selection rather than resetting it. Interrupted publication keeps the prior
catalog, and unchanged scans skip writes. Old fragments remain for rollback and
concurrent readers. Before a binary downgrade, restore the previous
`@knowledge/catalog` value; older clients cannot decode version 2. No assertion,
proposal, task or schema is changed by catalog publication.

The supervising agent records reviewed knowledge through a JSON file or stdin:

```json
{
  "id": "context-freshness",
  "kind": "solution",
  "summary": "Discard evidence from before the latest repository mutation",
  "module": "context-pack",
  "issue": "project-42",
  "base_commit": "the-inspected-commit",
  "problem": "A pack included evidence from before a repair",
  "cause": "The evidence selector ignored the latest mutation",
  "solution": "Reject evidence whose version is not newer than that mutation",
  "scope": "inspected",
  "evidence": "Implementation and test definition inspected; tests not rerun",
  "sources": [{"path": "src/context_pack.py"}],
  "related": ["context-module"]
}
```

```bash
bd knowledge record --file record.json
# Or: cat record.json | bd knowledge record --file -
```

Use an actual workspace-relative path for the file; absolute paths are refused.
Kinds are `objective`, `constraint`, `decision`, `module` and `solution`.
Evidence scope is explicitly `recorded`, `inspected`, `tested`, `installed` or
`productive`; scope above recorded requires an evidence description. Include
test output or qualification reports among source paths to bind their hashes.
Beads records the operator's claim, without executing or certifying it.
Related IDs must already exist. Issue IDs are references, not task mutations.

Record writes capture current source hashes, including DOX ancestors. When any
source changes, disappears or gains a new DOX ancestor, the assertion becomes
`needs_review`. A matching worktree is `current`, not a claim that tests ran.
Optional `symbols` lists named Python, Go, JavaScript or TypeScript IDs such as `src/context_pack.py::select_evidence`.
The CLI requires a current `bd code` index and binds each referenced file/DOX.
These are explicitly reviewed learning links, not inferred test coverage.
Updating a document or code catalog never refreshes assertion hashes. Re-record the same ID only
after reviewing its sources and evidence.

`bd prime` and the existing hooks recover a bounded projection of these records.
They report changed and newly discovered documents without writing a catalog
during a hook. They preserve plain memories and respect `--no-memories`.
Before editing, still read the applicable DOX chain. After changing a plan or
learning a supported solution, refresh the selection and record the reviewed
assertion. Semantic extraction is the supervising agent's responsibility.

## Supervised semantic learning

Codex performs the semantic extraction: read the selected documentation, plans,
DOX contracts, code and recorded verification; compare existing knowledge before
proposing a new problem/cause/solution. Beads stores and validates that proposal,
without a model service, embeddings, another database or automatic extraction.
A proposal is source material awaiting explicit review, never a verified fact.

Preparation and proposal reads exclude current and retained code-index blobs.
Symbol linking loads bounded routing metadata and only the referenced files;
it checks their parser, source and DOX hashes without parsing or discovering
unrelated roots. This validates the selected symbols, not the whole index.
Readonly diagnostics can also narrow `bd memories` with `--key-prefix` and
`--exclude-key-prefix`; both use literal case-sensitive user-key prefixes and
combine with the existing keyword search. Defaults retain full enumeration.


```bash
bd knowledge sources docs/plan.md src/context_pack.py --json
# Read those sources and every returned DOX; copy their inspected SHA-256 into
# the Record JSON above, set scope to recorded, and submit from stdin:
bd knowledge propose --file - --json
bd knowledge proposals --json
bd knowledge review p-DIGEST --decision accept --reviewer "Codex / owner" \
  --reason "Reviewed implementation and evidence" --scope inspected \
  --evidence "Implementation inspected; tests not rerun" --json
# Or reject with --decision reject --reviewer ... --reason ...
```

`knowledge sources` does not read code symbols automatically: include their file
paths in its selection. Propose requires the supplied hashes for **every** bound
source and DOX ancestor to still match; it refuses missing or changed hashes.
The agent must read their contents, not infer a solution from headings. Named
symbol and related-record links use the existing validation. Beads cannot prove
that the agent actually read a source or authenticate a reviewer label.

Proposal/review entries are immutable and addressed by content. Repeating the
same submission or review skips the write. An accepted assertion and its review
share one atomic existing Dolt memory entry; only an unambiguous acceptance joins
`knowledge list/context`. Concurrent conflicting reviews or two accepted
proposals for one assertion ID preserve all entries and withhold promotion.
Existing assertions win and are never replaced by a proposal. Resolve a conflict
or deliberately revise an established solution with the existing explicit
`knowledge record` workflow after source/evidence review. Do not delete review
history to hide a disagreement.

States are pending, accepted, rejected, superseded and conflict. They are
independent of current/needs_review source validity and evidence scope. Acceptance
refuses changed/deleted sources or a new DOX ancestor. Reading, maintenance and
hooks never refresh their hashes. Create a new inspected candidate with
`propose --supersedes p-OLD` for a pending/rejected topic, retaining its history;
accepted assertions require explicit record review. A filesystem change after
acceptance is reported on the next read and does not renew evidence.

At most 512 proposals and 1024 reviews are retained per workspace, with 32 KiB
per stored envelope. Selected source, Record and context limits still apply.
Archive explicitly when full; there is no automatic retention or deletion.
`bd prime`/hooks report proposal counts separately without injecting proposed
solutions as accepted knowledge. `--no-memories` suppresses this plane too.
The live/native graph shows proposal nodes, sources, task/symbol references,
supersession and acceptance links with inspectable review history. The viewer
has no promotion button or write API. Nothing grants production authority or
reopens completed work, and no application runtime is triggered.

The HTML graph shows actual stored source, DOX, assertion, related-record and
issue-reference edges. It uses the same native D3 viewer as `bd graph --html`, with typed colors, drag, zoom, search and readable node evidence. D3 is bundled, so it works offline;
exports contain private project information. It is a snapshot, not an AST call
graph or a live view of the database.

For a live view of that same saved knowledge, tasks and code selection:

```bash
bd serve --graph-viewer --addr 127.0.0.1:18740
# Open http://127.0.0.1:18740/viewer ; Ctrl-C stops the foreground server.
```

This opt-in mode uses the existing HTTP transport and the existing project
Dolt, including embedded Dolt. It exposes only a read-only graph and health
probe; normal `bd serve` and its issue-write API remain separate. Choose a
different port per project; start from that project's directory or use BEADS_DIR.
`--db` overrides are refused for this mode. Nothing is installed at startup.

The live viewer offers **Actualizar grafo** and an **Actualización** selector.
**Automático** checks every five seconds while visible, backing off to one minute
on errors and fully resynchronizing after reconnect. **Manual** queries only
when you press the refresh button or explicitly connect with a token; it does
not poll, retry or query when returning to the tab. An already running request
may finish after switching to manual. The button works in either mode.
Automatic is the initial default. The browser remembers only the selected mode,
not graph data or tokens; if storage is blocked the choice lasts for the page.
Opening in manual mode waits for the button before loading data. These controls
require a running viewer server and do not start it or maintain the index.
Offline exports hide them. Refresh keeps moved nodes,
zoom, filters, selection and labels; new nodes are placed without restarting
physics. Reset restores initial positions for the current graph. Connection
status is separate from source validity. A changed source/DOX gets
`needs_review`. Explicit `code scan` / `knowledge scan` selects roots; an
independently started `bd maintain watch` updates derived data and discovers
new files only within saved directory roots. Reading and maintenance never
renew learned evidence. See [maintenance](/core-concepts/code-index#automatic-maintenance).

Concurrent viewers share one bounded query per five-second interval. Each
strict read-only CLI snapshot opens and closes the store; the service holds
no embedded workspace lock between reads. The presentation cache is in RAM,
with ETags to avoid retransferring unchanged snapshots. There is no second
index, database, model or file watcher. A graph query is limited to 30 seconds
and 64 MiB of output; larger workloads fail visibly rather than truncate data.

Existing HTTP Host restrictions and optional `--auth-token-file` apply. The
public page shell includes no project data; with authentication enabled, enter
the token in the viewer's access field. It remains only in browser RAM, with
no URL/local-storage persistence. Loopback is the default; non-loopback still
requires the existing explicit bind/auth choices. Offline exports keep working
without networking. Stored paths and evidence remain private local information.

This fork holds at most 256 documents, 256 selected paths or directories and
512 assertions per project. Catalog size does not expand the bounded context
injected by prime or hooks. Files
must be UTF-8 text up to 16 MiB; record JSON is capped at 24 KiB with at most 16
bound sources and relations. Select narrow document roots in large projects.
Existing `bd memories --json` and `bd recall @knowledge/record/<id>` expose the
stored values. Explicit `bd forget @knowledge/record/<id>` removes an assertion;
`bd forget @knowledge/catalog` disables discovery. Accepted proposal assertions
are stored inside their immutable review entries, not `@knowledge/record/<id>`;
forgetting a legacy record does not remove an accepted proposal's assertion.
Use `bd recall @knowledge/proposal/<proposal-id>` and
`bd recall @knowledge/review/<proposal-id>/<review-id>` for the audit entries.
Generic forget is an explicit destructive memory operation, outside review;
removing a review may change derived conflict/acceptance status. Dolt backup/sync semantics
are unchanged, including the memory plane's convergent-on-pull merge policy.

A knowledge refresh reads each shared source once, reusing its hash and DOX
ancestry only during that query. The next query reads sources again. This
reduces repeated catalog reads without renewing evidence. Older fork clients
retain their smaller limits; a binary rollback preserves stored entries but
cannot fully display more than 64 assertions or validate sources over 1 MiB.


## Learning from observed session history

The project viewer (`bd serve --graph-viewer`) includes **Grafo** and
**Historial**. History reads the same local Dolt journal as `bd activity list`;
it shows up to 1,000 recent valid events, with total and invalid counts.
Filter by session ID, turn, event type or text, and select an event for its
reported summary, operation metadata and linked supervised proposals. Switching
views preserves graph positions, zoom, filters and navigation. Existing
Manual/Automático and refresh controls update both views; static HTML remains a
snapshot. Session IDs are observed hook identities, not imported chat titles.

Capture must already be enabled for this project. Disabled capture preserves
prior history. Only delivered hooks are covered; interrupted or unsupported
calls may be absent. Operation exit codes are observations, and turn summaries
are redacted reported claims, never verified repairs or authorization.

**Preparar aprendizaje** shows a copyable request for Codex. Paste it in a
project chat to ask Codex to select explicit current sources/DOX, compare existing
solutions and submit only a new justified pending candidate. The viewer executes
no commands and sends no chat messages. **Descargar paquete** retains the private
event export. Neither action submits or accepts a proposal. After reading the current code/docs and their DOX
and comparing established solutions, Codex can explicitly cite the originating
event when submitting a source-hashed candidate:

```bash
bd activity list --session SESSION --turn TURN --json
bd knowledge sources docs/plan.md src/context_pack.py --json
bd knowledge propose --file candidate.json --activity-event EVENT_ID --json
bd knowledge proposals --json
```

Repeat `--activity-event` for up to 16 events. Origins must exist in this
workspace and are pinned by ID, session, turn and exact envelope SHA-256.
Candidates still require inspected source/DOX hashes and recorded scope.
Explicit `knowledge review` remains the only proposal acceptance path.
Missing, changed or corrupt origins block new acceptance; they never silently
rebind. An already reviewed source-backed assertion survives later journal loss,
with the missing reported origin still visible in its immutable proposal
lineage. Proposals without activity origins retain their prior IDs.

Before downgrading, retain the proposal/review journal: older binaries cannot
validate origin-bearing proposal identities and omit them from context, while
legacy assertions and proposals remain readable. Restore the newer binary to
recover that lineage; do not rewrite entries to remove origins.


### Prepare a session learning in one readonly command

```bash
bd --readonly knowledge prepare docs/plan.md src/context_pack.py \
  --session SESSION --turn TURN --json
```

Alternatively repeat `--activity-event ID` for up to 16 explicit events; do not
combine it with session/turn selection. A session selection includes its latest
16 valid events and reports omitted/invalid counts. Narrow it to a turn or use
explicit IDs when earlier events matter. Only local delivered observations are
covered. Preparation is readonly and does not persist a queue item.

The packet returns actual current UTF-8 source/ancestor DOX text, full-file hashes,
existing topic IDs, bounded knowledge context and an editable `draft`. Source
material is limited to 16 KiB per file and 64 KiB total; `truncated` requires
reading the remainder before claiming inspection. Consult `knowledge context`,
`list` and `proposals` for full matching solutions and prior candidate history.
Preparation is not proof of semantic inspection, test execution or authority.

Codex fills only `draft.record` with one new justified candidate, retaining
`scope: recorded`, inspected source hashes and the prepared workspace/origins.
Submit **only** that edited draft via `knowledge propose --file - --json`; no
repeated event flags are needed. Blank, foreign, stale or altered drafts fail
before persistence. A draft for an established assertion ID is refused: inspect
that solution and use explicit `knowledge record` review for a justified revision.
Identical proposal retries preserve the immutable ledger. A changed candidate
can supersede a prior pending/rejected proposal through the existing flag.

Inspect `knowledge proposals --json`, then explicitly accept/reject with
`knowledge review`. Pending content remains separate from recovered facts;
accepted source-backed knowledge is recovered through `knowledge context` and
`prime`. Source drift still requires review. Neither the viewer nor hooks call a
model, execute source text or promote observations automatically.
