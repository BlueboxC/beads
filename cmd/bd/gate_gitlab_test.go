package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func TestGitLabGateOutcomes(t *testing.T) {
	cases := []struct {
		kind, body                 string
		resolved, escalated, fails bool
	}{
		{"gl:pipeline", `{"id":42,"status":"success"}`, true, false, false},
		{"gl:pipeline", `{"id":42,"status":"failed"}`, false, true, false},
		{"gl:pipeline", `{"id":42,"status":"canceled"}`, false, true, false},
		{"gl:pipeline", `{"id":42,"status":"skipped"}`, false, false, false},
		{"gl:pipeline", `{"id":42,"status":"manual"}`, false, false, false},
		{"gl:pipeline", `{"id":42,"status":"running"}`, false, false, false},
		{"gl:pipeline", `{"id":42,"status":"pending"}`, false, false, false},
		{"gl:pipeline", `{"id":42,"status":"new-provider-state"}`, false, false, false},
		{"gl:pipeline", `{"id":42}`, false, false, true},
		{"gl:pipeline", `{"id":41,"status":"success"}`, false, false, true},
		{"gl:pipeline", `null`, false, false, true},
		{"gl:pipeline", `not-json`, false, false, true},
		{"gl:mr", `{"id":901,"iid":42,"state":"merged"}`, true, false, false},
		{"gl:mr", `{"iid":42,"state":"closed"}`, false, true, false},
		{"gl:mr", `{"iid":42,"state":"opened"}`, false, false, false},
		{"gl:mr", `{"iid":42,"state":"locked"}`, false, false, false},
		{"gl:mr", `{"iid":42,"state":"new-provider-state"}`, false, false, false},
		{"gl:mr", `{"id":42,"state":"merged"}`, false, false, true},
		{"gl:mr", `{"iid":41,"state":"merged"}`, false, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.kind+tc.body, func(t *testing.T) {
			gate := &types.Issue{AwaitType: tc.kind, AwaitID: "42", Metadata: json.RawMessage(`{"repo":"group/sub/project"}`)}
			r, e, _, err := checkGitLabGateWithRunner(context.Background(), gate, func(_ context.Context, args ...string) ([]byte, error) {
				if !reflect.DeepEqual(args[:5], []string{"api", "--method", "GET", "--output", "json"}) {
					t.Fatalf("not an explicit GET: %v", args)
				}
				resource := "/pipelines/42"
				if tc.kind == "gl:mr" {
					resource = "/merge_requests/42"
				}
				if args[5] != "projects/group%2Fsub%2Fproject"+resource {
					t.Fatalf("wrong project/IID endpoint: %v", args)
				}
				return []byte(tc.body), nil
			})
			if r != tc.resolved || e != tc.escalated || (err != nil) != tc.fails {
				t.Fatalf("got %t/%t/%v", r, e, err)
			}
		})
	}
}

func TestGitLabGateRefusesBadSelectionAndTransport(t *testing.T) {
	for _, value := range []string{"0", "-1", "--help", "42/3", "9223372036854775808"} {
		gate := &types.Issue{AwaitType: "gl:mr", AwaitID: value}
		r, _, _, err := checkGitLabGateWithRunner(context.Background(), gate, func(context.Context, ...string) ([]byte, error) { t.Fatal("queried bad ID"); return nil, nil })
		if err == nil || r {
			t.Fatalf("admitted %q", value)
		}
	}
	for _, metadata := range []string{`{"repo":null}`, `{"repo":7}`, `{"repo":"g/../p"}`, `{"repo":"https://host/g/p"}`, `{"repo":"g/p?x=y"}`, `{"repo":"g//p"}`, `[]`} {
		if _, err := gitLabProjectFromIssue(&types.Issue{Metadata: json.RawMessage(metadata)}); err == nil {
			t.Fatalf("admitted %s", metadata)
		}
	}
	gate := &types.Issue{AwaitType: "gl:pipeline", AwaitID: "42"}
	for _, failure := range []error{context.Canceled, context.DeadlineExceeded, errors.New("auth or rate limit or not found")} {
		r, e, _, err := checkGitLabGateWithRunner(context.Background(), gate, func(context.Context, ...string) ([]byte, error) { return nil, failure })
		if r || e || !errors.Is(err, failure) {
			t.Fatalf("failure became outcome: %t %t %v", r, e, err)
		}
	}
}

func TestGitLabGateMetadataAndDispatch(t *testing.T) {
	metadata, err := repoMetadataForGate("gl:pipeline", &types.Issue{Metadata: json.RawMessage(`{"repo":"g/sub/p","unrelated":true}`)})
	if err != nil || string(metadata) != `{"repo":"g/sub/p"}` {
		t.Fatalf("inheritance: %s %v", metadata, err)
	}
	for _, kind := range []string{"gl:pipeline", "gl:mr"} {
		gate := &types.Issue{AwaitType: kind}
		if !shouldCheckGate(gate, "gl") || shouldCheckGate(gate, "gh") {
			t.Fatal("provider filter mismatch")
		}
		results := evaluateGates(context.Background(), []*types.Issue{gate}, time.Now(), nil, nil)
		if len(results) != 1 || results[0].resolved || results[0].err != nil {
			t.Fatalf("dispatch: %+v", results)
		}
	}
	if shouldCheckGate(&types.Issue{AwaitType: "gl:unknown"}, "gl") {
		t.Fatal("unknown provider type admitted")
	}
}

func TestGitLabPipelineDiscovery(t *testing.T) {
	gate := &types.Issue{AwaitType: "gl:pipeline", Metadata: json.RawMessage(`{"repo":"g/sub/p"}`)}
	var calls int
	run := func(_ context.Context, args ...string) ([]byte, error) {
		calls++
		if args[2] != "GET" {
			t.Fatal("not GET")
		}
		if calls == 1 {
			return []byte(`{"path_with_namespace":"g/sub/p"}`), nil
		}
		endpoint, err := url.Parse(args[5])
		if err != nil {
			t.Fatal(err)
		}
		if endpoint.RawQuery == "" || endpoint.Query().Get("ref") != "feature/a" || endpoint.Query().Get("sha") != "head-sha" || endpoint.Query().Get("per_page") != "100" {
			t.Fatalf("wrong selectors: %v", args)
		}
		return []byte(`[{"id":10,"ref":"feature/a","sha":"head-sha"},{"id":11,"ref":"feature/a","sha":"head-sha"},{"id":99,"ref":"other","sha":"head-sha"},{"id":100,"ref":"feature/a","sha":"old-sha"}]`), nil
	}
	id, err := discoverGitLabPipeline(context.Background(), gate, "feature/a", "head-sha", run)
	if err != nil || id != "11" || calls != 2 {
		t.Fatalf("match %s %v calls=%d", id, err, calls)
	}
	_, err = discoverGitLabPipeline(context.Background(), gate, "main", "head", func(_ context.Context, args ...string) ([]byte, error) {
		if strings.Contains(args[5], "pipelines") {
			t.Fatal("queried foreign project with local HEAD")
		}
		return []byte(`{"path_with_namespace":"another/project"}`), nil
	})
	if err == nil {
		t.Fatal("foreign project admitted")
	}
	for _, body := range []string{`[]`, `null`, `[{"id":42,"ref":"main","sha":"old"}]`, `{}`} {
		_, err = discoverGitLabPipeline(context.Background(), &types.Issue{}, "main", "head", func(context.Context, ...string) ([]byte, error) { return []byte(body), nil })
		if err == nil {
			t.Fatalf("bad match %s", body)
		}
	}
	_, err = discoverGitLabPipeline(context.Background(), &types.Issue{}, "HEAD", "head", func(context.Context, ...string) ([]byte, error) {
		t.Fatal("queried detached checkout")
		return nil, nil
	})
	if err == nil {
		t.Fatal("detached checkout admitted")
	}
}
