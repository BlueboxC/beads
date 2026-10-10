package graphstore

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	publicops "github.com/steveyegge/beads/issueops"
)

func comparisonMemory(version, body string) Record {
	return Record{ID: "https://example.test/beads/plan", Type: "https://example.test/types/memory", Version: version, Revision: version, Properties: Properties{Title: "Plan", Body: body}, Metadata: json.RawMessage(`{}`), Owned: []json.RawMessage{}, Attribution: Attribution{Actor: "author", Status: "claimed", RecordedAt: "2026-09-26T12:00:00Z"}}
}
func comparisonLink(version string) LinkRecord {
	return LinkRecord{ID: "https://example.test/links/context", Type: "https://example.test/types/related", Version: version, Revision: version, Source: "https://example.test/beads/plan", Target: "https://example.test/beads/work", Properties: map[string]any{"note": "context"}, Metadata: json.RawMessage(`{}`), Attribution: Attribution{Actor: "author", Status: "claimed", RecordedAt: "2026-09-26T12:00:00Z"}}
}
func comparisonRaw(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestVersionComparisonMemoryDirectionAndContext(t *testing.T) {
	before := comparisonMemory("before", "  old\r\n雪\n")
	after := comparisonMemory("after", "  new\r\n雪\n")
	after.Attribution.Actor = "editor"
	got, err := compareVersionRecords(before, after)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Changes) != 1 || got.Changes[0].Area != "properties" || got.Changes[0].Member != "body" || string(got.Changes[0].From.Value) != string(comparisonRaw(t, before.Properties.Body)) || string(got.Changes[0].To.Value) != string(comparisonRaw(t, after.Properties.Body)) {
		t.Fatalf("body comparison: %+v", got)
	}
	if got.From.Version != "before" || got.To.Version != "after" || got.To.Attribution.Actor != "editor" || !reflect.DeepEqual(got.Compared, []string{"properties", "metadata", "owned"}) || !reflect.DeepEqual(got.Unsupported, []string{"inception", "derivation"}) {
		t.Fatalf("context: %+v", got)
	}
	withMetadata := after
	withMetadata.Metadata = json.RawMessage(`{"team":"docs"}`)
	metadataChange, err := compareVersionRecords(after, withMetadata)
	if err != nil || len(metadataChange.Changes) != 1 || metadataChange.Changes[0].Area != "metadata" || metadataChange.Changes[0].Member != "team" || metadataChange.Changes[0].From.Present || string(metadataChange.Changes[0].To.Value) != `"docs"` {
		t.Fatalf("metadata comparison: %+v %v", metadataChange, err)
	}
	reverse, err := compareVersionRecords(after, before)
	if err != nil || !reflect.DeepEqual(reverse.Changes[0].From, got.Changes[0].To) || !reflect.DeepEqual(reverse.Changes[0].To, got.Changes[0].From) {
		t.Fatalf("reverse: %+v %v", reverse, err)
	}
	for _, other := range []Record{before, func() Record {
		v := before
		v.Version = "another"
		v.Revision = "another"
		v.Attribution.Actor = "other"
		return v
	}()} {
		same, err := compareVersionRecords(before, other)
		if err != nil || same.Changes == nil || len(same.Changes) != 0 {
			t.Fatalf("context-only difference: %+v %v", same, err)
		}
		raw := string(comparisonRaw(t, same))
		if !strings.Contains(raw, `"changes":[]`) {
			t.Fatal(raw)
		}
	}
}

func TestVersionComparisonOwnedCompleteIdentity(t *testing.T) {
	before, after := comparisonMemory("a", "body"), comparisonMemory("b", "body")
	link1, link2 := comparisonLink("link-a"), comparisonLink("link-b")
	before.Owned = []json.RawMessage{comparisonRaw(t, link1)}
	after.Owned = []json.RawMessage{comparisonRaw(t, link2)}
	got, err := compareVersionRecords(before, after)
	if err != nil || len(got.Changes) != 1 || got.Changes[0].Area != "owned" || got.Changes[0].ID != link1.ID || !reflect.DeepEqual(got.Changes[0].From.Value, before.Owned[0]) || !reflect.DeepEqual(got.Changes[0].To.Value, after.Owned[0]) {
		t.Fatalf("version-only owned change: %+v %v", got, err)
	}
	after.Owned = []json.RawMessage{}
	removed, err := compareVersionRecords(before, after)
	if err != nil || len(removed.Changes) != 1 || !removed.Changes[0].From.Present || removed.Changes[0].To.Present || removed.Changes[0].To.Value != nil {
		t.Fatalf("removal: %+v %v", removed, err)
	}
	if strings.Contains(string(comparisonRaw(t, removed.Changes[0].To)), "value") {
		t.Fatal("absent value serialized")
	}
	added, err := compareVersionRecords(after, before)
	if err != nil || added.Changes[0].From.Present || !added.Changes[0].To.Present {
		t.Fatalf("addition: %+v %v", added, err)
	}
	for _, mutate := range []func(*LinkRecord){func(v *LinkRecord) { v.Type += "-other" }, func(v *LinkRecord) { v.Source += "-other" }, func(v *LinkRecord) { v.Target += "-other" }} {
		bad := link2
		mutate(&bad)
		after.Owned = []json.RawMessage{comparisonRaw(t, bad)}
		if got, err := compareVersionRecords(before, after); !errors.Is(err, ErrInvalidStore) || !reflect.DeepEqual(got, VersionComparison{}) {
			t.Fatalf("immutable owned mismatch: %+v %v", got, err)
		}
	}
	after.Owned = []json.RawMessage{before.Owned[0], before.Owned[0]}
	if _, err := compareVersionRecords(before, after); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("duplicate owned ID: %v", err)
	}
}

func TestVersionComparisonJSONSemantics(t *testing.T) {
	cases := []struct {
		a, b  string
		equal bool
	}{
		{`9007199254740993`, `9007199254740992`, false},
		{`9007199254740993`, `9007199254740993.000`, true},
		{`1e1000000`, `10e999999`, true},
		{`1e1000000`, `1e999999`, false},
		{`0`, `-0.0e999999999999999999999999999999`, true},
		{`1.2300e-20`, `123e-22`, true},
		{`-1.23`, `1.23`, false},
		{`{"n":1,"a":[null,true,"雪"]}`, `{"a":[null,true,"\u96ea"],"n":1.0}`, true},
		{`[1,2]`, `[2,1]`, false},
		{`{"a":null}`, `{}`, false},
		{`null`, `false`, false},
		{`" body\n"`, `"body\n"`, false},
	}
	for _, tc := range cases {
		equal, err := comparisonJSONEqual([]byte(tc.a), []byte(tc.b))
		if err != nil || equal != tc.equal {
			t.Errorf("%s vs %s: %v %v", tc.a, tc.b, equal, err)
		}
	}
	if _, err := comparisonJSONEqual([]byte(`1 2`), []byte(`1`)); err == nil {
		t.Fatal("invalid JSON accepted")
	}
	// Whole changed values preserve the input exact decimal spelling/value.
	a, b := comparisonLink("a"), comparisonLink("b")
	a.Properties = map[string]any{"n": json.Number("9007199254740993"), "gone": nil, "same": json.RawMessage(`{"z":1,"a":2}`)}
	b.Properties = map[string]any{"n": json.Number("9007199254740992"), "new": nil, "same": json.RawMessage(`{"a":2.0,"z":1.0}`)}
	got, err := compareVersionRecords(a, b)
	if err != nil || len(got.Changes) != 3 {
		t.Fatalf("property values: %+v %v", got, err)
	}
	if got.Changes[0].Member != "gone" || !got.Changes[0].From.Present || string(got.Changes[0].From.Value) != "null" || got.Changes[0].To.Present || got.Changes[1].Member != "n" || string(got.Changes[1].From.Value) != "9007199254740993" || got.Changes[2].Member != "new" || got.Changes[2].From.Present || string(got.Changes[2].To.Value) != "null" {
		t.Fatalf("absence/null/number: %+v", got.Changes)
	}
	if !reflect.DeepEqual(got.Compared, []string{"properties", "metadata"}) || len(got.Unsupported) != 0 {
		t.Fatalf("Link scope: %+v", got)
	}
}

func TestVersionComparisonIssueAllSerializedFields(t *testing.T) {
	before := IssueRecord{ID: "https://example.test/beads/work", Type: "https://example.test/types/issue", Version: "a", Revision: "a", Properties: &publicops.Issue{ID: "work", Title: "Work", Priority: 2}, Metadata: json.RawMessage(`{}`), Owned: []json.RawMessage{}}
	after := before
	after.Version = "b"
	after.Revision = "b"
	changed := *before.Properties
	changed.Description = "description"
	changed.Design = "design"
	changed.AcceptanceCriteria = "accept"
	changed.Notes = "notes"
	changed.SpecID = "spec"
	changed.Assignee = "worker"
	changed.Owner = "owner"
	changed.Labels = []string{"a", "b"}
	after.Metadata = json.RawMessage(`{"null":null}`)
	changed.UpdatedAt = time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	changed.Status = "closed"
	changed.Priority = 1
	after.Properties = &changed
	got, err := compareVersionRecords(before, after)
	if err != nil {
		t.Fatal(err)
	}
	// Derive all serialized members, rather than maintaining a short handpicked
	// comparator projection that would silently omit future Issue fields.
	var oldJSON, newJSON map[string]json.RawMessage
	if err := json.Unmarshal(comparisonRaw(t, before.Properties), &oldJSON); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(comparisonRaw(t, after.Properties), &newJSON); err != nil {
		t.Fatal(err)
	}
	expected := []string{}
	for _, key := range comparisonKeys(oldJSON, newJSON) {
		if !reflect.DeepEqual(oldJSON[key], newJSON[key]) {
			expected = append(expected, key)
		}
	}
	actual := []string{}
	metadataChanges := []VersionChange{}
	for _, change := range got.Changes {
		if change.Area == "properties" {
			actual = append(actual, change.Member)
		} else if change.Area == "metadata" {
			metadataChanges = append(metadataChanges, change)
		}
	}
	if !reflect.DeepEqual(actual, expected) || len(actual) < 10 {
		t.Fatalf("Issue fields: %v want %v", actual, expected)
	}
	if len(metadataChanges) != 1 || metadataChanges[0].Member != "null" || metadataChanges[0].From.Present || !metadataChanges[0].To.Present || string(metadataChanges[0].To.Value) != "null" {
		t.Fatalf("Issue metadata changes: %+v", metadataChanges)
	}
	if len(got.Unsupported) != 0 {
		t.Fatal(got.Unsupported)
	}
	if before.Properties.Dependencies != nil {
		t.Fatal("comparator mutated its operand")
	}
}

func TestVersionComparisonOrderingAndIdentity(t *testing.T) {
	a, b := comparisonLink("a"), comparisonLink("b")
	a.Properties = map[string]any{}
	b.Properties = map[string]any{"\uffff": true, "😀": true, "a": true}
	got, err := compareVersionRecords(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if got.Changes[0].Member != "a" || got.Changes[1].Member != "😀" || got.Changes[2].Member != "\uffff" {
		t.Fatalf("UTF-16 order: %+v", got.Changes)
	}
	for _, mutate := range []func(*LinkRecord){func(v *LinkRecord) { v.ID += "other" }, func(v *LinkRecord) { v.Type += "other" }, func(v *LinkRecord) { v.Source += "other" }, func(v *LinkRecord) { v.Target += "other" }} {
		bad := b
		mutate(&bad)
		if result, err := compareVersionRecords(a, bad); !errors.Is(err, ErrInvalidStore) || !reflect.DeepEqual(result, VersionComparison{}) {
			t.Fatalf("immutable mismatch: %+v %v", result, err)
		}
	}
	if _, err := compareVersionRecords(a, comparisonMemory("a", "")); !errors.Is(err, ErrInvalidStore) {
		t.Fatal(err)
	}
}
