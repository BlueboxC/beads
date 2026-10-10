//go:build darwin

package doltserver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
)

// The helper implements only version/config and a controlled greeting listener.
// No Dolt database is opened; Start's real subprocess/port boundary is exercised.
func TestManagedDoltHelperProcess(t *testing.T) {
	if os.Getenv("BEADS_TEST_MANAGED_DOLT_HELPER") != "1" {
		return
	}
	args := os.Args
	for i, arg := range args {
		if arg == "--" {
			args = args[i+1:]
			break
		}
	}
	if len(args) == 0 {
		os.Exit(2)
	}
	switch args[0] {
	case "version":
		fmt.Println("dolt version 0.0.0")
		os.Exit(0)
	case "config":
		fmt.Println("fixture")
		os.Exit(0)
	case "sql-server":
	default:
		os.Exit(2)
	}
	port := 0
	for i, arg := range args {
		if arg == "-P" && i+1 < len(args) {
			port, _ = strconv.Atoi(args[i+1])
		}
	}
	if port == 0 {
		os.Exit(2)
	}
	path := os.Getenv("BEADS_TEST_MANAGED_DOLT_LAUNCHES")
	previous, _ := os.ReadFile(path)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		os.Exit(2)
	}
	_, _ = fmt.Fprintf(f, "%d %d\n", os.Getpid(), port)
	_ = f.Close()
	mode := os.Getenv("BEADS_TEST_MANAGED_DOLT_MODE")
	if mode == "foreign" || (mode == "foreign-first" && len(previous) == 0) {
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	if mode == "exit" {
		time.Sleep(300 * time.Millisecond)
		os.Exit(3)
	}
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		os.Exit(4)
	}
	serveManagedGreeting(ln)
	os.Exit(0)
}

func serveManagedGreeting(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		_, _ = conn.Write([]byte{8, 0, 0, 0, 10, 10, 10, 10, 10, 10, 10, 10})
		_ = conn.Close()
	}
}

func managedStartFixture(t *testing.T, mode string) (dir, launches string) {
	t.Helper()
	root := t.TempDir()
	dir = filepath.Join(root, ".beads")
	launches = filepath.Join(root, "launches")
	t.Setenv("BEADS_TEST_MANAGED_DOLT_HELPER", "1")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_TEST_MANAGED_DOLT_BINARY", executable)
	t.Setenv("BEADS_TEST_MANAGED_DOLT_LAUNCHES", launches)
	t.Setenv("BEADS_TEST_MANAGED_DOLT_MODE", mode)
	for _, name := range []string{"GT_ROOT", "BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_PORT", "BEADS_DOLT_DATA_DIR", "BEADS_DOLT_SHARED_SERVER", "BEADS_DOLT_SERVER_MODE", "BEADS_DIR"} {
		t.Setenv(name, "")
	}
	t.Setenv("BEADS_DOLT_READY_TIMEOUT", "1")
	t.Setenv("BD_DISABLE_METRICS", "1")
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	if err := os.MkdirAll(filepath.Join(dir, "dolt", ".dolt"), 0700); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	wrapper := "#!/bin/sh\nexec \"$BEADS_TEST_MANAGED_DOLT_BINARY\" -test.run=^TestManagedDoltHelperProcess$ -- \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "dolt"), []byte(wrapper), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Cleanup(func() {
		for _, row := range managedLaunches(t, launches) {
			p, _ := os.FindProcess(row[0])
			_ = p.Kill()
			_, _ = p.Wait()
		}
	})
	return dir, launches
}

func managedLaunches(t *testing.T, path string) [][2]int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var rows [][2]int
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var pid, port int
		if _, err := fmt.Sscanf(line, "%d %d", &pid, &port); err == nil {
			rows = append(rows, [2]int{pid, port})
		}
	}
	return rows
}

func takeManagedPort(t *testing.T, launches string) <-chan net.Listener {
	t.Helper()
	result := make(chan net.Listener, 1)
	go func() {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			raw, _ := os.ReadFile(launches)
			var pid, port int
			if _, err := fmt.Sscanf(string(raw), "%d %d", &pid, &port); err == nil {
				ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
				if err != nil {
					result <- nil
					return
				}
				result <- ln
				serveManagedGreeting(ln)
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		result <- nil
	}()
	return result
}

func TestStart_RecoversWhenEphemeralPortIsTaken(t *testing.T) {
	dir, launches := managedStartFixture(t, "foreign-first")
	ready := takeManagedPort(t, launches)
	state, err := Start(dir)
	ln := <-ready
	if ln == nil {
		t.Fatal("foreign fixture never listened")
	}
	t.Cleanup(func() { _ = ln.Close() })
	if err != nil {
		t.Fatal(err)
	}
	if state.Port == ln.Addr().(*net.TCPAddr).Port {
		t.Fatal("Start adopted a foreign listener")
	}
	rows := managedLaunches(t, launches)
	if len(rows) != 2 {
		t.Fatalf("launches=%v, want exactly two", rows)
	}
	if state.PID != rows[1][0] || state.Port != rows[1][1] {
		t.Fatalf("state %+v does not identify second child %v", state, rows[1])
	}
	if ReadPortFile(dir) != state.Port {
		t.Fatal("saved port does not match owned child")
	}
	if isProcessAlive(rows[0][0]) {
		t.Fatal("rejected child was not reaped before retry")
	}
	if greeted, err := ProbeSQLServer("tcp", ln.Addr().String(), time.Second); err != nil || !greeted {
		t.Fatal("foreign listener was disturbed")
	}
}

func TestStart_RejectsForeignGreetingWithoutPublishingState(t *testing.T) {
	dir, launches := managedStartFixture(t, "foreign")
	ready := takeManagedPort(t, launches)
	// An explicit port would be checked before launch; this fixture races the first
	// selected ephemeral port and subsequently permits only a bounded failure.
	state, err := Start(dir)
	ln := <-ready
	if ln == nil {
		t.Fatal("foreign fixture never listened")
	}
	t.Cleanup(func() { _ = ln.Close() })
	if err == nil || (state != nil && state.Running) {
		t.Fatalf("foreign greeting accepted: state=%+v err=%v", state, err)
	}
	for _, path := range []string{pidPath(dir), portPath(dir)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("failed startup published %s: %v", path, err)
		}
	}
	if greeted, err := ProbeSQLServer("tcp", ln.Addr().String(), time.Second); err != nil || !greeted {
		t.Fatal("foreign listener was disturbed")
	}
}

func foreignGreeter(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go serveManagedGreeting(ln)
	return ln.Addr().(*net.TCPAddr).Port
}

func freeLoopbackPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestStart_UnknownOwnershipFailsClosed(t *testing.T) {
	for _, tool := range []string{"lsof", "ps"} {
		t.Run(tool, func(t *testing.T) {
			dir, launches := managedStartFixture(t, "owned")
			if tool == "lsof" {
				withProcessReads(t, func(context.Context, int) ([]int, error) { return nil, errors.New("inspection denied") }, psParentPIDs)
			} else {
				withProcessReads(t, lsofListeningPIDs, func(context.Context) (map[int]int, error) { return nil, errors.New("inspection denied") })
			}
			start := time.Now()
			state, err := Start(dir)
			if err == nil || state != nil || !strings.Contains(err.Error(), "ownership") {
				t.Fatalf("state=%+v err=%v", state, err)
			}
			if elapsed := time.Since(start); elapsed > 4*time.Second {
				t.Fatalf("inspection timeout exceeded: %s", elapsed)
			}
			rows := managedLaunches(t, launches)
			if len(rows) != 1 || isProcessAlive(rows[0][0]) {
				t.Fatalf("failed startup did not reap its only child: %v", rows)
			}
			for _, path := range []string{pidPath(dir), portPath(dir)} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("failed startup published %s: %v", path, err)
				}
			}
		})
	}
}

func TestStart_OwnedListenerPublishesStateAfterProof(t *testing.T) {
	dir, launches := managedStartFixture(t, "owned")
	checked := false
	withProcessReads(t, func(ctx context.Context, port int) ([]int, error) {
		for _, path := range []string{pidPath(dir), portPath(dir)} {
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("startup published state before ownership proof: %s", path)
			}
		}
		checked = true
		return lsofListeningPIDs(ctx, port)
	}, psParentPIDs)
	state, err := Start(dir)
	if err != nil {
		t.Fatal(err)
	}
	rows := managedLaunches(t, launches)
	if !checked || len(rows) != 1 || !state.Running || state.PID != rows[0][0] || state.Port != rows[0][1] {
		t.Fatalf("owned startup state=%+v launches=%v checked=%v", state, rows, checked)
	}
	raw, err := os.ReadFile(pidPath(dir))
	if err != nil || strings.TrimSpace(string(raw)) != strconv.Itoa(state.PID) || ReadPortFile(dir) != state.Port {
		t.Fatal("owned state was not persisted")
	}
}

func TestStart_ExitedChildDoesNotPublishState(t *testing.T) {
	dir, launches := managedStartFixture(t, "exit")
	state, err := Start(dir)
	if err == nil || state != nil {
		t.Fatalf("exited child accepted: state=%+v err=%v", state, err)
	}
	rows := managedLaunches(t, launches)
	if len(rows) != 1 || isProcessAlive(rows[0][0]) {
		t.Fatalf("child was not reaped: %v", rows)
	}
	for _, path := range []string{pidPath(dir), portPath(dir)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("failed startup published %s: %v", path, err)
		}
	}
}

func TestOwnershipCommand_DeadlineAndPartialFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := ownershipCommand(ctx, "/bin/sleep", "30"); err == nil {
		t.Fatal("slow inspector ignored the deadline")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("inspector did not stop promptly: %s", elapsed)
	}
	out, err := ownershipCommand(context.Background(), "/bin/sh", "-c", "printf '123\\n'; exit 2")
	if err == nil || len(out) == 0 {
		t.Fatalf("partial failed output lost: out=%q err=%v", out, err)
	}
	withProcessReads(t, func(context.Context, int) ([]int, error) { return parseLsofPIDs(out), err }, psParentPIDs)
	if owned, known := listenerOwnership(context.Background(), os.Getpid(), 1); owned || known {
		t.Fatalf("partial failed inspection accepted: owned=%v known=%v", owned, known)
	}
}
