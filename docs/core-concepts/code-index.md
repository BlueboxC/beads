---
title: Code index (BlueboxC fork)
description: Query existing structure in 23 languages and formats and static references in the same Dolt as project knowledge
---

`bd code` is a local fork extension. It stores a regenerable code AST index
in the existing Dolt memory plane; it adds no schema or database. Python 3 is
required only when parsing changed files. The bundled stdlib parser receives
text through stdin under `-I -S`. Go uses the standard Go parser on supplied text;
no Go executable, package loading or build is needed. JavaScript and TypeScript
use bundled TypeScript 5.9.3 (Apache-2.0) under an operator-owned Node.js with
native addons/global search disabled and a sanitized environment. Its compiler
VM has no filesystem host. No project code, tsconfig or install scripts run.
Java, C#, Rust and C++ use pinned Tree-sitter WASM grammars from
Microsoft's MIT-licensed `@vscode/tree-sitter-wasm` 0.3.1 in isolated Node. Embedded bytes
provide the runtime and selected grammar; its VM has no filesystem/network host,
and WASM memory is bounded to 512 MiB separately from the Node heap. No Java,
.NET, Rust or C++ compiler, npm setup or project build is required.
`bd code --licenses` prints all bundled attribution;
`internal/codeindex/tree-sitter-provenance.json` records the original revisions/hashes; `common-parser-provenance.json` identifies the fourteen additional MIT grammar payloads from integrity-pinned `tree-sitter-wasm` 2.0.4. They reuse the original fixed runtime. XML uses Go encoding/xml on text, without a project executable, DTD loading, external entity resolution or XSD validation. No PHP, shell, mobile compiler or SQL server is needed or invoked. The archive hashes identify distributed bytes; they do not establish a reproducible upstream build.

```bash
bd code scan src tests --exclude src/generated --json
bd code scan src tests --languages all --json
bd code scan --languages java,csharp,rust,cpp --json  # replace saved languages, retain roots
bd code status --json
bd code query context_pack --json
bd code graph src/context_pack.py --html > /private/tmp/code-graph.html
bd code scan                 # refresh the saved selection incrementally
bd code scan --rebuild       # reparse, retaining all human assertions/tasks
```

Default selection is Python on a new index; absent `--languages` reuses saved
languages. `--python` and `--node` select operator interpreters only for changed
files. An unchanged scan needs neither interpreter. Saved selections are never
broadened automatically: choose the complete language list, or `all` for all 23 languages/formats.
`c#`/`cs`/`c-sharp` and `c++`/`cxx` are aliases for `csharp` and `cpp`; `sh`/`shell`, `ps1` and `yml` select `bash`, `powershell` and `yaml`.

| Language | Source extensions | Parser |
| --- | --- | --- |
| Python | `.py` | Python stdlib AST |
| Go | `.go` | Go stdlib parser |
| JavaScript | `.js`, `.jsx`, `.mjs`, `.cjs` | TypeScript 5.9.3 |
| TypeScript | `.ts`, `.tsx`, `.mts`, `.cts` | TypeScript 5.9.3 |
| Java | `.java` | Tree-sitter Java |
| C# | `.cs` | Tree-sitter C# |
| Rust | `.rs` | Tree-sitter Rust |
| C++ | `.cpp`, `.cc`, `.cxx`, `.c++`, `.C`, `.h`, `.hh`, `.hpp`, `.hxx`, `.h++` | Tree-sitter C++ |
| PHP | `.php`, `.phtml` | Tree-sitter PHP |
| C | `.c` | Tree-sitter C |
| Bash/sh | `.sh`, `.bash` | Tree-sitter Bash; no POSIX conformance checking |
| PowerShell | `.ps1`, `.psm1`, `.psd1` | Tree-sitter PowerShell |
| HTML5 | `.html`, `.htm` | Tree-sitter HTML |
| CSS | `.css` | Tree-sitter CSS |
| GraphQL | `.graphql`, `.gql` | Tree-sitter GraphQL |
| XML | `.xml`, `.xsd`, `.xsl`, `.xslt`, `.svg`, `.plist`, `.storyboard`, `.xib` | Go standard XML decoder |
| Kotlin | `.kt`, `.kts` | Tree-sitter Kotlin |
| Swift | `.swift` | Tree-sitter Swift |
| Dart | `.dart` | Tree-sitter Dart |
| SQL | `.sql` | Tree-sitter SQL; dialect-dependent syntax |
| JSON | `.json` | Tree-sitter JSON |
| YAML | `.yaml`, `.yml` | Tree-sitter YAML |
| TOML | `.toml` | Tree-sitter TOML |

C++ headers, including shared `.h`, retain the C++ convention; lowercase `.c` selects C and uppercase `.C` selects C++. Selecting C alone does not select `.h`.
Grammar support is syntax-based, not a guarantee of every compiler extension.

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

Java package/type imports resolve only unique selected declarations. Java/C#/Rust/C++
calls resolve unique local functions and known static methods; Java imported
static methods can link selected files. C++ quoted includes and ordinary Rust
`mod` declarations link unique selected paths. No include search path or crate
root is guessed. Rust `use`/crate aliases and C# `using` directives are recorded
but their bindings remain unresolved. Instance dispatch, overload selection,
traits, macro expansion, conditional compilation and build configuration are
outside resolution. Ambiguous C++ casts/declarators withhold local resolution.
Anonymous bodies are omitted; constructors and macros remain
unresolved call sites. Overloaded declarations retain distinct location-suffixed
IDs; their line suffix can change when code moves. Syntax errors retain no partial
symbols. These limits apply to impact results too.


The additional code languages extract named types/functions/methods and explicit
imports/call sites. C resolves unique local functions unless parameters,
declarations or preprocessor syntax make that name uncertain. PHP, Bash,
PowerShell, Kotlin, Swift and Dart calls remain unresolved: command lookup,
namespace aliases, conditional definitions, overloads and receiver/extension
dispatch are not inferred. Literal confined PHP includes, shell sources,
PowerShell dot-sources, Dart imports, C includes and HTML/CSS resources can link
selected files. No include search path, package resolver or interpolation runs.

HTML/XML record element/attribute names and hierarchy, omitting attribute values,
text and inline script/style bodies. XML repeated elements have location-suffixed
IDs. CSS records rule scopes, selector atoms and property names; attribute-selector
values and declarations are omitted. No DOM/CSS matching is inferred. GraphQL
records schema types/fields, operations/fragments and unresolved type/fragment
references; SQL records named DDL objects/columns and unresolved table references.
Neither queries a service or database. JSON/YAML/TOML record keys, tables and array
item scopes, omitting scalar values; complex YAML keys/aliases/tags are not expanded.
Duplicate names remain distinct location-suffixed nodes. Identifier/key names and
explicit resource paths are visible data, so keep sensitive runtime files outside
the selected source roots. These formats share the existing graph, queries, impact
and reviewed symbol links; structural presence never certifies a solution.

An explicit knowledge record can add up to 16 symbol IDs, for example
`"symbols": ["src/context_pack.py::select_evidence"]`. `bd knowledge record`
checks those IDs against a current index and binds their source files/DOX.
`bd code query` returns associated human learnings and their separate validity.
Adding a symbol link is an explicit review; scan/rebuild never renews a stale
assertion. Closing a task does not create a learned solution automatically.

Limits are 4096 files, the shared UTF-8 reader's 16 MiB per-file bound,
32 MiB total input, 8 MiB decoded JSON per structure and 60 KiB per stored row.
A large single-file structure is stored in content-addressed 48 KiB fragments
with a bounded descriptor; reads verify every fragment and the complete length,
order and hash. The decoded limit still applies. Dense XML/SVG exceeding its bounded symbol
budget is retained with source/DOX hashes and an explicit `ASTLimit` parse error,
without partial symbols. Other selected files can still be indexed. A structure or manifest
exceeding a bound fails before publication; no partial scan is accepted. Queries default to 50 matching files and report omitted matches;
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
Clients predating version 3 refuse it explicitly; four- and eight-language clients also
refuse manifests containing languages outside their supported selection. Keep an old-compatible manifest
when downgrading: back up the previous derived manifest
before migration and restore it before reverting the binary. Human records and
immutable blobs remain separate. A binary predating fragmented file storage also
requires restoring its previous complete derived generation before downgrade.
Prune recognizes fragments and retains those referenced by the current files.
Each scan prepares bounded immutable blob rows through memoryops. Embedded
atomic publication commits all needed rows and the manifest together; other
adapters publish blobs before one atomic manifest. Failure leaves the prior
index readable. Old blobs are retained unless explicitly pruned, and Dolt history can grow; the
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
must succeed before publication begins; code publishes as one embedded atomic batch; catalog parts precede their atomic
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


## Impact and rename review

```bash
bd code impact src/context_pack.py --depth 8 --limit 200 --json
bd code impact 'src/context_pack.py::select_evidence' --json
bd code relink src/old.py src/new.py --json
```

Impact traverses inverse resolved imports/calls over the whole saved index,
terminates cycles and returns shortest static witness paths. Both file and symbol
selectors use conservative **file** granularity. Depth is bounded to 1–32; the
display limit is 1–1024, with omitted-file/depth-limit flags. Linked knowledge
retains its validity and verification scope; issue IDs are references, not task
changes. Conventional test paths and tests bound by associated learning are
candidates to inspect, never verified coverage or tests that were executed.
Unresolved references remain visible; dynamic/external/unindexed code can add
effects. Refresh or review stale source hashes before relying on a path.

Relink prepares JSON review drafts only when the old path is missing, the new
indexed file is current and parsed, and exactly one destination has identical
content in the same language. Prior learning must bind that original content;
named symbols must exist at the destination. Edited or ambiguous moves are
refused. Drafts include current source/DOX hashes, any new ancestor contracts,
recorded scope and the complete previous assertion/evidence for comparison.
Nothing is written. Read all affected sources/DOX, then explicitly review the
draft with `knowledge record --file -`; never submit an established topic as a
replacement proposal or treat a move as new test evidence.

## Derived-blob retention

```bash
bd code prune --readonly --json       # inspect current/obsolete payload bytes
bd code prune --apply --json          # explicit upgraded embedded writer only
```

Prune removes only content-addressed code blobs not referenced by the complete
current manifest. It refuses an unreadable current generation and never changes
human memories, assertions, proposals, journal, task state or source files.
The default is a plan; hooks, maintenance and viewers never apply it. Keep a
backup and upgrade every writing client first: mixed older writers and shared,
server or proxied application are not qualified.

The embedded optional AtomicMemories batch checks the expected manifest and
candidate values, then deletes in one existing transaction. Embedded scans use
the same capability to publish **all** needed blobs and the manifest together,
guarding the previous manifest and skipping equal rows. This prevents a reused
blob from being pruned between selection and publication. A changed generation
or any failed write rolls back; reread before trying a new operation. Other
adapters retain their original publication route and refuse prune application.
Readers of a retired generation can fail closed; refresh to the current one.

Dolt commits/history remain intact, including old complete generations for
rollback. This reduces live rows and future enumeration payload, not necessarily
filesystem size or version history. No automatic retention, history flattening
or engine garbage collection is performed. Restoring only an old manifest after
prune is insufficient: restore its full historical generation or rebuild it.
