# BlueboxC Beads fork

This fork supports persistent project context for Codex: project direction,
work dependencies, existing modules, verified solutions and the evidence that
allows another session to resume without repeating completed work.

The bootstrap branch is `codex/software-factory-memory`, based on upstream
v1.3.1 (`c1c4b642ac1c08d8c828007a1c2f96e47e43ef7c`). The installed macOS binary is
the checksum-verified upstream release for that exact source revision. This
bootstrap adds DOX contracts; it does not change executable behavior.

Beads already provides durable tasks, relationships, project facts with
`bd remember`, and context recovery with `bd prime`. Its Codex integration
provides a skill and SessionStart, PreCompact, PostCompact and UserPromptSubmit
hooks. Codex separately requires trusting the installed hook definitions.

The extension objective is one product using the existing Dolt storage
boundary. Code indexing, automatic extraction of learned solutions, semantic
retrieval and a graphical explorer are not implemented in this bootstrap.
Derived code indexes must be reproducible; verified decisions and solutions
must retain source revision, affected module, evidence and validity status.
Workflow blockers and code/knowledge relationships have different semantics;
code cycles must not be treated as task dependency cycles.

Prefer existing metadata and interfaces before introducing schema. Follow the
[project charter](engdocs/PROJECT_CHARTER.md) and
[test authority](engdocs/TESTING.md) for implementation. Track extension work
in Beads rather than maintaining a competing Markdown task list.

Keep `main` available for upstream synchronization. Develop fork changes on
the bootstrap branch until a separately reviewed release is ready. Do not
publish private project databases or machine configuration in this public
source repository. Global Codex configuration and installation backups belong
to the operator's machine, outside Git.
