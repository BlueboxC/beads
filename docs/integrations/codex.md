---
title: Codex
description: Set up beads for Codex with the beads skill, a managed AGENTS.md section, and native hooks that survive compaction
---

Use Beads with Codex through the `beads` skill, managed `AGENTS.md` guidance, and native Codex hooks.

```bash
bd setup codex
bd setup codex --check
```

Project setup writes:

- `.agents/skills/beads/` for the Beads skill.
- `AGENTS.md` with a managed Beads section.
- `.codex/config.toml` with `[features].hooks = true`.
- `.codex/hooks.json` with the Beads hook fallback.

`bd init` runs this project setup by default unless `--skip-agents` or `--stealth` is used. Global setup uses `bd setup codex --global` and writes under `$CODEX_HOME` when set, otherwise `~/.codex`.

Codex 0.129.0+ supports `/hooks`, compact lifecycle hooks, and hook-provided developer context. Beads uses that lifecycle to inject `bd prime` on session start and recover context after compaction. Use `/hooks` to inspect or toggle the installed handlers.

## Hook Lifecycle

- `SessionStart` (`startup|resume|clear|compact`) injects full `bd prime` output and satisfies any pending refresh for that session and workspace.
- `PreCompact` (`manual|auto`) checks `bd prime --memories-only` and warns if Beads context is unavailable.
- `PostCompact` (`manual|auto`) records that the session needs a Beads refresh.
- `UserPromptSubmit` injects full `bd prime` when a refresh is still pending, then clears the marker after successful delivery.

Codex runs `SessionStart` with `source: "compact"` before the next model request, including an automatic compact in the middle of a turn. This configures Beads context recovery for the immediate continuation; qualify an actual context-limit-triggered compact in your Codex environment separately from handler/process tests. The post-compact marker retains the next-prompt fallback when delivery is unavailable; successful SessionStart delivery consumes it, preventing a duplicate refresh. Compact hooks do not inject context themselves. An empty result, prime failure or output failure leaves the marker pending for retry.

Refresh markers are stored in a user cache/temp directory keyed by Codex `session_id` and workspace path. They are not written to tracked files or to the Beads database.

The Beads Codex plugin stores hooks at `plugins/beads/.codex-plugin/hooks/hooks.json` and declares them in `plugins/beads/.codex-plugin/plugin.json` as `"hooks": "./.codex-plugin/hooks/hooks.json"`. Without the plugin, `bd setup codex` installs the same hook config in `.codex/hooks.json` and enables `[features].hooks = true`.

## Observed Activity

This fork adds three separate command handlers. They run only after normal Codex trust and project-local enablement:

```bash
bd setup codex --global
bd activity enable
bd activity status
bd activity list --session <session_id> --turn <turn_id>
bd activity disable
```

`PostToolUse` journals delivered supported local tool calls: session, turn and call IDs, tool/program, payload hashes, clean reported paths and structured exit/error observations. Unknown or running outcomes remain explicit. It does not retain raw commands, tool responses, user prompts or transcript contents. Shell-script file mutations are not inferred. Hosted or specialized tools, interrupted calls and undelivered hooks may be absent.

`Stop` saves a bounded redacted final assistant handoff as a **reported** statement. `SessionEnd` appends a closure marker with a shorter budget; a timeout can omit that marker. These handlers return advisory output without blocking tools or continuing a completed turn. Capture is synchronous and bounded (25 seconds; 2 seconds for SessionEnd), while normal events typically finish much sooner. Missing/malformed identities, readonly mode, foreign workspaces and disabled settings withhold writes.

Events use reserved @activity/ entries in the same embedded project Dolt. Identical retries skip writes; disabling retains prior events. No project is initialized by the hooks. No new database, background service, transcript reader, embedding provider, remote publication or automatic semantic promotion is installed. Server/proxied workspaces are unsupported. Journals remain private and are not a complete compliance audit trail. Redaction is best effort.

`bd prime` quotes at most three recent reported handoffs separately from established knowledge and points to the journal. Read current DOX, plans, accepted solutions and evidence before proceeding. Explicit source inspection and `knowledge propose/review` still govern durable semantic learning. Capture does not close tasks, accept proposals or renew evidence. Existing historical chats are not imported.

New activity hook definitions need their own normal trust review in `/hooks`; enabling activity does not trust them. The four existing context handlers retain their read-only behavior. Keep the CLI, plugin and fallback definitions aligned; avoid duplicate installs.

## Manual Fallback

If you manage `.codex/hooks.json` by hand instead of running `bd setup codex`, the equivalent shape is:

```json
{
  "hooks": {
    "SessionStart": [
      {
        "matcher": "startup|resume|clear|compact",
        "hooks": [{ "type": "command", "command": "bd codex-hook SessionStart", "statusMessage": "Loading Beads context" }]
      }
    ],
    "PreCompact": [
      {
        "matcher": "manual|auto",
        "hooks": [{ "type": "command", "command": "bd codex-hook PreCompact", "statusMessage": "Checking Beads context" }]
      }
    ],
    "PostCompact": [
      {
        "matcher": "manual|auto",
        "hooks": [{ "type": "command", "command": "bd codex-hook PostCompact", "statusMessage": "Scheduling Beads context refresh" }]
      }
    ],
    "UserPromptSubmit": [
      {
        "hooks": [{ "type": "command", "command": "bd codex-hook UserPromptSubmit", "statusMessage": "Refreshing Beads context" }]
      }
    ],
    "PostToolUse": [{ "hooks": [{ "type": "command", "command": "bd codex-activity PostToolUse", "statusMessage": "Recording Beads operation", "timeout": 30 }] }],
    "Stop": [{ "hooks": [{ "type": "command", "command": "bd codex-activity Stop", "statusMessage": "Recording Beads turn handoff", "timeout": 30 }] }],
    "SessionEnd": [{ "hooks": [{ "type": "command", "command": "bd codex-activity SessionEnd", "statusMessage": "Recording Beads session closure", "timeout": 3 }] }]
  }
}
```

Then ensure `.codex/config.toml` enables:

```toml
[features]
hooks = true
```
