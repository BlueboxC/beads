# DOX.md — Python MCP interface

## Purpose

Own python mcp interface within the fork.

## Ownership

Python package exposing Beads operations to MCP clients.

## Local Contracts

Root DOX.md and the parent chain remain binding.

- Comment/note IDs and bodies are literal positional arguments after `--`; configured CLI options precede that boundary.
- Published runtime requirements include PyJWT >=2.15.0; the dev group requires urllib3 >=2.8.0. Preserve these security minima and the locked dependency graph.

## Work Guidance

Use the existing package tests and dependency declarations. Installation into Codex uses the CLI skill and native hooks unless MCP is explicitly requested.

## Verification

Use `make ci-package-mcp` with the canonical candidate binary. Its Ruff, mypy, unit, real CLI/stdio and packaging checks qualify the connector. Comment/note round trips must preserve option-shaped text and actor identity. Verify indexed child contracts.

## Child DOX Index
