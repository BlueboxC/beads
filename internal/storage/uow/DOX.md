# DOX.md — Units of work

## Purpose

Own units of work within the fork.

## Ownership

Persistence transaction composition and completion.

## Local Contracts

Root DOX.md and the parent chain remain binding.

## Work Guidance

Verify commit, rollback and failure behavior at the affected transaction boundary.

Memories.List translates user prefixes to the memory namespace and calls ConfigUseCase.GetConfigByPrefix within one read-only unit of work. It never enumerates unrelated config or reserved namespaces to answer a selected list; empty selectors retain the complete memory plane. Remember, Recall and Forget derive presence from the selected key map within the same transaction, including stored empty values.

## Verification

The shared optional AtomicMemories case is explicitly wired and skips with a stated reason when the accessor does not advertise it; no atomic-batch capability is inferred for this adapter.

Use the affected existing checks selected in engdocs/TESTING.md; verify indexed child contracts. TestMemoriesListSelectionBoundsTransferredValues exercises the real config SQL/use-case boundary with an isolated Dolt fixture and counts returned key/value bytes, excluding engine reads and protocol overhead. TestMemoriesContract owns backend semantic conformance.

## Child DOX Index
