//go:build !windows

package doltserver

import (
	"os"
	"strconv"
	"testing"
)

func TestUpstreamUnreadableProcessListPreservesState(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GT_ROOT", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("PATH", t.TempDir())
	pid := os.Getpid()
	if err := os.WriteFile(pidPath(dir), []byte(strconv.Itoa(pid)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writePortFile(dir, 14567); err != nil {
		t.Fatal(err)
	}
	state, err := IsRunning(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Running || state.PID != pid || state.Port != 14567 {
		t.Errorf("unreadable process list erased live tracked state: %+v", state)
	}
	for _, path := range []string{pidPath(dir), portPath(dir)} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("state removed: %v", err)
		}
	}
}
