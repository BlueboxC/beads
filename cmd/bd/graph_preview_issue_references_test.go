package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

func TestGraphPreviewIssueReferencesPresence(t *testing.T) {
	for _, value := range []string{"", "-", "  雪 #42  "} {
		t.Run(value, func(t *testing.T) {
			cmd := issueTextCommand(t, "--external-ref="+value, "--spec-id="+value, "--if-revision=observed")
			got, err := graphPreviewIssueEditRequest(cmd, "beads/work")
			if err != nil || !graphPreviewIssueEditFlagsChanged(cmd) || got.ExternalRef == nil || *got.ExternalRef != value || got.SpecID == nil || *got.SpecID != value || got.ExpectedRevision != "observed" {
				t.Fatalf("literal presence or guard lost: %+v %v", got, err)
			}
		})
	}
	got, err := graphPreviewIssueEditRequest(issueTextCommand(t, "--title=Title"), "beads/work")
	if err != nil || got.ExternalRef != nil || got.SpecID != nil {
		t.Fatalf("omission became clear: %+v %v", got, err)
	}
	got, err = graphPreviewIssueEditRequest(issueTextCommand(t, "--external-ref=ref", "--spec-id=spec", "--title=Title", "-e", "0", "--append-notes=note"), "beads/work")
	if err != nil || got.ExternalRef == nil || got.SpecID == nil || got.Title == nil || got.EstimatedMinutes == nil || *got.EstimatedMinutes != 0 || got.AppendNotes == nil || !got.Unconditional {
		t.Fatalf("mixed intent lost: %+v %v", got, err)
	}
}

func TestGraphPreviewIssueReferencesRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		code int
	}{
		{"external-invalid-utf8", []string{"--external-ref=\xff"}, 2},
		{"spec-invalid-utf8", []string{"--spec-id=\xff"}, 2},
		{"status", []string{"--external-ref=x", "--status=open"}, 5},
		{"claim", []string{"--external-ref=x", "--claim"}, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			diagnostic := captureStderr(t, func() {
				_, err = graphPreviewIssueEditRequest(issueTextCommand(t, tc.args...), "beads/work")
			})
			if strings.HasSuffix(tc.name, "invalid-utf8") {
				field := "external-ref"
				if strings.HasPrefix(tc.name, "spec-") {
					field = "spec-id"
				}
				if err == nil || !strings.Contains(diagnostic, "Issue "+field+" must be valid UTF-8") {
					t.Fatalf("wrong reference diagnostic: %q (%v)", diagnostic, err)
				}
			}
			var refusal *exitError
			if !errors.As(err, &refusal) || refusal.Code != tc.code {
				t.Fatalf("want exit%d before storage: %v", tc.code, err)
			}
		})
	}
	// The generic setter may accompany a distinct native Issue edit flag.
	cmd := issueTextCommand(t, "--spec-id=x", "--properties={}")
	request, err := graphPreviewIssueEditRequest(cmd, "beads/work")
	if err != nil || !cmd.Flags().Changed("properties") || request.SpecID == nil || *request.SpecID != "x" {
		t.Fatalf("mixed spec id and empty property merge: %+v %v", request, err)
	}
}

func TestGraphPreviewIssueReferencesReadonly(t *testing.T) {
	old := readonlyMode
	readonlyMode = true
	t.Cleanup(func() { readonlyMode = old })
	err := runGraphPreviewUpdate(issueTextCommand(t, "--external-ref=x"), []string{"beads/work"})
	var refusal *exitError
	if !errors.As(err, &refusal) || refusal.Code != 5 {
		t.Fatalf("readonly edit reached storage: %v", err)
	}
}

func TestGraphPreviewIssueReferencesClaimDispatch(t *testing.T) {
	old := graphPreviewConfig
	graphPreviewConfig = &configfile.Config{GraphScopeURL: "https://example.invalid/"}
	t.Cleanup(func() { graphPreviewConfig = old })
	for _, tc := range []struct {
		claim string
		code  int
	}{{"true", 5}, {"false", 2}} {
		t.Run(tc.claim, func(t *testing.T) {
			err := runGraphPreviewUpdate(issueClaimCommand(t, "--claim="+tc.claim, "--external-ref=x", "--spec-id=y"), []string{"beads/work"})
			var failure *exitError
			if !errors.As(err, &failure) || failure.Code != tc.code {
				t.Fatalf("mixed claim/reference reached storage: %v", err)
			}
		})
	}
}
