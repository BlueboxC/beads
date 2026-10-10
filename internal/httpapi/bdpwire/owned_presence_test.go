package bdpwire

import (
	"encoding/json"
	"testing"
)

func TestOwnedLinksPresenceSurvivesRecordAndHistoryEncoding(t *testing.T) {
	for _, tc := range []struct {
		name  string
		owned OwnedLinks
		want  string
	}{{"absent", nil, ""}, {"empty wildcard", OwnedLinks{}, "{}"}, {"empty explicit", OwnedLinks{"https://example.test/types/blocks": {}}, `{"https://example.test/types/blocks":[]}`}} {
		t.Run(tc.name, func(t *testing.T) {
			record := BeadRecord{ID: "https://example.test/beads/plan", Type: "https://example.test/types/memory", Revision: "r1", OwnedLinks: tc.owned}
			historical := HistoricalBeadRecord{ID: record.ID, Type: record.Type, Revision: record.Revision, OwnedLinks: tc.owned}
			for _, value := range []any{record, historical} {
				raw, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				var members map[string]json.RawMessage
				if err := json.Unmarshal(raw, &members); err != nil {
					t.Fatal(err)
				}
				if string(members["ownedLinks"]) != tc.want {
					t.Fatalf("ownedLinks=%s, want %s", members["ownedLinks"], tc.want)
				}
				var decoded BeadRecord
				if err := Unmarshal(raw, &decoded); err != nil {
					t.Fatal(err)
				}
				if (decoded.OwnedLinks == nil) != (tc.owned == nil) {
					t.Fatal("absence changed during strict decode")
				}
			}
		})
	}
}
