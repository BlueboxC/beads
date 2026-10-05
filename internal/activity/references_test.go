package activity

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestActivityReferencesDetectTamperingMissingOriginsAndBounds(t *testing.T) {
	plane := map[string]string{}
	ids := []string{}
	for _, turn := range []string{"one", "two"} {
		e, err := Build(Input{SessionID: "chat", TurnID: turn, CWD: "/repo", Event: "Stop", LastAssistantMessage: "Unverified handoff"}, "/repo", time.Unix(1, 0))
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(e)
		plane[Prefix+"event/"+e.ID] = string(raw)
		ids = append(ids, e.ID)
	}
	refs, err := SelectReferences(plane, ids)
	if err != nil || ValidateReferences(refs) != nil || refs[0].ID >= refs[1].ID || len(ChangedReferences(plane, refs)) != 0 {
		t.Fatal(refs, err)
	}
	for _, bad := range [][]string{{"../foreign"}, {ids[0], ids[0]}, {strings.Repeat("a", 64)}, make([]string, MaxReferences+1)} {
		if _, err := SelectReferences(plane, bad); err == nil {
			t.Fatal("invalid selection accepted", bad)
		}
	}
	key := Prefix + "event/" + refs[0].ID
	original := plane[key]
	var e Event
	_ = json.Unmarshal([]byte(original), &e)
	e.Summary = "Changed handoff"
	raw, _ := json.Marshal(e)
	plane[key] = string(raw)
	if Read(plane, "", "", 100).Invalid != 1 || len(ChangedReferences(plane, refs)) != 1 {
		t.Fatal("tampered identity treated as current")
	}
	if _, err := SelectReferences(plane, []string{e.ID}); err == nil {
		t.Fatal("tampered origin selectable")
	}
	plane[key] = original
	// Even a changed timestamp (excluded from retry ID) invalidates pinned origin.
	e.Summary = "Unverified handoff"
	e.ObservedAt = time.Unix(2, 0).UTC().Format(time.RFC3339Nano)
	raw, _ = json.Marshal(e)
	plane[key] = string(raw)
	if Read(plane, "", "", 100).Invalid != 0 || len(ChangedReferences(plane, refs)) != 1 {
		t.Fatal("origin silently rebound")
	}
	delete(plane, key)
	if len(ChangedReferences(plane, refs)) != 1 {
		t.Fatal("missing origin treated as current")
	}
}
