package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/steveyegge/beads/internal/atomicfile"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/templates/agents"
)

// Graph initialization only replaces the exact managed marker format it knows.
// The shared parser deliberately accepts legacy and extensible metadata; using
// it alone would allow duplicate fields to hide a full profile behind minimal.
var graphPreviewAgentMarker = regexp.MustCompile(fmt.Sprintf(
	`^<!-- BEGIN BEADS INTEGRATION v:%d profile:[a-z][a-z-]* hash:[0-9a-f]{8} -->$`, agents.MarkerVersion))

// Graph init installs guidance in the existing configured agents file. The
// separate graph Claude adapter can then import it and install only Steph's
// supported Stop handler; ordinary setup also installs unsupported prime hooks.
type graphPreviewAgentInstructions struct {
	path    string
	before  []byte
	after   []byte
	mode    os.FileMode
	existed bool
}

// The step each command offers when it refuses the agents file. --skip-agents
// belongs to init alone: setup reads the same file but cannot skip it, so the
// file is the user's to repair there.
const (
	graphPreviewInitAgentsRemedy  = "preserve it with --skip-agents"
	graphPreviewSetupAgentsRemedy = "repair that file by hand and run bd setup claude again"
)

// remedy is the caller's step for a refused file and ends each refusal that
// names the file's own state. A caller with no step to offer passes "".
func prepareGraphPreviewAgentInstructions(workspace, filename string, skip bool, remedy string) (*graphPreviewAgentInstructions, error) {
	if skip {
		return nil, nil
	}
	if err := config.ValidateAgentsFile(filename); err != nil {
		return nil, err
	}
	step := ""
	if remedy != "" {
		step = "; " + remedy
	}
	plan := &graphPreviewAgentInstructions{path: filepath.Join(workspace, filename), mode: 0644}
	info, err := os.Lstat(plan.path)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect agent file: %w", err)
	}
	if err == nil {
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("agent file %s is not a regular file%s", filename, step)
		}
		plan.existed, plan.mode = true, info.Mode().Perm()
		plan.before, err = os.ReadFile(plan.path) // #nosec G304 -- validated simple filename in selected workspace
		if err != nil {
			return nil, fmt.Errorf("read agent file: %w", err)
		}
	}
	content := string(plan.before)
	const begin = "<!-- BEGIN BEADS INTEGRATION"
	const end = "<!-- END BEADS INTEGRATION -->"
	begins, ends := strings.Count(content, begin), strings.Count(content, end)
	if begins == 0 && ends == 0 {
		if content != "" && !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		if content != "" {
			content += "\n"
		}
		plan.after = []byte(content + agents.RenderSection(agents.ProfileGraphPreview))
		return plan, nil
	}
	if begins != 1 || ends != 1 {
		return nil, fmt.Errorf("agent file %s has malformed or duplicate managed sections%s", filename, step)
	}
	marker := strings.TrimSuffix(strings.SplitN(content[strings.Index(content, begin):], "\n", 2)[0], "\r")
	if !graphPreviewAgentMarker.MatchString(marker) {
		return nil, fmt.Errorf("agent file %s has an unsupported or malformed managed marker%s", filename, step)
	}
	meta := agents.ParseMarker(marker)
	if meta == nil || (meta.Profile != agents.ProfileMinimal && meta.Profile != agents.ProfileGraphPreview) {
		return nil, fmt.Errorf("agent file %s has a full or unknown managed profile%s", filename, step)
	}
	replaced, _, err := agents.ReplaceSection(content, agents.ProfileGraphPreview)
	if err != nil {
		return nil, fmt.Errorf("agent file %s: %w%s", filename, err, step)
	}
	plan.after = []byte(replaced)
	return plan, nil
}

func (p *graphPreviewAgentInstructions) install() error {
	if p == nil {
		return nil
	}
	// Recheck after DB initialization: do not overwrite an edit or symlink that
	// appeared since preflight. Atomic replacement avoids truncating user text
	// on an I/O failure; this is not a lock or a concurrent file-editor protocol.
	// The store exists by now and init cannot be run over it, so no step is offered.
	current, err := prepareGraphPreviewAgentInstructions(filepath.Dir(p.path), filepath.Base(p.path), false, "")
	if err != nil {
		return err
	}
	if p.existed != current.existed || p.mode != current.mode || !bytes.Equal(p.before, current.before) {
		return fmt.Errorf("agent file changed during initialization; it was not overwritten")
	}
	if p.existed && bytes.Equal(p.before, p.after) {
		return nil
	}
	if err := atomicfile.WriteFile(p.path, p.after, p.mode); err != nil {
		return fmt.Errorf("write agent file: %w", err)
	}
	return nil
}
