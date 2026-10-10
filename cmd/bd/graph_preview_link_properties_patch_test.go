package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/graphstore"
)

func TestGraphPreviewLinkPropertiesPatchRequest(t *testing.T) {
	oldActor := actor
	actor = "link-patch-author"
	t.Cleanup(func() { actor = oldActor })
	raw := `[{"op":"add","path":"/note","value":" 雪\r\n "}]`
	file := filepath.Join(t.TempDir(), "patch.json")
	if err := os.WriteFile(file, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, input                        string
		flags                              []string
		revision, source                   string
		unconditional, sourceUnconditional bool
	}{
		{"literal-dual-guard", raw, []string{"--if-revision=link-old", "--if-source-revision=source-old"}, "link-old", "source-old", false, false},
		{"file-Link-unconditional", "@" + file, []string{"--if-source-revision=source-old"}, "", "source-old", true, false},
		{"stdin-source-unconditional", "@-", []string{"--if-revision=link-old", "--unconditional-source"}, "link-old", "", false, true},
		{"default-source-unconditional", raw, []string{}, "", "", true, true},
		{"both-unconditional", raw, []string{"--unconditional-source"}, "", "", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := memoryPropertiesPatchCommand(t, append([]string{"--patch=" + tc.input}, tc.flags...)...)
			cmd.SetIn(strings.NewReader(raw))
			got, err := graphPreviewLinkPropertiesPatchRequest(cmd, "links/context")
			if err != nil || got.Path != "links/context" || !bytes.Equal(got.Patch, []byte(raw)) || got.Actor != actor || got.ExpectedRevision != tc.revision || got.ExpectedSourceRevision != tc.source || got.Unconditional != tc.unconditional || got.UnconditionalSource != tc.sourceUnconditional {
				t.Fatalf("request lost intent: %+v %v", got, err)
			}
		})
	}
}

func TestGraphPreviewLinkPropertiesPatchRefusesBeforeInput(t *testing.T) {
	isolatePropertiesPatchAdmission(t)
	oldConfig, oldReadonly, oldJSON, oldStructured := graphPreviewConfig, readonlyMode, jsonOutput, graphPreviewStructuredErrors
	t.Cleanup(func() {
		graphPreviewConfig, readonlyMode, jsonOutput, graphPreviewStructuredErrors = oldConfig, oldReadonly, oldJSON, oldStructured
	})
	graphPreviewConfig = &configfile.Config{GraphScopeURL: "https://example.invalid/"}
	readonlyMode, jsonOutput, graphPreviewStructuredErrors = false, false, false
	for _, tc := range []struct {
		name  string
		flags []string
		code  int
	}{
		{"empty-Link-guard", []string{"--if-revision="}, 2},
		{"invalid-Link-token", []string{"--if-revision=\xff"}, 2},
		{"oversized-Link-token", []string{"--if-revision=" + strings.Repeat("x", graphstore.PreviewVersionTokenLimit+1)}, 2},
		{"empty-source-guard", []string{"--if-source-revision="}, 2},
		{"both-source-guards", []string{"--if-source-revision=old", "--unconditional-source"}, 2},
		{"false-source-unconditional", []string{"--unconditional-source=false"}, 2},
		{"invalid-source-token", []string{"--if-source-revision=\xff"}, 2},
		{"oversized-source-token", []string{"--if-source-revision=" + strings.Repeat("x", graphstore.PreviewVersionTokenLimit+1)}, 2},
		{"replacement-conflict", []string{"--properties={}"}, 5},
		{"Issue-title-conflict", []string{"--title=x"}, 5},
		{"claim-conflict", []string{"--claim"}, 5},
		{"false-claim-conflict", []string{"--claim=false"}, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := memoryPropertiesPatchCommand(t, append([]string{"--patch=@-"}, tc.flags...)...)
			probe := &memoryPropertiesPatchProbe{}
			cmd.SetIn(probe)
			err := runGraphPreviewUpdate(cmd, []string{"https://example.invalid/links/context"})
			var failure *exitError
			if !errors.As(err, &failure) || failure.Code != tc.code || probe.reads != 0 {
				t.Fatalf("admission/read: %v reads=%d", err, probe.reads)
			}
		})
	}
	t.Run("malformed-Link-selector", func(t *testing.T) {
		cmd := memoryPropertiesPatchCommand(t, "--patch=@-")
		probe := &memoryPropertiesPatchProbe{}
		cmd.SetIn(probe)
		err := runGraphPreviewUpdate(cmd, []string{"links/"})
		var failure *exitError
		if !errors.As(err, &failure) || failure.Code != 2 || probe.reads != 0 {
			t.Fatalf("malformed selector/input: %v reads=%d", err, probe.reads)
		}
	})
	t.Run("readonly", func(t *testing.T) {
		readonlyMode = true
		defer func() { readonlyMode = false }()
		cmd := memoryPropertiesPatchCommand(t, "--patch=@-")
		probe := &memoryPropertiesPatchProbe{}
		cmd.SetIn(probe)
		err := runGraphPreviewUpdate(cmd, []string{"links/context"})
		var failure *exitError
		if !errors.As(err, &failure) || failure.Code != 5 || probe.reads != 0 {
			t.Fatalf("readonly/read: %v reads=%d", err, probe.reads)
		}
	})
}

func TestGraphPreviewLinkPropertiesPatchSyntaxBeforeStore(t *testing.T) {
	isolatePropertiesPatchAdmission(t)
	oldConfig, oldReadonly, oldJSON, oldStructured := graphPreviewConfig, readonlyMode, jsonOutput, graphPreviewStructuredErrors
	t.Cleanup(func() {
		graphPreviewConfig, readonlyMode, jsonOutput, graphPreviewStructuredErrors = oldConfig, oldReadonly, oldJSON, oldStructured
	})
	graphPreviewConfig = &configfile.Config{GraphScopeURL: "https://example.invalid/"}
	readonlyMode, jsonOutput, graphPreviewStructuredErrors = false, false, false
	for _, tc := range []struct{ name, raw string }{
		{"empty-operations", `[]`},
		{"unsupported-operation", `[{"op":"move","path":"/note","value":"x"}]`},
		{"duplicate-member", `[{"op":"add","path":"/note","value":1,"value":2}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := memoryPropertiesPatchCommand(t, "--patch="+tc.raw)
			err := runGraphPreviewUpdate(cmd, []string{"links/context"})
			var failure *exitError
			if !errors.As(err, &failure) || failure.Code != 2 {
				t.Fatalf("malformed patch reached store: %v", err)
			}
		})
	}
}
