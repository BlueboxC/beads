//go:build cgo

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

func TestGraphPreviewSelectedEnvironment(t *testing.T) {
	bd := buildBDUnderTest(t)
	work, home := t.TempDir(), t.TempDir()
	graphPolicyCLI(t, bd, work, home, nil, "", "init", "--graph-mode", "link", "--scope-url", "https://example.invalid/environment/", "--skip-hooks", "--skip-agents", "--non-interactive", "--json")
	envPath := filepath.Join(work, ".beads", ".env")
	for _, tc := range []struct{ name, setting, code string }{
		{"readonly", "BD_READONLY=true", "permission_denied"},
		{"workspace", "BEADS_DIR=" + t.TempDir(), "not_authority"},
		{"database", "BEADS_DB=" + filepath.Join(t.TempDir(), ".beads", "dolt"), "capability_unavailable"},
		{"deprecated-database", "BD_DB=" + filepath.Join(t.TempDir(), ".beads", "dolt"), "capability_unavailable"},
		{"backend", "BD_BACKEND=sqlite", "invalid_selector"},
		{"graph-mode", "BD_GRAPH_MODE=dependency", "not_authority"},
		{"server-host", "BEADS_DOLT_SERVER_HOST=other.invalid", "not_authority"},
		{"proxied-server", "BEADS_DOLT_PROXIED_SERVER=1", "not_authority"},
		{"server-port", "BEADS_DOLT_SERVER_PORT=invalid", "not_authority"},
		{"server-database", "BEADS_DOLT_SERVER_DATABASE=other", "not_authority"},
		{"data-dir", "BEADS_DOLT_DATA_DIR=" + t.TempDir(), "not_authority"},
		{"tls", "BEADS_DOLT_SERVER_TLS=true", "not_authority"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeFile(t, envPath, []byte(tc.setting+"\n"))
			before := legacyUpgradeTreeDigest(t, work)
			path := "beads/refused-" + tc.name
			operatorOnly := tc.name == "readonly" || tc.name == "backend" || tc.name == "graph-mode" || tc.name == "proxied-server" || tc.name == "data-dir"
			var env []string
			if operatorOnly {
				// The fork imports only passive selectors/connections from project .env.
				// Unsupported keys stay ignored; an explicit operator setting is enforced.
				graphPolicyCLI(t, bd, work, home, nil, "", "status", "--graph", "--json")
				writeFile(t, envPath, nil)
				before = legacyUpgradeTreeDigest(t, work)
				env = []string{tc.setting}
			}
			graphPolicyCLI(t, bd, work, home, env, tc.code, "remember", "must not persist", "--id", path, "--title", "Refused", "--json")
			if after := legacyUpgradeTreeDigest(t, work); after != before {
				t.Fatal("refusal changed selected workspace")
			}
			if err := os.Remove(envPath); err != nil {
				t.Fatal(err)
			}
			graphPolicyCLI(t, bd, work, home, nil, "not_found", "show", path, "--json")
		})
	}
	// Shell policy wins over .env, and relative explicit workspace selection
	// remains usable. This creates and reads through ordinary CLI processes.
	writeFile(t, envPath, []byte("BD_READONLY=true\n"))
	env := []string{"BD_READONLY=false", "BEADS_DIR=.beads"}
	graphPolicyCLI(t, bd, work, home, env, "", "remember", "shell policy", "--id", "beads/allowed", "--title", "Allowed", "--json")
	graphPolicyCLI(t, bd, work, home, env, "", "show", "beads/allowed", "--json")
}

func TestGraphPreviewInitRejectsProxiedRoute(t *testing.T) {
	bd := buildBDUnderTest(t)
	work, home := t.TempDir(), t.TempDir()
	before := legacyUpgradeTreeDigest(t, work)
	graphPolicyCLI(t, bd, work, home, []string{"BEADS_DOLT_PROXIED_SERVER=1"}, "not_authority", "init", "--graph-mode", "link", "--scope-url", "https://example.invalid/proxy/", "--skip-hooks", "--skip-agents", "--non-interactive", "--json")
	if after := legacyUpgradeTreeDigest(t, work); after != before {
		t.Fatal("refused proxied initialization changed workspace")
	}
}

func TestGraphPreviewStaticCredentials(t *testing.T) {
	// Only disposable test credentials are read. The endpoint is persisted;
	// environment route assertions must never change the lookup's host/port.
	credentials := filepath.Join(t.TempDir(), "credentials")
	writeFile(t, credentials, []byte("[persisted.invalid:4444]\npassword=file-test-value\n[other.invalid:5555]\npassword=wrong-route-value\n"))
	t.Setenv("BEADS_CREDENTIALS_FILE", credentials)
	t.Setenv("BEADS_DOLT_PASSWORD", "")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "other.invalid")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "5555")
	t.Setenv("BEADS_DOLT_SERVER_USER", "must-not-override-resolved-user")
	cfg := &configfile.Config{DoltMode: "server", DoltServerHost: "persisted.invalid", DoltServerPort: 4444, DoltServerUser: "test-user"}
	options := graphOptions(cfg)
	if options.ServerHost != cfg.DoltServerHost || options.ServerPort != cfg.DoltServerPort || options.ServerUser != "test-user" || options.ServerPassword != "file-test-value" {
		t.Fatal("credentials were not selected for the persisted route")
	}
	t.Setenv("BEADS_DOLT_PASSWORD", "environment-test-value")
	if graphOptions(cfg).ServerPassword != "environment-test-value" {
		t.Fatal("environment password did not override the credential file")
	}
}
