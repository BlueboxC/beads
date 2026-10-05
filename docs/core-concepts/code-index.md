---
title: Code index (BlueboxC fork)
description: Query existing Python, Go, JavaScript and TypeScript symbols and static references in the same Dolt as project knowledge
---

`bd code` is a local fork extension. It stores a regenerable code AST index
in the existing Dolt memory plane; it adds no schema or database. Python 3 is
required only when parsing changed files. The bundled stdlib parser receives
text through stdin under `-I -S`. Go uses the standard Go parser on supplied text;
no Go executable, package loading or build is needed. JavaScript and TypeScript
use bundled TypeScript 5.9.3 (Apache-2.0) under an operator-owned Node.js with
native addons/global search disabled and a sanitized environment. Its compiler
VM has no filesystem host. No project code, tsconfig or install scripts run.
`bd code --licenses` prints compiler attribution. No npm setup is required.

```bash
bd code scan src tests --exclude src/generated --json
bd code scan src tests --languages python,go,javascript,typescript --json
bd code status --json
bd code query context_pack --json
bd code graph src/context_pack.py --html > /private/tmp/code-graph.html
bd code scan                 # refresh the saved selection incrementally
bd code scan --rebuild       # reparse, retaining all human assertions/tasks
```

Default selection is Python on a new index; absent `--languages` reuses saved
languages. `--python` and `--node` select operator interpreters only for changed
files. Go, JS/JSX/MJS/CJS and TS/TSX/MTS/CTS extensions are supported. An unchanged
scan needs neither interpreter.

Roots and exclusions are workspace-relative, including nested CLI invocations.
Explicit roots replace the index selection, independently of the document
catalog. Hidden directories, node_modules, vendor, dist, build, backups, venv,
bytecode, logs, data and workspace scratch are excluded. Symlinks fail with an
explicit error. Select source and trusted test trees rather than runtime data.

The index contains file hashes, ancestor DOX hashes, named classes/functions,
line ranges, import statements and call sites. It stores no source bodies,
comments, docstrings or argument literals. Syntax failures remain visible as
parse errors; they do not produce guessed symbols or source excerpts.

References resolve unique lexical names and imports in selected files. Go
package names and renamed imports follow the nearest confined go.mod; its hash
is evidence. JS/TS relative named/default/namespace imports and simple CommonJS
require/exports use explicit bindings only. Duplicate declarations, shadowed
require and ambiguous paths stay unresolved. Type-only imports describe
structure, never callable values. No tsconfig alias, re-export, external package,
build-tag evaluation or type checking is inferred. Cycles
are valid code relations, never issue blockers. Unresolved references remain
queryable. Receiver types, re-exports, dynamic dispatch and anonymous
lambda/comprehension scopes are not inferred. Named Go methods, structs and
interfaces and JS/TS methods, interfaces, types, enums and objects are recorded.
Go top-level initializer calls and interface method headers are outside extraction.
Known static class methods can resolve; unknown instance receiver types cannot. A static reference does not prove
a function runs, a test passes or a module is installed.

An explicit knowledge record can add up to 16 symbol IDs, for example
`"symbols": ["src/context_pack.py::select_evidence"]`. `bd knowledge record`
checks those IDs against a current index and binds their source files/DOX.
`bd code query` returns associated human learnings and their separate validity.
Adding a symbol link is an explicit review; scan/rebuild never renews a stale
assertion. Closing a task does not create a learned solution automatically.

Limits are 4096 files, 1 MiB per UTF-8 file, 32 MiB total input, 8 MiB decoded
JSON and 60 KiB per compressed stored row. A row/manifest exceeding its bound
fails before publication; narrow the roots instead of assuming a partial scan
completed. Queries default to 50 matching files and report omitted matches;
`--limit` accepts 1–1024. Graph export refuses more than 3000 nodes and shares
the native offline viewer; select a module to keep it usable.

Unchanged files reuse AST only when parser and source hashes match. Queries
resolve relationships over the whole saved index before selecting results.
Status checks hashes, DOX ancestry, removed and new files without parsing or
writing. A unique identical-content rename is reported as a hint; old human
evidence still requires review. Branch switches are detected through bytes,
not treated as authority from a commit identifier.

AST parsing and reference storage use blocks of at most 128 files/entries,
so large catalogs keep the same per-row bound. Versions 1/2 remain readable. Version 3 adds language selection and per-file
parser fingerprints, so expanding a Python index reuses its unchanged files.
Older clients refuse version 3 explicitly: back up the previous derived manifest
before migration and restore it before reverting the binary. Human records and
immutable blobs remain separate.
Each scan prepares bounded immutable blob rows and atomically publishes one
manifest through memoryops. Failure before publication leaves the prior index
readable. Old blobs are retained for rollback and Dolt history can grow; the
reported stored payload is not the total database size. Human memories and
assertions are separate keys and remain unchanged.

Prime/hooks show only a bounded snapshot summary with a query route; they do
not launch parsers, scan code or update the index automatically. After source
changes run `bd code scan` or explicitly start the maintenance watcher below;
inspect `bd code status` before using a result. Offline HTML remains a snapshot.
This extension does not execute any application runtime or model.


## Global project explorer

```bash
bd graph --project --html > /private/tmp/project.html
bd graph --project --json
```

This direct-workspace mode combines tasks in all states, explicit knowledge and
every indexed directory. The overview aggregates resolved imports/calls between
directories and shows their reference counts separately from task blockers.
Unresolved references remain counted in directory details; internal directory
references are not drawn as self edges. No project routes are followed.

Select a directory to filter its files by path or symbol, inspect the saved
symbols, line ranges, source hashes and DOX paths, and open a local source.
Relationship buttons and back navigation connect objectives, modules, solutions,
provenance and actual task details. Type/status filters and search focus the view.
All indexed files are present; the code-query `--limit` does not truncate this
overview. `--max-rows` caps the complete task selection and fails on overage.

The export reads current source/DOX validity without parsing or writing. Stored
locations can be stale; opening a source does not renew its learning. Source
bodies are not embedded. Normal task/code/knowledge exports remain available.
The HTML remains a snapshot; `bd serve --graph-viewer` provides the separate
read-only live view of this saved data.

## Automatic maintenance

```bash
bd maintain once --json         # one changed-data publication
bd maintain watch               # foreground; Ctrl-C stops
bd maintain watch --interval 30s
```

This opt-in CLI extension uses the existing embedded Dolt, without a second
store or schema. It reads the latest saved code roots/languages/exclusions and
document roots on every pass. Select them explicitly with code/knowledge scan
before starting; the watcher does not invent or broaden a selection. A directory
root can discover newly created files/subdirectories. An exact file root tracks
that path only; newly created siblings need an explicit selection change.

Changed source/DOX/parser hashes trigger an incremental index rebuild; unchanged
ASTs are reused. Catalogs update hashes/headings only. Unchanged passes neither
launch parsers nor write derived rows/commits. Deleted files disappear from the
derived generation; missing selected roots remain saved so a returning branch
file is recovered. Exact-content renames are hints. Branch changes are observed
through bytes and selected paths, without a Git checkout or hook. No worktree,
Git index/config, issue status, blocker, human memory or assertion is changed.
Old blobs and Dolt history remain available; changed generations can grow history.

The foreground watcher runs sequential bounded child passes, releases the
embedded driver between passes, waits 30 seconds by default and backs off on
failures up to five minutes (or a longer selected interval). Its default is not
a freshness guarantee: a busy base or continuously changing/invalid selection
can delay updates. Each child has a two-minute budget and bounded output. Errors
are visible; use `bd maintain once` to inspect their details. A complete derivation
must succeed before publication begins; index blobs publish before its atomic
manifest. Code and catalog are separate publications: a write failure after one
succeeds can leave that complete new projection beside the previous other one;
the next pass converges without renewing human evidence. The driver owns concurrency; no Beads flock or engine recovery exists.
A changed pass makes a local Dolt commit, independently of general automatic
housekeeping. No project hook, template import, automatic export, backup, remote
push, runtime, test, model, tsconfig or package install runs. Server/shared/proxied
maintenance is refused until its concurrent publication contract is qualified.

`--python` and `--node` select operator-owned interpreters for changed files.
Existing discovery/input/storage limits still apply: exceeding one refuses the
pass instead of silently truncating. Human evidence keeps its original hashes
and needs review when its sources change, even if the derived index is current.

The watcher starts only when explicitly invoked. It is independent of the live
viewer and no boot/Codex hook starts it. A running viewer picks up published
changes on its next graph query, preserving its normal layout and filters.
