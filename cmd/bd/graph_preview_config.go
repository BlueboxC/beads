package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
)

// C0 admits one persisted route. Load policy and credentials only from that
// workspace; selectors in its .env cannot silently send a write elsewhere.
func configureGraphPreview(cmd *cobra.Command) error {
	loadBeadsEnvFile(graphPreviewDir)
	if err := checkBlockedEnvVars(); err != nil {
		return graphFailure("invalid_selector", err.Error(), 2)
	}
	if cmd.Root().PersistentFlags().Changed("db") || os.Getenv("BEADS_DB") != "" || os.Getenv("BD_DB") != "" {
		return graphFailure("capability_unavailable", "graph preview requires an explicit workspace via --directory or BEADS_DIR; database selectors are unsupported", 5)
	}
	if selected := os.Getenv("BEADS_DIR"); selected != "" {
		absolute, err := filepath.Abs(selected)
		if err != nil {
			return err
		}
		actual, err := filepath.EvalSymlinks(absolute)
		expected, expectedErr := filepath.EvalSymlinks(graphPreviewDir)
		if err != nil || expectedErr != nil || actual != expected {
			return graphFailure("not_authority", "selected graph workspace differs from the .env workspace selector", 5)
		}
	}
	if _, err := os.Lstat(filepath.Join(graphPreviewDir, "redirect")); !os.IsNotExist(err) {
		return graphFailure("capability_unavailable", "graph preview does not follow workspace redirects", 5)
	}
	if mode := os.Getenv("BD_GRAPH_MODE"); mode != "" && mode != "link" {
		return graphFailure("not_authority", "BD_GRAPH_MODE does not match the admitted graph workspace", 5)
	}

	old, present := os.LookupEnv("BEADS_DIR")
	if err := os.Setenv("BEADS_DIR", graphPreviewDir); err != nil {
		return err
	}
	loadErr := config.Initialize()
	var restoreErr error
	if present {
		restoreErr = os.Setenv("BEADS_DIR", old)
	} else {
		restoreErr = os.Unsetenv("BEADS_DIR")
	}
	if err := errors.Join(loadErr, restoreErr); err != nil {
		return graphFailure("graph_not_initialized", err.Error(), 5)
	}
	refreshBoundCommandConfig(cmd)
	return nil
}

func validateGraphPreviewRoute(cfg *configfile.Config) error {
	refuse := func(name string) error {
		return graphFailure("not_authority", fmt.Sprintf("%s requests a route different from the fixed graph preview route", name), 5)
	}
	if cfg.DoltDataDir != "" || os.Getenv("BEADS_DOLT_DATA_DIR") != "" {
		return refuse("dolt_data_dir / BEADS_DOLT_DATA_DIR")
	}
	if os.Getenv("BEADS_DOLT_PROXIED_SERVER") == "1" {
		return refuse("BEADS_DOLT_PROXIED_SERVER")
	}
	if cfg.IsDoltServerMode() != (cfg.DoltMode == configfile.DoltModeServer) || config.GetBool("dolt.shared-server") {
		return refuse("Dolt server mode configuration")
	}
	if cfg.DoltMode == configfile.DoltModeServer && cfg.GetDoltCredentialCommand() != "" {
		return graphFailure("capability_unavailable", "graph preview does not execute BEADS_DOLT_CREDENTIAL_COMMAND; supply static server credentials", 5)
	}
	for _, assertion := range []struct{ name, expected string }{
		{"BEADS_DOLT_SERVER_HOST", cfg.DoltServerHost},
		{"BEADS_DOLT_SERVER_SOCKET", cfg.DoltServerSocket},
		{"BEADS_DOLT_SERVER_DATABASE", cfg.DoltDatabase},
	} {
		if value := os.Getenv(assertion.name); value != "" && (cfg.DoltMode != configfile.DoltModeServer || value != assertion.expected) {
			return refuse(assertion.name)
		}
	}
	for _, name := range []string{"BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_PORT"} {
		if value := os.Getenv(name); value != "" {
			port, err := strconv.Atoi(value)
			if err != nil || cfg.DoltMode != configfile.DoltModeServer || port != cfg.DoltServerPort {
				return refuse(name)
			}
		}
	}
	// A YAML port is ambient in embedded mode. For a server it asserts a
	// route unless the higher-priority environment port overrides it.
	if cfg.DoltMode == configfile.DoltModeServer && os.Getenv("BEADS_DOLT_SERVER_PORT") == "" {
		if value := config.GetYamlConfig("dolt.port"); value != "" {
			port, err := strconv.Atoi(value)
			if err != nil || port != cfg.DoltServerPort {
				return refuse("dolt.port")
			}
		}
	}
	if value := os.Getenv("BEADS_DOLT_SERVER_TLS"); value != "" {
		tls, err := strconv.ParseBool(value)
		if err != nil || cfg.DoltMode != configfile.DoltModeServer || tls != cfg.DoltServerTLS {
			return refuse("BEADS_DOLT_SERVER_TLS")
		}
	}
	return nil
}
