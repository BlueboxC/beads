package graphread

// Semantic cases adapted from gastownhall/bdp read-selector.test.ts at
// 5b4eb46c7f21f2272f4da5a230f042ce753cb304. JavaScript prototype/accessor tests
// do not apply: Go candidates are JSON maps and arrays, with no user getters.

import (
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
)

var testSelectorLimits = SelectorLimits{Bytes: 16384, Depth: 256, Nodes: 2048}

func selectorCandidate(properties map[string]any) map[string]any {
	return map[string]any{"id": "https://beads.example/acme/beads/a", "type": "https://beads.example/acme/types/task", "revision": "revision-a", "source": "https://beads.example/acme/beads/b", "target": "urn:partner:item:42", "properties": properties}
}
func mustCompileSelector(t *testing.T, source string, limits SelectorLimits) *Selector {
	t.Helper()
	s, err := CompileSelector(source, limits)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func TestSelectorSemantics(t *testing.T) {
	candidate := selectorCandidate(map[string]any{
		"status": "open", "value": float64(2), "string": "10", "null": nil, "false": false, "zero": float64(0),
		"revision": "domain-value", "display name": "ready", "text": "line\n\"quoted\"\\slash", "number": float64(-125),
		"apostrophe": "it's ready", "quote": "say \"yes\"", "escaped": "line\nslash/", "astral": "🁁", "bmp": "\uE000",
		"rows": []any{map[string]any{"ignored": true}, map[string]any{"k.k": "match"}}, "jsonNumber": json.Number("2"),
		"nan": math.NaN(), "infinity": math.Inf(1),
	})
	cases := []struct {
		source string
		want   bool
	}{
		{`$[?@]`, true},
		{`$[?@.properties.status == "open"]`, true},
		{`$[?@.properties.status == "closed"]`, false},
		{`$[?@.properties.value == 2]`, true}, {`$[?@.properties.value != 2]`, false},
		{`$[?@.properties.value < 3]`, true}, {`$[?@.properties.value <= 2]`, true},
		{`$[?@.properties.value > 1]`, true}, {`$[?@.properties.value >= 2]`, true},
		{`$[?@.properties.value < 2]`, false}, {`$[?@.properties.value > 2]`, false},
		{`$[?@.properties.string < "2"]`, true}, {`$[?@.properties.string < 20]`, false},
		{`$[?@.properties.string == 10]`, false}, {`$[?@.properties.string != 10]`, true},
		{`$[?@.properties.absent]`, false}, {`$[?@.properties.null]`, true},
		{`$[?@.properties.false]`, true}, {`$[?@.properties.zero]`, true},
		{`$[?!@.properties.absent]`, true}, {`$[?!@.properties.null]`, false},
		{`$[?@.properties.null == null]`, true}, {`$[?@.properties.absent == null]`, false},
		{`$[?@.properties.absent != null]`, true}, {`$[?@.properties.absent < null]`, false},
		{`$[?@.id == "https://beads.example/acme/beads/a"]`, true},
		{`$[?@.type == "https://beads.example/acme/types/task"]`, true},
		{`$[?@.source == "https://beads.example/acme/beads/b" && @.target == "urn:partner:item:42"]`, true},
		{`$[?@.source == "https://BEADS.example/acme/beads/b"]`, false},
		{`$[?@.revision]`, false}, {`$[?@["revision"] == "revision-a"]`, false},
		{`$[?@.properties.revision == "domain-value"]`, true},
		{`$[?@.properties.text == "line\n\"quoted\"\\slash" && @.properties.number == -1.25e2 && @.properties.false == false && @.properties.null == null]`, true},
		{`$[?2 <= @.properties.value]`, true},
		{`$[?@["properties"]['display name'] == 'ready']`, true},
		{`$[?@.properties.rows[1]["k.k"] == "match"]`, true},
		{`$[?@.properties.rows[-1]["k.k"] == "match"]`, true},
		{`$[?@.properties.rows[-2]["ignored"]]`, true},
		{`$[?@.properties.rows[-3]]`, false}, {`$[?@.properties.rows[2]]`, false},
		{`$[?@.properties.rows[9007199254740991]]`, false}, {`$[?@.properties.rows[-9007199254740991]]`, false},
		{`$[?@.properties.rows["0"]]`, false}, {`$[?@.properties[0]]`, false},
		{`$[?@.properties.rows.length]`, false},
		{`$[?@.properties.apostrophe == 'it\'s ready']`, true},
		{`$[?@.properties.quote == 'say "yes"']`, true},
		{`$[?@.properties.escaped == 'line\nslash\/']`, true},
		{`$[?@.properties.astral == '\uD83C\uDC41']`, true},
		{`$[?@.properties.bmp < "𐀀"]`, true}, {`$[?@.properties.bmp >= "𐀀"]`, false},
		{`$[?@.properties.jsonNumber == 2]`, true}, {`$[?@.properties.jsonNumber >= 2]`, true},
		{`$[?@.properties.nan == 2]`, false}, {`$[?@.properties.infinity > 2]`, false},
		{`$[?@.properties == null]`, false}, {`$[?@.properties.rows != null]`, true},
		{`$[?@ .properties .status == "open"]`, true},
		{`$[?null == null]`, true}, {`$[?1 == 1.0]`, true}, {`$[?true > false]`, false},
	}
	before := map[string]any{}
	for k, v := range candidate {
		before[k] = v
	}
	for _, test := range cases {
		t.Run(test.source, func(t *testing.T) {
			if got := mustCompileSelector(t, test.source, testSelectorLimits).Matches(candidate); got != test.want {
				t.Fatalf("Matches = %t; want %t", got, test.want)
			}
		})
	}
	// NaN is deliberately non-reflexive; verify stable metadata and nested identity
	// independently of it rather than comparing the full value recursively.
	if candidate["revision"] != before["revision"] || reflect.ValueOf(candidate["properties"]).Pointer() != reflect.ValueOf(before["properties"]).Pointer() {
		t.Fatal("candidate changed")
	}
}

func TestSelectorPrecedence(t *testing.T) {
	onlyA := selectorCandidate(map[string]any{"a": true, "b": false, "c": false})
	bAndC := selectorCandidate(map[string]any{"a": false, "b": true, "c": true})
	onlyB := selectorCandidate(map[string]any{"a": false, "b": true, "c": false})
	for _, tc := range []struct {
		source string
		want   []bool
	}{
		{`$[?@.properties.a == true || @.properties.b == true && @.properties.c == true]`, []bool{true, true, false}},
		{`$[?(@.properties.a == true || @.properties.b == true) && !(@.properties.c == true)]`, []bool{true, false, true}},
		{`$[?!(!(@.properties.a == true))]`, []bool{true, false, false}},
	} {
		s := mustCompileSelector(t, tc.source, testSelectorLimits)
		for i, c := range []map[string]any{onlyA, bAndC, onlyB} {
			if s.Matches(c) != tc.want[i] {
				t.Fatalf("%s candidate %d mismatch", tc.source, i)
			}
		}
	}
}

func TestSelectorPinnedEndpointsAreExact(t *testing.T) {
	candidate := selectorCandidate(nil)
	candidate["source"] = map[string]any{"uri": "https://beads.example/acme/beads/b", "revision": "r1"}
	if mustCompileSelector(t, `$[?@.source == "https://beads.example/acme/beads/b"]`, testSelectorLimits).Matches(candidate) {
		t.Fatal("Selector silently unpinned Reference")
	}
	if !mustCompileSelector(t, `$[?@.source.uri == "https://beads.example/acme/beads/b" && @.source.revision == "r1"]`, testSelectorLimits).Matches(candidate) {
		t.Fatal("stored Reference members inaccessible")
	}
}

func TestSelectorErrors(t *testing.T) {
	unsupported := []string{
		`$[?@..properties.status == "open"]`, `$[?@.*]`, `$[?length(@.properties) == 1]`,
		`$[?@.properties.status =~ "open"]`, `$[?@.properties[?@.status]]`,
		`$[?@.properties.left == @.properties.right]`, `$[?@.properties.a,@.properties.b]`,
		`$[?@.properties.value == [1]]`, `$[?@.properties.value == {"x":1}]`,
		`$[?@.properties.status].name`, `$[?$.properties.status]`, `$[?@.properties.rows[*]]`,
		`$[?@.properties.rows[0:2]]`, `$[?@.properties.rows[0,1]]`,
		`$[?@.properties.status][0]`, `$[?@.properties.status]@.id`,
	}
	syntax := []string{
		``, `$`, `$[]`, `$[?]`, `$[?true]`, `$[?@.properties.value ==]`, `$[?== 1]`,
		`$[?@.properties.value = 1]`, `$[?@.properties.value &&]`, `$[?(@.properties.value]`,
		`$[?@.properties.value)]`, `$[?@.properties.value == "unterminated] `,
		`$[?@.properties.value == 01]`, `$[?@.properties.value == NaN]`,
		`$[?@.properties.value == true false]`, `$[?!@.properties.value == true]`,
		`$[?!!@.properties.value]`, `$[?! !@.properties.value]`, `$[?@.properties.rows[01]]`,
		`$[?@.properties.rows[-0]]`, `$[?@.properties.rows[-01]]`,
		`$[?@.properties.rows[9007199254740992]]`, `$[?@.properties.rows[-9007199254740992]]`,
		`$[?@.properties.value == "\uD800"]`, `$[?@.properties.value == "\uDC00"]`,
		`$[?@.properties.value == "\uD800\uD800"]`, `$[?@.properties.value == 1e999]`,
		`$[?@.properties.value == "\'"]`, `$[?@.properties.value == '\"']`,
		`$[?@.properties.value == "\u+123"]`, `$[?@.properties.value == "\u-123"]`,
		"$[?@.properties.value == \"\xff\"]", `$[?@.properties[ "value"]]`,
		`$[?(@.id &&)]`, `$[?(@.id) == 1]`, `$[?@.id] true`,
	}
	for code, cases := range map[string][]string{"syntax": syntax, "unsupported-feature": unsupported} {
		for _, source := range cases {
			t.Run(source, func(t *testing.T) {
				_, err := CompileSelector(source, testSelectorLimits)
				var typed *SelectorError
				if !errors.As(err, &typed) || typed.Code != code {
					t.Fatalf("error = %v; want %s", err, code)
				}
			})
		}
	}
}

func TestSelectorBounds(t *testing.T) {
	source := `$[?@.properties.name == "café☕"]`
	mustCompileSelector(t, source, SelectorLimits{Bytes: len(source), Depth: 4, Nodes: 5})
	for _, tc := range []struct {
		limits        SelectorLimits
		code          string
		limit, actual int
	}{
		{SelectorLimits{Bytes: len(source) - 1, Depth: 4, Nodes: 5}, "source-bytes-limit-exceeded", len(source) - 1, len(source)},
		{SelectorLimits{Bytes: len(source), Depth: 3, Nodes: 5}, "ast-depth-limit-exceeded", 3, 4},
		{SelectorLimits{Bytes: len(source), Depth: 4, Nodes: 4}, "ast-nodes-limit-exceeded", 4, 5},
	} {
		_, err := CompileSelector(source, tc.limits)
		var typed *SelectorError
		if !errors.As(err, &typed) || typed.Code != tc.code || typed.Limit != tc.limit || typed.Actual != tc.actual {
			t.Fatalf("bound error = %#v; want %#v", err, tc)
		}
	}
	for _, limits := range []SelectorLimits{{}, {Bytes: 1, Depth: -1, Nodes: 1}, {Bytes: 1, Depth: 1, Nodes: 0}} {
		if _, err := CompileSelector(source, limits); err == nil {
			t.Fatalf("accepted invalid limits %+v", limits)
		}
	}
}

func TestSelectorPathologicalShapes(t *testing.T) {
	nested := "$[?" + strings.Repeat("(", 10000) + "@.id" + strings.Repeat(")", 10000) + "]"
	negated := "$[?" + strings.Repeat("!(", 10000) + "@.id" + strings.Repeat(")", 10000) + "]"
	wide := "$[?" + strings.Repeat("@.id || ", 9999) + "@.id]"
	for _, tc := range []struct {
		source string
		limits SelectorLimits
		code   string
	}{
		{nested, SelectorLimits{Bytes: 100000, Depth: 32, Nodes: 100000}, "ast-depth-limit-exceeded"},
		{negated, SelectorLimits{Bytes: 100000, Depth: 32, Nodes: 100000}, "ast-depth-limit-exceeded"},
		{wide, SelectorLimits{Bytes: 100000, Depth: 100000, Nodes: 32}, "ast-nodes-limit-exceeded"},
	} {
		_, err := CompileSelector(tc.source, tc.limits)
		var typed *SelectorError
		if !errors.As(err, &typed) || typed.Code != tc.code {
			t.Fatalf("shape error = %v; want %s", err, tc.code)
		}
	}
	enormous := SelectorLimits{Bytes: math.MaxInt, Depth: math.MaxInt, Nodes: math.MaxInt}
	for _, source := range []string{nested, negated, wide} {
		if !mustCompileSelector(t, source, enormous).Matches(selectorCandidate(nil)) {
			t.Fatal("deep iterative evaluation failed")
		}
	}
}

func FuzzSelectorNeverPanics(f *testing.F) {
	for _, source := range []string{`$[?@.id]`, `$[?@.properties.status == "open"]`, `$[?((@.id))]`, `$[?@.source[0]]`, `$[?@.id == '\uD83C\uDC41']`} {
		f.Add(source)
	}
	f.Fuzz(func(t *testing.T, source string) {
		s, err := CompileSelector(source, testSelectorLimits)
		if err == nil {
			s.Matches(selectorCandidate(map[string]any{"status": "open"}))
		}
	})
}
