package activity

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/memoryops"
)

func TestBuildCapturesFailuresAndRedactsWithoutRawPayload(t *testing.T) {
	in := Input{SessionID: "chat-a", TurnID: "turn-1", CWD: "/repo", Event: "PostToolUse", Tool: "apply_patch", ToolUseID: "call-1", ToolInput: json.RawMessage(`{"command":"*** Begin Patch\n*** Update File: src/a.go\n*** Add File: ../foreign\npassword=hidden-credential\n*** End Patch","description":"token=hidden-credential"}`), ToolResponse: json.RawMessage(`{"exit_code":1,"output":"hidden-credential"}`)}
	e, err := Build(in, "/repo", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(e)
	if strings.Contains(string(raw), "hidden-credential") || len(e.Files) != 1 || e.Files[0] != "src/a.go" || e.Outcome != "exit_nonzero" || e.ExitCode == nil || *e.ExitCode != 1 {
		t.Fatalf("unexpected capture: %s", raw)
	}
	in.Event = "Stop"
	in.LastAssistantMessage = "Done\npassword=hidden-credential\n-----BEGIN PRIVATE KEY-----\nkey-body\n-----END PRIVATE KEY-----\nNext step"
	e, err = Build(in, "/repo", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if e.SummaryScope != "reported" || strings.Contains(e.Summary, "hidden-credential") || strings.Contains(e.Summary, "key-body") || !strings.Contains(e.Summary, "Next step") {
		t.Fatalf("summary: %+v", e)
	}
}

func TestMissingIdentityAndForeignWorkspaceWithholdCapture(t *testing.T) {
	in := Input{SessionID: "chat", TurnID: "turn", ToolUseID: "call", Tool: "Bash", CWD: "/other", Event: "PostToolUse"}
	if _, err := Build(in, "/repo", time.Now()); err == nil {
		t.Fatal("foreign cwd accepted")
	}
	in.CWD = "/repo"
	in.TurnID = ""
	if _, err := Build(in, "/repo", time.Now()); err == nil {
		t.Fatal("missing turn accepted")
	}
	in.TurnID = "turn"
	in.ToolUseID = ""
	if _, err := Build(in, "/repo", time.Now()); err == nil {
		t.Fatal("missing call accepted")
	}
}

type recordingMemory struct {
	memoryops.Memories
	values map[string]string
	writes int
}

func (m *recordingMemory) Recall(_ context.Context, r memoryops.RecallRequest) (memoryops.RecallResult, error) {
	v := m.values[r.Key]
	return memoryops.RecallResult{Key: r.Key, Value: v, Found: v != ""}, nil
}
func (m *recordingMemory) Remember(_ context.Context, r memoryops.RememberRequest) (memoryops.RememberResult, error) {
	m.values[r.Key] = r.Content
	m.writes++
	return memoryops.RememberResult{Key: r.Key, Value: r.Content}, nil
}

func TestRetryPreservesOriginalObservationAndUnrelatedKnowledge(t *testing.T) {
	m := &recordingMemory{values: map[string]string{"@knowledge/record/solved": "original evidence", "plain": "original memory"}}
	in := Input{SessionID: "chat", TurnID: "turn", CWD: "/repo", Event: "Stop", LastAssistantMessage: "Pending: continue approved work"}
	a, err := Build(in, "/repo", time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Build(in, "/repo", time.Unix(2, 0))
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != b.ID {
		t.Fatal("retry changed identity")
	}
	changed, err := Save(context.Background(), m, a)
	if err != nil || !changed {
		t.Fatal(changed, err)
	}
	changed, err = Save(context.Background(), m, b)
	if err != nil || changed || m.writes != 1 {
		t.Fatal(changed, err, m.writes)
	}
	v := Read(m.values, "chat", "turn", 100)
	if v.Total != 1 || v.Events[0].ObservedAt != a.ObservedAt || m.values["@knowledge/record/solved"] != "original evidence" || m.values["plain"] != "original memory" {
		t.Fatal("retry or unrelated state changed")
	}
	m.values[SettingsKey] = `{"version":1,"enabled":true}`
	text := Context(m.values)
	if !strings.Contains(text, "[reported]") || !strings.Contains(text, "stored events: 1") || strings.Contains(text, "original evidence") {
		t.Fatal(text)
	}
}

func TestFilteringAndRecoveryStayBounded(t *testing.T) {
	plane := map[string]string{SettingsKey: `{"version":1,"enabled":true}`}
	for i := 0; i < 20; i++ {
		in := Input{SessionID: "chat", TurnID: strings.Repeat("a", i+1), CWD: "/repo", Event: "Stop", LastAssistantMessage: strings.Repeat("x", 9000)}
		e, err := Build(in, "/repo", time.Unix(int64(i), 0))
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(e)
		plane[Prefix+"event/"+e.ID] = string(raw)
	}
	if v := Read(plane, "other", "", 2); v.Total != 0 {
		t.Fatal("foreign session matched")
	}
	v := Read(plane, "chat", "", 2)
	if v.Total != 20 || len(v.Events) != 2 || !v.Events[0].SummaryTruncated {
		t.Fatal(v)
	}
	if text := Context(plane); len(text) > 4000 || strings.Count(text, "[reported]") != 3 {
		t.Fatal("unbounded recovery")
	}
}

func TestSerializedBoundsAndCanonicalRetries(t *testing.T) {
	in := Input{SessionID: "chat", TurnID: "turn", ToolUseID: "call", Tool: "MCP", CWD: "/repo", Event: "PostToolUse", ToolInput: json.RawMessage(`{"n":1234567890123456789,"path":"a"}`), ToolResponse: json.RawMessage(`{"session_id":42}`)}
	first, err := Build(in, "/repo", time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	in.ToolInput = json.RawMessage(`{ "path": "a", "n": 1234567890123456789 }`)
	retry, err := Build(in, "/repo", time.Unix(2, 0))
	if err != nil || first.ID != retry.ID || retry.Outcome != "running" {
		t.Fatal("canonical retry or running outcome", err, retry)
	}
	in.Event = "Stop"
	in.LastAssistantMessage = strings.Repeat("<>&á", 6000)
	e, err := Build(in, "/repo", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(e)
	if len(raw) > MaxRecord || !e.SummaryTruncated || !json.Valid(raw) {
		t.Fatal("escaped summary exceeded bound")
	}
	in.Event = "PostToolUse"
	in.ToolInput = json.RawMessage(`{}`)
	in.ToolResponse = json.RawMessage(`"Test passed"`)
	e, err = Build(in, "/repo", time.Now())
	if err != nil || e.Outcome != "unknown" {
		t.Fatal("text output treated as structured success")
	}
	in.SessionID = "chat\nInjected instruction"
	if _, err = Build(in, "/repo", time.Now()); err == nil {
		t.Fatal("control-bearing identity accepted")
	}
}

func activityBenchmarkPlane(b *testing.B) map[string]string {
	b.Helper()
	plane := map[string]string{SettingsKey: `{"version":1,"enabled":true}`}
	for i := 0; i < 10000; i++ {
		in := Input{SessionID: "session", TurnID: fmt.Sprint(i), CWD: "/repo", Event: "PostToolUse", Tool: "apply_patch", ToolUseID: fmt.Sprint(i)}
		if i%20 == 0 {
			in.Event = "Stop"
			in.LastAssistantMessage = "Reported handoff"
		}
		e, err := Build(in, "/repo", time.Unix(int64(i), 0))
		if err != nil {
			b.Fatal(err)
		}
		raw, _ := json.Marshal(e)
		plane[Prefix+"event/"+e.ID] = string(raw)
	}
	return plane
}
func BenchmarkActivityRecent1000(b *testing.B) {
	plane := activityBenchmarkPlane(b)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		Read(plane, "", "", 1000)
	}
}
func BenchmarkActivityContext(b *testing.B) {
	plane := activityBenchmarkPlane(b)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		Context(plane)
	}
}

func TestRecentSelectionKeepsCountsFiltersTiesAndHandoffs(t *testing.T) {
	plane := map[string]string{SettingsKey: `{"version":1,"enabled":true}`, Prefix + "event/broken": "invalid"}
	var events []Event
	for i := 0; i < 100; i++ {
		in := Input{SessionID: fmt.Sprintf("session-%d", i%2), TurnID: fmt.Sprint(i), CWD: "/repo", Event: "PostToolUse", Tool: "apply_patch", ToolUseID: fmt.Sprint(i)}
		if i%3 == 0 {
			in.Event = "Stop"
			in.LastAssistantMessage = fmt.Sprintf("handoff-%03d", i)
		}
		e, err := Build(in, "/repo", time.Unix(int64(i/2), 0))
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(e)
		plane[Prefix+"event/"+e.ID] = string(raw)
		events = append(events, e)
	}
	sort.Slice(events, func(i, j int) bool {
		if events[i].ObservedAt == events[j].ObservedAt {
			return events[i].ID < events[j].ID
		}
		return events[i].ObservedAt > events[j].ObservedAt
	})
	for _, session := range []string{"", "session-0", "missing"} {
		for _, turn := range []string{"", "99", "missing"} {
			expected := []Event{}
			for _, e := range events {
				if (session == "" || e.SessionID == session) && (turn == "" || e.TurnID == turn) {
					expected = append(expected, e)
				}
			}
			for _, limit := range []int{-1, 0, 1, 3, 16, 100, 200} {
				got := Read(plane, session, turn, limit)
				want := expected
				if limit > 0 && len(want) > limit {
					want = want[:limit]
				}
				if !got.Enabled || got.Total != len(expected) || got.Invalid != 1 || !reflect.DeepEqual(got.Events, want) {
					t.Fatalf("session=%s turn=%s limit=%d: %+v", session, turn, limit, got)
				}
			}
		}
	}
	context := Context(plane)
	n := 0
	for _, e := range events {
		if e.Kind != "turn_end" {
			continue
		}
		n++
		if strings.Contains(context, e.Summary) != (n <= 3) {
			t.Fatalf("handoff selection mismatch %d: %s", n, context)
		}
	}
	if !strings.Contains(context, "stored events: 100") {
		t.Fatal("context count lost")
	}
}
