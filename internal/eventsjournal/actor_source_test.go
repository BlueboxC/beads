package eventsjournal

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

func TestActorSourceWireProjectionPreservesHistoricalRecords(t *testing.T) {
	for _, source := range []string{"", "git", "provided", "flag"} {
		rec := NewRecord(storage.EventsJournalRow{Actor: "Owner", ActorSource: source})
		data, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatal(err)
		}
		if rec.Actor != "Owner" || rec.ActorSource != source {
			t.Fatal(rec)
		}
		if source == "" && strings.Contains(string(data), "actor_source") {
			t.Fatalf("historical row gained provenance: %s", data)
		}
		if source != "" && decoded["actor_source"] != source {
			t.Fatal(decoded)
		}
	}
}
