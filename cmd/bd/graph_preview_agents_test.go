package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/templates/agents"
)

func TestGraphPreviewAgentInstructionsFileRouting(t *testing.T) {
	for _, filename := range []string{"AGENTS.md", "TEAM.md"} {
		t.Run(filename, func(t *testing.T) {
			work := t.TempDir()
			path := filepath.Join(work, filename)
			user := "# User instructions\nKeep my instructions.\n"
			if err := os.WriteFile(path, []byte(user), 0600); err != nil {
				t.Fatal(err)
			}
			claude := "@" + filename + "\n"
			if err := os.WriteFile(filepath.Join(work, "CLAUDE.md"), []byte(claude), 0600); err != nil {
				t.Fatal(err)
			}
			plan, err := prepareGraphPreviewAgentInstructions(work, filename, false, "")
			if err != nil {
				t.Fatal(err)
			}
			if err := plan.install(); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if err != nil || !strings.HasPrefix(string(got), user) || strings.Count(string(got), "profile:graph-preview") != 1 {
				t.Fatalf("managed graph guidance did not preserve user content: %q err=%v", got, err)
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("existing permissions changed: info=%v err=%v", info, err)
			}
			importFile, err := os.ReadFile(filepath.Join(work, "CLAUDE.md"))
			if err != nil || string(importFile) != claude {
				t.Fatalf("CLAUDE import changed: %q err=%v", importFile, err)
			}
			plan, err = prepareGraphPreviewAgentInstructions(work, filename, false, "")
			if err != nil {
				t.Fatal(err)
			}
			if err := plan.install(); err != nil {
				t.Fatal(err)
			}
			next, err := os.ReadFile(path)
			if err != nil || string(next) != string(got) {
				t.Fatalf("repeated managed install changed current content: err=%v", err)
			}
		})
	}
}

func TestGraphPreviewAgentInstructionsRefuseUnsafeFiles(t *testing.T) {
	minimal := agents.RenderSection(agents.ProfileMinimal)
	cases := map[string]string{
		"full":                           agents.RenderSection(agents.ProfileFull),
		"unknown":                        strings.Replace(minimal, "profile:minimal", "profile:future", 1),
		"legacy":                         "<!-- BEGIN BEADS INTEGRATION -->\nkeep\n<!-- END BEADS INTEGRATION -->\n",
		"missing-end":                    "<!-- BEGIN BEADS INTEGRATION v:1 profile:minimal hash:12345678 -->\nkeep\n",
		"missing-begin":                  "keep\n<!-- END BEADS INTEGRATION -->\n",
		"duplicate":                      minimal + minimal,
		"reversed":                       "<!-- END BEADS INTEGRATION -->\n<!-- BEGIN BEADS INTEGRATION v:1 profile:minimal hash:12345678 -->\n",
		"malformed-begin":                strings.Replace(minimal, " -->", "", 1),
		"duplicate-profile-full-minimal": strings.Replace(minimal, "profile:minimal", "profile:full profile:minimal", 1),
		"duplicate-profile-minimal-full": strings.Replace(minimal, "profile:minimal", "profile:minimal profile:full", 1),
		"missing-version":                strings.Replace(minimal, " v:1", "", 1),
		"invalid-version":                strings.Replace(minimal, "v:1", "v:not-a-version", 1),
		"unsupported-version":            strings.Replace(minimal, "v:1", "v:2", 1),
		"duplicate-version":              strings.Replace(minimal, "v:1", "v:2 v:1", 1),
		"missing-hash":                   strings.Replace(minimal, " hash:"+agents.CurrentHash(agents.ProfileMinimal), "", 1),
		"invalid-hash":                   strings.Replace(minimal, "hash:"+agents.CurrentHash(agents.ProfileMinimal), "hash:not-a-digest", 1),
		"duplicate-hash":                 strings.Replace(minimal, "hash:", "hash:12345678 hash:", 1),
		"unknown-marker-field":           strings.Replace(minimal, " -->", " extra:value -->", 1),
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			work := t.TempDir()
			path := filepath.Join(work, "AGENTS.md")
			if err := os.WriteFile(path, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			if plan, err := prepareGraphPreviewAgentInstructions(work, "AGENTS.md", false, graphPreviewInitAgentsRemedy); err == nil || plan != nil || !strings.Contains(err.Error(), "--skip-agents") {
				t.Fatalf("unsafe managed section accepted: plan=%+v err=%v", plan, err)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != content {
				t.Fatalf("refusal changed user file: %q err=%v", got, err)
			}
			plan, err := prepareGraphPreviewAgentInstructions(work, "AGENTS.md", true, "")
			if err != nil || plan != nil {
				t.Fatalf("explicit skip must bypass inspection: %+v %v", plan, err)
			}
			if err := plan.install(); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, kind := range []string{"directory", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			work := t.TempDir()
			path := filepath.Join(work, "AGENTS.md")
			outside := filepath.Join(t.TempDir(), "outside.md")
			if err := os.WriteFile(outside, []byte("outside sentinel"), 0600); err != nil {
				t.Fatal(err)
			}
			var err error
			if kind == "directory" {
				err = os.Mkdir(path, 0700)
			} else {
				err = os.Symlink(outside, path)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := prepareGraphPreviewAgentInstructions(work, "AGENTS.md", false, ""); err == nil {
				t.Fatal("nonregular agent target accepted")
			}
			got, err := os.ReadFile(outside)
			if err != nil || string(got) != "outside sentinel" {
				t.Fatalf("outside file changed: %q %v", got, err)
			}
		})
	}
}

func TestGraphPreviewAgentInstructionsFreshAndMinimal(t *testing.T) {
	for _, old := range []string{"", "user\n" + agents.RenderSection(agents.ProfileMinimal) + "tail\n"} {
		work := t.TempDir()
		path := filepath.Join(work, "AGENTS.md")
		if old != "" {
			if err := os.WriteFile(path, []byte(old), 0600); err != nil {
				t.Fatal(err)
			}
		}
		plan, err := prepareGraphPreviewAgentInstructions(work, "AGENTS.md", false, "")
		if err != nil {
			t.Fatal(err)
		}
		if err := plan.install(); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil || strings.Contains(string(got), "bd prime") || !strings.Contains(string(got), "bd status --graph") {
			t.Fatalf("graph install contains ordinary preamble or lacks graph guidance: %q %v", got, err)
		}
		if old != "" && (!strings.HasPrefix(string(got), "user\n") || !strings.HasSuffix(string(got), "tail\n")) {
			t.Fatal("minimal profile replacement lost surrounding user content")
		}
	}
}

func TestGraphPreviewAgentInstructionsDetectChangedTarget(t *testing.T) {
	work := t.TempDir()
	path := filepath.Join(work, "AGENTS.md")
	plan, err := prepareGraphPreviewAgentInstructions(work, "AGENTS.md", false, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("concurrent user edit"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := plan.install(); err == nil {
		t.Fatal("installation overwrote an intervening edit")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "concurrent user edit" {
		t.Fatalf("intervening edit lost: %q %v", got, err)
	}
	if _, err := prepareGraphPreviewAgentInstructions(work, "../outside.md", false, ""); err == nil {
		t.Fatal("unsafe routing filename accepted")
	}
}

func TestGraphPreviewAgentInstructionsWriteFailure(t *testing.T) {
	work := filepath.Join(t.TempDir(), "workspace")
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	plan, err := prepareGraphPreviewAgentInstructions(work, "AGENTS.md", false, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(work); err != nil {
		t.Fatal(err)
	}
	if err := plan.install(); err == nil || !strings.Contains(err.Error(), "write agent file") {
		t.Fatalf("file-write failure was not returned: %v", err)
	}
	if _, err := os.Lstat(work); !os.IsNotExist(err) {
		t.Fatalf("failed installation created unexpected files: %v", err)
	}
}

// One input for each message prepareGraphPreviewAgentInstructions builds when it
// refuses an agents file. problem is the part of the message that names the cause.
var graphPreviewAgentRefusals = []struct {
	name, problem string
	arrange       func(t *testing.T, path string)
}{
	{"not-regular", "is not a regular file", func(t *testing.T, path string) {
		outside := filepath.Join(t.TempDir(), "outside.md")
		if err := os.WriteFile(outside, []byte("outside sentinel"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, path); err != nil {
			t.Fatal(err)
		}
	}},
	{"duplicate-sections", "has malformed or duplicate managed sections", func(t *testing.T, path string) {
		minimal := agents.RenderSection(agents.ProfileMinimal)
		writeAgentRefusalFile(t, path, minimal+minimal)
	}},
	{"unknown-marker-field", "has an unsupported or malformed managed marker", func(t *testing.T, path string) {
		minimal := agents.RenderSection(agents.ProfileMinimal)
		writeAgentRefusalFile(t, path, strings.Replace(minimal, " -->", " extra:value -->", 1))
	}},
	{"full-profile", "has a full or unknown managed profile", func(t *testing.T, path string) {
		writeAgentRefusalFile(t, path, agents.RenderSection(agents.ProfileFull))
	}},
	{"reversed-markers", "before BEGIN at", func(t *testing.T, path string) {
		writeAgentRefusalFile(t, path, "<!-- END BEADS INTEGRATION -->\n<!-- BEGIN BEADS INTEGRATION v:1 profile:minimal hash:12345678 -->\n")
	}},
}

func writeAgentRefusalFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// captureGraphFailures routes graphFailure diagnostics into a buffer, as plain
// text, for the length of one test.
func captureGraphFailures(t *testing.T) *bytes.Buffer {
	t.Helper()
	oldOutput, oldJSON, oldStructured := graphFailureOutput, jsonOutput, graphPreviewStructuredErrors
	var diagnostic bytes.Buffer
	graphFailureOutput, jsonOutput, graphPreviewStructuredErrors = &diagnostic, false, false
	t.Cleanup(func() {
		graphFailureOutput, jsonOutput, graphPreviewStructuredErrors = oldOutput, oldJSON, oldStructured
	})
	return &diagnostic
}

// bd setup claude reads the agents file but has no way to skip it, so a refusal
// it prints must not send the user to --skip-agents, which only init accepts.
func TestGraphPreviewSetupAgentFileRefusalsOfferNoInitFlag(t *testing.T) {
	file := config.SafeAgentsFile()
	for _, tc := range graphPreviewAgentRefusals {
		for _, check := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/check=%t", tc.name, check), func(t *testing.T) {
				work := t.TempDir()
				tc.arrange(t, filepath.Join(work, file))
				oldDir := graphPreviewDir
				graphPreviewDir = filepath.Join(work, ".beads")
				t.Cleanup(func() { graphPreviewDir = oldDir })
				diagnostic := captureGraphFailures(t)

				cmd := &cobra.Command{}
				for _, name := range []string{"project", "check", "remove"} {
					cmd.Flags().Bool(name, false, "")
				}
				if check {
					if err := cmd.ParseFlags([]string{"--check"}); err != nil {
						t.Fatal(err)
					}
				}
				err := runGraphPreviewSetup(cmd, []string{"claude"})
				var exit *exitError
				if !errors.As(err, &exit) || exit.Code != 5 {
					t.Fatalf("setup did not refuse with exit 5: %v", err)
				}
				got := diagnostic.String()
				for _, want := range []string{"capability_unavailable: agent file " + file, tc.problem} {
					if !strings.Contains(got, want) {
						t.Errorf("refusal omits %q: %q", want, got)
					}
				}
				if strings.Contains(got, "--skip-agents") {
					t.Errorf("setup offered an init-only flag: %q", got)
				}
				if want := "; repair that file by hand and run bd setup claude again\n"; !strings.HasSuffix(got, want) {
					t.Errorf("refusal does not end with the step setup can offer %q: %q", want, got)
				}
			})
		}
	}
}

// install() rechecks the file after the store exists. Init cannot be run again
// from there, so a refusal it returns must not offer --skip-agents either.
func TestGraphPreviewAgentInstructionsInstallRecheckOffersNoInitFlag(t *testing.T) {
	for _, tc := range graphPreviewAgentRefusals {
		t.Run(tc.name, func(t *testing.T) {
			work := t.TempDir()
			plan, err := prepareGraphPreviewAgentInstructions(work, "AGENTS.md", false, "")
			if err != nil {
				t.Fatal(err)
			}
			tc.arrange(t, filepath.Join(work, "AGENTS.md"))
			err = plan.install()
			if err == nil {
				t.Fatal("install accepted a file that became unsafe after preflight")
			}
			got := err.Error()
			for _, want := range []string{"agent file AGENTS.md", tc.problem} {
				if !strings.Contains(got, want) {
					t.Errorf("refusal omits %q: %q", want, got)
				}
			}
			if strings.Contains(got, "--skip-agents") {
				t.Errorf("install offered an init-only flag: %q", got)
			}
			if strings.HasSuffix(got, ";") || strings.HasSuffix(got, "; ") {
				t.Errorf("refusal ends in a dangling separator: %q", got)
			}
		})
	}
}
