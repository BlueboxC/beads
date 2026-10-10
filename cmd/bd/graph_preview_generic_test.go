package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/graphstore"
)

func genericPreviewCommand(t *testing.T, args ...string) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{RunE: graphCmd.RunE, Args: graphCmd.Args}
	values := pflag.NewFlagSet("generic-test-values", pflag.ContinueOnError)
	clone := func(flag *pflag.Flag) {
		switch flag.Value.Type() {
		case "string":
			values.String(flag.Name, flag.DefValue, flag.Usage)
		case "bool":
			value, err := strconv.ParseBool(flag.DefValue)
			if err != nil {
				t.Fatal(err)
			}
			values.Bool(flag.Name, value, flag.Usage)
		case "int":
			value, err := strconv.Atoi(flag.DefValue)
			if err != nil {
				t.Fatal(err)
			}
			values.Int(flag.Name, value, flag.Usage)
		default:
			t.Fatalf("clone registered graph flag --%s type %s explicitly", flag.Name, flag.Value.Type())
		}
		copied := *flag
		copied.Value = values.Lookup(flag.Name).Value
		copied.Changed = false
		cmd.Flags().AddFlag(&copied)
	}
	graphCmd.LocalNonPersistentFlags().VisitAll(clone)
	for _, name := range []string{"json", "readonly", "quiet"} {
		if cmd.Flags().Lookup(name) == nil {
			flag := rootCmd.PersistentFlags().Lookup(name)
			if flag == nil {
				t.Fatalf("missing registered flag %s", name)
			}
			clone(flag)
		}
	}
	if err := cmd.ParseFlags(args); err != nil {
		t.Fatal(err)
	}
	return cmd
}

func TestGraphPreviewGenericInput(t *testing.T) {
	savedJSON, savedStructured := jsonOutput, graphPreviewStructuredErrors
	jsonOutput, graphPreviewStructuredErrors = false, false
	t.Cleanup(func() { jsonOutput, graphPreviewStructuredErrors = savedJSON, savedStructured })
	for _, tc := range []struct {
		name        string
		flags, args []string
		env         string
		code        int
	}{
		{name: "defaults", flags: []string{"--view=generic"}, args: []string{"beads/a"}},
		{name: "bare-bead-id", flags: []string{"--view=generic"}, args: []string{"a"}},
		{name: "exact-url", flags: []string{"--view=generic", "--readonly", "--quiet", "--json"}, args: []string{genericTestScope + "beads/a"}},
		{name: "zero-depth", flags: []string{"--view=generic", "--depth=0", "--max-nodes=1", "--max-links=1"}, args: []string{"beads/a"}},
		{name: "hard-max", flags: []string{"--view=generic", "--depth=1000", "--max-nodes=1000", "--max-links=1000"}, args: []string{"beads/a"}},
		{name: "zero-env", flags: []string{"--view=generic"}, args: []string{"beads/a"}, env: "0"},
		{name: "missing-view", args: []string{"beads/a"}, code: 5},
		{name: "wrong-view", flags: []string{"--view=issue"}, args: []string{"beads/a"}, code: 5},
		{name: "missing-root", flags: []string{"--view=generic"}, code: 2},
		{name: "link-root", flags: []string{"--view=generic"}, args: []string{"links/a"}, code: 2},
		{name: "type-root", flags: []string{"--view=generic"}, args: []string{genericTestScope + "types/preview-memory-v1"}, code: 2},
		{name: "foreign-root", flags: []string{"--view=generic"}, args: []string{"https://foreign.test/beads/a"}, code: 2},
		{name: "version-root", flags: []string{"--view=generic"}, args: []string{genericTestScope + "beads/a?version=old"}, code: 2},
		{name: "unsupported-root", flags: []string{"--view=generic"}, args: []string{"alias/a"}, code: 2},
		{name: "direction", flags: []string{"--view=generic", "--direction=sideways"}, args: []string{"beads/a"}, code: 2},
		{name: "negative-depth", flags: []string{"--view=generic", "--depth=-1"}, args: []string{"beads/a"}, code: 2},
		{name: "excess-depth", flags: []string{"--view=generic", "--depth=1001"}, args: []string{"beads/a"}, code: 2},
		{name: "zero-nodes", flags: []string{"--view=generic", "--max-nodes=0"}, args: []string{"beads/a"}, code: 2},
		{name: "excess-nodes", flags: []string{"--view=generic", "--max-nodes=1001"}, args: []string{"beads/a"}, code: 2},
		{name: "zero-links", flags: []string{"--view=generic", "--max-links=0"}, args: []string{"beads/a"}, code: 2},
		{name: "excess-links", flags: []string{"--view=generic", "--max-links=1001"}, args: []string{"beads/a"}, code: 2},
		{name: "positive-env", flags: []string{"--view=generic"}, args: []string{"beads/a"}, env: "1", code: 5},
		{name: "bad-env", flags: []string{"--view=generic"}, args: []string{"beads/a"}, env: "bad", code: 2},
		{name: "negative-env", flags: []string{"--view=generic"}, args: []string{"beads/a"}, env: "-1", code: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(maxRowsEnvVar, tc.env)
			cmd := genericPreviewCommand(t, tc.flags...)
			in, err := graphPreviewGenericInput(cmd, tc.args, genericTestScope)
			if tc.code != 0 {
				var exit *exitError
				if !errors.As(err, &exit) || exit.Code != tc.code || !reflect.DeepEqual(in, graphGenericInput{}) {
					t.Fatalf("invalid input returned partial request or wrong exit: %+v %v", in, err)
				}
				return
			}
			if err != nil || in.Root != genericTestScope+"beads/a" {
				t.Fatalf("root admission: %+v %v", in, err)
			}
			if tc.name == "defaults" && (in.Direction != "both" || in.Depth != 1 || in.MaxNodes != 100 || in.MaxLinks != 200) {
				t.Fatalf("wrong defaults: %+v", in)
			}
		})
	}
}

func TestGraphPreviewGenericLegacyFlagConflicts(t *testing.T) {
	savedJSON, savedStructured := jsonOutput, graphPreviewStructuredErrors
	jsonOutput, graphPreviewStructuredErrors = false, false
	t.Cleanup(func() { jsonOutput, graphPreviewStructuredErrors = savedJSON, savedStructured })
	t.Setenv(maxRowsEnvVar, "")
	for _, name := range []string{"all", "compact", "box", "dot", "html", "open", "max-rows"} {
		t.Run(name, func(t *testing.T) {
			value := "false"
			if name == "max-rows" {
				value = "0"
			}
			cmd := genericPreviewCommand(t, "--view=generic", "--"+name+"="+value)
			_, err := graphPreviewGenericInput(cmd, []string{"beads/a"}, genericTestScope)
			var exit *exitError
			if !errors.As(err, &exit) || exit.Code != 5 {
				t.Fatalf("changed legacy option silently accepted: %v", err)
			}
		})
	}
	cmd := genericPreviewCommand(t, "--view=generic")
	if cmd.ValidateArgs([]string{"beads/a", "beads/b"}) == nil {
		t.Fatal("existing Cobra excess-argument gate changed")
	}
	if cmd.ValidateArgs([]string{"beads/a"}) != nil {
		t.Fatal("one root rejected by existing Args")
	}
}

// This test replaces command globals and must not run in parallel.
func TestGraphPreviewGenericLegacyAdmission(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, name := range []string{"BEADS_DB", "BD_DB", "BD_GRAPH_MODE"} {
		t.Setenv(name, "")
	}
	directory := t.TempDir()
	t.Setenv("BEADS_DIR", directory)
	oldCmd, oldActive, oldConfig, oldDir := graphCmd, graphPreviewActive, graphPreviewConfig, graphPreviewDir
	oldJSON, oldStructured := jsonOutput, graphPreviewStructuredErrors
	t.Cleanup(func() {
		graphCmd, graphPreviewActive, graphPreviewConfig, graphPreviewDir = oldCmd, oldActive, oldConfig, oldDir
		jsonOutput, graphPreviewStructuredErrors = oldJSON, oldStructured
	})
	jsonOutput, graphPreviewStructuredErrors = false, false
	for _, flag := range []string{"--view=generic", "--view=", "--direction=both", "--depth=0", "--max-nodes=100", "--max-links=200"} {
		t.Run(flag, func(t *testing.T) {
			graphCmd = oldCmd
			cmd := genericPreviewCommand(t, flag)
			graphCmd = cmd
			handled, err := admitGraphPreview(cmd)
			var exit *exitError
			if !handled || !errors.As(err, &exit) || exit.Code != 5 || graphPreviewActive || graphPreviewConfig != nil {
				t.Fatalf("legacy generic flag opened/admitted store: %t %v", handled, err)
			}
		})
	}
	graphCmd = oldCmd
	cmd := genericPreviewCommand(t)
	graphCmd = cmd
	if handled, err := admitGraphPreview(cmd); handled || err != nil {
		t.Fatalf("bare legacy graph was hijacked: %t %v", handled, err)
	}
	if err := os.WriteFile(filepath.Join(directory, "metadata.json"), []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	graphCmd = oldCmd
	cmd = genericPreviewCommand(t, "--view=generic")
	graphCmd = cmd
	var handled bool
	var err error
	diagnostic := captureStderr(t, func() { handled, err = admitGraphPreview(cmd) })
	var exit *exitError
	if !handled || !errors.As(err, &exit) || exit.Code != 5 || graphPreviewActive {
		t.Fatalf("corrupt legacy metadata fell through with generic flags: %t %v", handled, err)
	}
	if !strings.Contains(diagnostic, "graph_not_initialized") || !strings.Contains(diagnostic, "invalid character") {
		t.Fatalf("corrupt metadata diagnostic lost its cause: %q", diagnostic)
	}
}

func TestGraphPreviewGenericDispatchBeforeStore(t *testing.T) {
	oldActive, oldConfig, oldJSON, oldStructured := graphPreviewActive, graphPreviewConfig, jsonOutput, graphPreviewStructuredErrors
	graphPreviewActive = true
	graphPreviewConfig = &configfile.Config{GraphScopeURL: genericTestScope}
	jsonOutput = false
	graphPreviewStructuredErrors = false
	t.Cleanup(func() {
		graphPreviewActive, graphPreviewConfig, jsonOutput, graphPreviewStructuredErrors = oldActive, oldConfig, oldJSON, oldStructured
	})
	cmd := genericPreviewCommand(t, "--view=generic", "--all=false")
	err := cmd.RunE(cmd, []string{"beads/a"})
	var exit *exitError
	if !errors.As(err, &exit) || exit.Code != 5 {
		t.Fatalf("graph command bypassed early preview admission: %v", err)
	}
}

type genericFailWriter struct {
	err   error
	short bool
}

func (w genericFailWriter) Write(p []byte) (int, error) {
	if w.short {
		return len(p) - 1, nil
	}
	return 0, w.err
}

func TestGraphPreviewGenericOutput(t *testing.T) {
	result, err := projectGraphGeneric(genericTestSnapshot(t, genericTestEdge{"ab", "a", "b"}), genericTestScope, genericTestInput("a", "out", 0))
	if err != nil {
		t.Fatal(err)
	}
	output, err := renderGraphGeneric(result, false, false)
	expected := "Generic graph (summary; graph preview)\nRoot \"https://example.test/graph/beads/a\" direction out depth 0\nBead \"https://example.test/graph/beads/a\" \"Title a — 雪\"\nComplete: false; frontier: 1\nFrontier \"https://example.test/graph/beads/a\"\n"
	if err != nil || output != expected {
		t.Fatalf("human summary differs: %q %v", output, err)
	}
	if output, err := renderGraphGeneric(result, false, true); err != nil || output != "" {
		t.Fatalf("quiet emitted: %q %v", output, err)
	}
	full, err := renderGraphGeneric(result, true, false)
	if err != nil {
		t.Fatal(err)
	}
	quiet, err := renderGraphGeneric(result, true, true)
	if err != nil || full != quiet {
		t.Fatal("quiet suppressed or changed JSON")
	}
	result.Nodes[0].Title = "Line\n雪"
	if output, err := renderGraphGeneric(result, false, false); err != nil || !strings.Contains(output, `"Line\n雪"`) {
		t.Fatal("title escaped presentation changed")
	}
	for _, structured := range []bool{false, true} {
		result.Nodes[0].Title = ""
		base, err := renderGraphGeneric(result, structured, false)
		if err != nil {
			t.Fatal(err)
		}
		result.Nodes[0].Title = strings.Repeat("x", graphGenericOutputLimit-len(base))
		at, err := renderGraphGeneric(result, structured, false)
		if err != nil || len(at) != graphGenericOutputLimit {
			t.Fatalf("exact serialized cap refused: bytes=%d err=%v", len(at), err)
		}
		result.Nodes[0].Title += "x"
		output, err := renderGraphGeneric(result, structured, false)
		if !errors.Is(err, graphstore.ErrLimitExceeded) || output != "" {
			t.Fatal("oversized output returned partial bytes")
		}
	}
	sentinel := errors.New("output device failed")
	if err := writeGraphGeneric(genericFailWriter{err: sentinel}, "result"); !errors.Is(err, sentinel) {
		t.Fatalf("writer failure swallowed: %v", err)
	}
	if err := writeGraphGeneric(genericFailWriter{short: true}, "result"); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short write swallowed: %v", err)
	}
	var out bytes.Buffer
	if err := writeGraphGeneric(&out, full); err != nil || out.String() != full {
		t.Fatal("complete output altered")
	}
}
