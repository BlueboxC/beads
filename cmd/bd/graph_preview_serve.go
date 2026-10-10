package main

import (
	"context"
	"errors"
	"time"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/httpapi"
	"github.com/steveyegge/beads/internal/httpapi/graphread"
	"github.com/steveyegge/beads/internal/storage/graphstore"
)

// runGraphServe opens only the admitted existing ordinary-server graph store.
// No initialization, legacy storage source, hooks or write routes are involved.
// The store outlives HTTP drain; the short CLI operation helper cannot own it.
func runGraphServe(cmd *cobra.Command, opts serveOptions) (result error) {
	if err := graphPreviewFlags(cmd, "addr", "allow-non-loopback", "auth-token-file", "insecure-no-auth", "allowed-host"); err != nil {
		return err
	}
	if graphPreviewConfig == nil || graphPreviewConfig.DoltMode != configfile.DoltModeServer {
		return graphFailure("capability_unavailable", "graph HTTP serving requires an ordinary shared-server Dolt workspace; embedded serving is not supported", 5)
	}
	ctx, cancel := context.WithTimeout(rootCtx, 30*time.Second)
	defer cancel()
	store, err := graphstore.OpenExisting(ctx, graphOptions(graphPreviewConfig))
	if err != nil {
		return graphStorageError(err)
	}
	defer func() { result = errors.Join(result, store.Close()) }()
	reader := graphread.New(store)
	if err := reader.ValidateAuthority(ctx); err != nil {
		return graphStorageError(err)
	}
	reads, err := httpapi.NewGraphRead(reader, graphPreviewConfig.GraphScopeURL)
	if err != nil {
		return err
	}
	defer reads.Close()
	return serveListen(opts, httpapi.Config{GraphRead: reads, Mode: "graph-read"})
}
