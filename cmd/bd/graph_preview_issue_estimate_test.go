package main

import (
	"errors"
	"strconv"
	"testing"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/configfile"
)

func TestGraphPreviewIssueEstimatePresence(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want int
	}{
		{"zero", []string{"--estimate=0"}, 0},
		{"positive", []string{"--estimate=45"}, 45},
		{"shorthand", []string{"-e", "12"}, 12},
		{"storage-max", []string{"--estimate=2147483647"}, 2147483647},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := issueTextCommand(t, append(tc.args, "--if-revision=observed")...)
			got, err := graphPreviewIssueEditRequest(cmd, "beads/work")
			if err != nil || !graphPreviewIssueEditFlagsChanged(cmd) || got.EstimatedMinutes == nil || *got.EstimatedMinutes != tc.want || got.ExpectedRevision != "observed" {
				t.Fatalf("estimate presence/route/guard lost: %+v %v", got, err)
			}
		})
	}
	got, err := graphPreviewIssueEditRequest(issueTextCommand(t, "--title=Sized"), "beads/work")
	if err != nil || got.EstimatedMinutes != nil {
		t.Fatalf("omission became a zero: %+v %v", got, err)
	}
	got, err = graphPreviewIssueEditRequest(issueTextCommand(t, "-e", "0", "--title=Sized", "--priority=1", "--append-notes=Done"), "beads/work")
	if err != nil || got.EstimatedMinutes == nil || *got.EstimatedMinutes != 0 || got.Title == nil || *got.Title != "Sized" || got.Priority == nil || *got.Priority != 1 || got.AppendNotes == nil || *got.AppendNotes != "Done" || !got.Unconditional {
		t.Fatalf("mixed intent lost: %+v %v", got, err)
	}
}

func TestGraphPreviewIssueEstimateRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		code int
	}{
		{"negative", []string{"-e", "-1"}, 2},
		{"status", []string{"-e", "0", "--status=open"}, 5},
		{"claim", []string{"-e", "0", "--claim"}, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := graphPreviewIssueEditRequest(issueTextCommand(t, tc.args...), "beads/work")
			var refusal *exitError
			if !errors.As(err, &refusal) || refusal.Code != tc.code {
				t.Fatalf("want exit%d before storage: %v", tc.code, err)
			}
		})
	}
	cmd := issueTextCommand(t, "-e", "0", "--properties={}")
	request, err := graphPreviewIssueEditRequest(cmd, "beads/work")
	if err != nil || !cmd.Flags().Changed("properties") || request.EstimatedMinutes == nil || *request.EstimatedMinutes != 0 {
		t.Fatalf("mixed estimate and empty property merge: %+v %v", request, err)
	}
	// Parsing belongs to the existing integer flag, including platform overflow.
	for _, value := range []string{"", "1.5", "null", "999999999999999999999999999"} {
		t.Run("parser-"+strconv.Quote(value), func(t *testing.T) {
			cmd := &cobra.Command{}
			cmd.Flags().IntP("estimate", "e", 0, "")
			if err := cmd.ParseFlags([]string{"--estimate=" + value}); err == nil {
				t.Fatal("invalid integer accepted")
			}
		})
	}
}

func TestGraphPreviewIssueEstimateReadonly(t *testing.T) {
	old := readonlyMode
	readonlyMode = true
	t.Cleanup(func() { readonlyMode = old })
	err := runGraphPreviewUpdate(issueTextCommand(t, "-e", "0"), []string{"beads/work"})
	var refusal *exitError
	if !errors.As(err, &refusal) || refusal.Code != 5 {
		t.Fatalf("readonly estimate reached storage: %v", err)
	}
}

// Production dispatch selects claim admission before scalar editing, even for
// --claim=false. Neither spelling may silently discard an accompanying estimate.
func TestGraphPreviewIssueEstimateClaimDispatch(t *testing.T) {
	old := graphPreviewConfig
	graphPreviewConfig = &configfile.Config{GraphScopeURL: "https://example.invalid/"}
	t.Cleanup(func() { graphPreviewConfig = old })
	for _, tc := range []struct {
		claim string
		code  int
	}{{"true", 5}, {"false", 2}} {
		t.Run(tc.claim, func(t *testing.T) {
			err := runGraphPreviewUpdate(issueClaimCommand(t, "--claim="+tc.claim, "-e", "0"), []string{"beads/work"})
			var failure *exitError
			if !errors.As(err, &failure) || failure.Code != tc.code {
				t.Fatalf("mixed claim/estimate reached storage: %v", err)
			}
		})
	}
}
