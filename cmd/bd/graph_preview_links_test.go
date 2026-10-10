package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/configfile"
)

func TestGraphPreviewPropertiesAdmission(t *testing.T) {
	const valid = `{"note":"context — 雪"}`
	file := filepath.Join(t.TempDir(), "properties.json")
	if err := os.WriteFile(file, []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{valid, "@" + file, "@-"} {
		properties, err := graphPreviewProperties(input, strings.NewReader(valid))
		if err != nil || !reflect.DeepEqual(properties, map[string]any{"note": "context — 雪"}) {
			t.Fatalf("%q: properties=%v error=%v", input, properties, err)
		}
	}
	for name, input := range map[string]string{
		"null": "null", "array": "[]", "scalar": `"note"`, "trailing": `{} {}`,
		"duplicate":         `{"note":"one","note":"two"}`,
		"escaped duplicate": `{"note":"one","\u006eote":"two"}`,
		"lone surrogate":    `{"note":"\ud800"}`,
		"invalid UTF8":      "{\"note\":\"\xff\"}",
		"oversized":         strings.Repeat(" ", graphPreviewPropertiesLimit) + "{}",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := graphPreviewProperties(input, strings.NewReader("")); err == nil {
				t.Fatal("invalid input admitted")
			}
		})
	}
}

func TestGraphPreviewRevisionGuardIsExplicit(t *testing.T) {
	for _, tc := range []struct {
		name     string
		args     []string
		required bool
		wantErr  bool
	}{
		{"optional absent", nil, false, false},
		{"required absent", nil, true, true},
		{"observed", []string{"--if-revision=observed"}, true, false},
		{"empty", []string{"--if-revision="}, true, true},
		{"unconditional", []string{"--unconditional"}, true, false},
		{"false is not permission", []string{"--unconditional=false"}, false, true},
		{"both", []string{"--if-revision=observed", "--unconditional"}, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := &cobra.Command{}
			cmd.Flags().String("if-revision", "", "")
			cmd.Flags().Bool("unconditional", false, "")
			if err := cmd.ParseFlags(tc.args); err != nil {
				t.Fatal(err)
			}
			_, _, err := graphPreviewRevisionGuard(cmd, false, tc.required)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v, wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func TestGraphPreviewEditRevisionGuardDefaultsToCurrent(t *testing.T) {
	for _, command := range []*cobra.Command{rememberCmd, updateCmd} {
		if command.Flags().Lookup("unconditional") != nil {
			t.Fatalf("%s still advertises the removed edit flag", command.CommandPath())
		}
	}
	for _, tc := range []struct {
		args         []string
		wantRevision string
		wantCurrent  bool
		wantError    bool
	}{
		{nil, "", true, false},
		{[]string{"--if-revision=observed"}, "observed", false, false},
		{[]string{"--if-revision="}, "", false, true},
	} {
		cmd := &cobra.Command{}
		cmd.Flags().String("if-revision", "", "")
		if err := cmd.ParseFlags(tc.args); err != nil {
			t.Fatal(err)
		}
		revision, current, err := graphPreviewEditRevisionGuard(cmd)
		if revision != tc.wantRevision || current != tc.wantCurrent || (err != nil) != tc.wantError {
			t.Fatalf("args=%v revision=%q current=%t err=%v", tc.args, revision, current, err)
		}
	}
}

func TestGraphPreviewLocalTypeSelectors(t *testing.T) {
	const scope = "https://example.invalid/demo/"
	for _, selector := range []string{"types/preview-related-v2", scope + "types/preview-related-v2", "types/example-follows"} {
		got, err := graphPreviewTypeURL(scope, selector)
		if err != nil || got != scope+strings.TrimPrefix(selector, scope) {
			t.Fatalf("%q: %q, %v", selector, got, err)
		}
	}
	for _, selector := range []string{"", "types/", "types/../x", "types/example%2Dfollows", "types/example?x=1", "types//x", "beads/foo", "https://other.invalid/types/example-follows", scope + "types/example-follows#x"} {
		if _, err := graphPreviewTypeURL(scope, selector); err == nil {
			t.Errorf("accepted %q", selector)
		}
	}
}

func TestGraphPreviewLinkDisplayKeepsForeignURLs(t *testing.T) {
	old := graphPreviewConfig
	graphPreviewConfig = &configfile.Config{GraphScopeURL: "https://example.invalid/demo/"}
	t.Cleanup(func() { graphPreviewConfig = old })
	for input, want := range map[string]string{
		"https://example.invalid/demo/types/preview-related-v2": "types/preview-related-v2",
		"https://example.invalid/demo/links/edge":               "links/edge",
		"https://example.invalid/demo/beads/work":               "beads/work",
		"https://remote.invalid/beads/work":                     "https://remote.invalid/beads/work",
	} {
		if got := graphPreviewDisplayLocalURL(input); got != want {
			t.Errorf("%q displayed as %q, want %q", input, got, want)
		}
	}
}

func TestGraphPreviewLinkTypeFlags(t *testing.T) {
	old := graphPreviewConfig
	graphPreviewConfig = &configfile.Config{GraphScopeURL: "https://example.invalid/demo/"}
	defer func() { graphPreviewConfig = old }()
	for _, tc := range []struct {
		args []string
		want string
		fail bool
	}{
		{nil, "", false},
		{[]string{"--link-type", "types/preview-related-v2"}, "https://example.invalid/demo/types/preview-related-v2", false},
		{[]string{"--resource-type", "types/preview-related-v2"}, "https://example.invalid/demo/types/preview-related-v2", false},
		{[]string{"--link-type="}, "", true},
		{[]string{"--link-type", "types/example-follows", "--resource-type", "types/example-follows"}, "", true},
	} {
		cmd := &cobra.Command{}
		registerGraphLinkTypeFlag(cmd)
		if err := cmd.ParseFlags(tc.args); err != nil {
			t.Fatal(err)
		}
		got, err := graphPreviewLinkType(cmd)
		if (err != nil) != tc.fail || got != tc.want {
			t.Fatalf("%v: got=%q err=%v", tc.args, got, err)
		}
		if !cmd.Flags().Lookup("resource-type").Hidden || cmd.Flags().Lookup("link-type").Hidden {
			t.Fatal("preferred flag not exposed correctly")
		}
	}
}

// --type belongs to the ordinary blocking Dependency route. Once --link-type
// selects an informational Type, the flag allow-list refuses --type before any
// later check could see it, so the route needs no second refusal of its own.
func TestGraphPreviewLinkTypeRouteRefusesTypeFlagFirst(t *testing.T) {
	oldConfig, oldDir := graphPreviewConfig, graphPreviewDir
	graphPreviewConfig = &configfile.Config{GraphScopeURL: "https://example.invalid/demo/"}
	graphPreviewDir = filepath.Join(t.TempDir(), ".beads")
	t.Cleanup(func() { graphPreviewConfig, graphPreviewDir = oldConfig, oldDir })
	diagnostic := captureGraphFailures(t)

	cmd := &cobra.Command{}
	cmd.Flags().StringP("type", "t", "blocks", "")
	registerGraphLinkTypeFlag(cmd)
	cmd.Flags().String("id", "", "")
	cmd.Flags().String("properties", "", "")
	cmd.Flags().String("if-source-revision", "", "")
	cmd.Flags().Bool("unconditional-source", false, "")
	if err := cmd.ParseFlags([]string{"--type", "blocks", "--link-type", "types/preview-related-v2", "--unconditional-source"}); err != nil {
		t.Fatal(err)
	}
	if !cmd.Flags().Changed("type") || !graphPreviewLinkTypeChanged(cmd) {
		t.Fatal("the combination under test was not parsed as set")
	}
	err := runGraphPreviewLink(cmd, []string{"policy", "work"})
	var exit *exitError
	if !errors.As(err, &exit) || exit.Code != 5 {
		t.Fatalf("--type with --link-type was not refused with exit 5: %v", err)
	}
	if got, want := diagnostic.String(), "capability_unavailable: graph preview does not implement --type\n"; got != want {
		t.Fatalf("refusal = %q, want %q", got, want)
	}
}
