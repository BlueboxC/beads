//go:build darwin

package doltserver

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

func withProcessReads(t *testing.T, pids func(context.Context, int) ([]int, error), parents func(context.Context) (map[int]int, error)) {
	t.Helper()
	origPIDs, origParents := listeningPIDs, processParents
	listeningPIDs, processParents = pids, parents
	t.Cleanup(func() { listeningPIDs, processParents = origPIDs, origParents })
}

func TestListenerOwnership_FakeReads(t *testing.T) {
	self := os.Getpid()
	tree := map[int]int{1: 0, self: 1, 501: self, 502: 501, 600: 1, 700: 700}
	cases := []struct {
		name       string
		pids       []int
		pidsErr    error
		parents    map[int]int
		parentsErr error
		owned      bool
		known      bool
	}{
		{name: "child holds the listener", pids: []int{self}, parents: tree, owned: true, known: true},
		{name: "grandchild holds the listener (wrapper)", pids: []int{502}, parents: tree, owned: true, known: true},
		{name: "foreign process holds it", pids: []int{600}, parents: tree, owned: false, known: true},
		{name: "foreign and own both listed", pids: []int{600, 501}, parents: tree, owned: false, known: true},
		{name: "holder missing from the snapshot", pids: []int{999}, parents: tree, owned: false, known: false},
		{name: "cyclic snapshot does not loop", pids: []int{700}, parents: tree, owned: false, known: false},
		{name: "no listener listed", pids: nil, parents: tree, owned: false, known: false},
		{name: "lsof fails", pidsErr: errors.New("lsof: not found"), parents: tree, owned: false, known: false},
		{name: "ps fails", pids: []int{self}, parentsErr: errors.New("ps: not found"), owned: false, known: false},
		{name: "empty snapshot", pids: []int{self}, parents: map[int]int{}, owned: false, known: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withProcessReads(t,
				func(context.Context, int) ([]int, error) { return tc.pids, tc.pidsErr },
				func(context.Context) (map[int]int, error) { return tc.parents, tc.parentsErr },
			)
			owned, known := listenerOwnership(context.Background(), self, 1)
			if owned != tc.owned || known != tc.known {
				t.Errorf("listenerOwnership = (%v, %v), want (%v, %v)", owned, known, tc.owned, tc.known)
			}
		})
	}
}

func TestListenerOwnership_DeadChildIsKnown(t *testing.T) {
	withProcessReads(t,
		func(context.Context, int) ([]int, error) { t.Fatal("listener read on a dead child"); return nil, nil },
		func(context.Context) (map[int]int, error) { t.Fatal("tree read on a dead child"); return nil, nil },
	)
	if owned, known := listenerOwnership(context.Background(), 1<<30, 1); owned || !known {
		t.Errorf("listenerOwnership(context.Background(), no such pid) = (%v, %v), want (false, true)", owned, known)
	}
}

func TestParseLsofPIDs(t *testing.T) {
	got := parseLsofPIDs([]byte("12245\n\n  77360 \nnot-a-pid\n0\n"))
	want := []int{12245, 77360}
	if len(got) != len(want) {
		t.Fatalf("parseLsofPIDs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("parseLsofPIDs = %v, want %v", got, want)
		}
	}
}

func TestParsePSParents(t *testing.T) {
	got := parsePSParents([]byte("    1     0\n  501     1\n12245   501\nbad line here\n  77360 x\n444 -1\n"))
	want := map[int]int{1: 0, 501: 1, 12245: 501}
	if len(got) != len(want) {
		t.Fatalf("parsePSParents = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("parsePSParents[%d] = %d, want %d", k, got[k], v)
		}
	}
}

// TestListenerOwnership_RealTools preserves PR #7257 coverage: our own
// in-process listener is ours, a pid that does not exist owns nothing, a
// sibling process's listener is not ours, and a listener in a child process
// we spawn is ours through the tree.
func TestListenerOwnership_RealTools(t *testing.T) {
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Fatal("lsof not on PATH")
	}
	port := foreignGreeter(t)
	if owned, known := listenerOwnership(context.Background(), os.Getpid(), port); !owned || !known {
		t.Errorf("listenerOwnership(context.Background(), self, own listener) = (%v, %v), want (true, true)", owned, known)
	}
	if owned, known := listenerOwnership(context.Background(), 1<<30, port); owned || !known {
		t.Errorf("listenerOwnership(context.Background(), no such pid) = (%v, %v), want (false, true)", owned, known)
	}

	// A child that holds its own listener: nc -l on a fresh port, under a
	// shell that does not exec it, so the listener is a grandchild.
	ncPort := freeLoopbackPort(t)
	cmd := exec.Command("sh", "-c", "nc -l 127.0.0.1 "+strconv.Itoa(ncPort)+"; exit 0")
	cmd.SysProcAttr = procAttrDetached()
	if err := cmd.Start(); err != nil {
		t.Fatalf("cannot start nc under sh: %v", err)
	}
	t.Cleanup(func() { killStartedGroup(cmd.Process.Pid); _ = cmd.Process.Kill(); _ = cmd.Wait() })
	deadline := time.Now().Add(5 * time.Second)
	for {
		pids, err := listeningPIDs(context.Background(), ncPort)
		if err == nil && len(pids) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("nc never listened within 5s")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if owned, known := listenerOwnership(context.Background(), cmd.Process.Pid, ncPort); !owned || !known {
		t.Errorf("listenerOwnership(context.Background(), sh child, grandchild nc listener) = (%v, %v), want (true, true)", owned, known)
	}
	if owned, known := listenerOwnership(context.Background(), os.Getpid(), ncPort); !owned || !known {
		t.Errorf("listenerOwnership(context.Background(), self, descendant nc listener) = (%v, %v), want (true, true)", owned, known)
	}
	// The child does not own its parent's listener: the tree is walked up
	// from the holder, never down from the pid being asked about.
	if owned, known := listenerOwnership(context.Background(), cmd.Process.Pid, port); owned || !known {
		t.Errorf("listenerOwnership(context.Background(), sh child, parent's listener) = (%v, %v), want (false, true)", owned, known)
	}
}
