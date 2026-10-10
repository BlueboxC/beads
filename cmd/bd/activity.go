package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/activity"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/graphstore"
	"github.com/steveyegge/beads/memoryops"
)

var activityCmd = &cobra.Command{Use: "activity", GroupID: "advanced", Short: "Observed chat operations and reported turn handoffs in the existing Dolt", Annotations: map[string]string{skipStoreAnnotation: "1"}}
var codexActivityCmd = &cobra.Command{Use: "codex-activity <PostToolUse|Stop|SessionEnd>", Hidden: true, Args: cobra.ExactArgs(1), Annotations: map[string]string{skipStoreAnnotation: "1"}, RunE: func(cmd *cobra.Command, args []string) error {
	return runCodexActivity(cmd.Context(), args[0], cmd.InOrStdin(), cmd.OutOrStdout())
}}

func init() {
	for _, name := range []string{"enable", "disable"} {
		enabled := name == "enable"
		cmd := &cobra.Command{Use: name, Short: "Set project-local automatic capture; does not trust hooks", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			CheckReadonly("activity configuration")
			_, m, closeStore, err := openActivity(rootCtx, true)
			if err != nil {
				return err
			}
			defer func() { _ = closeStore(false) }()
			raw, _ := json.Marshal(activity.Settings{Version: 1, Enabled: enabled})
			prior, err := m.Recall(rootCtx, memoryops.RecallRequest{Key: activity.SettingsKey})
			if err != nil {
				return err
			}
			changed := prior.Value != string(raw)
			if changed {
				if _, err = m.Remember(rootCtx, memoryops.RememberRequest{Key: activity.SettingsKey, Content: string(raw)}); err != nil {
					return err
				}
			}
			if err = closeStore(changed); err != nil {
				return err
			}
			return outputJSON(activity.Settings{Version: 1, Enabled: enabled})
		}}
		activityCmd.AddCommand(cmd)
	}
	for _, name := range []string{"list", "status"} {
		cmd := &cobra.Command{Use: name, Short: "Read captured operations and reported summaries", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			_, m, closeStore, err := openActivity(rootCtx, false)
			if err != nil {
				return err
			}
			defer func() { _ = closeStore(false) }()
			plane, err := m.List(rootCtx, memoryops.ListRequest{Search: activity.Prefix})
			if err != nil {
				return err
			}
			session, _ := cmd.Flags().GetString("session")
			turn, _ := cmd.Flags().GetString("turn")
			limit, _ := cmd.Flags().GetInt("limit")
			if limit < 1 || limit > 1000 {
				return errors.New("limit must be 1-1000")
			}
			return outputJSON(activity.Read(plane.Memories, session, turn, limit))
		}}
		cmd.Flags().String("session", "", "Filter by exact Codex session identity")
		cmd.Flags().String("turn", "", "Filter by exact turn identity")
		cmd.Flags().Int("limit", 100, "Maximum latest returned events, 1-1000")
		activityCmd.AddCommand(cmd)
	}
	rootCmd.AddCommand(activityCmd, codexActivityCmd)
}

// openActivity is existing-only and embedded-only, like maintain. No housekeeping or route fallback.
func openActivity(ctx context.Context, write bool) (string, memoryops.Memories, func(bool) error, error) {
	if dbPath != "" || databaseFlag != "" || globalFlag {
		return "", nil, nil, errors.New("activity selects one discovered workspace; database/global overrides refused")
	}
	if write && readonlyMode {
		return "", nil, nil, errors.New("activity capture is disabled by readonly mode")
	}
	dir := beads.FindBeadsDir()
	if dir == "" {
		return "", nil, nil, errors.New("activity requires an initialized workspace")
	}
	cfg, err := configfile.Load(dir)
	if err != nil {
		return "", nil, nil, err
	}
	if dir == "" || cfg == nil || cfg.GetBackend() != configfile.BackendDolt || cfg.IsDoltServerMode() || cfg.IsDoltProxiedServerMode() {
		return "", nil, nil, errors.New("activity requires an initialized embedded Dolt workspace")
	}
	mode, err := cfg.GetGraphMode()
	if err != nil {
		return "", nil, nil, err
	}
	if mode == "link" {
		marker, err := os.ReadFile(filepath.Join(dir, graphPreviewMarker)) // #nosec G304 -- fixed sentinel in the selected workspace
		if err != nil || !cfg.GraphReady || cfg.GraphSchemaVersion != graphstore.SchemaVersion || !graphPreviewGenerationSupported(marker) {
			return "", nil, nil, errors.New("activity requires a ready compatible Graph Preview workspace")
		}
		physical, err := filepath.EvalSymlinks(dir)
		if err != nil || physical != cfg.GraphWorkspace {
			return "", nil, nil, errors.New("activity graph workspace binding differs")
		}
		if write && graphPreviewActive {
			if err := graphPreviewWritePolicy(); err != nil {
				return "", nil, nil, err
			}
		}
		st, err := graphstore.OpenExisting(ctx, graphOptionsFor(dir, cfg))
		if err != nil {
			return "", nil, nil, err
		}
		return filepath.Dir(dir), st.Memories(getActor()), func(bool) error { return st.Close() }, nil
	}
	selected := &dolt.Config{BeadsDir: dir, Database: cfg.GetDoltDatabase(), ReadOnly: true, DisableAutoStart: true}
	st, err := newDoltStore(ctx, selected)
	if err != nil {
		return "", nil, nil, err
	}
	if write {
		if err = st.Close(); err != nil {
			return "", nil, nil, err
		}
		selected.ReadOnly = false
		st, err = newDoltStore(ctx, selected)
		if err != nil {
			return "", nil, nil, err
		}
	}
	m, err := st.Memories()
	if err != nil {
		_ = st.Close()
		return "", nil, nil, err
	}
	closed := false
	closeStore := func(changed bool) error {
		if closed {
			return nil
		}
		closed = true
		var commitErr error
		if changed {
			commitErr = st.Commit(ctx, "bd activity: observed Codex metadata")
		}
		closeErr := st.Close()
		return errors.Join(commitErr, closeErr)
	}
	return filepath.Dir(dir), m, closeStore, nil
}

func runCodexActivity(ctx context.Context, event string, stdin io.Reader, stdout io.Writer) error {
	// Never block the originating tool or continue a completed turn.
	reply := func(message string) error {
		value := map[string]string{}
		if message != "" {
			value["systemMessage"] = message
		}
		return json.NewEncoder(stdout).Encode(value)
	}
	if beads.FindBeadsDir() == "" || readonlyMode {
		return reply("")
	}
	budget := 25 * time.Second
	if event == "SessionEnd" {
		budget = 2 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	var input activity.Input
	raw, readErr := io.ReadAll(io.LimitReader(stdin, activity.MaxInput+1))
	if readErr != nil || len(raw) > activity.MaxInput || json.Unmarshal(raw, &input) != nil {
		return reply("Beads activity input could not be recorded; existing project knowledge is unchanged.")
	}
	if input.Event != "" && input.Event != event {
		return reply("Beads activity event mismatch; capture withheld.")
	}
	input.Event = event
	root, m, closeStore, err := openActivity(ctx, false)
	if err != nil {
		return reply("Beads activity workspace unavailable; no capture or initialization performed.")
	}
	settings, err := m.Recall(ctx, memoryops.RecallRequest{Key: activity.SettingsKey})
	_ = closeStore(false)
	if err != nil {
		return reply("Beads activity settings unavailable; capture withheld.")
	}
	var cfg activity.Settings
	if !settings.Found || json.Unmarshal([]byte(settings.Value), &cfg) != nil || cfg.Version != 1 || !cfg.Enabled {
		return reply("")
	}
	// Beads discovery uses a physical directory; hook cwd can use /var or another symlink alias.
	physicalRoot, rootErr := filepath.EvalSymlinks(root)
	physicalCWD, cwdErr := filepath.EvalSymlinks(input.CWD)
	if rootErr != nil || cwdErr != nil {
		return reply("Beads activity workspace identity unavailable; capture withheld.")
	}
	input.CWD = physicalCWD
	record, err := activity.Build(input, physicalRoot, time.Now())
	if err != nil {
		return reply("Beads activity metadata is incomplete or outside this workspace; capture withheld.")
	}
	_, m, closeStore, err = openActivity(ctx, true)
	if err != nil {
		return reply("Beads activity write unavailable; existing project knowledge is unchanged.")
	}
	defer func() { _ = closeStore(false) }()
	// An explicit disable observed after the writable open also withholds the event.
	settings, err = m.Recall(ctx, memoryops.RecallRequest{Key: activity.SettingsKey})
	if err != nil || !settings.Found || json.Unmarshal([]byte(settings.Value), &cfg) != nil || cfg.Version != 1 || !cfg.Enabled {
		return reply("")
	}
	changed, err := activity.Save(ctx, m, record)
	if err != nil {
		return reply("Beads activity event could not be saved; inspect bd activity status.")
	}
	if err = closeStore(changed); err != nil {
		return reply("Beads activity durability was not confirmed; inspect bd activity status.")
	}
	return reply("")
}
