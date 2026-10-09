//go:build darwin

package doltserver

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Adapted from bee-ghosttrack's gastownhall/beads PR #7257 (MIT).
// Unknown ownership remains unready; all listed holders must be descendants.
const requireListenerOwnership = true

var (
	listeningPIDs  = lsofListeningPIDs
	processParents = psParentPIDs
)

func listenerOwnership(ctx context.Context, pid, port int) (owned, known bool) {
	if pid <= 0 || !isProcessAlive(pid) {
		return false, true
	}
	holders, err := listeningPIDs(ctx, port)
	if err != nil || len(holders) == 0 {
		return false, false
	}
	parents, err := processParents(ctx)
	if err != nil || len(parents) == 0 {
		return false, false
	}
	unknown := false
	for _, holder := range holders {
		owned, known := descendsFrom(holder, pid, parents)
		if known && !owned {
			return false, true
		}
		unknown = unknown || !known
	}
	return !unknown, !unknown
}

// Missing/cyclic snapshots are inconclusive, not evidence of a foreign owner.
func descendsFrom(p, ancestor int, parents map[int]int) (owned, known bool) {
	seen := map[int]bool{}
	for p > 0 && !seen[p] {
		if p == ancestor {
			return true, true
		}
		seen[p] = true
		next, ok := parents[p]
		if !ok {
			return false, false
		}
		p = next
	}
	return false, p == 0
}

func ownershipCommand(ctx context.Context, path string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...) //nolint:gosec // G204,G702: fixed system tools and internal numeric arguments
	cmd.WaitDelay = 100 * time.Millisecond
	return cmd.Output()
}

func lsofListeningPIDs(ctx context.Context, port int) ([]int, error) {
	out, err := ownershipCommand(ctx, "/usr/sbin/lsof", "-nP", "-t", "-iTCP:"+strconv.Itoa(port), "-sTCP:LISTEN")
	if err != nil {
		var exitErr *exec.ExitError
		if len(out) == 0 && errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return nil, nil
		}
		return nil, err
	}
	return parseLsofPIDs(out), nil
}

func parseLsofPIDs(out []byte) []int {
	var pids []int
	for _, line := range strings.Split(string(out), "\n") {
		if pid, err := strconv.Atoi(strings.TrimSpace(line)); err == nil && pid > 0 {
			pids = append(pids, pid)
		}
	}
	return pids
}

func psParentPIDs(ctx context.Context) (map[int]int, error) {
	out, err := ownershipCommand(ctx, "/bin/ps", "-axo", "pid=,ppid=")
	if err != nil {
		return nil, err
	}
	return parsePSParents(out), nil
}

func parsePSParents(out []byte) map[int]int {
	parents := map[int]int{}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		pid, err1 := strconv.Atoi(fields[0])
		ppid, err2 := strconv.Atoi(fields[1])
		if err1 != nil || err2 != nil || pid <= 0 || ppid < 0 {
			continue
		}
		parents[pid] = ppid
	}
	return parents
}
