package bdpwire

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func historyTarget(name string) any {
	switch name {
	case "readDiscovery":
		return new(ReadDiscovery)
	case "readProblem":
		return new(ReadProblem)
	case "historicalBeadRecord":
		return new(HistoricalBeadRecord)
	case "historicalLinkRecord":
		return new(HistoricalLinkRecord)
	case "historyVersionsPage":
		return new(HistoryVersionsPage)
	case "historyMissing":
		return new(HistoryMissing)
	case "changeContext":
		return new(ChangeContext)
	}
	return nil
}

// Every upstream example is classified; excluded cases remain in the corpus.
var excludedHistoryCases = map[string]string{
	"discovery-read-update": "discovery", "discovery-transactional": "discovery",
	"allocation-ru-direct": "allocation", "allocation-tx-direct": "allocation", "allocation-tx-receipt": "allocation", "allocation-ru-member": "allocation",
	"create-bead-input": "input", "delete-owned-link-input": "input", "batch-inputs": "input", "sequence-input": "input", "set-update-context": "input", "set-delete-context": "input", "update-bead-input-context": "input", "update-link-input-context": "input",
	"created-event-data": "event", "updated-owned-event-context": "event", "updated-properties-event-context": "event",
	"mutation-context": "result", "receipt-context": "result", "owned-link-delta-context": "result",
}
var selectedHistoryIDs = []string{"discovery-read", "exact-bead", "exact-link", "legacy-link", "versions-replacement", "versions-undetermined", "versions-not-tracked", "versions-first-page", "revision-unknown", "revision-unretained", "revision-reorganized", "revision-not-tracked", "revision-unrepresentable", "resource-pruned", "resource-erased", "missing-17", "missing-18", "missing-19", "bounded-unknown-inventory", "native-context", "import-unknown-context", "versions-empty-visible-page"}

func TestHistoryFixtureCensusAndRoundTrips(t *testing.T) {
	doc := asMap(t, loadJSON(t, "fixtures/history-wire.json"), "history fixture")
	seen := map[string]bool{}
	covered := map[string]int{}
	classes := map[string]int{}
	selected := sliceSet(selectedHistoryIDs)
	defs := selectedBundleDefs(t)
	for _, example := range asSlice(t, doc["examples"], "examples") {
		e := asMap(t, example, "example")
		id := asString(t, e["id"], "id")
		if seen[id] {
			t.Fatalf("duplicate example %s", id)
		}
		seen[id] = true
		name, err := localDefinition(asString(t, e["schema"], "schema"))
		if err != nil {
			t.Fatal(err)
		}
		body, err := json.Marshal(e["body"])
		if err != nil {
			t.Fatal(err)
		}
		if class, excluded := excludedHistoryCases[id]; excluded {
			classes[class]++
			if _, ok := defs[name]; ok {
				t.Fatalf("excluded %s reaches selected schema", id)
			}
			switch class {
			case "discovery":
				var d ReadDiscovery
				if err := Unmarshal(body, &d); err == nil && d.Validate() == nil {
					t.Errorf("accepted %s as Read discovery", id)
				}
			case "allocation":
				var p ReadProblem
				if err := Unmarshal(body, &p); err != nil {
					t.Errorf("open problem shape %s: %v", id, err)
				} else if p.Validate() == nil {
					t.Errorf("accepted unsupported allocation code %s", id)
				}
			}
			continue
		}
		if !selected[id] {
			t.Fatalf("unclassified example %s", id)
		}
		target := historyTarget(name)
		if target == nil {
			t.Fatalf("no target for %s", name)
		}
		for _, entry := range []struct {
			name   string
			decode func([]byte, any) error
		}{{"strict", Unmarshal}, {"reader", func(b []byte, v any) error { return Decode(bytes.NewReader(b), v) }}, {"ordinary", json.Unmarshal}} {
			fresh := reflect.New(reflect.TypeOf(target).Elem()).Interface()
			if err := entry.decode(body, fresh); err != nil {
				t.Errorf("%s/%s: %v", id, entry.name, err)
				continue
			}
			if v, ok := fresh.(interface{ Validate() error }); ok {
				if err := v.Validate(); err != nil {
					t.Errorf("%s validation: %v", id, err)
				}
			}
			out, err := json.Marshal(fresh)
			if err != nil {
				t.Errorf("%s marshal: %v", id, err)
				continue
			}
			if !bytes.Equal(canonical(t, body), canonical(t, out)) {
				t.Errorf("%s/%s lost fixture fields: %s", id, entry.name, out)
			}
		}
		covered[name]++
	}
	if len(seen) != 42 || len(selected) != 22 || len(excludedHistoryCases) != 20 {
		t.Fatal("History corpus count drift")
	}
	for id := range selected {
		if !seen[id] {
			t.Errorf("missing %s", id)
		}
	}
	for id := range excludedHistoryCases {
		if !seen[id] {
			t.Errorf("missing %s", id)
		}
	}
	for _, name := range []string{"readDiscovery", "readProblem", "historicalBeadRecord", "historicalLinkRecord", "historyVersionsPage", "historyMissing", "changeContext"} {
		if covered[name] == 0 {
			t.Errorf("no fixture for %s", name)
		}
	}
	if !reflect.DeepEqual(classes, map[string]int{"input": 8, "discovery": 2, "allocation": 4, "event": 3, "result": 3}) {
		t.Fatalf("classification drift: %v", classes)
	}
}
func strictEntries() []struct {
	name   string
	decode func([]byte, any) error
} {
	return []struct {
		name   string
		decode func([]byte, any) error
	}{{"Unmarshal", Unmarshal}, {"Decode", func(b []byte, v any) error { return Decode(bytes.NewReader(b), v) }}, {"json.Unmarshal", json.Unmarshal}}
}
func TestHistoryStructuralRefusals(t *testing.T) {
	cases := []struct {
		body   string
		target func() any
	}{
		{`{"state":"absent"}`, newTarget[ContextTime]()}, {`{"state":"present"}`, newTarget[ContextTime]()}, {`{"state":"present","value":null}`, newTarget[ContextTime]()},
		{`{"state":"present","value":""}`, newTarget[ContextString]()}, {`{"state":"absent","value":"x"}`, newTarget[ContextString]()}, {`{"state":"undetermined","value":null}`, newTarget[ContextMessage]()}, {`{"State":"undetermined"}`, newTarget[ContextMessage]()}, {`{"state":"present","value":"x","extra":1}`, newTarget[ContextMessage]()},
		{`{"version":0}`, newTarget[HistoryCapability]()}, {`{"version":2}`, newTarget[HistoryCapability]()}, {`{}`, newTarget[HistoryCapability]()}, {`{"version":null}`, newTarget[HistoryCapability]()}, {`{"version":"1"}`, newTarget[HistoryCapability]()}, {`{"version":1.5}`, newTarget[HistoryCapability]()},
		{`{"kind":"record","type":"x"}`, newTarget[HistoryMissingItem]()}, {`{"kind":"record","pointer":null}`, newTarget[HistoryMissingItem]()}, {`{"kind":"property"}`, newTarget[HistoryMissingItem]()}, {`{"kind":"property","pointer":"/id"}`, newTarget[HistoryMissingItem]()}, {`{"kind":"property","pointer":"/properties/~2"}`, newTarget[HistoryMissingItem]()}, {`{"kind":"owned-links","pointer":"/properties/x"}`, newTarget[HistoryMissingItem]()},
		{`{"complete":true,"items":[]}`, newTarget[HistoryMissing]()}, {`{"complete":0,"items":[]}`, newTarget[HistoryMissing]()}, {`{"complete":false,"items":null}`, newTarget[HistoryMissing]()}, {`{"complete":false,"items":[{"kind":"record"},{"kind":"record"}]}`, newTarget[HistoryMissing]()},
		{`{"newest":"r","oldest":null,"complete":false}`, newTarget[HistoryWindow]()}, {`{"newest":null,"oldest":"r","complete":false}`, newTarget[HistoryWindow]()}, {`{"newest":null,"complete":false}`, newTarget[HistoryWindow]()},
		{`{"subject":"x","population":"all-retained","participation":"undetermined","window":{"newest":null,"oldest":null,"complete":false},"items":[],"next":"x"}`, newTarget[HistoryVersionsPage]()},
		{`{"id":"x","type":"x","revision":"r","properties":{},"links":{"items":[],"next":null}}`, newTarget[HistoricalBeadRecord]()},
		{`{"revision":"r","lineage":"bogus","body":"complete"}`, newTarget[HistoryVersionRow]()},
		{`{"revision":"r","lineage":"current","body":"bogus"}`, newTarget[HistoryVersionRow]()},
		{`{"subject":"x","population":"all-retained","participation":"bogus","window":{"newest":null,"oldest":null,"complete":false},"items":[],"next":null}`, newTarget[HistoryVersionsPage]()},
	}
	for i, c := range cases {
		for _, entry := range strictEntries() {
			if err := entry.decode([]byte(c.body), c.target()); err == nil {
				t.Errorf("case%d %s accepted %s", i, entry.name, c.body)
			}
		}
	}
}
func TestHistoryPositiveShapesAndBounds(t *testing.T) {
	for _, body := range []string{`{"state":"present","value":""}`, `{"state":"absent"}`, `{"state":"undetermined"}`} {
		for _, e := range strictEntries() {
			var x ContextMessage
			if err := e.decode([]byte(body), &x); err != nil {
				t.Fatal(err)
			}
			b, err := json.Marshal(x)
			if err != nil || !bytes.Equal(canonical(t, b), canonical(t, []byte(body))) {
				t.Fatalf("lost message state %s", b)
			}
		}
	}
	for _, number := range []string{"1", "1.0", "1e0"} {
		for _, e := range strictEntries() {
			var x HistoryCapability
			if err := e.decode([]byte(`{"version":`+number+`}`), &x); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, n := range []int{64, 65} {
		items := make(HistoryMissingItems, n)
		for i := range items {
			p := "/properties/" + itoa(i)
			items[i] = HistoryMissingItem{Kind: MissingProperty, Pointer: &p}
		}
		x := HistoryMissing{Complete: true, Items: items}
		_, err := json.Marshal(x)
		if (err == nil) != (n == 64) {
			t.Errorf("inventory%d: %v", n, err)
		}
	}
	r := "r"
	row := HistoryVersionRow{Revision: r, Lineage: HistoryCurrent, Body: HistoryComplete}
	page := HistoryVersionsPage{Subject: "x", Population: HistoryPopulation, Participation: HistoryTracked, Window: HistoryWindow{Newest: &r, Oldest: &r}, Items: HistoryVersionRows{row, row}}
	if _, err := json.Marshal(page); err == nil {
		t.Error("duplicate revisions")
	}
	page.Items = page.Items[:1]
	page.Window = HistoryWindow{}
	if _, err := json.Marshal(page); err == nil {
		t.Error("rows without bounds")
	}
	page.Window = HistoryWindow{Newest: &r, Oldest: &r}
	row.Revision = "s"
	page.Items = append(page.Items, row)
	if _, err := json.Marshal(page); err != nil {
		t.Errorf("multiple current rows are legal: %v", err)
	}
}
func TestNullableDecodeClearsReusedDestination(t *testing.T) {
	for _, e := range strictEntries() {
		r := "r"
		w := HistoryWindow{Newest: &r, Oldest: &r}
		if err := e.decode([]byte(`{"newest":null,"oldest":null,"complete":false}`), &w); err != nil || w.Newest != nil || w.Oldest != nil {
			t.Errorf("%s history bounds: %+v %v", e.name, w, err)
		}
		next := "next"
		collection := LinkCollection{Next: &next}
		if err := e.decode([]byte(`{"items":[],"next":null}`), &collection); err != nil || collection.Next != nil {
			t.Errorf("%s collection null: %v", e.name, err)
		}
		page := HistoryVersionsPage{Next: &next}
		if err := e.decode([]byte(`{"subject":"x","population":"all-retained","participation":"undetermined","window":{"newest":null,"oldest":null,"complete":false},"items":[],"next":null}`), &page); err != nil || page.Next != nil {
			t.Errorf("%s page null: %v", e.name, err)
		}
	}
}
func TestHistoryProblemWireConditions(t *testing.T) {
	for _, code := range []ReadProblemCode{CodeRevisionUnknown, CodeRevisionUnretained, CodeRevisionReorganized, CodeRevisionNotTracked, CodeRevisionUnrepresentable} {
		p := NewReadProblem(code)
		if code == CodeRevisionUnretained {
			p.Missing = explicitHistoryMissing()
		}
		if err := p.Validate(); err != nil {
			t.Fatal(err)
		}
		for name := range problemForbiddenMembers(code) {
			q := p
			q.Extensions = map[string]json.RawMessage{name: json.RawMessage(`null`)}
			if _, err := json.Marshal(q); err == nil {
				t.Errorf("%s Marshal permits %s", code, name)
			}
			if q.Validate() == nil {
				t.Errorf("%s Validate permits %s", code, name)
			}
			body, _ := json.Marshal(p)
			body = append(append(body[:len(body)-1], []byte(`,"`+name+`":null`)...), '}')
			for _, e := range strictEntries() {
				var x ReadProblem
				if e.decode(body, &x) == nil {
					t.Errorf("%s/%s accepted forbidden %s", code, e.name, name)
				}
			}
		}
		p.Extensions = map[string]json.RawMessage{"trace": json.RawMessage(`{"ok":true}`)}
		if _, err := json.Marshal(p); err != nil {
			t.Fatal(err)
		}
	}
	if NewReadProblem(CodeRevisionUnretained).Validate() == nil {
		t.Fatal("constructor fabricated missing")
	}
	for _, version := range []int{0, 2} {
		d := ReadDiscovery{BDPVersion: BDPVersion, Profile: ProfileRead, HistoricalResolution: &HistoryCapability{Version: version}}
		if d.Validate() == nil {
			t.Fatal("discovery accepted invalid nested capability")
		}
	}
}
func TestCarrierPreservesUnicodeWithoutRepair(t *testing.T) {
	for _, payload := range []string{`"\ud800"`, `"\udc00"`, `"\ud800x"`, `"\ud800\u0041"`, string([]byte{'"', 0xff, '"'})} {
		for _, body := range []string{`{"state":"present","value":` + payload + `}`, `{"state":"present",` + payload + `:"x"}`} {
			for _, e := range strictEntries() {
				var x ContextMessage
				if e.decode([]byte(body), &x) == nil {
					t.Errorf("%s accepted invalid carrier", e.name)
				}
			}
		}
		for _, body := range []string{`{"x":` + payload + `}`, `{` + payload + `:1}`} {
			var p Properties
			if Unmarshal([]byte(body), &p) == nil {
				t.Error("invalid nested properties")
			}
			problem := `{"type":"x","code":"forbidden","retry":"never","trace":` + body + `}`
			for _, e := range strictEntries() {
				var p ReadProblem
				if e.decode([]byte(problem), &p) == nil {
					t.Error("invalid nested extension")
				}
			}
		}
	}
	for _, value := range []string{`"\ud83d\ude00"`, `"😀"`, `"�"`, `"\\ud800"`} {
		body := []byte(`{"state":"present","value":` + value + `}`)
		for _, e := range strictEntries() {
			var x ContextMessage
			if err := e.decode(body, &x); err != nil {
				t.Errorf("valid scalar %s: %v", value, err)
			}
		}
	}
	var p Properties
	if Unmarshal([]byte(`{"x":1,"\u0078":2}`), &p) == nil {
		t.Error("duplicate escaped property")
	}
	bad := string([]byte{0xff})
	v := ContextMessage{State: ContextPresent, Value: &bad}
	if _, err := json.Marshal(v); err == nil {
		t.Fatal("Marshal repaired invalid Go string")
	}
}
func TestHistoryZeroSumsRefuseAndLegacyDiscoveryStaysRead(t *testing.T) {
	for _, v := range []any{ContextTime{}, ContextString{}, ContextMessage{}, HistoryMissingItem{}, HistoryCapability{}} {
		if _, err := json.Marshal(v); err == nil {
			t.Errorf("zero %T accepted", v)
		}
	}
	b, err := json.Marshal(ReadDiscovery{})
	if err != nil || strings.Contains(string(b), "historicalResolution") {
		t.Fatal("fabricated capability")
	}
}

func TestHistoryNestedCarrierAndPresenceControls(t *testing.T) {
	for _, e := range strictEntries() {
		for _, body := range []string{
			`{"committedAt":{"state":"undetermined"},"agent":{"state":"undetermined"},"message":{"state":"present","value":"x","\u0076alue":"y"}}`,
			`{"committedAt":{"state":"undetermined"},"agent":{"state":"undetermined"},"message":null}`,
			`{"committedAt":{"state":"undetermined"},"agent":{"state":"undetermined"}}`,
		} {
			var x ChangeContext
			if e.decode([]byte(body), &x) == nil {
				t.Errorf("%s accepted nested shape", e.name)
			}
		}
		var p ReadProblem
		if e.decode([]byte(`{"type":"https://github.com/gastownhall/bdp/problems/conflict","code":"revision-unretained","retry":"after-state-change"}`), &p) == nil {
			t.Errorf("%s accepted absent missing", e.name)
		}
	}
	var c BeadCollection
	if err := Unmarshal([]byte(`{"items":[],"next":"next"}`), &c); err != nil || c.Next == nil {
		t.Fatal("nonnull collection control")
	}
	if Unmarshal([]byte(`{"items":[]}`), &c) == nil {
		t.Fatal("missing next accepted")
	}
	bad := string([]byte{0xff})
	v := HistoricalBeadRecord{Properties: Properties{bad: json.RawMessage(`true`)}}
	if _, err := json.Marshal(v); err == nil {
		t.Fatal("Marshal repaired invalid property name")
	}
	v.Properties = Properties{"x": json.RawMessage(`"\ud800"`)}
	if _, err := json.Marshal(v); err == nil {
		t.Fatal("Marshal repaired raw property surrogate")
	}
	for _, body := range []string{`{"x":{"y":1,"\u0079":2}}`, `{"x":[{"y":1,"y":2}]}`} {
		var p Properties
		if Unmarshal([]byte(body), &p) == nil {
			t.Fatal("nested duplicate raw property")
		}
	}
}

func explicitHistoryMissing() *HistoryMissing {
	pointer := "/properties/title"
	return &HistoryMissing{Complete: true, Items: HistoryMissingItems{
		{Kind: MissingProperty, Pointer: &pointer},
		{Kind: MissingOwnedLinks},
	}}
}

func TestHistoricalLinkStrictRoot(t *testing.T) {
	const valid = `{"id":"https://example.test/l","type":"https://example.test/t","revision":"r","source":"https://example.test/s","target":"https://example.test/d","properties":{"n":1.50,"text":"\ud83d\ude00"}}`
	for _, entry := range strictEntries() {
		t.Run(entry.name, func(t *testing.T) {
			var good HistoricalLinkRecord
			if err := entry.decode([]byte(valid), &good); err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(good)
			if err != nil || !bytes.Equal(canonical(t, encoded), canonical(t, []byte(valid))) {
				t.Fatalf("valid root lost wire values: %s, %v", encoded, err)
			}
			for name, body := range map[string]string{
				"case":             strings.Replace(valid, `"revision":"r"`, `"Revision":"r"`, 1),
				"case-collision":   strings.Replace(valid, `"revision":"r"`, `"revision":"r","Revision":"s"`, 1),
				"duplicate":        strings.Replace(valid, `"revision":"r"`, `"revision":"r","revi\u0073ion":"s"`, 1),
				"null":             strings.Replace(valid, `"source":"https://example.test/s"`, `"source":null`, 1),
				"missing":          strings.Replace(valid, `"revision":"r",`, ``, 1),
				"unknown":          strings.Replace(valid, `"revision":"r"`, `"revision":"r","extra":0`, 1),
				"surrogate":        strings.Replace(valid, `1.50`, `"\ud800"`, 1),
				"invalid-UTF8":     strings.Replace(valid, `"r"`, "\""+string([]byte{0xff})+"\"", 1),
				"nested-duplicate": strings.Replace(valid, `"n":1.50`, `"n":1,"\u006e":2`, 1),
			} {
				t.Run(name, func(t *testing.T) {
					var bad HistoricalLinkRecord
					if err := entry.decode([]byte(body), &bad); err == nil {
						t.Fatal("accepted malformed historical Link")
					}
				})
			}
		})
	}
	var good HistoricalLinkRecord
	if err := Unmarshal([]byte(valid), &good); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*HistoricalLinkRecord){
		"field": func(v *HistoricalLinkRecord) { v.Revision = string([]byte{0xff}) },
		"key":   func(v *HistoricalLinkRecord) { v.Properties = Properties{string([]byte{0xff}): json.RawMessage(`1`)} },
		"raw":   func(v *HistoricalLinkRecord) { v.Properties = Properties{"x": json.RawMessage(`"\ud800"`)} },
	} {
		t.Run("marshal-"+name, func(t *testing.T) {
			bad := good
			mutate(&bad)
			if _, err := json.Marshal(bad); err == nil {
				t.Fatal("Marshal repaired malformed carrier")
			}
		})
	}
	// Pin the deliberately narrower change: ordinary Link stays on its prior
	// encoding/json path, which accepts case-insensitive envelope names.
	var ordinary LinkRecord
	if err := json.Unmarshal([]byte(strings.Replace(valid, `"revision"`, `"Revision"`, 1)), &ordinary); err != nil || ordinary.Revision != "r" {
		t.Fatalf("ordinary Link boundary changed: %v", err)
	}
}
