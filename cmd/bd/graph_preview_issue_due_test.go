package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
)

func TestGraphPreviewIssueDuePresence(t *testing.T) {
	for _, tc := range []struct {
		name       string
		args       []string
		set, clear bool
		want       string
	}{
		{"omitted", []string{"--title=Keep"}, false, false, ""},
		{"clear", []string{"--due=", "--if-revision=observed"}, true, true, ""},
		{"offset", []string{"--due=2030-01-02T03:04:05-07:00", "--if-revision=observed"}, true, false, "2030-01-02T10:04:05Z"},
		{"fraction-preserved-for-storage", []string{"--due=2030-01-02T03:04:05.6Z"}, true, false, "2030-01-02T03:04:05.6Z"},
		{"mixed", []string{"--due=2030-01-02T03:04:05Z", "--title=New", "--priority=P0"}, true, false, "2030-01-02T03:04:05Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := issueTextCommand(t, tc.args...)
			got, err := graphPreviewIssueEditRequest(cmd, "beads/work")
			if err != nil || got.DueAt.Set != tc.set {
				t.Fatalf("due presence: %+v %v", got, err)
			}
			if tc.set && !graphPreviewIssueEditFlagsChanged(cmd) {
				t.Fatal("due did not select Issue edit")
			}
			if (got.DueAt.Value == nil) != (tc.clear || !tc.set) {
				t.Fatal("clear/omission lost")
			}
			if tc.want != "" && got.DueAt.Value.Format(time.RFC3339Nano) != tc.want {
				t.Fatalf("parsed due=%s", got.DueAt.Value)
			}
			if tc.name == "mixed" && (got.Title == nil || *got.Title != "New" || got.Priority == nil || *got.Priority != 0) {
				t.Fatal("mixed fields lost")
			}
			if cmd.Flags().Changed("if-revision") && got.ExpectedRevision != "observed" {
				t.Fatal("guard lost")
			}
		})
	}
	for _, tc := range []struct {
		name, input string
		duration    time.Duration
	}{{"relative", "+6h", 6 * time.Hour}, {"relative-minutes", "+30min", 30 * time.Minute}} {
		t.Run(tc.name, func(t *testing.T) {
			before := time.Now().Add(tc.duration)
			got, err := graphPreviewIssueEditRequest(issueTextCommand(t, "--due="+tc.input), "beads/work")
			after := time.Now().Add(tc.duration)
			if err != nil || got.DueAt.Value == nil || got.DueAt.Value.Before(before) || got.DueAt.Value.After(after) {
				t.Fatalf("native relative input: %+v %v", got, err)
			}
		})
	}
}

func TestGraphPreviewIssueDueRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		code int
	}{
		{"syntax", []string{"--due=not-a-date"}, 2},
		{"utf8", []string{"--due=\xff"}, 2},
		{"oversize", []string{"--due=" + strings.Repeat("x", 4097)}, 2},
		{"status", []string{"--due=", "--status=open"}, 5},
		{"source-guard", []string{"--due=", "--if-source-revision=x"}, 5},
		{"file", []string{"--due=", "--body-file=missing"}, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := graphPreviewIssueEditRequest(issueTextCommand(t, tc.args...), "beads/work")
			var failure *exitError
			if !errors.As(err, &failure) || failure.Code != tc.code {
				t.Fatalf("want exit%d before storage, got %v", tc.code, err)
			}
		})
	}
	// The generic setter may accompany a distinct native Issue edit flag.
	cmd := issueTextCommand(t, "--due=", "--properties={}")
	request, err := graphPreviewIssueEditRequest(cmd, "beads/work")
	if err != nil || !cmd.Flags().Changed("properties") || !request.DueAt.Set || request.DueAt.Value != nil {
		t.Fatalf("mixed due clear and empty property merge: %+v %v", request, err)
	}
}

func TestGraphPreviewIssueDuePolicyDispatch(t *testing.T) {
	priorConfig, priorReadonly := graphPreviewConfig, readonlyMode
	graphPreviewConfig = &configfile.Config{GraphScopeURL: "https://example.invalid/"}
	t.Cleanup(func() { graphPreviewConfig, readonlyMode = priorConfig, priorReadonly })
	t.Run("readonly-before-invalid-input", func(t *testing.T) {
		readonlyMode = true
		defer func() { readonlyMode = priorReadonly }()
		err := runGraphPreviewUpdate(issueTextCommand(t, "--due=not-a-date"), []string{"beads/work"})
		var failure *exitError
		if !errors.As(err, &failure) || failure.Code != 5 {
			t.Fatalf("readonly did not precede parsing: %v", err)
		}
	})
	for _, tc := range []struct {
		claim string
		code  int
	}{{"true", 5}, {"false", 2}} {
		t.Run("claim-"+tc.claim, func(t *testing.T) {
			err := runGraphPreviewUpdate(issueClaimCommand(t, "--claim="+tc.claim, "--due="), []string{"beads/work"})
			var failure *exitError
			if !errors.As(err, &failure) || failure.Code != tc.code {
				t.Fatalf("claim mixed with due: %v", err)
			}
		})
	}
}

func TestGraphPreviewIssueDueListInput(t *testing.T) {
	t.Chdir(t.TempDir())
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	if err := config.Initialize(); err != nil {
		t.Fatal(err)
	}
	prior := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = prior })
	for _, tc := range []struct {
		name          string
		args          []string
		before, after string
		overdue       bool
		code          int
	}{
		{name: "omitted", args: []string{"--flat"}},
		{name: "empty", args: []string{"--flat", "--due-before=", "--due-after=", "--overdue=false"}},
		{name: "intersection", args: []string{"--format=records-json", "--due-before=2030-01-03T00:00:00Z", "--due-after=2030-01-01T17:00:00-07:00", "--overdue", "--all", "--limit=2"}, before: "2030-01-03T00:00:00Z", after: "2030-01-02T00:00:00Z", overdue: true},
		{name: "native-syntax", args: []string{"--flat", "--due-before=not-a-date"}, code: 1},
		{name: "utf8", args: []string{"--flat", "--due-before=\xff"}, code: 2},
		{name: "oversize", args: []string{"--flat", "--due-after=" + strings.Repeat("x", 4097)}, code: 2},
		{name: "no-due-sort", args: []string{"--flat", "--sort=due"}, code: 5},
		{name: "legacy-json-gate", args: []string{"--flat", "--overdue", "--json"}, code: 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := graphListTestCommand(t, nil)
			if err := cmd.ParseFlags(tc.args); err != nil {
				t.Fatal(err)
			}
			got, _, err := graphIssueListInput(cmd, append([]string{"list"}, tc.args...))
			if tc.code != 0 {
				var failure *exitError
				if !errors.As(err, &failure) || failure.Code != tc.code {
					t.Fatalf("want exit%d got %v", tc.code, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			format := func(v *time.Time) string {
				if v == nil {
					return ""
				}
				return v.Format(time.RFC3339Nano)
			}
			if format(got.DueBefore) != tc.before || format(got.DueAfter) != tc.after || got.OverdueFlag != tc.overdue {
				t.Fatalf("native filter lost: %+v", got.ListRequest)
			}
			if tc.name == "intersection" && (!got.AllFlag || got.Limit == nil || *got.Limit != 2) {
				t.Fatal("native limit/all policy lost")
			}
		})
	}
}
