package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/graphview"
	"github.com/steveyegge/beads/internal/httpapi"
	"github.com/steveyegge/beads/internal/storage/contextinfo"
)

// runGraphViewer holds no store. The existing CLI owns each strict read-only
// open/close, so an embedded reader releases its lock between refreshes.
func runGraphViewer(opts serveOptions) error {
	if dbPath != "" || databaseFlag != "" || globalFlag {
		return errors.New("--graph-viewer selects one discovered workspace; use BEADS_DIR instead of --db, --database or --global")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	info, err := contextinfo.NewContextProvider(cwd, Version).ContextUseCase().GetContextInfo(rootCtx)
	if err != nil {
		return err
	}
	workspace := filepath.Dir(info.BeadsDir)
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	// Pin the selected workspace even if the launcher environment routes other
	// commands elsewhere. No request can supply a root, command or selection.
	env := graphViewerEnv(os.Environ(), info.BeadsDir, info.Database)
	load := func(ctx context.Context) (graphview.Page, error) {
		args := []string{"graph", "--project", "--readonly", "--json"}
		child := exec.CommandContext(ctx, exe, args...) // #nosec G204 -- own executable, fixed arguments; no shell or project script
		child.Dir, child.Env = workspace, env
		out, stderr := &graphOutput{limit: 64 << 20}, &graphOutput{limit: 1 << 20}
		child.Stdout, child.Stderr = out, stderr
		if err := child.Run(); err != nil {
			if ctx.Err() != nil {
				return graphview.Page{}, ctx.Err()
			}
			return graphview.Page{}, fmt.Errorf("read-only graph query: %w", err)
		}
		var page graphview.Page
		if err := json.Unmarshal(out.Bytes(), &page); err != nil {
			return page, err
		}
		if page.Workspace != workspace {
			return page, errors.New("graph query returned a different workspace")
		}
		return page, nil
	}
	viewer := graphview.NewLiveHandler(load)
	return serveListen(opts, httpapi.Config{GraphViewer: viewer, Workspace: info, Mode: "graph-viewer-readonly"})
}

func graphViewerEnv(env []string, beadsDir, database string) []string {
	result := make([]string, 0, len(env)+4)
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "BEADS_DIR", "BEADS_DB", "BD_DB", "BEADS_DOLT_SERVER_DATABASE":
			continue
		}
		result = append(result, entry)
	}
	// Existing empty values prevent project dotenv from reintroducing selectors.
	return append(result, "BEADS_DIR="+beadsDir, "BEADS_DB=", "BD_DB=", "BEADS_DOLT_SERVER_DATABASE="+database)
}

type graphOutput struct {
	bytes.Buffer
	limit int
}

func (b *graphOutput) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, errors.New("graph query exceeded output bound")
	}
	return b.Buffer.Write(p)
}
