package configfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGraphModeDiscoveryAndRoundTrip(t *testing.T) {
	for _, filename := range []string{ConfigFileName, "config.json"} {
		for _, mode := range []string{"", GraphModeDependency, GraphModeLink, "future-format"} {
			t.Run(filename+"/"+mode, func(t *testing.T) {
				dir := t.TempDir()
				input := `{"backend":"dolt","graph_mode":"` + mode + `"}`
				path := filepath.Join(dir, filename)
				if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
					t.Fatal(err)
				}
				cfg, err := LoadForDiscovery(dir)
				if err != nil {
					t.Fatal(err)
				}
				got, err := cfg.GetGraphMode()
				if mode == "future-format" {
					if err == nil || got != "" {
						t.Fatalf("unknown marker resolved to %q, %v", got, err)
					}
				} else {
					want := mode
					if want == "" {
						want = GraphModeDependency
					}
					if err != nil || got != want {
						t.Fatalf("mode = %q, %v; want %q", got, err, want)
					}
				}
				entries, err := os.ReadDir(dir)
				if err != nil || len(entries) != 1 {
					t.Fatalf("discovery changed directory: %v, %v", entries, err)
				}
				after, err := os.ReadFile(path)
				if err != nil || string(after) != input {
					t.Fatalf("discovery changed metadata: %q, %v", after, err)
				}
				// Saving otherwise unrelated config must retain the marker,
				// including one this client does not understand.
				if err := cfg.Save(dir); err != nil {
					t.Fatal(err)
				}
				loaded, err := LoadForDiscovery(dir)
				if err != nil || loaded == nil || loaded.GraphMode != mode {
					t.Fatalf("marker lost during save: %#v, %v", loaded, err)
				}
			})
		}
	}
}

func TestGraphModeAbsentConfigPreservesLegacyDefault(t *testing.T) {
	cfg, err := LoadForDiscovery(t.TempDir())
	if err != nil || cfg != nil {
		t.Fatalf("absent config = %#v, %v", cfg, err)
	}
	mode, err := cfg.GetGraphMode()
	if err != nil || mode != GraphModeDependency {
		t.Fatalf("absent config mode = %q, %v", mode, err)
	}
}
