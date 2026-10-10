package agents

import (
	"strings"
	"testing"
)

func TestGraphPreviewGuidanceReusesDurableStorage(t *testing.T) {
	const durable = "## Durable storage\n\nYou have your own memory. It lives in beads: `bd remember` stores a fact and `bd recall` or `bd memories` reads it back, and its contents are available to you in later sessions in this project."
	section := RenderSection(ProfileGraphPreview)
	if strings.Count(section, durable) != 1 {
		t.Fatal("graph guidance must preserve the actual contributor section exactly once")
	}
	for _, command := range []string{
		`bd remember "Use UTC for timestamps" --id beads/time-policy --title "Timestamp policy"`,
		`bd recall beads/time-policy`,
		`bd memories timestamps --format records-json`,
		`bd status --graph`,
		"IDs and titles are generated when omitted",
		`bd setup claude`,
		`bd setup claude --check`,
		`bd setup claude --remove`,
	} {
		if !strings.Contains(section, command) {
			t.Errorf("missing supported graph guidance %q", command)
		}
	}
	if !strings.Contains(section, "registers the project-local Claude Stop reminder by default") ||
		!strings.Contains(section, "unless --skip-hooks or --skip-agents is used") ||
		!strings.Contains(section, "In an existing graph workspace without that hook") {
		t.Fatal("graph guidance must describe automatic registration and the existing-workspace setup path")
	}
	for _, line := range strings.Split(section, "`") {
		if strings.HasPrefix(line, "bd setup") && line != "bd setup claude" && line != "bd setup claude --check" && line != "bd setup claude --remove" {
			t.Fatalf("unsupported setup guidance %q", line)
		}
	}
	if strings.Count(section, "bd setup") != 3 {
		t.Fatal("graph guidance must contain exactly the three supported Claude setup commands")
	}
	for _, unsupported := range []string{"bd prime", "bd dolt", "git push", "git pull", "bd hooks"} {
		if strings.Contains(section, unsupported) {
			t.Errorf("graph guidance contains unsupported instruction %q", unsupported)
		}
	}
	for _, profile := range []Profile{ProfileMinimal, ProfileFull} {
		if !strings.Contains(RenderSection(profile), "bd prime") {
			t.Fatalf("ordinary %s rendering changed", profile)
		}
	}
}

func TestGraphPreviewGuidanceManagedReplacement(t *testing.T) {
	content := "user before\n" + RenderSection(ProfileMinimal) + "user after\n"
	got, changed, err := ReplaceSection(content, ProfileGraphPreview)
	if err != nil || !changed || !strings.HasPrefix(got, "user before\n") || !strings.HasSuffix(got, "user after\n") {
		t.Fatalf("replace managed section: changed=%t err=%v content=%q", changed, err, got)
	}
	idx := strings.Index(got, "<!-- BEGIN BEADS INTEGRATION")
	meta := ParseMarker(strings.SplitN(got[idx:], "\n", 2)[0])
	if meta == nil || meta.Profile != ProfileGraphPreview || meta.Hash != CurrentHash(ProfileGraphPreview) {
		t.Fatalf("incorrect graph marker: %+v", meta)
	}
	if next, changed, err := ReplaceSection(got, ProfileGraphPreview); err != nil || changed || next != got {
		t.Fatalf("current graph block must be idempotent: changed=%t err=%v", changed, err)
	}
}
