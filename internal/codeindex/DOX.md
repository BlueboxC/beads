# DOX.md — Derived code index

## Purpose

Recover existing Python, Go, JavaScript and TypeScript structure and static references beside human knowledge in the same Beads Dolt.

## Ownership

Isolated language AST parsers, confined discovery, incremental file analysis, typed references, queries and native graph projection. CLI wiring belongs to cmd/bd; explicit assertions belong to knowledge.

## Local Contracts

- Use memoryops exclusively; no schema, second database, daemon, embeddings or model. Compressed content-addressed file rows and one published manifest use the existing memory table.
- Parse only supplied UTF-8 text: Python stdlib under operator Python `-I -S`, Go standard go/parser in-process, JS/TS through pinned TypeScript 5.9.3 in an operator-owned Node process with sanitized environment, native addons/global search disabled and bounded memory/output/time. The compiler VM has no filesystem/require/process host. Never import or execute project code, load tsconfig or install project packages; omit source bodies, literals and comments. Preserve bundled Apache-2.0 license/notices and official npm integrity provenance; expose `bd code --licenses`.
- Select explicit workspace-relative roots. Refuse symlinks and escapes; exclude hidden, generated, dependency, backup, runtime-data and operator-excluded subtrees. Bound input to 4096 files, 1 MiB per file and 32 MiB total; decoded JSON to 8 MiB and each stored TEXT row to 60 KiB.
- Reuse AST only when content and parser hashes match. Recompute cross-file references after every query; unresolved calls stay explicit. Lexical/import references are syntactic evidence, never runtime reachability. Resolve selected Go package declarations/imports and relative JS/TS imports only when unique; bind nearest confined go.mod as provenance. Preserve type-only imports structurally but never as calls. Shadowed require, duplicate exports/packages, unknown receivers, re-exports, dynamic dispatch, anonymous functions and Python lambda/comprehension bodies remain unresolved. Go top-level initializer calls and interface method signatures are not extracted; build tags and type checking are not evaluated.
- Parse in batches and store reference parts of at most 128 entries before replacing the manifest atomically (the embedded optional batch publishes all blobs and the manifest in one guarded transaction). Versions 1/2 remain readable; version 3 records languages and per-file parser fingerprints, reusing unchanged Python during expansion. Older clients refuse version 3; restore the prior derived manifest before a binary downgrade. Existing default Python and saved language selections remain stable. Cache repeated DOX snapshots per scan and detect exact-content renames with hash maps, avoiding cubic discovery cost. Interrupted publication retains the previous index. Old blobs remain available unless explicit qualified prune removes obsolete live rows; Dolt history still supports full-generation rollback. Never automatically delete concurrent generations. Limits fail explicitly rather than silently truncating the index.
- Index rebuilds never write assertions, task state or blockers. Unique exact-content rename matches are hints, not authority to renew learned evidence. Refresh checks source and DOX hashes plus file discovery without AST parsing or writes. The explicit maintenance caller may allow missing roots: omit removed files from the derived generation while retaining roots, so returning branch files are rediscovered. Ordinary explicit scan still refuses missing roots; incomplete/error/escaping discovery is never accepted as a removal.
- Symbol-link validation loads the manifest and all bounded reference parts, then only selected file blobs (at most 16 paths). Validate routing digests/unique paths, selected blob/path/language identity, the linked file’s parser fingerprint and current source/DOX/go.mod hashes; unrelated language-parser drift does not invalidate a current version-3 file; skip unrelated blobs and discovery. Unrelated index availability is not implied. No parsing or writes occurs during validation.
- Impact walks inverse static file dependencies with cycle-safe breadth-first traversal, witness paths, explicit display/depth bounds, unresolved-reference counts and source-backed knowledge/test candidates. Symbol selectors remain conservative file scope; no runtime or test coverage is inferred.
- Relink is readonly preparation for one missing old path and one unique identical-content destination of the same language. Require current parsed symbols and matching prior learning hashes; bind new DOX ancestors in recorded-scope drafts, return the original assertion intact and refuse edited/ambiguous moves. Explicit review remains necessary.
- Explicit prune plans select only obsolete content-addressed code blobs. Apply requires guarded atomic memory batches and upgraded embedded writers; current manifest/blobs, human planes and Dolt history remain intact. Atomic Save publishes all needed blobs and the manifest together under a prior-manifest guard, skipping equal values inside storage. Other adapters retain existing publication but cannot prune. Mixed older writers are unqualified; never automate pruning, flatten history or rewrite claims.
- Native graph snapshots share graphview, show symbols, file provenance and explicit learned-at links, and keep code cycles independent of task blockers.

## Work Guidance

Measure the chosen pilot's cold scan, unchanged incremental scan, query time and stored payload separately from Dolt history. Keep source-index maintenance operator-started, through scan or maintain; hooks expose only a bounded snapshot summary and query route.

## Verification

- AST scenarios cover all four languages, relative imports, cycles, lexical/require shadowing, ambiguous declarations/exports, type-only imports, static versus unknown receivers, syntax errors and non-execution.
- Incremental rebuild/rename, contract drift, interrupted publication and corrupt data checks preserve human entries.
- Confined discovery reports excluded sources, syntax errors and refused symlinks; malformed source excerpts do not enter errors.
- A 1031-file regression verifies batched persistence, version-1 migration, interrupted publication, missing/corrupt parts and unchanged reuse without Python.
- A real CLI process check owns persistence, knowledge-symbol linking and prime projection.

## Child DOX Index
