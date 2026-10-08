package main

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

// trackerStoreForCommand selects the tracker synchronization seam without
// opening a second direct store in proxied mode.
func trackerStoreForCommand(ctx context.Context) (tracker.Store, error) {
	if usesProxiedServer() {
		if uowProvider == nil {
			return nil, fmt.Errorf("proxied-server provider not initialized")
		}
		store := tracker.NewUOWStore(uowProvider)
		if store == nil {
			return nil, fmt.Errorf("proxied-server provider is invalid")
		}
		return store, nil
	}
	if err := ensureStoreActiveWithContext(ctx); err != nil {
		return nil, err
	}
	return tracker.NewStore(store), nil
}

func buildTrackerPullHooks(ctx context.Context) *tracker.PullHooks {
	prefix := "bd"
	// YAML config takes precedence — in shared-server mode the DB
	// may belong to a different project (GH#2469).
	if p := config.GetString("issue-prefix"); p != "" {
		prefix = p
	} else if store != nil {
		if p, err := store.GetConfig(ctx, "issue_prefix"); err == nil && p != "" {
			prefix = p
		}
	}

	return &tracker.PullHooks{
		GenerateID: func(_ context.Context, issue *types.Issue) error {
			if issue.ID == "" {
				issue.ID = generateIssueID(prefix)
			}
			return nil
		},
	}
}
