//go:build !darwin

package doltserver

import "context"

// Preserve greeting-based readiness on platforms outside this Darwin fix.
const requireListenerOwnership = false

func listenerOwnership(context.Context, int, int) (owned, known bool) { return false, false }
