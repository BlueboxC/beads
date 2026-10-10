package uimd

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/ui"
)

func TestRenderMarkdownRejectsEncodedTerminalControls(t *testing.T) {
	withMarkdownEnv(t, map[string]string{"NO_COLOR": "", "TERM": "xterm-256color", "CLICOLOR_FORCE": "1", "FORCE_HYPERLINK": "", "BD_AGENT_MODE": "", "CLAUDE_CODE": ""})
	for _, payload := range []string{"before &#27;[2J after", "before &#x1b;]52;c;dGVzdA==&#7; after", "before \x1b[2J after"} {
		out := RenderMarkdown(ui.SanitizeForTerminal(payload))
		if strings.Contains(out, "\x1b[2J") || strings.Contains(out, "\x1b]52;") {
			t.Errorf("encoded terminal control survived rendering: %q", out)
		}
		if !strings.Contains(out, "before") || !strings.Contains(out, "after") {
			t.Errorf("printable content lost: %q", out)
		}
	}
	if out := RenderMarkdown("**safe** &#169; text"); !strings.Contains(out, "\x1b[") || !strings.Contains(out, "safe") || !strings.Contains(out, "©") {
		t.Errorf("legitimate style or printable entity lost: %q", out)
	}
}
