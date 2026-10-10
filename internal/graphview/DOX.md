# DOX.md — Native graph viewer

## Purpose

Render task dependencies, source-backed knowledge and code references with the same native Beads D3 viewer.

## Ownership

HTML template, presentation data, overview file/symbol drilldown and bundled D3 7.9.0 with its license. Callers own database queries and relationship semantics.

## Local Contracts

- Offline exports make no database connection, model call or network request. Only the served bootstrap offers same-origin `/viewer/graph` queries. Automatic mode polls every five seconds while visible; inactive tabs pause, reconnects resynchronize, errors retain the last snapshot with an explicit disconnected state. Manual mode queries only through Actualizar grafo or explicit authentication; switching to manual stops future timers/retries/visibility queries, while an existing request may finish. The refresh button works in both modes without resetting the view. Automatic is the initial default; only the chosen mode may persist in browser storage, with an in-page fallback if storage is unavailable. Offline exports hide these controls. Neither mode starts a server or maintains the code/document index. Tokens stay in browser RAM, never URL or storage.
- Live refresh preserves existing positions, zoom, filters, labels, selection and navigation; defer updates during dragging. New nodes get a stable initial position without restarting physics. Removed selections are reported. Reset uses saved initial positions for the current node set.
- The live HTTP adapter coalesces concurrent viewers into one read per five-second interval, with a 30-second query deadline, ETags and an in-memory presentation cache. Failed reads return unavailable, never stale success. The public bootstrap contains no workspace data.
- Graph Preview project callers reuse this viewer with a distinct Memory color and canonical record/version details. Canonical Link details retain their typed identity; projected code/provenance edges remain presentation evidence.
- Preserve native task status colors. Knowledge colors identify node types; source validity and evidence scope do not imply task completion or qualification.
- Share zoom, drag, search, selection and readable evidence inspection across graph exports. Overview filters, relationship navigation, back navigation and file/symbol lists use the exported snapshot. Local source links are constructed only from confined relative paths beneath the supplied workspace; show recorded line/hash and validity, never execute source.
- Project families use explicit path/module scopes and task parents for presentation only. Keep families separated, label their boundaries and freeze positions after initial layout. Dragging a family header or principal node translates its members together without restarting physics; Shift drags only that node. Directory yellow remains distinct from module green.
- Fit View only adjusts framing. Reset View restores every node to its saved initial position, redraws family boundaries and edges, cancels active drags, clears selection/filters and restores labels before fitting the full graph. Never rerun physics to reset.
- Keep the legend narrow (185 px) and natively collapsible by pointer or keyboard. Closing leaves only its header, freeing the graph for interaction. Scroll only the expanded content; keep the header reachable. Live updates replace content without reopening the legend or altering the graph view.
- Insert stored strings through JSON escaping and DOM text operations; never execute document contents.
- Keep D3 pinned to 7.9.0, SHA-256 f2094bbf6141b359722c4fe454eb6c4b0f0e42cc10cc7af921fc158fceb86539, and preserve the bundled license in exported HTML.

- Project snapshots include the latest 1,000 valid observed events, total/invalid counts and capture setting from the same readonly memory read. Historial filters sessions, turns, event types and text; switching views preserves graph layout, zoom, filters, navigation and collapsed legend. Both views share existing manual/automatic refresh. Display limited coverage and reported scope; never infer successful repairs from exit codes or summaries.
- Selected events prepare a visible, copyable request for Codex to inspect explicit sources/current DOX and existing solutions, prepare a pinned draft, and submit only a pending proposal. Clipboard refusal leaves selectable text; refresh retains the selected request. A separate download preserves the private review packet. Viewing/downloading never submits or accepts a proposal, executes commands or introduces an HTTP write route. Proposal-origin navigation retains missing/out-of-window origin references explicitly.
- Supervised proposals have their own color/type and inspectable status, source validity and review evidence. Proposed provenance, supersession and explicit acceptance relations remain separate from task blockers. Viewing never reviews or promotes proposals.

## Work Guidance

Reuse the native viewer; do not create a parallel presentation or storage system for knowledge. Keep task blockers distinct from provenance, related-record and cyclic code edges. Code symbols have typed colors and source/line/hash details.

## Verification

- Renderer checks preserve JSON data and errors, escape hostile strings and embed every browser dependency.
- Live checks cover read coalescing, 304 responses, failed-query recovery and public-shell confinement. HTTP transport owns Host/auth controls and exclusive route registration; the CLI owns workspace pinning and store lifetime.
- Browser qualification checks real exports for typed colors, drag, zoom, search and node evidence with networking disabled; project exports also cover separated families, stable group/individual drag, initial-position reset distinct from fit, filters, relationship/back navigation, file/symbol source locations, and compact legend collapse/expand with keyboard and live-update preservation, and manual/button/automatic refresh including stored mode, idle-tab behavior and retained layout/selection.

## Child DOX Index
