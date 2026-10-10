---
title: Gates
description: Async waits for a human decision, timer, GitHub run/PR or optional GitLab pipeline/MR.
---

Some workflow steps can't proceed on code alone: a release needs CI to go
green, a deploy needs a human sign-off, a cleanup should wait 24 hours. A
**gate** is an issue that represents that wait. It blocks a step the same way
any blocker does — the step leaves the ready frontier until the gate closes —
so agents never need to poll or spin.

## How a gate works

A gate is a bead like any other: created open, it blocks its waiters through
a normal dependency edge, and the step becomes ready the moment the gate
closes. Gates close in one of two ways:

- **Manually** — `bd gate resolve <gate-id>` or the equivalent `bd close`.
  Human gates are never automatically resolved by `gate check`.
- **Via `bd gate check`** — evaluates open timer, GitHub, GitLab and bead gates
  against the real world and closes the ones whose condition is met.

```bash
bd gate list                 # open gates
bd gate list --all           # include closed
bd gate show <gate-id>       # details and waiters
bd gate check                # evaluate open gates, close satisfied ones
bd gate check --dry-run      # report without closing
bd gate resolve <gate-id>    # close a gate manually
```

## Optional human resolver policy (BlueboxC fork)

Human gates are workflow records. By default, any caller can resolve them.
To reduce accidental self-approval, an operator can install a workspace policy:

```bash
bd config set gates.human.resolvers '["reviewer"]'
bd --actor reviewer gate resolve <gate-id> --reason "Reviewed the evidence"
# Remove the opt-in policy:
bd config unset gates.human.resolvers
```

Names are exact and case-sensitive. A listed actor must be explicit: `--actor`,
`BEADS_ACTOR`/`BD_ACTOR`, or a named API caller. Git, user and saved actor defaults
are refused. `[]` refuses everyone; malformed, null or blank values refuse
protected operations. An unreadable policy does not silently disable checks.
The policy is read inside the existing mutation transaction; no schema, hook
command or second database is needed.

The rule covers human gate closure, status/type/defer/persistence edits,
deletion including cascades, removal or metadata replacement of blocking and
parent-child edges targeting a human gate, and forced closure/status completion
of a directly gated task. It applies through shared storage, domain/UOW, batch
and HTTP mutation roles. Ordinary notes and unrelated tasks remain editable.
Atomic batches roll back on a refusal; best-effort close batches report refused
gates while allowing independent items to close. `--force` cannot waive this rule.

This is a caller-asserted allowlist, not proof that a person approved anything.
A caller can impersonate a listed name or change the policy if given those
permissions. The invoking system must control actor/configuration access,
imports, SQL, sync and restoration. The policy does not authorize deployments,
orders or other external actions, and it does not infer approval from `ready`.
It is not enabled automatically in existing projects.

## Gate types

| Type | Waits for | Closed by |
|------|-----------|-----------|
| `human` | a manual decision | `bd gate resolve` or `bd close`; optional resolver policy above |
| `timer` | a duration after gate creation | `bd gate check` once the timeout elapses |
| `gh:run` | a GitHub Actions workflow to complete successfully | `bd gate check` (uses `gh run view`) |
| `gh:pr` | a pull request to merge | `bd gate check` (uses `gh pr view`) |
| `gl:pipeline` | a GitLab pipeline to succeed | `bd gate check` (uses `glab api` GET) |
| `gl:mr` | a GitLab merge request to merge | `bd gate check` (uses the project-local MR IID) |
| `bead` | a bead to close — a plain ID names a bead in this rig, the cross-rig form is `<rig>:<bead-id>` | `bd gate check` for plain local IDs; cross-rig values cannot be checked — resolve those manually |

Timeouts use Go duration syntax: `30m`, `1h`, `24h` (there is no `d` unit —
write `24h`, not `1d`).

GitHub gates use the current Git repository by default. To evaluate a PR or
workflow run in another repository, set the gate's string `metadata.repo` value
to `OWNER/REPO` or `HOST/OWNER/REPO`. An ad-hoc `gh:run`/`gh:pr` gate created
with `bd gate create` inherits a valid `metadata.repo` value from the issue it
blocks; `human`/`timer`/`bead` gates do not, since `metadata.repo` is
unrelated, ordinary metadata for those types. `bd gate check` rejects
malformed repository values instead of falling back to the current
repository.

### GitLab gates (BlueboxC fork)

Use an operator-installed, authenticated `glab`. Beads delegates host selection
to `glab`, including configured self-hosted instances whose names do not contain
`gitlab`; it does not install the CLI or store another token. Reads use explicit
GET requests, a 30-second subprocess deadline and a 1 MiB response limit.

```bash
bd gate create --type=gl:pipeline --blocks bd-abc --await-id=12345
bd gate create --type=gl:mr --blocks bd-abc --await-id=42
bd gate check --type=gl --dry-run
bd gate check --type=gl
```

`metadata.repo` is an optional `group/project` or nested `group/subgroup/project`
path on the currently selected GitLab host. Ad-hoc GitLab gates inherit it from
the blocked issue; absent or empty values use glab's current repository. Malformed
values are rejected. Pipeline IDs are global numeric IDs; MR numbers are **IIDs
within the project**, not the API's global MR `id`.

Only pipeline `success` and MR `merged` resolve a gate. Pipeline `failed` or
`canceled` and MR `closed` report escalation through the existing gate flow.
Manual, skipped, pending and unknown states do not resolve. Authentication,
transport, missing CLI, malformed output, missing state and ID mismatches leave
the gate open and report an error; these are not successful checks.

For an empty pipeline await ID, run:

```bash
bd gate discover --type=gl:pipeline --dry-run
bd gate discover --type=gl:pipeline
```

Discovery uses the current checkout's branch and exact HEAD SHA, validates the
returned selectors and pins the highest matching ID from at most 100 candidates.
A detached checkout, another branch or an explicit foreign project needs an
explicit pipeline ID. GitHub discovery remains the default. Discovery stays
unavailable in proxied-server mode; dry-run does not write an ID or close a gate.
Provider tests use fixtures and a simulated glab process; they do not establish
live hosted/self-hosted credentials or every deployed GitLab version.

### Known limitations: multi-rig and proxied-server topologies

Bead gates and prefix routing (`routes.jsonl`) do not work in every
topology. Three limitations to expect, all tracked in
[#5861](https://github.com/gastownhall/beads/issues/5861):

- **Cross-rig bead gates never resolve on their own.** A `<rig>:<bead-id>`
  await value reports `cannot be checked (multi-rig routing removed)` and
  stays pending regardless of the awaited bead's status. Close it with
  `bd gate resolve`.
- **`bd close` cannot verify a bead gate in the experimental proxied-server
  mode.** Proxied-server commands never open a local store, so closing the
  gate refuses with `no local store available` even when the awaited bead is
  closed. Run `bd gate check`, which evaluates bead gates in proxied-server
  mode and closes the satisfied ones, or `bd close --force`.
- **Prefix routing cannot open a proxied-server target rig.** A routed
  lookup into a rig that is itself in proxied-server mode fails with
  `proxy server store needs to be uow provider`, so an all-proxied
  shared-server topology cannot resolve routed targets at all.

## Gates in formulas

A formula step declares a gate with a `[steps.gate]` block. When the formula
is instantiated, bd creates the gate issue and wires it as a blocker of that
step. The schema has five fields: `type`, `id`, `await_id`, `timeout`, and
`repo`.

This is the release gate from beads' own release formula — the step that
waits for the GitHub release workflow:

```toml
[[steps]]
id = "wait-for-ci"
title = "Wait for release workflow"

[steps.gate]
type = "gh:run"
id = "release.yml"       # which workflow to watch
timeout = "30m"          # escalate if it takes longer
```

For a `gh:run` or `gh:pr` gate that watches another repository, set `repo`
the same way a `metadata.repo` value works for an ad-hoc gate — `OWNER/REPO`
or `HOST/OWNER/REPO`. Malformed values are rejected when the gate is checked:

```toml
[[steps]]
id = "wait-for-downstream"
title = "Wait for downstream release"

[steps.gate]
type = "gh:run"
id = "release.yml"
repo = "org/downstream-repo"   # check gh:run against this repo, not the current one
```

`repo` accepts a `{{var}}` placeholder (e.g. `repo = "{{gate_repo}}"`); for a
formula persisted with `bd cook --persist`, the placeholder is substituted
when the proto is later poured with `bd mol pour --var gate_repo=...`, the
same as `title`, `description`, and `await_id`.

`bd gate discover` (auto-discovery of a `gh:run` gate's run ID) requires a
workflow name hint (`await_id`/`id`, not left blank) for a gate targeting
another repository — without one, the local commit/branch heuristics that
narrow a same-repo match don't apply across repos, so nothing but the
workflow name can identify the right run. A cross-repo gate discovery also
ignores the local checkout's branch unless `--branch` is passed explicitly;
an auto-detected local branch has no relationship to the target repo's
branches.

A human sign-off gate:

```toml
[[steps]]
id = "approve-deploy"
title = "Human approves the deploy"

[steps.gate]
type = "human"
```

And a cooling-off timer:

```toml
[[steps]]
id = "wait-24h"
title = "Let the release bake"

[steps.gate]
type = "timer"
timeout = "24h"
```

Verify what the parser actually understood before pouring — unknown keys in
TOML are dropped silently:

```bash
bd formula show <formula> --json   # inspect the parsed gate blocks
```

## Creating gates outside formulas

`bd gate create` attaches a gate to existing work:

```bash
# Block bd-abc until a PR merges
bd gate create --type=gh:pr --blocks bd-abc --await-id=42

# Block bd-abc until a human resolves the gate
bd gate create --type=human --blocks bd-abc --reason "Design sign-off"

# Add another waiter to an existing gate
bd gate add-waiter <gate-id> <issue-id>
```

## Fan-in: waiting on other steps

Waiting on *other steps* is not a gate — it's a dependency. Use `needs` to
fan in on named steps, and `waits_for` when a step must wait for
dynamically-created children:

```toml
[[steps]]
id = "merge-results"
title = "Merge results"
needs = ["test-a", "test-b"]     # fan-in on named steps

[[steps]]
id = "summarize"
title = "Summarize all spawned work"
waits_for = "all-children"       # or "any-children", or "children-of(step-id)"
```

## Working with gated molecules

```bash
bd ready --gated        # molecules where a gate just closed (ready to resume)
bd blocked              # what's waiting, and on which gates
```

Automation patterns: run `bd gate check` on a schedule (cron, CI, or an
orchestrator loop) so timer and GitHub gates close without a human in the
loop; keep `human` gates for the decisions that should never auto-close.
