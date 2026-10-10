package graphpatch

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/graphops"
)

func properties(t *testing.T, raw string) graphops.Properties {
	t.Helper()
	p, err := graphops.NewProperties([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func admitted(t *testing.T, raw string) *Patch {
	t.Helper()
	p, err := Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// The escaped-name/array case ports resource-evaluator.test.ts:902–924 from
// BDP 53bdbd03136875f952af184fce7b3c7af8f74e96. The other cases pin the same
// operation subset plus Go numeric admission and preview representation bounds.
func TestPropertyChangeSemantics(t *testing.T) {
	cases := []struct{ name, before, patch, want string }{
		{"reference-arrays-escapes", `{"list":[1,3],"a/b":{"~x":1}}`, `[{"op":"add","path":"/list/1","value":2},{"op":"remove","path":"/list/0"},{"op":"replace","path":"/a~1b/~0x","value":7},{"op":"add","path":"/__proto__","value":{"polluted":true}}]`, `{"list":[2,3],"a/b":{"~x":7},"__proto__":{"polluted":true}}`},
		{"null-is-present", `{"x":null}`, `[{"op":"replace","path":"/x","value":null}]`, `{"x":null}`},
		{"null-remove", `{"x":null}`, `[{"op":"remove","path":"/x"}]`, `{}`},
		{"root-remove-add", `{"x":1}`, `[{"op":"remove","path":""},{"op":"add","path":"","value":{"x":2}}]`, `{"x":2}`},
		{"temporary-root-array", `{}`, `[{"op":"replace","path":"","value":[0]},{"op":"add","path":"/-","value":1},{"op":"remove","path":"/0"},{"op":"replace","path":"","value":{}}]`, `{}`},
		{"temporary-root-null", `{}`, `[{"op":"replace","path":"","value":null},{"op":"replace","path":"","value":{}}]`, `{}`},
		{"temporary-member-array", `{"body":"old"}`, `[{"op":"add","path":"/work","value":[1]},{"op":"add","path":"/work/0","value":2},{"op":"replace","path":"/work/1","value":3},{"op":"remove","path":"/work"},{"op":"replace","path":"/body","value":"new"}]`, `{"body":"new"}`},
		{"literal-body", `{"title":"keep","body":"old"}`, `[{"op":"replace","path":"/body","value":"---\r\nλ 😀\r\n  "}]`, `{"title":"keep","body":"---\r\nλ 😀\r\n  "}`},
		{"numeric-equivalence", `{"n":1}`, `[{"op":"replace","path":"/n","value":1.0}]`, `{"n":1}`},
		{"large-exact-admitted", `{}`, `[{"op":"add","path":"/n","value":9007199254740994}]`, `{"n":9007199254740994}`},
		{"reversing-noop", `{"x":1}`, `[{"op":"replace","path":"/x","value":2},{"op":"replace","path":"/x","value":1}]`, `{"x":1}`},
		{"empty-name-and-literal-tilde", `{"":{"~1":0}}`, `[{"op":"replace","path":"//~01","value":1}]`, `{"":{"~1":1}}`},
		{"unicode-key-size", `{}`, `[{"op":"add","path":"/ <😀>","value":true}]`, `{" <😀>":true}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := properties(t, tc.before)
			saved := before.String()
			patch := admitted(t, tc.patch)
			got, err := patch.Apply(before)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Equal(properties(t, tc.want)) {
				t.Fatalf("unexpected result: %s", got.String())
			}
			if before.String() != saved {
				t.Fatal("predecessor changed")
			}
			again, err := patch.Apply(before)
			if err != nil || !again.Equal(got) {
				t.Fatal("patch reuse changed result", err)
			}
		})
	}
}

func TestPropertyChangeAdmission(t *testing.T) {
	cases := map[string]string{
		"empty-list": `[]`, "object": `{}`, "null": `null`, "entry-null": `[null]`,
		"move": `[{"op":"move","path":"/x","from":"/y"}]`, "copy": `[{"op":"copy","path":"/x","from":"/y"}]`, "test": `[{"op":"test","path":"/x","value":1}]`,
		"missing-value": `[{"op":"add","path":"/x"}]`, "remove-value": `[{"op":"remove","path":"/x","value":1}]`, "extra-member": `[{"op":"add","path":"/x","value":1,"extra":true}]`,
		"missing-path": `[{"op":"add","value":1}]`, "null-path": `[{"op":"add","path":null,"value":1}]`, "no-leading-slash": `[{"op":"remove","path":"x"}]`, "bad-escape": `[{"op":"remove","path":"/~2"}]`, "trailing-tilde": `[{"op":"remove","path":"/~"}]`,
		"duplicate-op": `[{"op":"add","op":"remove","path":"/x","value":1}]`, "duplicate-value-key": `[{"op":"add","path":"/x","value":{"a":1,"a":2}}]`, "lone-surrogate": `[{"op":"add","path":"/x","value":"\ud800"}]`, "rounded-number": `[{"op":"add","path":"/x","value":9007199254740993}]`, "trailing-document": `[{"op":"remove","path":"/x"}] {}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			p, err := Parse([]byte(raw))
			if p != nil || !errors.Is(err, ErrInvalid) {
				t.Fatalf("got patch=%v error=%v", p, err)
			}
		})
	}
	raw := []byte(`[{"op":"add","path":"/x","value":"a"}]`)
	raw[len(raw)-4] = 0xff
	if p, err := Parse(raw); p != nil || !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid UTF-8 accepted", err)
	}
}

func TestPropertyChangeApplicationRefusals(t *testing.T) {
	cases := map[string]string{
		"missing-remove": `[{"op":"remove","path":"/absent"}]`, "missing-replace": `[{"op":"replace","path":"/absent","value":1}]`, "missing-parent": `[{"op":"add","path":"/absent/x","value":1}]`, "scalar-parent": `[{"op":"add","path":"/scalar/x","value":1}]`,
		"array-leading-zero": `[{"op":"remove","path":"/a/00"}]`, "array-negative": `[{"op":"remove","path":"/a/-1"}]`, "array-large": `[{"op":"add","path":"/a/999999999999999999999999999999","value":1}]`, "array-end-remove": `[{"op":"remove","path":"/a/1"}]`, "array-dash-replace": `[{"op":"replace","path":"/a/-","value":1}]`, "array-dash-parent": `[{"op":"add","path":"/a/-/x","value":1}]`,
		"final-missing": `[{"op":"remove","path":""}]`, "final-null": `[{"op":"replace","path":"","value":null}]`, "final-array": `[{"op":"replace","path":"","value":[]}]`, "removed-root-replace": `[{"op":"remove","path":""},{"op":"replace","path":"","value":{}}]`,
		"later-failure": `[{"op":"replace","path":"/scalar","value":2},{"op":"remove","path":"/missing"}]`,
	}
	before := properties(t, `{"a":[0],"scalar":1}`)
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := admitted(t, raw).Apply(before)
			if !errors.Is(err, ErrInvalid) || !got.IsEmpty() {
				t.Fatalf("expected zero failure, got %s %v", got.String(), err)
			}
			if before.String() != `{"a":[0],"scalar":1}` {
				t.Fatal("input modified")
			}
		})
	}
	for _, p := range []*Patch{nil, {}} {
		if _, err := p.Apply(before); !errors.Is(err, ErrInvalid) {
			t.Fatal("uninitialized patch accepted")
		}
	}
}

func TestPropertyChangeLimits(t *testing.T) {
	operation := `{"op":"add","path":"/x","value":0}`
	at := `[` + strings.TrimSuffix(strings.Repeat(operation+",", MaxOperations), ",") + `]`
	if _, err := Parse([]byte(at)); err != nil {
		t.Fatal(err)
	}
	if _, err := Parse([]byte(strings.TrimSuffix(at, "]") + "," + operation + "]")); !errors.Is(err, ErrLimit) {
		t.Fatal("operation limit", err)
	}
	raw := []byte("[" + operation + "]")
	padded := append(bytes.Clone(raw), bytes.Repeat([]byte(" "), MaxInputBytes-len(raw))...)
	if _, err := Parse(padded); err != nil {
		t.Fatal("exact input limit", err)
	}
	if _, err := Parse(append(padded, ' ')); !errors.Is(err, ErrLimit) {
		t.Fatal("input limit", err)
	}
	for _, tc := range []struct {
		name, path string
		want       error
	}{
		{"pointer-at", "/" + strings.Repeat("x", MaxPointerBytes-1), nil}, {"pointer-over", "/" + strings.Repeat("x", MaxPointerBytes), ErrLimit},
		{"segments-at", strings.Repeat("/x", MaxDepth), nil}, {"segments-over", strings.Repeat("/x", MaxDepth+1), ErrLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, _ := json.Marshal(tc.path)
			_, err := Parse([]byte(`[{"op":"remove","path":` + string(v) + `}]`))
			if !errors.Is(err, tc.want) {
				t.Fatal(err)
			}
		})
	}
	for _, depth := range []int{MaxDepth, MaxDepth + 1} {
		value := strings.Repeat("[", depth) + "0" + strings.Repeat("]", depth)
		_, err := Parse([]byte(`[{"op":"add","path":"/x","value":` + value + `}]`))
		if depth == MaxDepth && err != nil {
			t.Fatal("value depth at", err)
		}
		if depth > MaxDepth && !errors.Is(err, ErrLimit) {
			t.Fatal("value depth over", err)
		}
	}
	exact := properties(t, `{"x":"`+strings.Repeat("a", MaxDocumentBytes-len(`{"x":""}`))+`"}`)
	noop := admitted(t, `[{"op":"add","path":"/temporary","value":null},{"op":"remove","path":"/temporary"}]`)
	if _, err := noop.Apply(exact); !errors.Is(err, ErrLimit) {
		t.Fatal("intermediate bytes not bounded", err)
	}
	if _, err := admitted(t, `[{"op":"remove","path":"/x"}]`).Apply(exact); err != nil {
		t.Fatal("exact current bytes", err)
	}
	tooLarge := properties(t, `{"x":"`+strings.Repeat("a", MaxDocumentBytes)+`"}`)
	if _, err := admitted(t, `[{"op":"remove","path":"/x"}]`).Apply(tooLarge); !errors.Is(err, ErrLimit) {
		t.Fatal("entry bytes", err)
	}
	// A separately admissible value can exceed the limit when nested in the root.
	value := strings.Repeat("[", MaxDepth) + "0" + strings.Repeat("]", MaxDepth)
	deep := admitted(t, `[{"op":"add","path":"/x","value":`+value+`}]`)
	if _, err := deep.Apply(properties(t, `{}`)); !errors.Is(err, ErrLimit) {
		t.Fatal("working depth", err)
	}
	current := properties(t, `{"x":`+strings.Repeat("[", MaxDepth)+"0"+strings.Repeat("]", MaxDepth)+`}`)
	if _, err := admitted(t, `[{"op":"remove","path":"/x"}]`).Apply(current); !errors.Is(err, ErrLimit) {
		t.Fatal("entry depth", err)
	}
}

func TestPropertyChangeCaptureAndReuse(t *testing.T) {
	raw := []byte(`[{"op":"add","path":"/a","value":{"x":[1]}},{"op":"add","path":"/a/x/-","value":2}]`)
	p, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	clear(raw)
	before := properties(t, `{}`)
	want := properties(t, `{"a":{"x":[1,2]}}`)
	var workers sync.WaitGroup
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			got, err := p.Apply(before)
			if err != nil || !got.Equal(want) {
				t.Errorf("reused patch: %v", err)
			}
		}()
	}
	workers.Wait()
}

func TestPropertyChangeCachedSize(t *testing.T) {
	before := properties(t, `{"a":[0,1,2],"x":{" <😀>":true},"drop":null}`)
	p := admitted(t, `[{"op":"add","path":"/a/1","value":{"x":null}},{"op":"remove","path":"/a/0"},{"op":"replace","path":"/a/1","value":[]},{"op":"remove","path":"/drop"},{"op":"add","path":"/new","value":null},{"op":"remove","path":"/x/ <😀>"}]`)
	n, err := parseNode(before.Bytes(), 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range p.operations {
		n, err = edit(n, op.path, op)
		if err != nil {
			t.Fatal(err)
		}
		actual, err := graphops.CanonicalizeJSON(n.encode())
		if err != nil {
			t.Fatal(err)
		}
		if n.size != len(actual) {
			t.Fatalf("cached size %d actual %d", n.size, len(actual))
		}
	}
}

func TestPropertyChangeEvaluationBudget(t *testing.T) {
	operation := `{"op":"replace","path":"/x","value":0}`
	raw := "[" + strings.TrimSuffix(strings.Repeat(operation+",", MaxOperations), ",") + "]"
	if len(raw) >= MaxInputBytes {
		t.Fatal("fixture exceeds independent input bound")
	}
	patch := admitted(t, raw)
	const framing = len(`{"padding":"","x":0}`)
	for _, tc := range []struct {
		name          string
		documentBytes int
		wantError     bool
	}{
		{"exact", 65535, false}, {"one-byte-larger-document", 65536, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := properties(t, `{"padding":"`+strings.Repeat("a", tc.documentBytes-framing)+`","x":0}`)
			if len(before.Bytes()) != tc.documentBytes {
				t.Fatal("incorrect fixture size")
			}
			cost := MaxOperations * (tc.documentBytes + 1)
			if !tc.wantError && cost != MaxEvaluationBytes {
				t.Fatal("fixture is not exactly at evaluation limit")
			}
			if tc.wantError && cost <= MaxEvaluationBytes {
				t.Fatal("fixture does not cross evaluation limit")
			}
			saved := before.String()
			got, err := patch.Apply(before)
			if tc.wantError {
				if !errors.Is(err, ErrLimit) || !strings.Contains(err.Error(), "cumulative evaluation bytes") || !got.IsEmpty() {
					t.Fatalf("expected zero budget refusal, got error=%v", err)
				}
			} else if err != nil || !got.Equal(before) {
				t.Fatalf("exact budget rejected or changed result: %v", err)
			}
			if before.String() != saved {
				t.Fatal("evaluation modified predecessor")
			}
		})
	}
}

func TestPropertyChangeResultGrowthBoundary(t *testing.T) {
	padding := strings.Repeat("a", MaxDocumentBytes/2)
	before := properties(t, `{"a":"`+padding+`","b":""}`)
	beforeBytes := len(before.Bytes())
	for _, tc := range []struct {
		name      string
		extra     int
		wantError bool
	}{
		{"exact", 0, false}, {"one-byte-over", 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value := strings.Repeat("b", MaxDocumentBytes-beforeBytes+tc.extra)
			encoded, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			raw := `[{"op":"replace","path":"/b","value":` + string(encoded) + `}]`
			if len(raw) >= MaxInputBytes {
				t.Fatal("fixture exceeds independent input bound")
			}
			got, err := admitted(t, raw).Apply(before)
			if tc.wantError {
				if !errors.Is(err, ErrLimit) || !strings.Contains(err.Error(), "working properties bytes") || !got.IsEmpty() {
					t.Fatalf("expected zero document-bound refusal: %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if len(got.Bytes()) != MaxDocumentBytes {
					t.Fatal("result is not exactly at document limit")
				}
				want := properties(t, `{"a":"`+padding+`","b":"`+value+`"}`)
				if !got.Equal(want) {
					t.Fatal("grown properties differ")
				}
			}
			if len(before.Bytes()) != beforeBytes || !before.Equal(properties(t, `{"a":"`+padding+`","b":""}`)) {
				t.Fatal("predecessor changed")
			}
		})
	}
}
