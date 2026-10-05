//go:build cgo

package main

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/graphview"
	"github.com/steveyegge/beads/internal/types"
)

// Real process boundary: a running viewer must release the embedded workspace
// so CLI writes land, and the next graph must detect source staleness without
// renewing any saved human evidence.
func TestLiveGraphProcessReleasesWorkspaceAndTracksEvidence(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 for real embedded store")
	}
	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix=live", "--stealth", "--skip-hooks", "--skip-agents", "--role=maintainer")
	run := func(input string, args ...string) string {
		t.Helper()
		cmd := exec.Command(bd, args...)
		cmd.Dir = dir
		cmd.Env = append(bdEnv(dir), "BEADS_DIR="+filepath.Join(dir, ".beads"), "BEADS_DB=", "BD_DB=", "BEADS_DOLT_SERVER_DATABASE=live")
		cmd.Stdin = strings.NewReader(input)
		out, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd %v: %v stdout=%s stderr=%s", args, err, out.String(), stderr.String())
		}
		return out.String()
	}
	source := filepath.Join(dir, "DOX.md")
	if err := os.WriteFile(source, []byte("# Fixture contract\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run(`{"id":"learned","kind":"solution","summary":"Retain solved behavior","scope":"inspected","evidence":"fixture inspected","sources":[{"path":"DOX.md"}]}`, "knowledge", "record", "--file=-")
	before := run("", "memories", "--readonly", "--json")
	server := exec.Command(bd, "serve", "--graph-viewer")
	server.Dir = dir
	server.Env = bdEnv(dir)
	server.Stderr = io.Discard
	stdout, err := server.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Process.Kill(); _ = server.Wait() })
	address := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if _, url, ok := strings.Cut(scanner.Text(), "listening on "); ok {
				address <- url
				return
			}
		}
	}()
	var base string
	select {
	case base = <-address:
	case <-time.After(15 * time.Second):
		t.Fatal("viewer failed to bind")
	}
	get := func() graphview.Page {
		t.Helper()
		resp, err := http.Get(base + "/viewer/graph")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("graph = %d %s", resp.StatusCode, body)
		}
		var p graphview.Page
		if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	physicalDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if p := get(); p.Workspace != physicalDir {
		t.Fatalf("wrong workspace %s", p.Workspace)
	}
	// A project may change its dotenv selectors after the viewer starts. The
	// child query must keep the original workspace instead of opening another
	// store or becoming unavailable because of those newly loaded selectors.
	foreign, _, _ := bdInit(t, bd, "--prefix=other", "--stealth", "--skip-hooks", "--skip-agents", "--role=maintainer")
	selectors := "BEADS_DIR=" + filepath.Join(foreign, ".beads") + "\nBEADS_DB=" + filepath.Join(foreign, ".beads", "embeddeddolt") + "\nBD_DB=" + filepath.Join(foreign, ".beads", "embeddeddolt") + "\nBEADS_DOLT_SERVER_DATABASE=other\n"
	if err := os.WriteFile(filepath.Join(dir, ".beads", ".env"), []byte(selectors), 0600); err != nil {
		t.Fatal(err)
	}
	var issue types.Issue
	if err := json.Unmarshal([]byte(run("", "create", "Write while viewer running", "--json")), &issue); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("# Changed fixture contract\n"), 0600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	observed := false
	for time.Now().Before(deadline) {
		p := get()
		task, stale := false, false
		for _, n := range p.Nodes {
			if n.ID == "issue:"+issue.ID {
				task = true
			}
			if n.ID == "record:learned" && n.Validity == "needs_review" {
				stale = true
			}
		}
		if task && stale {
			observed = true
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !observed {
		t.Fatal("viewer failed to observe committed issue and stale evidence")
	}
	if after := run("", "memories", "--readonly", "--json"); after != before {
		t.Fatal("viewer renewed human evidence or mutated memory plane")
	}
}
