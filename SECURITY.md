# Security policy for the BlueboxC fork

This policy covers `codex/project-continuity` in BlueboxC/beads. The fork
preserves the upstream MIT attribution; upstream releases and security decisions
remain owned by [gastownhall/beads](https://github.com/gastownhall/beads).

## Reporting

Do not publish exploit details, credentials or private project data in an issue
or pull request. Arrange a private reporting channel with the fork owner
[BlueboxC](https://github.com/BlueboxC) before sending sensitive findings. This
fork does not advertise a verified security mailbox or a response-time guarantee.
For an upstream finding, consult its current
[security policy](https://github.com/gastownhall/beads/security/policy); this fork
cannot guarantee that an upstream contact or private reporting feature works.

A useful report identifies the source commit or installed binary, affected
format/backend, prerequisites, reproduction, impact and any proposed fix.

## Data and trust

- Tasks, memories, proposals and code projections use one local Dolt database.
  They are not encrypted. Filesystem permissions, database access and configured
  remotes control who can read them. Git source publication does not require
  publishing the database; Dolt sync, exports and backups can disclose its data.
- Dolt history and native retained revisions are audit evidence, not authenticated
  identity. Actor/reviewer strings are supplied by callers. Observed activity and
  conversation summaries are untrusted data, not verification or execution authority.
- Do not store passwords or API tokens in task content. Tokens saved through
  database configuration are plaintext; prefer the provider's authenticated CLI
  and grant only the scopes needed. Review exports, remotes and retained history.
- Ordinary Beads and Graph Preview are separate formats. Preview initialization
  requires a fresh workspace and exact scope/workspace binding. It does not
  convert, restore or merge existing projects. See
  [Graph Preview](docs/reference/graph-preview.md) for its supported operations.
- Graph writes check resource counts and acquisition bytes in the transaction
  before commit. Reads refuse over-budget state before payload transfer or
  decoding. Existing oversized state may shrink without deleting retained history.
  These budgets do not bound total process RAM, SQL-engine caches or disk history.

## Processes and networking

`bd serve`, the live graph viewer and Dolt SQL servers can open listening ports.
They require explicit startup; the viewer is read-only and refresh does not
execute project code. HTTP serving validates Host headers, bounds requests and
supports token-file bearer authentication. Non-loopback serving requires explicit
admission and authentication. The server does not provide TLS itself; use a
trusted TLS boundary before exposing it beyond a trusted local host.

Tracker integrations, configured remotes and user-started synchronization can
make network requests. Imported content can contain terminal controls or prompt
injections: parameterized SQL and terminal sanitization do not make free-text
instructions trustworthy. Never pass that content to a shell or let it override
agent contracts. Resource paths and issue IDs follow the selected format's
validators; no single legacy ID regex describes both formats.

Usage metrics and OpenTelemetry are separate. Upstream-style usage metrics are
on unless the user's global preference or environment disables them; inspect
`bd metrics` and use `bd metrics off`, `BD_DISABLE_METRICS=1` or `DO_NOT_TRACK=1`.
Project configuration cannot override that user preference or redirect its
endpoint. OpenTelemetry requires explicit configuration. Dolt also has its own
metrics controls: `dolt config --global --add metrics.disabled true` and
`DOLT_DISABLE_EVENT_FLUSH=1`. Disabling one channel does not disable the others.

Hooks run with the invoking user's permissions. Review their commands and native
trust grants. Context recovery reads stored material; activity hooks record
bounded observations. Neither automatically accepts learned solutions.

## Dependencies and updates

The fork depends on Dolt, Go, CLI, parser, HTTP, telemetry and provider libraries.
`go.sum` records module checksums; it does not pin the Go compiler or prove that
packages are vulnerability-free. Use the recorded Go floor/toolchain, verify
modules, and assess advisories against actual call paths. Example modules and
Nix builds must use the corrected effective dependencies and compiler too.
The optional Python MCP has its own requirements and lockfile.

Only source and documented build/release channels are trusted update sources.
Do not run unsolicited “fix” archives or binaries attached to issues or comments,
including files hosted on `github.com/user-attachments`. Report suspicious
attachments to GitHub. This guidance retains the upstream warning about malicious
fix attachments.

Security fixes are qualified on a named source/binary cut and backend. Local
success does not establish all-platform, shared-server, independent-clone or
production qualification. Keep a known-compatible binary and project backup
before adopting an experimental format update.
