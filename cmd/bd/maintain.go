package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/codeindex"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/knowledge"
	"github.com/steveyegge/beads/internal/storage/contextinfo"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/memoryops"
)

type maintenanceResult struct {
	Workspace      string            `json:"workspace"`
	CodeUpdated    bool              `json:"code_updated"`
	CatalogUpdated bool              `json:"catalog_updated"`
	Files          int               `json:"files"`
	Documents      int               `json:"documents"`
	Changes        codeindex.Changes `json:"changes"`
	ElapsedMS      int64             `json:"elapsed_ms"`
}

var maintainCmd = &cobra.Command{
	Use: "maintain", GroupID: "advanced", Short: "Opt-in maintenance of saved derived code and document selections",
	Annotations: map[string]string{skipStoreAnnotation: "1"},
	Long: `Refresh derived code and document catalogs in the existing embedded Dolt.
Human memories, assertions and task states are never renewed. Saved directory
roots discover new files; file roots follow only those exact paths. A missing
root stays selected so it can reappear. No project execution, model or new DB.
The watcher releases Dolt between bounded passes. No hooks or boot startup.`,
}

func init() {
	once := &cobra.Command{Use: "once", Short: "Refresh changed derived data once; unchanged data is not written", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			CheckReadonly("maintain once")
			if dbPath != "" || databaseFlag != "" || globalFlag {
				return errors.New("maintain selects one discovered workspace; use BEADS_DIR instead of database flags")
			}
			dir := beads.FindBeadsDir()
			cfg, err := configfile.Load(dir)
			if err != nil {
				return err
			}
			if dir == "" || cfg == nil || cfg.GetBackend() != configfile.BackendDolt || cfg.IsDoltServerMode() || cfg.IsDoltProxiedServerMode() {
				return errors.New("maintain requires an initialized embedded Dolt workspace; shared/server maintenance is not supported")
			}
			// This scoped factory open uses the existing driver; no root-command
			// import/template/hook/backup/export/push housekeeping runs on this path.
			// A strict existing-only open validates presence and schema before any
			// writable open. Pin the explicit embedded config; later dotenv/metadata
			// changes cannot turn this pass into a server start or a different database.
			selected := &dolt.Config{BeadsDir: dir, Database: cfg.GetDoltDatabase(), ReadOnly: true, DisableAutoStart: true}
			existing, err := newDoltStore(rootCtx, selected)
			if err != nil {
				return err
			}
			if err := existing.Close(); err != nil {
				return err
			}
			selected.ReadOnly = false
			st, err := newDoltStore(rootCtx, selected)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()
			memories, err := st.Memories()
			if err != nil {
				return err
			}
			python, _ := cmd.Flags().GetString("python")
			node, _ := cmd.Flags().GetString("node")
			result, err := maintainDerived(rootCtx, memories, filepath.Dir(dir), codeindex.ScanOptions{Python: python, Node: node, AllowMissingRoots: true})
			if err != nil {
				return err
			}
			if result.CodeUpdated || result.CatalogUpdated {
				// This is an explicit derived-data publication, independent of automatic
				// housekeeping. It commits locally; it never synchronizes a remote.
				if err := st.Commit(rootCtx, "bd maintain: derived code/catalog"); err != nil {
					return err
				}
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
		},
	}
	once.Flags().String("python", "python3", "Operator Python for changed selected Python only")
	once.Flags().String("node", "node", "Operator Node for changed selected JS/TS only")
	watch := &cobra.Command{Use: "watch", Short: "Maintain saved selections until interrupted; release Dolt between passes", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			CheckReadonly("maintain watch")
			interval, _ := cmd.Flags().GetDuration("interval")
			if interval < time.Second || interval > time.Hour {
				return errors.New("interval must be 1s–1h")
			}
			if dbPath != "" || databaseFlag != "" || globalFlag {
				return errors.New("maintain selects one discovered workspace; use BEADS_DIR instead of database flags")
			}
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			info, err := contextinfo.NewContextProvider(cwd, Version).ContextUseCase().GetContextInfo(rootCtx)
			if err != nil {
				return err
			}
			if info.DoltMode != configfile.DoltModeEmbedded {
				return errors.New("maintain watch requires embedded Dolt")
			}
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			workspace := filepath.Dir(info.BeadsDir)
			env := graphViewerEnv(os.Environ(), info.BeadsDir, info.Database)
			python, _ := cmd.Flags().GetString("python")
			node, _ := cmd.Flags().GetString("node")
			pass := func(ctx context.Context) error {
				child := exec.CommandContext(ctx, exe, "maintain", "once", "--python="+python, "--node="+node, "--json") // #nosec G204 -- own executable/fixed command; no shell
				child.Dir, child.Env = workspace, env
				out, stderr := &graphOutput{limit: 1 << 20}, &graphOutput{limit: 1 << 20}
				child.Stdout, child.Stderr = out, stderr
				if err := child.Run(); err != nil {
					return fmt.Errorf("maintenance pass: %w", err)
				}
				var result maintenanceResult
				if err := json.Unmarshal(out.Bytes(), &result); err != nil {
					return err
				}
				if result.Workspace != workspace {
					return errors.New("maintenance returned a different workspace")
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
			}
			return watchMaintenance(rootCtx, interval, pass, func(err error, retry time.Duration) {
				fmt.Fprintf(cmd.ErrOrStderr(), "Maintenance failed: %v; retry in %s. Inspect bd maintain once for details.\n", err, retry)
			})
		},
	}
	watch.Flags().Duration("interval", 30*time.Second, "Delay after each pass; errors back off to at least 5m")
	watch.Flags().String("python", "python3", "Operator Python for changed selected Python only")
	watch.Flags().String("node", "node", "Operator Node for changed selected JS/TS only")
	maintainCmd.AddCommand(once, watch)
	rootCmd.AddCommand(maintainCmd)
}

// Each pass uses the latest saved selection, including concurrent explicit
// operator changes. All derivation succeeds before publication begins.
func maintainDerived(ctx context.Context, memories memoryops.Memories, workspace string, options codeindex.ScanOptions) (maintenanceResult, error) {
	started := time.Now()
	result := maintenanceResult{Workspace: workspace}
	plane, err := memories.List(ctx, memoryops.ListRequest{})
	if err != nil {
		return result, err
	}
	if _, err := knowledge.LoadCatalog(plane.Memories); err != nil {
		return result, err
	}
	index, err := codeindex.Load(plane.Memories)
	if err != nil {
		return result, err
	}
	state := knowledge.Decode(plane.Memories)
	if index.Version == 0 && len(state.Catalog.Roots) == 0 {
		return result, errors.New("no saved selections; select roots with bd code scan or bd knowledge scan first")
	}
	if index.Version != 0 {
		reader, err := codeindex.Open(workspace)
		if err != nil {
			return result, err
		}
		defer func() { _ = reader.Close() }()
		if checked := reader.Refresh(index); len(checked.Warnings) > 0 {
			options.AllowMissingRoots = true
			index, err = reader.ScanWithOptions(ctx, index.Roots, index.Exclude, index, options)
			if err != nil {
				return result, err
			}
			result.CodeUpdated = true
			result.Changes = index.Changes
		}
		result.Files = index.Stats.Files
	}
	var catalog knowledge.Catalog
	if len(state.Catalog.Roots) > 0 {
		reader, err := knowledge.Open(workspace)
		if err != nil {
			return result, err
		}
		defer func() { _ = reader.Close() }()
		catalog, err = reader.ScanAvailable(state.Catalog.Roots)
		if err != nil {
			return result, err
		}
		result.CatalogUpdated = !reflect.DeepEqual(catalog, state.Catalog)
		result.Documents = len(catalog.Sources)
		if result.CatalogUpdated {
			checked, err := reader.ScanAvailable(state.Catalog.Roots)
			if err != nil || !reflect.DeepEqual(catalog, checked) {
				return result, errors.New("document selection changed during scan; previous catalog retained")
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if result.CodeUpdated {
		if _, err := codeindex.Save(ctx, memories, plane.Memories, index); err != nil {
			return result, err
		}
	}
	if result.CatalogUpdated {
		if err := knowledge.SaveCatalog(ctx, memories, catalog); err != nil {
			return result, err
		}
	}
	result.ElapsedMS = time.Since(started).Milliseconds()
	return result, nil
}

func watchMaintenance(ctx context.Context, interval time.Duration, pass func(context.Context) error, report func(error, time.Duration)) error {
	delay := interval
	for {
		if ctx.Err() != nil {
			return nil
		}
		passCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		err := pass(passCtx)
		cancel()
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			delay = min(max(interval, 5*time.Minute), max(interval, delay*2))
			report(err, delay)
		} else {
			delay = interval
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}
