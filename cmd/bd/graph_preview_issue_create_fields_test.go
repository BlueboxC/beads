//go:build cgo

package main

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/storage/graphstore"
	"github.com/steveyegge/beads/internal/types"
)

func issueCreateFieldsString(value string) *string { return &value }

func issueCreateFieldsCommand(t *testing.T, args ...string) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{}
	for _, name := range []string{"design", "acceptance", "external-ref", "spec-id", "notes"} {
		cmd.Flags().String(name, "", "")
	}
	cmd.Flags().StringP("assignee", "a", "", "")
	cmd.Flags().IntP("estimate", "e", 0, "")
	if err := cmd.ParseFlags(args); err != nil {
		t.Fatal(err)
	}
	return cmd
}

func TestGraphPreviewIssueCreateFieldsPresence(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want types.Issue
	}{
		{name: "omitted"},
		{name: "explicit-empty", args: []string{"--design=", "--acceptance=", "--assignee=", "--external-ref=", "--spec-id="}},
		{name: "literal", args: []string{"--design=  雪\r\n ", "--acceptance= café e\u0301 ", "-a", " rig/crew/雪 ", "--external-ref= tracker #42 ", "--spec-id= spec\r\nsection "},
			want: types.Issue{Design: "  雪\r\n ", AcceptanceCriteria: " café e\u0301 ", Assignee: " rig/crew/雪 ", ExternalRef: issueCreateFieldsString(" tracker #42 "), SpecID: " spec\r\nsection "}},
		{name: "hyphens", args: []string{"--design=-", "--acceptance=-", "--assignee=-", "--external-ref=-", "--spec-id=-"},
			want: types.Issue{Design: "-", AcceptanceCriteria: "-", Assignee: "-", ExternalRef: issueCreateFieldsString("-"), SpecID: "-"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := types.Issue{}
			if err := graphPreviewIssueCreateFields(issueCreateFieldsCommand(t, tc.args...), &got); err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("initial fields changed: got=%+v want=%+v err=%v", got, tc.want, err)
			}
		})
	}
	for _, tc := range []struct {
		name string
		args []string
		want int
	}{
		{"zero-long", []string{"--estimate=0"}, 0},
		{"zero-short", []string{"-e", "0"}, 0},
		{"positive", []string{"--estimate=17"}, 17},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := types.Issue{Title: "Keep title", Status: types.StatusOpen}
			if err := graphPreviewIssueCreateFields(issueCreateFieldsCommand(t, tc.args...), &got); err != nil || got.EstimatedMinutes == nil || *got.EstimatedMinutes != tc.want || got.Title != "Keep title" || got.Status != types.StatusOpen {
				t.Fatalf("estimate presence or initial scalar lost: %+v %v", got, err)
			}
		})
	}
}

func TestGraphPreviewIssueCreateFieldsInputRefusals(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"negative-estimate", []string{"--estimate=-1"}},
		{"long-assignee", []string{"--assignee=" + strings.Repeat("雪", 256)}},
	}
	for _, field := range []string{"design", "acceptance", "assignee", "external-ref", "spec-id"} {
		cases = append(cases, struct {
			name string
			args []string
		}{field + "-invalid-utf8", []string{"--" + field + "=\xff"}})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			diagnostic := captureStderr(t, func() {
				err = graphPreviewIssueCreateFields(issueCreateFieldsCommand(t, tc.args...), &types.Issue{})
			})
			var failure *exitError
			if !errors.As(err, &failure) || failure.Code != 2 || !strings.Contains(diagnostic, "invalid_properties") {
				t.Fatalf("expected typed validation refusal before storage: %v %q", err, diagnostic)
			}
		})
	}
}

func TestGraphPreviewIssueCreateFieldsDispatch(t *testing.T) {
	bd := buildBDUnderTest(t)
	work, home := t.TempDir(), t.TempDir()
	graphPolicyCLI(t, bd, work, home, nil, "", "init", "--graph-mode", "link", "--prefix", "fields", "--scope-url", "https://example.invalid/create-fields/", "--skip-hooks", "--skip-agents", "--non-interactive", "--json")
	created := graphPolicyCLI(t, bd, work, home, nil, "", "create", "First fields", "--id", "beads/work", "--design", "  雪\r\n ", "--acceptance", "accepted", "-a", "author", "-e", "0", "--external-ref", "tracker #42", "--spec-id", "spec/one", "--actor", "create-author", "--json")
	var envelope struct {
		Result struct {
			Revision   string
			Properties types.Issue
		}
	}
	if err := json.Unmarshal([]byte(created), &envelope); err != nil {
		t.Fatal(err)
	}
	i := envelope.Result.Properties
	if envelope.Result.Revision == "" || i.Design != "  雪\r\n " || i.AcceptanceCriteria != "accepted" || i.Assignee != "author" || i.EstimatedMinutes == nil || *i.EstimatedMinutes != 0 || i.ExternalRef == nil || *i.ExternalRef != "tracker #42" || i.SpecID != "spec/one" || i.Status != types.StatusOpen {
		t.Fatalf("production create lost fields: %+v", i)
	}
	fromProperties := graphMixedResult[graphstore.IssueRecord](t, graphPolicyCLI(t, bd, work, home, nil, "", "create", "--id", "beads/from-properties", "--properties", `{"title":"Typed issue","description":"Exact description","priority":1,"estimated_minutes":45,"labels":["preview"],"assignee":"reviewer"}`, "--metadata", `{"team":"docs"}`, "--json"))
	if fromProperties.Properties.Title != "Typed issue" || fromProperties.Properties.Description != "Exact description" || fromProperties.Properties.Priority != 1 || fromProperties.Properties.EstimatedMinutes == nil || *fromProperties.Properties.EstimatedMinutes != 45 || fromProperties.Properties.Assignee != "reviewer" || len(fromProperties.Properties.Labels) != 1 || string(fromProperties.Metadata) != `{"team":"docs"}` {
		t.Fatalf("Issue properties initializer lost typed values or metadata: %+v", fromProperties)
	}
	memory := graphMixedResult[graphstore.Record](t, graphPolicyCLI(t, bd, work, home, nil, "", "create", "--bead-type", "types/preview-memory-v2", "--id", "beads/memory-properties", "--properties", `{"title":"Typed memory","body":"Exact body"}`, "--json"))
	if memory.Properties.Title != "Typed memory" || memory.Properties.Body != "Exact body" {
		t.Fatalf("Memory properties initializer lost typed values: %+v", memory)
	}
	graphPolicyCLI(t, bd, work, home, nil, "invalid_properties", "create", "--id", "beads/duplicate-properties", "--title", "Title", "--properties", `{"title":"Title"}`, "--json")
	graphPolicyCLI(t, bd, work, home, nil, "invalid_properties", "create", "--id", "beads/immutable-properties", "--properties", `{"title":"Refused","status":"closed"}`, "--json")
	for _, flags := range [][]string{nil, {"--version", envelope.Result.Revision}} {
		args := append([]string{"show", "beads/work", "--json"}, flags...)
		if got := graphPolicyCLI(t, bd, work, home, nil, "", args...); got != created {
			t.Fatal("fresh current/exact read differs from the complete first record")
		}
	}
	for _, tc := range []struct {
		name, flag, code string
	}{
		{"negative", "--estimate=-1", "invalid_properties"},
		{"overflow", "--estimate=2147483648", "invalid_properties"},
		{"external-long", "--external-ref=" + strings.Repeat("雪", 256), "invalid_properties"},
		{"spec-long", "--spec-id=" + strings.Repeat("雪", 1025), "invalid_properties"},
		{"status-with-notes", "--notes=", "capability_unavailable"},
		{"status", "--status=open", "capability_unavailable"},
		{"defer", "--defer=tomorrow", "capability_unavailable"},
		{"design-file", "--design-file=/missing-create-design", "capability_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := []string{"create", "Refused", "--id", "beads/refused", tc.flag, "--json"}
			if tc.name == "status-with-notes" {
				// Initial notes are now admitted; status remains a refusal even alongside them.
				args = append(args, "--status=open")
			}
			graphPolicyCLI(t, bd, work, home, nil, tc.code, args...)
		})
	}
	graphPolicyCLI(t, bd, work, home, nil, "permission_denied", "create", "Read-only", "--id", "beads/refused", "--design-file=/missing-create-design", "--readonly", "--json")
	graphPolicyCLI(t, bd, work, home, nil, "not_found", "show", "beads/refused", "--json")
	if got := graphPolicyCLI(t, bd, work, home, nil, "", "show", "beads/work", "--json"); got != created {
		t.Fatal("refused creates changed accepted record")
	}
}
