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
	"github.com/steveyegge/beads/internal/knowledge"
	"github.com/steveyegge/beads/internal/storage/graphstore"
)

// Separate processes prove that graph admission reaches the continuity role,
// hooks reopen the selected database, and live viewers release embedded locks.
func TestGraphContinuityProcessLearningRecoveryCodeAndViewer(t *testing.T) {
	bd := buildBDUnderTest(t)
	for _, engine := range []string{"embedded", "server"} {
		t.Run(engine, func(t *testing.T) {
			work, home := t.TempDir(), t.TempDir()
			args := []string{"init", "--graph-mode=link", "--scope-url=https://example.invalid/continuity/", "--prefix=gc", "--skip-hooks", "--skip-agents", "--non-interactive"}
			if engine == "server" {
				port := os.Getenv("BEADS_GRAPH_TEST_SERVER_PORT")
				if port == "" {
					t.Skip("set BEADS_GRAPH_TEST_SERVER_PORT for server continuity")
				}
				args = append(args, "--server", "--external", "--server-host=127.0.0.1", "--server-user=root", "--server-port="+port)
			}
			call := func(args ...string) string { t.Helper(); return graphPolicyCLI(t, bd, work, home, nil, "", args...) }
			call(args...)
			writeFile(t, filepath.Join(work, "DOX.md"), []byte("# Contract\nKeep verified solutions.\n"))
			writeFile(t, filepath.Join(work, "solution.md"), []byte("# Solution\nPersist reviewed evidence.\n"))
			writeFile(t, filepath.Join(work, "module.go"), []byte("package module\nfunc Solved() bool { return true }\n"))
			var sources []knowledge.Source
			if err := json.Unmarshal([]byte(call("knowledge", "sources", "solution.md", "module.go", "--json")), &sources); err != nil {
				t.Fatal(err)
			}
			record := knowledge.Record{ID: "preserved-fix", Kind: "solution", Summary: "Preserve the solved module", Solution: "Persist reviewed evidence", Scope: "recorded", Sources: sources}
			raw, _ := json.Marshal(record)
			draft := filepath.Join(work, "draft.json")
			writeFile(t, draft, raw)
			var proposal knowledge.Proposal
			if err := json.Unmarshal([]byte(call("knowledge", "propose", "--file=draft.json", "--json")), &proposal); err != nil {
				t.Fatal(err)
			}
			if out := call("prime", "--memories-only"); !strings.Contains(out, "pending=1") || strings.Contains(out, "Persist reviewed evidence") {
				t.Fatalf("unreviewed proposal promoted: %s", out)
			}
			call("knowledge", "review", proposal.ID, "--decision=accept", "--reviewer=BlueboxC", "--reason=Sources inspected", "--scope=inspected", "--evidence=Source fixture inspected; no project execution", "--json")
			call("code", "scan", "module.go", "--languages=go", "--json")
			if out := call("code", "status", "--json"); !strings.Contains(out, "go") {
				t.Fatalf("lost code index: %s", out)
			}
			call("remember", "Direct graph policy survives recovery", "--id=policy")
			if out := call("prime", "--memories-only"); !strings.Contains(out, "preserved-fix") || !strings.Contains(out, "Direct graph policy") {
				t.Fatalf("lost recovery: %s", out)
			}
			var page graphview.Page
			if err := json.Unmarshal([]byte(call("graph", "--project", "--readonly", "--json")), &page); err != nil {
				t.Fatal(err)
			}
			found := map[string]graphview.Node{}
			for _, n := range page.Nodes {
				found[n.ID] = n
			}
			if _, ok := found["record:preserved-fix"]; !ok || len(page.Files) != 1 {
				t.Fatalf("missing combined graph: %+v", page)
			}
			detail, _ := json.Marshal(found["record:preserved-fix"].Detail)
			if !strings.Contains(string(detail), "canonical_record") {
				t.Fatal("accepted solution disconnected from retained graph record")
			}
			if out := call("graph", "--project", "--html"); !strings.Contains(out, "Reset View") || !strings.Contains(out, "canonical_record") {
				t.Fatal("native interactive viewer not wired")
			}
			// Graph guards remain active on fork writes, before opening ordinary storage.
			before := call("knowledge", "list", "--json")
			command := exec.Command(bd, "--readonly", "knowledge", "record", "--file=draft.json", "--json")
			command.Dir = work
			command.Env = bdEnv(work)
			if out, err := command.CombinedOutput(); err == nil {
				t.Fatalf("readonly write allowed: %s", out)
			}
			if call("knowledge", "list", "--json") != before {
				t.Fatal("readonly mutation changed learning")
			}
			if engine != "embedded" {
				return
			}
			call("activity", "enable", "--json")
			hook := func(event string) string {
				t.Helper()
				input, _ := json.Marshal(map[string]string{"session_id": "graph-continuity", "turn_id": "graph-turn", "cwd": work})
				name := "codex-hook"
				if event == "Stop" {
					name = "codex-activity"
				}
				cmd := exec.Command(bd, name, event)
				cmd.Dir = work
				cmd.Env = append(bdEnv(work), "HOME="+home, "XDG_CONFIG_HOME="+filepath.Join(home, ".config"))
				cmd.Stdin = strings.NewReader(string(input))
				out, stderr, err := runCommandBuffers(t, cmd)
				if err != nil {
					t.Fatalf("hook %s: %v %s", event, err, stderr.String())
				}
				return out.String()
			}
			hook("Stop")
			if out := call("activity", "list", "--json"); !strings.Contains(out, "graph-continuity") {
				t.Fatalf("activity did not persist: %s", out)
			}
			for _, event := range []string{"SessionStart", "PreCompact", "PostCompact", "UserPromptSubmit"} {
				out := hook(event)
				if (event == "SessionStart" || event == "UserPromptSubmit") && !strings.Contains(out, "preserved-fix") {
					t.Fatalf("hook %s lost solution: %s", event, out)
				}
			}
			if out := hook("UserPromptSubmit"); out != "" {
				t.Fatal("compact recovery repeated")
			}
			call("maintain", "once", "--json")
			server := exec.Command(bd, "serve", "--graph-viewer")
			server.Dir = work
			server.Env = append(bdEnv(work), "HOME="+home)
			serverLog, err := os.CreateTemp(work, "viewer-*.log")
			if err != nil {
				t.Fatal(err)
			}
			defer serverLog.Close()
			server.Stderr = serverLog
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
				scan := bufio.NewScanner(stdout)
				for scan.Scan() {
					if _, url, ok := strings.Cut(scan.Text(), "listening on "); ok {
						address <- url
						return
					}
				}
			}()
			var base string
			select {
			case base = <-address:
			case <-time.After(15 * time.Second):
				_ = server.Process.Kill()
				_ = server.Wait()
				log, _ := os.ReadFile(serverLog.Name())
				t.Fatalf("graph viewer failed to bind: %s", log)
			}
			get := func() graphview.Page {
				t.Helper()
				resp, err := http.Get(base + "/viewer/graph")
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				if resp.StatusCode != 200 {
					raw, _ := io.ReadAll(resp.Body)
					t.Fatalf("viewer %d %s", resp.StatusCode, raw)
				}
				var p graphview.Page
				if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
					t.Fatal(err)
				}
				return p
			}
			if p := get(); len(p.Files) != 1 || p.Activity == nil {
				t.Fatal("live viewer omitted continuity")
			}
			created := graphMixedResult[graphstore.IssueRecord](t, call("create", "Write while viewer runs", "--id=live", "--json"))
			writeFile(t, filepath.Join(work, "module.go"), []byte("package module\nfunc Solved() bool { return false }\n"))
			deadline := time.Now().Add(15 * time.Second)
			observed := false
			for time.Now().Before(deadline) {
				p := get()
				task, stale := false, false
				for _, n := range p.Nodes {
					task = task || n.ID == "issue:"+created.Properties.ID
					stale = stale || (n.ID == "record:preserved-fix" && n.Validity == "needs_review")
				}
				if task && stale {
					observed = true
					break
				}
				time.Sleep(250 * time.Millisecond)
			}
			if !observed {
				t.Fatalf("viewer lost live tasks or silently renewed stale evidence: task ID=%s page=%+v", created.Properties.ID, get())
			}
		})
	}
}
