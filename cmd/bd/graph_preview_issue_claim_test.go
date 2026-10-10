package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

func issueClaimCommand(t *testing.T, args ...string) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{}
	cmd.Flags().IntP("estimate", "e", 0, "")
	for _, name := range append(append([]string{}, graphPreviewIssueEditFlags...), "properties", "patch", "notes", "append-notes", "body-file", "design-file", "status", "if-assignee", "if-status", "if-revision", "if-source-revision", "set-labels", "add-label", "remove-label", "parent", "metadata", "actor") {
		if cmd.Flags().Lookup(name) == nil {
			cmd.Flags().String(name, "", "")
		}
	}
	for _, name := range []string{"claim", "force", "unconditional", "unconditional-source", "stdin", "json", "quiet", "readonly", "graph-mode", "no-color"} {
		cmd.Flags().Bool(name, false, "")
	}
	if err := cmd.ParseFlags(args); err != nil {
		t.Fatal(err)
	}
	cmd.SetIn(failingMemoryBodyReader{})
	return cmd
}

func TestGraphPreviewIssueClaimAdmission(t *testing.T) {
	for _, tc := range []struct {
		name, actor string
		args        []string
	}{
		{"ordinary", "alice", []string{"--claim"}},
		{"explicit-true", "rig--crew--alice", []string{"--claim=true"}},
		{"literal-spaces", "  雪  ", []string{"--claim", "--json", "--quiet"}},
		{"max-runes", strings.Repeat("雪", types.MaxFieldLen), []string{"--claim", "--actor=explicit", "--no-color"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := graphPreviewIssueClaimInput(issueClaimCommand(t, tc.args...), "beads/work", tc.actor); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGraphPreviewIssueClaimRefusals(t *testing.T) {
	for _, tc := range []struct {
		name, path, actor string
		args              []string
	}{
		{"missing-flag", "beads/work", "alice", nil},
		{"false-claim", "beads/work", "alice", []string{"--claim=false"}},
		{"false-with-priority", "beads/work", "alice", []string{"--claim=false", "--priority=1"}},
		{"false-with-assignee", "beads/work", "alice", []string{"--claim=false", "--assignee=alice"}},
		{"link", "links/context", "alice", []string{"--claim"}},
		{"empty-path", "", "alice", []string{"--claim"}},
		{"legacy-id", "bd-work", "alice", []string{"--claim"}},
		{"empty-actor", "beads/work", "", []string{"--claim"}},
		{"invalid-actor", "beads/work", "\xff", []string{"--claim"}},
		{"long-actor", "beads/work", strings.Repeat("雪", types.MaxFieldLen+1), []string{"--claim"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := graphPreviewIssueClaimInput(issueClaimCommand(t, tc.args...), tc.path, tc.actor)
			var refusal *exitError
			if !errors.As(err, &refusal) || refusal.Code != 2 {
				t.Fatalf("expected admission exit2, got %v", err)
			}
		})
	}
	for _, flag := range []string{
		"--title=", "--description=", "--body=", "--message=", "--design=", "--acceptance=", "--priority=0", "--assignee=",
		"--patch=[]", "--status=open", "--notes=", "--append-notes=", "--properties={}", "--body-file=missing", "--design-file=missing", "--stdin=false",
		"--if-revision=", "--unconditional=false", "--if-assignee=", "--if-status=", "--if-source-revision=", "--unconditional-source=false", "--force=false",
		"--set-labels=", "--add-label=", "--remove-label=", "--parent=", "--metadata={}",
	} {
		t.Run(flag, func(t *testing.T) {
			err := graphPreviewIssueClaimInput(issueClaimCommand(t, "--claim", flag), "beads/work", "alice")
			var refusal *exitError
			if !errors.As(err, &refusal) || refusal.Code != 5 {
				t.Fatalf("mixed claim must refuse before input or storage, got %v", err)
			}
		})
	}
}

func TestGraphPreviewIssueClaimReadonlyBeforeDispatch(t *testing.T) {
	old := readonlyMode
	readonlyMode = true
	t.Cleanup(func() { readonlyMode = old })
	// No graph config or database is needed: write policy must run first.
	err := runGraphPreviewUpdate(issueClaimCommand(t, "--claim"), []string{"beads/work"})
	var refusal *exitError
	if !errors.As(err, &refusal) || refusal.Code != 5 {
		t.Fatalf("read-only claim reached dispatch: %v", err)
	}
}

func TestGraphPreviewIssueClaimConstraintErrors(t *testing.T) {
	for _, sentinel := range []error{storage.ErrAlreadyClaimed, storage.ErrNotClaimable} {
		t.Run(sentinel.Error(), func(t *testing.T) {
			stderr, err := os.CreateTemp(t.TempDir(), "stderr")
			if err != nil {
				t.Fatal(err)
			}
			oldStderr, oldJSON := os.Stderr, jsonOutput
			os.Stderr, jsonOutput = stderr, true
			t.Cleanup(func() {
				os.Stderr, jsonOutput = oldStderr, oldJSON
				_ = stderr.Close()
			})
			err = graphStorageError(fmt.Errorf("claim refusal: %w", sentinel))
			var refusal *exitError
			if !errors.As(err, &refusal) || refusal.Code != 4 {
				t.Fatalf("claim constraint must exit4: %v", err)
			}
			if _, err := stderr.Seek(0, 0); err != nil {
				t.Fatal(err)
			}
			var result struct {
				Code      string `json:"code"`
				Message   string `json:"message"`
				Retryable bool   `json:"retryable"`
			}
			if err := json.NewDecoder(stderr).Decode(&result); err != nil {
				t.Fatal(err)
			}
			if result.Code != "constraint_violation" || result.Retryable || result.Message != "claim refusal: "+sentinel.Error() {
				t.Fatalf("wrong claim refusal: %+v", result)
			}
		})
	}
}
