package main

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/storage/contextinfo"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/graphstore"
	"github.com/steveyegge/beads/memoryops"
)

func graphContinuityCommand(cmd *cobra.Command) bool {
	if cmd == primeCmd || cmd == codexHookCmd || cmd == codexActivityCmd {
		return true
	}
	for c := cmd; c != nil; c = c.Parent() {
		if c == knowledgeCmd || c == codeCmd || c == activityCmd || c == maintainCmd {
			return true
		}
	}
	return false
}

// Each role call releases its engine before return. Code publication uses the
// optional batch role, so its guards and replacement remain one transaction.
type graphContinuityRole struct{}

func graphContinuityCall[T any](ctx context.Context, write bool, fn func(*graphstore.ContinuityMemories) (T, error)) (result T, err error) {
	if write {
		if err := graphPreviewWritePolicy(); err != nil {
			return result, err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	st, err := graphstore.OpenExisting(ctx, graphOptions(graphPreviewConfig))
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, st.Close()) }()
	return fn(st.Memories(getActor()))
}
func (graphContinuityRole) Remember(ctx context.Context, req memoryops.RememberRequest) (memoryops.RememberResult, error) {
	return graphContinuityCall(ctx, true, func(m *graphstore.ContinuityMemories) (memoryops.RememberResult, error) { return m.Remember(ctx, req) })
}
func (graphContinuityRole) Recall(ctx context.Context, req memoryops.RecallRequest) (memoryops.RecallResult, error) {
	return graphContinuityCall(ctx, false, func(m *graphstore.ContinuityMemories) (memoryops.RecallResult, error) { return m.Recall(ctx, req) })
}
func (graphContinuityRole) Forget(ctx context.Context, req memoryops.ForgetRequest) (memoryops.ForgetResult, error) {
	return graphContinuityCall(ctx, true, func(m *graphstore.ContinuityMemories) (memoryops.ForgetResult, error) { return m.Forget(ctx, req) })
}
func (graphContinuityRole) List(ctx context.Context, req memoryops.ListRequest) (memoryops.ListResult, error) {
	return graphContinuityCall(ctx, false, func(m *graphstore.ContinuityMemories) (memoryops.ListResult, error) { return m.List(ctx, req) })
}
func (graphContinuityRole) Apply(ctx context.Context, req memoryops.BatchRequest) (memoryops.BatchResult, error) {
	return graphContinuityCall(ctx, true, func(m *graphstore.ContinuityMemories) (memoryops.BatchResult, error) { return m.Apply(ctx, req) })
}

func continuityWorkspaceInfo(ctx context.Context, cwd string) (domain.ContextInfo, error) {
	if graphPreviewActive {
		cfg := graphPreviewConfig
		info := domain.ContextInfo{BeadsDir: cfg.GraphWorkspace, RepoRoot: filepath.Dir(cfg.GraphWorkspace), CWDRepoRoot: filepath.Dir(cfg.GraphWorkspace), BdVersion: Version, ProjectID: cfg.ProjectID}
		info.SetBackendIdentity(cfg.GetBackend(), cfg.DoltMode, cfg.DoltDatabase)
		return info, nil
	}
	return contextinfo.NewContextProvider(cwd, Version).ContextUseCase().GetContextInfo(ctx)
}
