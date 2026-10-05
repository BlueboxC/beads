package knowledge

import (
	"encoding/json"
	"github.com/steveyegge/beads/internal/activity"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLearningPreparationAndPinnedSubmission(t *testing.T) {
	root := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("DOX.md", "# Contract: keep prior solutions\n")
	write("solution.md", "# Definition\nResolved module boundary\n")
	reader, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	plane := map[string]string{}
	var event activity.Event
	for i := 0; i < 17; i++ {
		event, err = activity.Build(activity.Input{SessionID: "chat-a", TurnID: "turn-a", CWD: root, Event: "Stop", LastAssistantMessage: strings.Repeat("x", i+1)}, root, time.Date(2026, 10, 4, 1, i, 0, 0, time.UTC))
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(event)
		plane[activity.Prefix+"event/"+event.ID] = string(raw)
	}
	original, _ := json.Marshal(plane)
	packet, err := reader.PrepareLearning(plane, []string{"solution.md"}, "chat-a", "turn-a", nil)
	if err != nil || len(packet.Draft.ActivityEvents) != 16 || packet.OmittedEvents != 1 || packet.Activity.Total != 17 || len(packet.Sources) != 2 {
		t.Fatal(packet, err)
	}
	for _, s := range packet.Sources {
		if s.Text == "" || activity.Digest([]byte(s.Text)) != s.SHA256 {
			t.Fatal("exact source material missing", s)
		}
	}
	after, _ := json.Marshal(plane)
	if string(after) != string(original) {
		t.Fatal("readonly preparation wrote")
	}
	packet, err = reader.PrepareLearning(plane, []string{"solution.md"}, "", "", []string{event.ID})
	if err != nil {
		t.Fatal(err)
	}
	packet.Draft.Record.ID = "new-solution"
	packet.Draft.Record.Summary = "Inspected definition with reported origin"
	raw, _ := json.Marshal(packet.Draft)
	record, refs, err := reader.ProposalInput(raw, plane, State{})
	if err != nil || !reflect.DeepEqual(refs, packet.Draft.ActivityEvents) {
		t.Fatal(err, refs)
	}
	proposal, err := reader.PrepareActivityProposal(record, "", State{}, refs)
	if err != nil || proposal.Scope != "recorded" {
		t.Fatal(err, proposal)
	}
	if _, _, err = reader.ProposalInput(raw, plane, State{Records: []View{{Record: record}}}); err == nil {
		t.Fatal("duplicated established solution")
	}
	full, _ := json.Marshal(packet)
	if _, _, err = reader.ProposalInput(full, plane, State{}); err == nil {
		t.Fatal("uninspected full packet was submitted")
	}
	legacy, _ := json.Marshal(record)
	if old, origins, err := reader.ProposalInput(legacy, plane, State{}); err != nil || old.ID != record.ID || len(origins) != 0 {
		t.Fatal("legacy input changed", err)
	}
	foreign, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = foreign.Close() }()
	if _, _, err = foreign.ProposalInput(raw, plane, State{}); err == nil {
		t.Fatal("foreign workspace accepted same journal")
	}
	key := activity.Prefix + "event/" + event.ID
	originalEvent := plane[key]
	event.ObservedAt = time.Now().UTC().Format(time.RFC3339Nano)
	changed, _ := json.Marshal(event)
	plane[key] = string(changed)
	if _, _, err = reader.ProposalInput(raw, plane, State{}); err == nil {
		t.Fatal("origin silently renewed")
	}
	plane[key] = originalEvent
	write("solution.md", "Changed after preparation")
	if _, err = reader.PrepareActivityProposal(record, "", State{}, refs); err == nil {
		t.Fatal("source silently renewed")
	}
	for _, path := range []string{"../outside.md", "/etc/passwd"} {
		if _, err = reader.PrepareLearning(plane, []string{path}, "", "", []string{event.ID}); err == nil {
			t.Fatal("outside source allowed", path)
		}
	}
	if _, err = reader.PrepareLearning(plane, []string{"solution.md"}, "unknown", "", nil); err == nil {
		t.Fatal("empty journal allowed")
	}
	if _, err = reader.PrepareLearning(plane, []string{"solution.md"}, "chat-a", "", []string{event.ID}); err == nil {
		t.Fatal("ambiguous selection allowed")
	}
}

func TestLearningSourceBudgetIsUTF8AndHonest(t *testing.T) {
	root := t.TempDir()
	paths := []string{}
	for i := 0; i < 8; i++ {
		name := string(rune('a'+i)) + ".md"
		paths = append(paths, name)
		if err := os.WriteFile(filepath.Join(root, name), []byte(strings.Repeat("é", 20000)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	reader, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	event, err := activity.Build(activity.Input{SessionID: "chat", TurnID: "turn", CWD: root, Event: "Stop"}, root, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(event)
	packet, err := reader.PrepareLearning(map[string]string{activity.Prefix + "event/" + event.ID: string(raw)}, paths, "chat", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	size := 0
	for _, source := range packet.Sources {
		size += len(source.Text)
		if !source.Truncated {
			t.Fatal("omitted source tail not reported")
		}
	}
	if size != 64*1024 {
		t.Fatal("source budget", size)
	}
}
