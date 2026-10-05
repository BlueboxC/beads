package setup

import (
	"encoding/json"
	"os"
	"regexp"
	"testing"
)

// The host dispatches SessionStart with source=compact before continuing an
// automatically compacted turn; a next-user-prompt fallback cannot cover it.
func TestCodexSessionStartDispatchesCompactedContinuation(t *testing.T) {
	for _, global := range []bool{false, true} {
		name := "project"
		if global {
			name = "global"
		}
		t.Run(name, func(t *testing.T) {
			env, _, _ := newCodexTestEnv(t)
			if err := installCodexNativeHooks(env, global); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(codexHooksPath(env, global))
			if err != nil {
				t.Fatal(err)
			}
			var config map[string]interface{}
			if err := json.Unmarshal(raw, &config); err != nil {
				t.Fatal(err)
			}
			hooks := config["hooks"].(map[string]interface{})
			for _, source := range []string{"startup", "resume", "clear", "compact"} {
				count := 0
				for _, value := range toInterfaceSlice(hooks["SessionStart"]) {
					entry := value.(map[string]interface{})
					matcher := entry["matcher"].(string)
					matches, err := regexp.MatchString(matcher, source)
					if err != nil {
						t.Fatal(err)
					}
					if !matches {
						continue
					}
					for _, value := range toInterfaceSlice(entry["hooks"]) {
						hook := value.(map[string]interface{})
						if hook["command"] == "bd codex-hook SessionStart" {
							count++
						}
					}
				}
				if count != 1 {
					t.Errorf("source=%s dispatches %d context hooks, want one", source, count)
				}
			}
		})
	}
}
