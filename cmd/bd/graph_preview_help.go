package main

import "github.com/spf13/cobra"

// Cobra renders help before graph workspace admission. Keep the ordinary help
// intact and add an explicitly scoped section for commands reused by the graph
// preview, so their legacy descriptions do not mislead graph operators.
func init() {
	// These commands have long ordinary-mode descriptions. Put the graph
	// distinction first so help does not lead graph users to turn on legacy
	// recording or expect Dolt commits and unsupported flags.
	versionsCmd.Short = "List retained graph Resource versions or recorded ordinary Bead versions"
	versionsCmd.Long = `Graph preview workspaces:
Use bd versions ID to list one Memory, Issue or Link's retained versions newest
first. Bare ID means beads/ID; use links/PATH for a Link. Every new graph
Resource has a creation version; do not enable ordinary versioned-history
recording for this command. Cite the opaque version token with bd show ID
--version TOKEN or bd compare ID --from TOKEN --to TOKEN. The store-local
local_revision only orders versions within this store. --json names that field
local_revision, matching ordinary bd versions. BDP HTTP History is unavailable.
A removed Link lists only its prior citable versions; deletion adds no version.

Ordinary Issue workspaces:
` + versionsCmd.Long
	historyCmd.Short = "Show graph Resource versions or ordinary Issue commit history"
	historyCmd.Long = `Graph preview workspaces:
bd history ID is an alias for bd versions ID. It lists retained Memory, Issue
or Link versions newest first; bare ID means beads/ID and Link IDs use
links/PATH. Use the opaque token for exact reads, not local_revision.
--limit and --events are not supported by the graph alias. BDP HTTP History
is not available.

Ordinary Issue workspaces:
` + historyCmd.Long
	deferCmd.Long = `Graph preview workspaces:
Use bd defer ID... to defer one or more Issues. --until accepts ordinary bd
date and relative-time forms; an undated defer stays deferred until restored.
--reason appends to Issue notes. A later bd ready wakes due Issues, recording
one native and graph version per changed Issue. A repeated dateless defer is a
no-op when neither date nor notes change. Use --if-revision TOKEN for a
single-Issue stale-write check or --unconditional to spell out the default.

Ordinary Issue workspaces:
` + deferCmd.Long
	undeferCmd.Long = `Graph preview workspaces:
Use bd undefer ID... to restore one or more deferred Issues to open. It also
clears a stale defer date on a non-deferred Issue. A repeated undefer is a
no-op if nothing changes. Use --if-revision TOKEN for a single-Issue
stale-write check or --unconditional to spell out the default.

Ordinary Issue workspaces:
` + undeferCmd.Long
	for _, entry := range []struct {
		cmd  *cobra.Command
		text string
	}{
		{initCmd, `Use bd init --graph-mode link --scope-url URL in a fresh workspace.
The Scope URL names local identities; it does not start a web server.
--server --external selects an ordinary shared Dolt server; otherwise storage
is embedded. Existing .beads directories are never adopted or overwritten.`},
		{rememberCmd, `Store a Memory with bd remember 'Policy text' [--id policy]
[--title 'Policy'] [--metadata '{"team":"docs"}']. Alternatively,
--properties '{"title":"Policy"}' initializes the typed Memory property;
do not supply the same field with both --properties and a shorthand flag.
Metadata is a JSON object separate from Memory title/body properties. An omitted ID is generated; an omitted creation title
summarizes the body. With --id, an unused ID creates and an existing Memory
updates in place, as ordinary bd remember --key does. Bare policy means
canonical beads/policy. Graph Memories use canonical IDs, not legacy keys.

To refuse any previously allocated ID, add --create-only with --id.
To require an existing Memory, keep using --update instead of --id:
  bd remember 'Revised policy' --id policy
  bd remember 'Revised policy' --update policy
  bd remember --update policy --title 'New title'
Omitted fields remain unchanged on update. Use --body-file PATH or --stdin
instead of positional body text. An existing-ID update accepts the current
revision by default; add --if-revision TOKEN to reject a stale update. Read
the token with bd show policy --json.`},
		{memoriesCmd, `Search Memory titles and bodies with bd memories [SEARCH].
Use --all for a complete bounded result, --details for version/Link counts,
or --format records-json for machine-readable summaries. --json is unavailable;
use bd recall ID for the exact body.`},
		{recallCmd, `Use bd recall ID for one exact Memory body. --version TOKEN
selects a retained body; use bd show ID --json for the record. Graph
recall does not accept legacy keys or --json.`},
		{createCmd, `Create an Issue by default, with an optional --id ID.
--metadata accepts one JSON object on Issue or Memory creation; omitted
metadata is {}. Metadata is separate from Type-validated properties.
--properties JSON initializes the selected Type's writable fields, such as
Issue description or Memory body. It may accompany shorthand flags only when
they supply different fields; duplicate fields refuse.
Use --bead-type types/preview-memory-v2 to create a Memory instead:
  bd create --bead-type types/preview-memory-v2 --id policy --body 'Code flow policy'
Use bd types to see Bead Types installed in this workspace. Both types/NAME
and full local Type URLs work. --type remains the Issue classification
(for example task or bug), not the Bead Type. Memory creation
accepts body/description/message and an optional title; unsupported Issue-only
fields refuse.`},
		{showCmd, `Use bd show ID (equivalent to beads/ID) for a current Memory or
Issue; use bd show links/ID for a Link. --version TOKEN selects one exact
retained record; this is not
an ordered history listing. --json returns the experimental graph record.
Showing an Issue makes it the last-touched Issue for interactive update/close;
showing a Memory or Link does not.`},
		{updateCmd, `Use bd remember --update ID for selected Memory title/body
edits. --properties JSON shallowly merges named top-level properties on an
Issue, Memory, or informational Link, preserving omitted keys. The same flag
initializes properties on bd create and bd remember. For example:
  bd update policy --properties '{"body":"Revised text"}' --if-revision TOKEN
An empty object is a no-op. --patch applies ordered add/replace/remove property
operations, including on writable native Issue scalar properties.
--metadata merges a JSON object's top-level keys; --set-metadata KEY=VALUE
sets one typed JSON value and --unset-metadata KEY removes one key. Set and
unset may combine (unset wins); --metadata cannot combine with either.
Metadata may accompany a property or Issue scalar edit atomically, and a
metadata-only update uses the same Resource and owning-source guards.
Generic updates accept the current state when --if-revision is omitted.
Informational Links owned by a Memory may also use --if-source-revision TOKEN;
without it, the current source is
accepted. Blocking Dependency properties are not editable here. Issue scalar
edits and standalone --claim are separate graph operations. Without an ID,
interactive Issue update uses the last-touched Issue; scripts require an ID
unless BD_LAST_TOUCHED_FALLBACK=1 explicitly enables the fallback.`},
		{deleteCmd, `For an unreferenced Memory, bd delete ID previews the
deletion without writing. Apply with --force and either --if-revision TOKEN or
--unconditional. Referenced Memories refuse; graph deletion does not cascade.`},
		{forgetCmd, `Use bd forget ID to delete one unreferenced Memory now.
Supply --if-revision TOKEN or --unconditional. Canonical IDs are retained and
incident Links prevent deletion; no cascade is performed.`},
		{depCmd, `Use bd dep BLOCKER --blocks BLOCKED to create a blocking
Dependency between two live Issues. Bare bd dep prints help; the ordinary
--no-cycle-check option is unavailable in graph preview workspaces.`},
		{depAddCmd, `Use bd dep add issue blocker for a blocking
Dependency between two live Issues, optionally with --id links/ID.
bd dep blocker --blocks issue is the same operation with reversed arguments.
Memory endpoints, remote routing and bulk dependency flags are unavailable
in this preview.`},
		{linkCmd, `Without --link-type, bd link SOURCE TARGET creates the ordinary
blocking Dependency between two live Issues. For a Memory or Issue endpoint,
choose an installed informational Type, for example on a fresh workspace:
  bd link policy work --link-type types/example-cites
Use bd types to see Link Types installed in this workspace. --link-type
accepts types/NAME or a full local Type URL. An optional --id
selects a bare Link ID or links/PATH; --properties supplies informational Link properties.
Memory-owned Links accept the current source by default, or use
--if-source-revision TOKEN to reject a stale source. --metadata JSON supplies
an initial open metadata object for informational Links; blocking Dependencies
do not accept it. --unconditional-source
explicitly selects the default. The blocking Type types/preview-blocks-v1
requires Issue endpoints and, unlike informational Types, one of
--if-source-revision TOKEN or --unconditional-source.`},
		{closeCmd, `Close one or more local Issues by ID or beads/ID. One --reason
applies to all IDs; repeat it once per ID for positional reasons, or use
--reason-file PATH for literal file content. The done alias accepts a trailing
positional reason. A batch reports successful Issues on stdout and per-Issue
failures on stderr, then exits nonzero if any failed. --force bypasses pinned,
holder, blocker and open-child policy; --session (or CLAUDE_SESSION_ID) records
the closing session. On one Issue, --if-revision TOKEN from bd show --json
rejects a stale close, including an already-closed retry. It cannot be used
with multiple IDs, --suggest-next or --claim-next. Without it, close accepts
the current revision. On one Issue, --suggest-next lists Issues that closing it
newly unblocks, without claiming them. --claim-next atomically claims the
highest-priority ready Issue when at least one close lands; an already-closed
retry earns no new claim. A mixed batch keeps its successful closes and claim
even when another ID refuses. Without an ID, interactive close uses the
last-touched Issue; scripts require an ID unless BD_LAST_TOUCHED_FALLBACK=1
explicitly enables the fallback. Gate evaluation, --continue, molecule
advancement and remote-routing forms remain unavailable.`},
		{reopenCmd, `Reopen one or more local closed Issues by ID or beads/ID,
optionally with --reason. A batch reports changed Issues on stdout and
per-Issue failures on stderr, then exits nonzero if any failed. Already-open
Issues remain unchanged. Remote-routing forms remain unavailable.`},
		{unclaimCmd, `Use bd unclaim ID... to release one or more assigned open or
in-progress Issues. By default only the current holder may release a claim.
--force bypasses holder authorization but still respects the native row CAS;
--if-assignee HOLDER releases only while that holder remains assigned. The two
flags cannot be combined. --reason TEXT adds a native Issue comment after a
successful release; bd comments ID reads it. The release clears the assignee,
lease and started time, returns the Issue to open, and records one Issue and
graph version. A repeated release refuses without creating a version.`},
		{commentsCmd, `Use bd comments ID to read the native comment feed for a
graph Issue. bd unclaim --reason appends there. Comments are outside the
retained Issue snapshot and do not mint an Issue version. Comment creation
through bd comments add is not available in this preview.`},
		{readyCmd, `Show current ready Issues with admitted priority, type, label,
assignee, sort and deferred-state filters. --claim atomically claims the first
matching ready Issue. This graph preview refuses positive BEADS_MAX_ROWS
instead of truncating; parent, molecule, ephemeral and metadata filters remain
unfinished.`},
		{listCmd, `Without Issue filters, bd list reads one bounded snapshot of current Beads
of all installed Bead Types and lists every Memory and every Issue the
ordinary bd list would show, newest recorded change first. Closed and pinned
Issues are hidden unless --all is given; --all also removes the row limit.
Each human row shows the local ID and kind; an Issue row also shows its
status and priority. Use --bead-type types/NAME to narrow by Bead Type.
--limit returns a prefix with hasMore, not a continuation cursor. A positive
BEADS_MAX_ROWS refuses a page of more Beads than that.
Issue-specific filters (--status, --type, --title, --title-contains,
--priority, --priority-min, --priority-max, --assignee, --no-assignee,
--label, --label-any, --exclude-label, --pinned, --no-pinned, --due-before,
--due-after, --overdue, --sort, --reverse) or a matching configured
directory label switch to the existing Issue query: Issues only, closed and
pinned Issues omitted unless --all or a filter selects them, quoted rows
with status and priority, and a line under the header saying Memories are
not listed. A typed filter cannot be combined with a non-Issue --bead-type;
a configured directory label alone does not refuse one, it is simply not
applied to Memories. Tree output and legacy --json are unavailable.`},
		{blockedCmd, `Show current dependency-blocked Issues with canonical
blocker IDs. Filters and positive BEADS_MAX_ROWS are unavailable.`},
		{graphCmd, `Use bd graph ID --view generic to traverse the current
local graph. Control direction, depth, node and Link bounds with --direction,
--depth, --max-nodes and --max-links. Legacy visualization modes are unavailable.`},
		{statusCmd, `Use bd status --graph to inspect this workspace's supported
graph capabilities and limits. Ordinary issue-statistics mode is unavailable.`},
		{typesCmd, `Use bd types to list the Bead and Link Types actually installed
in this workspace, grouped by category. The displayed types/NAME IDs are
accepted by --bead-type and --link-type. Add --details to show each complete
persisted Type descriptor. --json returns the full descriptors as structured
data. Legacy Issue classifications such as task and bug belong to --type;
--sections is unavailable in graph preview workspaces.`},
	} {
		entry.cmd.Long += "\n\nGraph preview workspaces:\n" + entry.text
	}
}
