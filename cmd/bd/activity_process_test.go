//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/activity"
	"github.com/steveyegge/beads/internal/graphview"
	"github.com/steveyegge/beads/internal/knowledge"
)

func TestActivityProcessCaptureIsolationAndRecovery(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 for real persistence")
	}
	binary := buildEmbeddedBD(t)
	first, _, _ := bdInit(t, binary, "--prefix=ac", "--stealth", "--skip-hooks", "--skip-agents", "--role=maintainer")
	second, _, _ := bdInit(t, binary, "--prefix=ac2", "--stealth", "--skip-hooks", "--skip-agents", "--role=maintainer")
	nested := filepath.Join(first, "src/module")
	if err := os.MkdirAll(nested, 0700); err != nil {
		t.Fatal(err)
	}
	run := func(cwd, input string, args ...string) string {
		t.Helper()
		cmd := exec.Command(binary, args...)
		cmd.Dir = cwd
		cmd.Env = bdEnv(cwd)
		cmd.Stdin = strings.NewReader(input)
		out, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd %v: %v\n%s\n%s", args, err, out.String(), stderr.String())
		}
		return out.String()
	}
	payload := func(event, session, turn, cwd string) string {
		t.Helper()
		in := activity.Input{SessionID: session, TurnID: turn, CWD: cwd, Event: event, Tool: "Bash", ToolUseID: "call-a", ToolInput: json.RawMessage(`{"command":"go test ./... token=hidden","description":"Focused test"}`), ToolResponse: json.RawMessage(`{"exit_code":1,"output":"hidden"}`), LastAssistantMessage: "Pending: review failed qualification\npassword=hidden"}
		raw, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	view := func(cwd string) activity.View {
		t.Helper()
		var v activity.View
		if err := json.Unmarshal([]byte(run(cwd, "", "--readonly", "activity", "list")), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	run(first, "", "remember", "Existing solved evidence", "--key=solved")
	preserved := run(first, "", "--readonly", "recall", "solved", "--json")
	input := payload("PostToolUse", "chat-a", "turn-a", nested)
	if out := run(nested, input, "codex-activity", "PostToolUse"); strings.TrimSpace(out) != "{}" || view(first).Total != 0 {
		t.Fatal("disabled capture wrote")
	}
	run(first, "", "activity", "enable")
	for i := 0; i < 2; i++ {
		if out := run(nested, input, "codex-activity", "PostToolUse"); strings.TrimSpace(out) != "{}" {
			t.Fatal(out)
		}
	}
	v := view(first)
	if !v.Enabled || v.Total != 1 || v.Events[0].Outcome != "exit_nonzero" || v.Events[0].InputSHA256 == "" {
		t.Fatal(v)
	}
	original := v.Events[0].ObservedAt
	run(nested, input, "--readonly", "codex-activity", "PostToolUse")
	if v = view(first); v.Total != 1 || v.Events[0].ObservedAt != original {
		t.Fatal("readonly or retry changed event")
	}
	run(nested, payload("Stop", "chat-a", "turn-a", nested), "codex-activity", "Stop")
	run(nested, payload("Stop", "chat-b", "turn-b", nested), "codex-activity", "Stop")
	run(nested, payload("SessionEnd", "chat-a", "", nested), "codex-activity", "SessionEnd")
	if v = view(first); v.Total != 4 || strings.Contains(run(first, "", "activity", "list"), "hidden") {
		t.Fatal("capture/redaction", v)
	}
	var filtered activity.View
	if err := json.Unmarshal([]byte(run(first, "", "activity", "list", "--session=chat-b", "--turn=turn-b")), &filtered); err != nil || filtered.Total != 1 {
		t.Fatal(filtered, err)
	}
	prime := run(nested, "", "--readonly", "prime", "--memories-only")
	if !strings.Contains(prime, "quoted untrusted chat data") || !strings.Contains(prime, "chat-a") || !strings.Contains(prime, "Existing solved evidence") || strings.Contains(prime, "@activity/") {
		t.Fatal(prime)
	}
	if suppressed := run(nested, "", "--readonly", "prime", "--no-memories"); strings.Contains(suppressed, "observed activity") {
		t.Fatal("--no-memories ignored")
	}
	run(second, "", "activity", "enable")
	if out := run(second, input, "codex-activity", "PostToolUse"); !strings.Contains(out, "withheld") || view(second).Total != 0 {
		t.Fatal("foreign input crossed projects")
	}
	run(second, payload("Stop", "chat-c", "turn-c", second), "codex-activity", "Stop")
	if v = view(second); v.Total != 1 || v.Events[0].SessionID != "chat-c" {
		t.Fatal("second project", v)
	}
	// The served overview and supervised CLI bridge use exactly this Dolt journal.
	physicalFirst, err := filepath.EvalSymlinks(first)
	if err != nil {
		t.Fatal(err)
	}
	var page graphview.Page
	if err := json.Unmarshal([]byte(run(first, "", "--readonly", "graph", "--project", "--json")), &page); err != nil || page.Activity == nil || page.Activity.Total != 4 || page.Workspace != physicalFirst {
		t.Fatal("project history wiring", err, page.Activity)
	}
	for _, cwd := range []string{first, second} {
		for name, body := range map[string]string{"DOX.md": "# Contract\n", "solution.md": "# Inspected definition\n"} {
			if err := os.WriteFile(filepath.Join(cwd, name), []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	var sources []knowledge.Source
	if err := json.Unmarshal([]byte(run(first, "", "knowledge", "sources", "solution.md", "--json")), &sources); err != nil {
		t.Fatal(err)
	}
	record := knowledge.Record{ID: "observed-origin", Kind: "decision", Summary: "Supervised handoff provenance", Scope: "recorded", Sources: sources}
	candidate, _ := json.Marshal(record)
	event := view(first).Events[0]
	var proposal knowledge.Proposal
	if err := json.Unmarshal([]byte(run(first, string(candidate), "knowledge", "propose", "--file=-", "--activity-event="+event.ID, "--json")), &proposal); err != nil || len(proposal.ActivityEvents) != 1 || proposal.ActivityEvents[0].SessionID != event.SessionID {
		t.Fatal("origin not persisted", err, proposal)
	}
	allBefore := run(first, "", "--readonly", "memories", "--json")
	run(first, string(candidate), "knowledge", "propose", "--file=-", "--activity-event="+event.ID, "--json")
	if allBefore != run(first, "", "--readonly", "memories", "--json") {
		t.Fatal("origin retry changed ledger")
	}
	var plane map[string]any
	if err := json.Unmarshal([]byte(allBefore), &plane); err != nil {
		t.Fatal(err)
	}
	key := activity.Prefix + "event/" + event.ID
	run(first, "", "remember", "{}", "--key="+key)
	failed := exec.Command(binary, "knowledge", "review", proposal.ID, "--decision=accept", "--reviewer=process", "--reason=Inspected sources", "--scope=inspected", "--evidence=Definition read")
	failed.Dir = first
	failed.Env = bdEnv(first)
	out, err := failed.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "origins changed") {
		t.Fatal("missing origin accepted", err, string(out))
	}
	run(first, "", "remember", plane[key].(string), "--key="+key)
	// A valid source selection in another workspace still cannot cite this origin.
	foreign := exec.Command(binary, "knowledge", "propose", "--file=-", "--activity-event="+event.ID)
	foreign.Dir = second
	foreign.Env = bdEnv(second)
	foreign.Stdin = strings.NewReader(string(candidate))
	out, err = foreign.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "absent from this workspace") {
		t.Fatal("foreign origin accepted", err, string(out))
	}
	var state knowledge.State
	if err := json.Unmarshal([]byte(run(first, "", "--readonly", "knowledge", "list", "--json")), &state); err != nil || len(state.Records) != 0 || state.Proposals[0].Status != "pending" || state.Proposals[0].ActivityValidity != "current" {
		t.Fatal("supervision bypassed", err, state)
	}
	run(first, "", "knowledge", "review", proposal.ID, "--decision=accept", "--reviewer=process supervisor", "--reason=Inspected definition", "--scope=inspected", "--evidence=Definition read; no execution", "--json")
	if err := json.Unmarshal([]byte(run(first, "", "--readonly", "knowledge", "list", "--json")), &state); err != nil || len(state.Records) != 1 || state.Records[0].Scope != "inspected" {
		t.Fatal("explicit review lost", err, state)
	}
	// Prepared drafts wire the readonly session packet to pending persistence in
	// a fresh process, and protect the same origin in a second workspace.
	beforePrepare := run(first, "", "--readonly", "memories", "--json")
	var packet knowledge.LearningPacket
	if err := json.Unmarshal([]byte(run(nested, "", "--readonly", "knowledge", "prepare", "solution.md", "--session=chat-a", "--turn=turn-a", "--json")), &packet); err != nil || len(packet.Draft.ActivityEvents) != 2 || len(packet.Sources) != 2 {
		t.Fatal("prepare wiring", err, packet)
	}
	if beforePrepare != run(first, "", "--readonly", "memories", "--json") {
		t.Fatal("prepare mutated memory")
	}
	packet.Draft.Record.ID = "prepared-solution"
	packet.Draft.Record.Summary = "Current definition preserved with observed session origins"
	draft, _ := json.Marshal(packet.Draft)
	var prepared knowledge.Proposal
	if err := json.Unmarshal([]byte(run(first, string(draft), "knowledge", "propose", "--file=-", "--json")), &prepared); err != nil || len(prepared.ActivityEvents) != 2 {
		t.Fatal("prepared origin submission", err, prepared)
	}
	beforeRetry := run(first, "", "--readonly", "memories", "--json")
	run(first, string(draft), "knowledge", "propose", "--file=-", "--json")
	if beforeRetry != run(first, "", "--readonly", "memories", "--json") {
		t.Fatal("prepared retry changed journal")
	}
	if err := json.Unmarshal([]byte(run(first, "", "--readonly", "knowledge", "list", "--json")), &state); err != nil || len(state.Records) != 1 {
		t.Fatal("prepared draft prematurely accepted", err, state)
	}
	foreignDraft := exec.Command(binary, "knowledge", "propose", "--file=-")
	foreignDraft.Dir = second
	foreignDraft.Env = bdEnv(second)
	foreignDraft.Stdin = strings.NewReader(string(draft))
	if out, err := foreignDraft.CombinedOutput(); err == nil || !strings.Contains(string(out), "workspace identity") {
		t.Fatal("prepared workspace isolation", err, string(out))
	}
	run(first, "", "knowledge", "review", prepared.ID, "--decision=accept", "--reviewer=process supervisor", "--reason=Inspected current definition", "--scope=inspected", "--evidence=Definition read; no runtime qualification", "--json")
	if recovered := run(nested, "", "--readonly", "prime", "--memories-only"); !strings.Contains(recovered, "prepared-solution") || !strings.Contains(recovered, "knowledge prepare") {
		t.Fatal("prepared learning not recovered", recovered)
	}
	run(first, "", "activity", "disable")
	run(nested, payload("Stop", "chat-d", "turn-d", nested), "codex-activity", "Stop")
	if v = view(first); v.Enabled || v.Total != 4 || run(first, "", "--readonly", "recall", "solved", "--json") != preserved {
		t.Fatal("disable or knowledge preservation")
	}
	outside := t.TempDir()
	if out := run(outside, input, "codex-activity", "PostToolUse"); strings.TrimSpace(out) != "{}" {
		t.Fatal(out)
	}
	if _, err := os.Stat(filepath.Join(outside, ".beads")); !os.IsNotExist(err) {
		t.Fatal("hook initialized outside project")
	}
}
