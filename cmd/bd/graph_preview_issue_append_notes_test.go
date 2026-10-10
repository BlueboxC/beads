package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/configfile"
)

func appendNotesNoInput(t *testing.T, cmd *cobra.Command) {
	t.Helper()
	probe := &selectedRememberInputProbe{}
	cmd.SetIn(probe)
	t.Cleanup(func() {
		if probe.reads != 0 {
			t.Errorf("inline append or refused request read stdin %d times", probe.reads)
		}
	})
}

// These checks exercise the real request parser. Existing transactional tests
// own append/no-op/newline semantics; the CLI must preserve the exact supplied
// fragment and the caller's guard rather than reading or composing old notes.
func TestGraphPreviewIssueAppendNotesInput(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
	}{
		{"text", "Progress"},
		{"empty", ""},
		{"literal-dash", "-"},
		{"unicode-whitespace", "  Zoë 雪\r\nnext line\t  "},
		{"literal-file-marker", "@missing-notes.txt"},
		// This interface adds no Memory-properties input cap or title/assignee
		// length restriction to ordinary notes text.
		{"above-memory-input-limit", strings.Repeat("x", (1<<20)+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := issueTextCommand(t, "--append-notes="+tc.value, "--if-revision=stale-observed-token")
			appendNotesNoInput(t, cmd)
			if !graphPreviewIssueEditFlagsChanged(cmd) {
				t.Fatal("append-only presence did not select the Issue field route")
			}
			request, err := graphPreviewIssueEditRequest(cmd, "beads/work")
			if err != nil {
				t.Fatal(err)
			}
			if request.AppendNotes == nil || *request.AppendNotes != tc.value {
				t.Fatalf("literal append presence/content lost for %s (%d bytes)", tc.name, len(tc.value))
			}
			if request.Path != "beads/work" || request.ExpectedRevision != "stale-observed-token" || request.Unconditional {
				t.Fatal("request path or caller's original guard was replaced")
			}
			if request.Title != nil || request.Description != nil || request.Design != nil || request.AcceptanceCriteria != nil || request.Priority != nil || request.Assignee != nil {
				t.Fatal("append-only request manufactured other Issue edits")
			}
		})
	}
}

func TestGraphPreviewIssueAppendNotesMixedAndOmitted(t *testing.T) {
	cmd := issueTextCommand(t, "--append-notes=\nLiteral fragment", "--title=  Revised 雪  ",
		"--description=", "--priority=P0", "--assignee= crew.alice ")
	appendNotesNoInput(t, cmd)
	request, err := graphPreviewIssueEditRequest(cmd, "beads/work")
	if err != nil {
		t.Fatal(err)
	}
	if request.AppendNotes == nil || *request.AppendNotes != "\nLiteral fragment" || request.Title == nil || *request.Title != "Revised 雪" ||
		request.Description == nil || *request.Description != "" || request.Priority == nil || *request.Priority != 0 ||
		request.Assignee == nil || *request.Assignee != " crew.alice " || !request.Unconditional || request.ExpectedRevision != "" {
		t.Fatal("mixed request lost the append fragment, field presence or current-revision default")
	}
	request, err = graphPreviewIssueEditRequest(issueTextCommand(t, "--title=Only title"), "beads/work")
	if err != nil || request.AppendNotes != nil {
		t.Fatalf("omitted append became an explicit empty fragment: %v", err)
	}
	request, err = graphPreviewIssueEditRequest(issueTextCommand(t, "--append-notes="), "beads/work")
	if err != nil || request.AppendNotes == nil || *request.AppendNotes != "" || !request.Unconditional {
		t.Fatalf("explicit empty append was discarded: %v", err)
	}
}

func TestGraphPreviewIssueAppendNotesRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		code int
	}{
		{"invalid-utf8", []string{"--append-notes=\xff"}, 2},
		{"empty-guard", []string{"--append-notes=", "--if-revision="}, 2},
		{"replacement-notes", []string{"--append-notes=Progress", "--notes=Replacement"}, 2},
		{"clear-replacement-notes", []string{"--append-notes=Progress", "--notes="}, 2},
		{"force", []string{"--append-notes=Progress", "--force"}, 2},
		{"false-force", []string{"--append-notes=Progress", "--force=false"}, 2},
		{"claim", []string{"--append-notes=Progress", "--claim"}, 5},
		{"false-claim-direct-request", []string{"--append-notes=Progress", "--claim=false"}, 5},
		{"workflow", []string{"--append-notes=Progress", "--status=closed"}, 5},
		{"source-guard", []string{"--append-notes=Progress", "--if-source-revision=other"}, 5},
		{"assignee-guard", []string{"--append-notes=Progress", "--if-assignee="}, 5},
		{"status-guard", []string{"--append-notes=Progress", "--if-status=open"}, 5},
		{"file", []string{"--append-notes=Progress", "--body-file=missing"}, 5},
		{"stdin-false", []string{"--append-notes=Progress", "--stdin=false"}, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := issueTextCommand(t, tc.args...)
			appendNotesNoInput(t, cmd)
			_, err := graphPreviewIssueEditRequest(cmd, "beads/work")
			var failure *exitError
			if !errors.As(err, &failure) || failure.Code != tc.code {
				t.Fatalf("expected refusal exit%d before input/storage, got %v", tc.code, err)
			}
		})
	}
	cmd := issueTextCommand(t, "--append-notes=Progress", "--properties={}")
	appendNotesNoInput(t, cmd)
	request, err := graphPreviewIssueEditRequest(cmd, "beads/work")
	if err != nil || !cmd.Flags().Changed("properties") || request.AppendNotes == nil || *request.AppendNotes != "Progress" {
		t.Fatalf("mixed append and empty property merge: %+v %v", request, err)
	}
}

func TestGraphPreviewIssueAppendNotesDispatchRefusals(t *testing.T) {
	t.Chdir(t.TempDir())
	oldConfig, oldReadonly := graphPreviewConfig, readonlyMode
	t.Cleanup(func() { graphPreviewConfig, readonlyMode = oldConfig, oldReadonly })
	graphPreviewConfig = &configfile.Config{GraphScopeURL: "https://example.invalid/"}
	readonlyMode = false
	for _, tc := range []struct {
		name     string
		selector string
		flags    []string
		code     int
	}{
		// Invalid text reaches Issue admission (exit2), rather than falling
		// through to Memory's unsupported-field refusal (exit5).
		{"issue-route", "beads/work", []string{"--append-notes=\xff"}, 2},
		{"canonical-issue-route", "https://example.invalid/beads/work", []string{"--append-notes=\xff"}, 2},
		{"link-route", "links/context", []string{"--append-notes=Progress"}, 5},
		{"foreign-selector", "https://foreign.invalid/beads/work", []string{"--append-notes=Progress"}, 2},
		{"unsupported-selector", "alias/work-123", []string{"--append-notes=Progress"}, 2},
		{"false-claim-dispatch", "beads/work", []string{"--append-notes=Progress", "--claim=false"}, 2},
		{"true-claim-dispatch", "beads/work", []string{"--append-notes=Progress", "--claim"}, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := issueTextCommand(t, tc.flags...)
			appendNotesNoInput(t, cmd)
			err := runGraphPreviewUpdate(cmd, []string{tc.selector})
			var failure *exitError
			if !errors.As(err, &failure) || failure.Code != tc.code {
				t.Fatalf("expected pre-store dispatch refusal exit%d, got %v", tc.code, err)
			}
		})
	}
	// This tests the Memory route's own field admission, not the store-backed
	// classification of a healthy Memory selected through generic update.
	t.Run("direct-memory-route", func(t *testing.T) {
		cmd := issueTextCommand(t, "--append-notes=Progress")
		appendNotesNoInput(t, cmd)
		err := runGraphPreviewUpdateMemory(cmd, "beads/plan")
		var failure *exitError
		if !errors.As(err, &failure) || failure.Code != 5 {
			t.Fatalf("Memory writer admitted append notes: %v", err)
		}
	})
}

func TestGraphPreviewIssueAppendNotesReadonlyBeforeDispatch(t *testing.T) {
	oldConfig, oldReadonly := graphPreviewConfig, readonlyMode
	graphPreviewConfig, readonlyMode = nil, true
	t.Cleanup(func() { graphPreviewConfig, readonlyMode = oldConfig, oldReadonly })
	cmd := issueTextCommand(t, "--append-notes=-")
	appendNotesNoInput(t, cmd)
	err := runGraphPreviewUpdate(cmd, []string{"beads/work"})
	var failure *exitError
	if !errors.As(err, &failure) || failure.Code != 5 {
		t.Fatalf("readonly append reached input, configuration or storage: %v", err)
	}
}
