//go:build cgo

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/graphstore"
)

func graphPatchProcess(t *testing.T, bd, work, home string, input io.Reader, code string, timeout time.Duration, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	defer cancel()
	cmd := graphMemoryReadCommand(ctx, bd, work, home, args...)
	cmd.Stdin = input
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	err := cmd.Run()
	cleanupGraphTestServer(t, work, args)
	if ctx.Err() != nil {
		t.Fatalf("patch command timed out (possibly consumed forbidden input): %v args=%v stderr=%s", ctx.Err(), args, stderr.String())
	}
	if code == "" {
		if err != nil || stderr.Len() != 0 {
			t.Fatalf("patch command %v: %v stderr=%s", args, err, stderr.String())
		}
		return out.String()
	}
	var exit *exec.ExitError
	var diagnostic struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		Retryable *bool  `json:"retryable"`
	}
	wantExit := map[string]int{"invalid_selector": 2, "invalid_properties": 2, "gone": 3, "revision_conflict": 4, "capability_unavailable": 5, "permission_denied": 5}[code]
	if !errors.As(err, &exit) || wantExit == 0 || exit.ExitCode() != wantExit || out.Len() != 0 || json.Unmarshal(stderr.Bytes(), &diagnostic) != nil || diagnostic.Code != code || diagnostic.Message == "" || diagnostic.Retryable == nil || *diagnostic.Retryable {
		t.Fatalf("patch refusal want=%s/%d err=%v stdout=%s stderr=%s", code, wantExit, err, out.String(), stderr.String())
	}
	return ""
}

func graphPatchEqual(t *testing.T, got, want any) {
	t.Helper()
	semantic := func(v any) any {
		raw, err := graphProjectCompleteRecords(v)
		if err != nil {
			t.Fatal(err)
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	if !reflect.DeepEqual(semantic(got), semantic(want)) {
		t.Fatalf("complete patch value differs: got=%+v want=%+v", got, want)
	}
}

func graphPatchMemoryTransition(t *testing.T, before, after graphstore.Record, properties graphstore.Properties, owned []json.RawMessage) {
	t.Helper()
	want := before
	want.Properties, want.Owned = properties, owned
	want.Revision, want.Attribution = after.Revision, after.Attribution
	if after.Revision == before.Revision || after.Revision == "" || after.Attribution.Actor != "patch-author" {
		t.Fatalf("Memory patch did not mint one attributed postimage: %+v", after)
	}
	graphPatchEqual(t, after, want)
}

func graphPatchReplaced(t *testing.T, got *graphstore.ReplacedMemory, before graphstore.Record, expected bool) {
	t.Helper()
	var want *graphstore.ReplacedMemory
	if expected {
		want = &graphstore.ReplacedMemory{ID: before.ID, Version: before.Revision, Attribution: before.Attribution}
	}
	graphPatchEqual(t, got, want)
}

func graphPatchOwned(t *testing.T, before graphstore.Record, replacement graphstore.LinkRecord) []json.RawMessage {
	t.Helper()
	owned := append([]json.RawMessage(nil), before.Owned...)
	found := 0
	for i, raw := range owned {
		var link graphstore.LinkRecord
		if err := json.Unmarshal(raw, &link); err != nil {
			t.Fatal(err)
		}
		if link.ID == replacement.ID {
			encoded, err := json.Marshal(replacement)
			if err != nil {
				t.Fatal(err)
			}
			owned[i], found = encoded, found+1
		}
	}
	if found != 1 {
		t.Fatalf("expected one owned Link to replace, got %d", found)
	}
	return owned
}

// This journey authors state only through independent installed CLI processes.
// Server process races prove independent callers, not forced SQL overlap. The
// storage suite owns real transactional barriers and embedded concurrency proof.
func TestGraphPreviewPropertiesPatchWorkflow(t *testing.T) {
	bd := buildBDUnderTest(t)
	for _, engine := range []string{"embedded", "server"} {
		t.Run(engine, func(t *testing.T) {
			work, home := t.TempDir(), t.TempDir()
			const scope = "https://example.invalid/patch-workflow/"
			call := func(args ...string) string {
				t.Helper()
				return graphPatchProcess(t, bd, work, home, nil, "", 90*time.Second, append(args, "--json")...)
			}
			refuse := func(code string, args ...string) {
				t.Helper()
				graphPatchProcess(t, bd, work, home, nil, code, 90*time.Second, append(args, "--json")...)
			}
			file := func(name, data string) string {
				t.Helper()
				path := filepath.Join(work, name)
				if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
					t.Fatal(err)
				}
				return path
			}
			patch := func(op, path string, value any) string {
				t.Helper()
				raw, err := json.Marshal([]map[string]any{{"op": op, "path": path, "value": value}})
				if err != nil {
					t.Fatal(err)
				}
				return string(raw)
			}
			// The pipe remains open without a producer. Returning the typed
			// refusal proves @- was not consumed; EOF cannot mask an early read.
			early := func(code, selector string, flags ...string) {
				t.Helper()
				for _, input := range []string{"@" + filepath.Join(work, "missing-patch"), "@-"} {
					read, write, err := os.Pipe()
					if err != nil {
						t.Fatal(err)
					}
					args := append([]string{"update", selector, "--patch", input}, flags...)
					func() {
						defer func() {
							if err := read.Close(); err != nil {
								t.Error(err)
							}
							if err := write.Close(); err != nil {
								t.Error(err)
							}
						}()
						graphPatchProcess(t, bd, work, home, read, code, 15*time.Second, append(args, "--json")...)
					}()
				}
			}
			early("capability_unavailable", "beads/plan")
			args := []string{"init", "--graph-mode", "link", "--scope-url", scope, "--skip-hooks", "--skip-agents", "--non-interactive"}
			if engine == "server" {
				port := os.Getenv("BEADS_GRAPH_TEST_SERVER_PORT")
				if port == "" {
					t.Skip("set BEADS_GRAPH_TEST_SERVER_PORT for ordinary shared-server patch qualification")
				}
				args = append(args, "--server", "--external", "--server-host", "127.0.0.1", "--server-port", port, "--server-user", "root")
			}
			call(args...)
			status := graphMixedResult[struct {
				Capabilities map[string]bool `json:"capabilities"`
				Limits       map[string]int  `json:"limits"`
			}](t, call("status", "--graph"))
			if !status.Capabilities["memoryPropertiesPatch"] || !status.Capabilities["linkPropertiesPatch"] || status.Capabilities["memory"] || status.Capabilities["historyExact"] || status.Capabilities["requestStatus"] {
				t.Fatalf("patch capability overclaim: %+v", status)
			}
			for _, prefix := range []string{"memoryPatch", "linkPatch"} {
				for suffix, n := range map[string]int{"InputBytes": 1 << 20, "DocumentBytes": 1 << 20, "Operations": 256, "PointerBytes": 4096, "PointerSegments": 64, "Depth": 64, "EvaluationBytes": 16 << 20} {
					if status.Limits[prefix+suffix] != n {
						t.Fatalf("wrong patch limit %s%s: %+v", prefix, suffix, status.Limits)
					}
				}
			}
			memory := graphMixedResult[graphstore.Record](t, call("remember", "Original body", "--id", "beads/plan", "--title", "Original title"))
			survivor := graphMixedResult[graphstore.Record](t, call("remember", "Untouched", "--id", "beads/context", "--title", "Context"))
			issue := graphMixedResult[graphstore.IssueRecord](t, call("create", "Work", "--id", "beads/work"))
			target := graphMixedResult[graphstore.IssueRecord](t, call("create", "Target", "--id", "beads/target"))
			dependency := graphMixedResult[graphstore.DependencyResult](t, call("dep", "add", issue.ID, target.ID))
			issueCurrent, targetCurrent := call("show", issue.ID), call("show", target.ID)
			showMemory := func() graphstore.Record {
				t.Helper()
				return graphMixedResult[graphstore.Record](t, call("show", memory.ID))
			}
			saved := map[string]any{}
			retain := func(id, version string, value any) {
				t.Helper()
				saved[id+"@"+version] = value
				graphPatchEqual(t, graphMixedResult[any](t, call("show", id, "--version", version)), value)
			}
			retain(memory.ID, memory.Revision, memory)
			add := func(id, dest string, properties string) graphstore.LinkRecord {
				t.Helper()
				result := graphMixedResult[graphstore.LinkMutationResult](t, call("link", memory.ID, dest, "--id", id, "--resource-type", scope+"types/preview-related-v2", "--properties", properties, "--if-source-revision", memory.Revision))
				memory = showMemory()
				retain(memory.ID, memory.Revision, memory)
				retain(result.Link.ID, result.Link.Revision, result.Link)
				return result.Link
			}
			link := add("links/owned", issue.ID, `{}`)
			twin := add("links/twin", issue.ID, `{"note":"untouched twin"}`)
			self := add("links/self", memory.ID, `{"note":"self"}`)
			unowned := graphMixedResult[graphstore.LinkMutationResult](t, call("link", issue.ID, target.ID, "--id", "links/unowned", "--resource-type", scope+"types/preview-related-v2")).Link
			retain(unowned.ID, unowned.Revision, unowned)
			unchanged := func() {
				t.Helper()
				if call("show", issue.ID) != issueCurrent || call("show", target.ID) != targetCurrent {
					t.Fatal("patch changed unrelated native Issue")
				}
				graphPatchEqual(t, graphMixedResult[graphstore.Record](t, call("show", survivor.ID)), survivor)
				for _, v := range []graphstore.LinkRecord{twin, self} {
					graphPatchEqual(t, graphMixedResult[graphstore.LinkRecord](t, call("show", v.ID)), v)
				}
			}
			applyMemory := func(input string, stdin io.Reader, want graphstore.Properties, unconditional, changed bool) graphstore.MemoryMutationResult {
				t.Helper()
				before := memory
				args := []string{"update", memory.ID, "--patch", input, "--actor", "patch-author", "--json"}
				if !unconditional {
					args = append(args, "--if-revision", before.Revision)
				}
				result := graphMixedResult[graphstore.MemoryMutationResult](t, graphPatchProcess(t, bd, work, home, stdin, "", 90*time.Second, args...))
				if result.Changed != changed {
					t.Fatalf("Memory changed=%t want=%t", result.Changed, changed)
				}
				if changed {
					graphPatchMemoryTransition(t, before, result.Memory, want, before.Owned)
				} else {
					graphPatchEqual(t, result.Memory, before)
				}
				graphPatchReplaced(t, result.Replaced, before, unconditional && changed)
				memory = result.Memory
				graphPatchEqual(t, showMemory(), memory)
				retain(before.ID, before.Revision, before)
				retain(memory.ID, memory.Revision, memory)
				unchanged()
				return result
			}
			applyLink := func(input string, stdin io.Reader, want map[string]any, unconditional, sourceUnconditional, changed bool) {
				t.Helper()
				before, source := link, memory
				args := []string{"update", link.ID, "--patch", input, "--actor", "patch-author", "--json"}
				if !unconditional {
					args = append(args, "--if-revision", link.Revision)
				}
				if sourceUnconditional {
					args = append(args, "--unconditional-source")
				} else {
					args = append(args, "--if-source-revision", memory.Revision)
				}
				result := graphMixedResult[graphstore.LinkMutationResult](t, graphPatchProcess(t, bd, work, home, stdin, "", 90*time.Second, args...))
				if result.Changed != changed {
					t.Fatal("Link changed flag differs")
				}
				memory = showMemory()
				graphPatchEqual(t, result.Source, memory)
				if changed {
					expected := before
					expected.Properties = want
					expected.Revision, expected.Attribution = result.Link.Revision, result.Link.Attribution
					if result.Link.Revision == before.Revision || result.Link.Attribution.Actor != "patch-author" {
						t.Fatal("Link patch did not mint attributed postimage")
					}
					graphPatchEqual(t, result.Link, expected)
					graphPatchMemoryTransition(t, source, memory, source.Properties, graphPatchOwned(t, source, result.Link))
				} else {
					graphPatchEqual(t, result.Link, before)
					graphPatchEqual(t, memory, source)
				}
				graphPatchReplaced(t, result.ReplacedSource, source, sourceUnconditional && changed)
				link = result.Link
				graphPatchEqual(t, graphMixedResult[graphstore.LinkRecord](t, call("show", link.ID)), link)
				retain(before.ID, before.Revision, before)
				retain(link.ID, link.Revision, link)
				retain(source.ID, source.Revision, source)
				retain(memory.ID, memory.Revision, memory)
				unchanged()
			}
			initial := memory
			want := graphstore.Properties{Title: "Patch title 雪", Body: "  patchtoken body\r\n😀  "}
			applyMemory(`[{"op":"replace","path":"/title","value":"Patch title 雪"},{"op":"replace","path":"/body","value":"  patchtoken body\r\n😀  "}]`, nil, want, false, true)
			graphMemoryReadRaw(t, bd, work, home, want.Body, "", "recall", memory.ID)
			discovery := graphMixedResult[graphMemoryDiscoveryResult](t, graphPatchProcess(t, bd, work, home, nil, "", 90*time.Second, "memories", "patchtoken", "--details", "--format", "records-json"))
			if len(discovery.Items) != 1 || discovery.Items[0].Version != memory.Revision || !discovery.Complete {
				t.Fatalf("patched discovery: %+v", discovery)
			}
			graphMemoryReadComparison(t, call("compare", memory.ID, "--from", initial.Revision, "--to", memory.Revision), graphstore.VersionComparison{Resource: graphstore.VersionComparisonResource{ID: memory.ID, Type: memory.Type}, From: graphstore.VersionComparisonEndpoint{Version: initial.Revision, Attribution: initial.Attribution}, To: graphstore.VersionComparisonEndpoint{Version: memory.Revision, Attribution: memory.Attribution}, Compared: []string{"properties", "metadata", "owned"}, Unsupported: []string{"inception", "derivation"}, Changes: []graphstore.VersionChange{{Area: "properties", Member: "body", From: graphMemoryReadValue(t, initial.Properties.Body), To: graphMemoryReadValue(t, want.Body)}, {Area: "properties", Member: "title", From: graphMemoryReadValue(t, initial.Properties.Title), To: graphMemoryReadValue(t, want.Title)}}})
			noopBefore := graphMemoryReadSnapshot(t, work)
			applyMemory(patch("replace", "/title", want.Title), nil, want, false, false)
			applyMemory(`[{"op":"replace","path":"/title","value":"temporary"},{"op":"replace","path":"/title","value":"Patch title 雪"}]`, nil, want, true, false)
			refuse("revision_conflict", "update", memory.ID, "--patch", patch("replace", "/title", want.Title), "--if-revision", initial.Revision)
			graphPatchEqual(t, graphMemoryReadSnapshot(t, work), noopBefore)
			want.Body = ""
			applyMemory("@"+file("empty-body.json", `[{"op":"remove","path":"/body"},{"op":"add","path":"/body","value":""}]`), nil, want, false, true)
			graphMemoryReadRaw(t, bd, work, home, "", "", "recall", memory.ID, "--quiet")
			want = graphstore.Properties{Title: "From stdin", Body: "stdin 雪\r\n  "}
			applyMemory("@-", strings.NewReader(patch("replace", "", want)), want, true, true)
			beforeIntervening := memory
			applyLink(patch("add", "/note", ""), nil, map[string]any{"note": ""}, false, false, true)
			refuse("revision_conflict", "update", memory.ID, "--patch", patch("replace", "/title", want.Title), "--if-revision", beforeIntervening.Revision)
			want.Title = "Actual predecessor"
			applyMemory(patch("replace", "/title", want.Title), nil, want, true, true)
			applyLink("@"+file("link-patch.json", `[{"op":"replace","path":"/note","value":"  雪\r\n😀  "}]`), nil, map[string]any{"note": "  雪\r\n😀  "}, true, false, true)
			noopBefore = graphMemoryReadSnapshot(t, work)
			applyLink(patch("replace", "/note", link.Properties["note"]), nil, link.Properties, true, true, false)
			applyLink(`[{"op":"add","path":"/temporary","value":[1,null]},{"op":"remove","path":"/temporary"}]`, nil, link.Properties, false, false, false)
			graphPatchEqual(t, graphMemoryReadSnapshot(t, work), noopBefore)
			applyLink("@-", strings.NewReader(`[{"op":"remove","path":"/note"}]`), map[string]any{}, false, true, true)
			applyLink(patch("replace", "", map[string]any{"note": "root"}), nil, map[string]any{"note": "root"}, true, true, true)
			beforeUnowned := unowned
			unownedResult := graphMixedResult[graphstore.LinkMutationResult](t, call("update", unowned.ID, "--patch", patch("add", "/note", "unowned"), "--if-revision", unowned.Revision, "--actor", "patch-author"))
			if !unownedResult.Changed || unownedResult.ReplacedSource != nil {
				t.Fatal("unowned Link patch disclosure/changed")
			}
			graphPatchEqual(t, unownedResult.Source, graphMixedResult[any](t, issueCurrent))
			unowned = unownedResult.Link
			expectedUnowned := beforeUnowned
			expectedUnowned.Properties = map[string]any{"note": "unowned"}
			expectedUnowned.Revision, expectedUnowned.Attribution = unowned.Revision, unowned.Attribution
			if unowned.Revision == beforeUnowned.Revision || unowned.Attribution.Actor != "patch-author" {
				t.Fatal("unowned patch version")
			}
			graphPatchEqual(t, unowned, expectedUnowned)
			retain(unowned.ID, unowned.Revision, unowned)
			unchanged()

			beforeRefusal := graphMemoryReadSnapshot(t, work)
			for _, bad := range []string{`[{"op":"replace","path":"/title","value":"must rollback"},{"op":"remove","path":"/body"}]`, `[{"op":"replace","path":"/body","value":"must rollback"},{"op":"replace","path":"/missing","value":1}]`, `[{"op":"copy","from":"/title","path":"/body"}]`, `[{"op":"remove","path":"/bad~2"}]`, `[{"op":"replace","op":"add","path":"/body","value":"x"}]`, `[{"op":"add","path":"/temporary","value":9007199254740993}]`, `[{"op":"replace","path":"/body","value":"\ud800"}]`, `[]`} {
				refuse("invalid_properties", "update", memory.ID, "--patch", bad)
			}
			for _, selector := range []string{memory.ID, link.ID} {
				refuse("invalid_properties", "update", selector, "--patch", "@"+file("invalid-utf8.json", string([]byte{255})))
				refuse("invalid_properties", "update", selector, "--patch", "@"+file("oversize.json", strings.Repeat(" ", (1<<20)+1)))
			}
			for _, bad := range []string{
				`[{"op":"replace","path":"/note","value":"must rollback"},{"op":"remove","path":"/missing"}]`,
				`[{"op":"replace","path":"/note","value":"must rollback"},{"op":"replace","path":"/note","value":null}]`,
			} {
				refuse("invalid_properties", "update", link.ID, "--patch", bad, "--unconditional-source")
			}
			tooMany := "[" + strings.TrimSuffix(strings.Repeat(`{"op":"replace","path":"/title","value":"x"},`, 257), ",") + "]"
			refuse("invalid_properties", "update", memory.ID, "--patch", tooMany)
			refuse("invalid_properties", "update", memory.ID, "--patch", `[{"op":"remove","path":"/`+strings.Repeat("x", 4096)+`"}]`)
			// The large intermediate value is valid input; repeated operations
			// exceed the evaluation-work proxy before final removal can hide it.
			expensive := `[{"op":"add","path":"/work","value":[` + strings.TrimSuffix(strings.Repeat("0,", 50000), ",") + `]},` + strings.Repeat(`{"op":"replace","path":"/work/0","value":0},`, 220) + `{"op":"remove","path":"/work"}]`
			refuse("capability_unavailable", "update", memory.ID, "--patch", "@"+file("evaluation-bound.json", expensive))
			defaultSourceNoop := graphMixedResult[graphstore.LinkMutationResult](t, call("update", link.ID, "--patch", patch("replace", "/note", "root")))
			if defaultSourceNoop.Changed || defaultSourceNoop.ReplacedSource != nil {
				t.Fatal("default source acceptance must preserve a semantic no-op")
			}
			graphPatchEqual(t, defaultSourceNoop.Link, link)
			graphPatchEqual(t, defaultSourceNoop.Source, memory)
			refuse("revision_conflict", "update", link.ID, "--patch", patch("replace", "/note", "root"), "--if-revision", beforeIntervening.Revision, "--if-source-revision", memory.Revision)
			refuse("revision_conflict", "update", link.ID, "--patch", patch("replace", "/note", "root"), "--if-revision", link.Revision, "--if-source-revision", beforeIntervening.Revision)
			refuse("revision_conflict", "update", unowned.ID, "--patch", patch("replace", "/note", "unowned"), "--if-source-revision", initial.Revision)
			refuse("invalid_properties", "update", dependency.Link.ID, "--patch", patch("add", "/note", "unsupported"))
			refuse("invalid_properties", "update", issue.ID, "--patch", patch("add", "/status", "closed"))
			early("invalid_selector", memory.ID, "--if-revision", "")
			early("invalid_selector", memory.ID, "--if-revision", string([]byte{255}))
			early("invalid_selector", "beads/")
			early("capability_unavailable", memory.ID, "--properties", "{}")
			early("invalid_selector", link.ID, "--if-source-revision", memory.Revision, "--unconditional-source")
			early("permission_denied", memory.ID, "--readonly")
			writeFile(t, filepath.Join(work, "mayor", "town.json"), []byte("{}\n"))
			freeze := filepath.Join(work, "MIGRATION-FREEZE")
			writeFile(t, freeze, []byte("patch\t2026-09-28T00:00:00Z\tpolicy\n"))
			early("permission_denied", link.ID, "--unconditional-source")
			if err := os.Remove(freeze); err != nil {
				t.Fatal(err)
			}
			graphPatchEqual(t, graphMemoryReadSnapshot(t, work), beforeRefusal)
			unchanged()

			// Human and quiet successful writes still use the same full writer.
			for _, quiet := range []bool{false, true} {
				before := memory
				body := "human body"
				args := []string{"update", memory.ID, "--patch", patch("replace", "/body", body), "--actor", "patch-author"}
				if quiet {
					body = "quiet body"
					args[3] = patch("replace", "/body", body)
					args = append(args, "--quiet")
				}
				out := graphPatchProcess(t, bd, work, home, nil, "", 90*time.Second, args...)
				if quiet {
					if out != "" {
						t.Fatal("quiet patch leaked output")
					}
				} else if !strings.Contains(out, "Replaced Memory "+before.ID+" version "+before.Revision) {
					t.Fatalf("human predecessor absent: %q", out)
				}
				memory = showMemory()
				want = before.Properties
				want.Body = body
				graphPatchMemoryTransition(t, before, memory, want, before.Owned)
				retain(memory.ID, memory.Revision, memory)
			}
			noopBefore = graphMemoryReadSnapshot(t, work)
			for _, quiet := range []bool{false, true} {
				args := []string{"update", memory.ID, "--patch", patch("replace", "/body", memory.Properties.Body)}
				if quiet {
					args = append(args, "--quiet")
				}
				out := graphPatchProcess(t, bd, work, home, nil, "", 90*time.Second, args...)
				if (quiet && out != "") || (!quiet && (!strings.Contains(out, "Unchanged "+memory.ID) || strings.Contains(out, "Replaced"))) {
					t.Fatalf("human/quiet no-op output: %q", out)
				}
			}
			graphPatchEqual(t, graphMemoryReadSnapshot(t, work), noopBefore)
			// The accepted write precedes output failure. Observe it; do not replay
			// or claim rollback merely because the caller received no receipt.
			beforePipe := memory
			read, write, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := read.Close(); err != nil {
				_ = write.Close()
				t.Fatal(err)
			}
			func() {
				defer func() {
					if err := write.Close(); err != nil {
						t.Error(err)
					}
				}()
				ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
				defer cancel()
				cmd := graphMemoryReadCommand(ctx, bd, work, home, "update", memory.ID, "--patch", patch("replace", "/body", "committed before broken pipe"), "--if-revision", memory.Revision, "--actor", "patch-author", "--json")
				var stderr bytes.Buffer
				cmd.Stdout, cmd.Stderr = write, &stderr
				err := cmd.Run()
				var exit *exec.ExitError
				if ctx.Err() != nil || !errors.As(err, &exit) || (!strings.Contains(stderr.String(), "broken pipe") && exit.ProcessState.String() != "signal: broken pipe") {
					t.Fatalf("patch output failure not observed: %v stderr=%s", err, stderr.String())
				}
			}()
			memory = showMemory()
			want = beforePipe.Properties
			want.Body = "committed before broken pipe"
			graphPatchMemoryTransition(t, beforePipe, memory, want, beforePipe.Owned)
			retain(memory.ID, memory.Revision, memory)

			if engine == "server" {
				for _, race := range []string{"memory-patch-vs-link-write", "link-patch-vs-memory-write"} {
					before, oldLink := memory, link
					commands := [2][]string{{"update", memory.ID, "--patch", patch("replace", "/body", race), "--if-revision", memory.Revision, "--actor", "patch-author", "--json"}, {"update", link.ID, "--properties", `{"note":"race"}`, "--if-revision", link.Revision, "--if-source-revision", memory.Revision, "--actor", "patch-author", "--json"}}
					if race == "link-patch-vs-memory-write" {
						commands[1][2] = "--patch"
						commands[1][3] = patch("replace", "/note", "second race")
					}
					results := graphDeleteRacePair(t, bd, work, home, commands)
					if (results[0].code == "") == (results[1].code == "") {
						t.Fatalf("guarded patch race needs exactly one winner: %+v", results)
					}
					memory = showMemory()
					link = graphMixedResult[graphstore.LinkRecord](t, call("show", link.ID))
					if results[0].code == "" {
						if results[1].code != "revision_conflict" {
							t.Fatal(results[1].code)
						}
						result := graphMixedResult[graphstore.MemoryMutationResult](t, results[0].stdout)
						if !result.Changed || result.Replaced != nil {
							t.Fatal("guarded race receipt")
						}
						graphPatchEqual(t, result.Memory, memory)
						want := before.Properties
						want.Body = race
						graphPatchMemoryTransition(t, before, memory, want, before.Owned)
						graphPatchEqual(t, link, oldLink)
					} else {
						if results[0].code != "revision_conflict" {
							t.Fatal(results[0].code)
						}
						result := graphMixedResult[graphstore.LinkMutationResult](t, results[1].stdout)
						if !result.Changed || result.ReplacedSource != nil {
							t.Fatal("guarded Link race receipt")
						}
						graphPatchEqual(t, result.Link, link)
						graphPatchEqual(t, result.Source, memory)
						if link.Revision == oldLink.Revision || link.Attribution.Actor != "patch-author" {
							t.Fatal("Link race did not retain an attributed changed version")
						}
						graphPatchMemoryTransition(t, before, memory, before.Properties, graphPatchOwned(t, before, link))
						expected := oldLink
						expected.Properties = map[string]any{"note": "race"}
						if race == "link-patch-vs-memory-write" {
							expected.Properties["note"] = "second race"
						}
						expected.Revision, expected.Attribution = link.Revision, link.Attribution
						graphPatchEqual(t, link, expected)
					}
					retain(before.ID, before.Revision, before)
					retain(oldLink.ID, oldLink.Revision, oldLink)
					retain(memory.ID, memory.Revision, memory)
					retain(link.ID, link.Revision, link)
					unchanged()
				}
				before := memory
				commands := [2][]string{}
				for i, title := range []string{"Unconditional A", "Unconditional B"} {
					commands[i] = []string{"update", memory.ID, "--patch", patch("replace", "/title", title), "--actor", "patch-author", "--json"}
				}
				results := graphDeleteRacePair(t, bd, work, home, commands)
				accepted := []graphstore.MemoryMutationResult{}
				for _, r := range results {
					if r.code == "" {
						v := graphMixedResult[graphstore.MemoryMutationResult](t, r.stdout)
						if !v.Changed || v.Replaced == nil {
							t.Fatal("unconditional race missing predecessor")
						}
						accepted = append(accepted, v)
					} else if r.code != "revision_conflict" {
						t.Fatal(r.code)
					}
				}
				if len(accepted) == 0 {
					t.Fatal("unconditional race had no winner")
				}
				if len(accepted) == 2 && accepted[1].Replaced.Version == before.Revision {
					accepted[0], accepted[1] = accepted[1], accepted[0]
				}
				predecessor := before
				for _, v := range accepted {
					graphPatchReplaced(t, v.Replaced, predecessor, true)
					want := predecessor.Properties
					want.Title = v.Memory.Properties.Title
					if want.Title != "Unconditional A" && want.Title != "Unconditional B" {
						t.Fatal("unexpected race title")
					}
					graphPatchMemoryTransition(t, predecessor, v.Memory, want, predecessor.Owned)
					retain(v.Memory.ID, v.Memory.Revision, v.Memory)
					predecessor = v.Memory
				}
				memory = showMemory()
				graphPatchEqual(t, memory, predecessor)
				want = memory.Properties
				want.Title = "After unconditional race"
				applyMemory(patch("replace", "/title", want.Title), nil, want, true, true)
			}
			unchanged()
			for _, owned := range []graphstore.LinkRecord{link, twin, self} {
				call("unlink", owned.ID, "--if-revision", owned.Revision, "--if-source-revision", memory.Revision)
				memory = showMemory()
				retain(memory.ID, memory.Revision, memory)
			}
			final := memory
			call("delete", memory.ID, "--force", "--if-revision", memory.Revision)
			refuse("gone", "update", memory.ID, "--patch", patch("replace", "/title", "no resurrection"))
			refuse("gone", "update", link.ID, "--patch", patch("add", "/note", "no resurrection"), "--unconditional-source")
			graphPolicyCLI(t, bd, work, home, nil, "identity_reserved", "remember", "no reuse", "--id", "beads/plan", "--title", "Reserved", "--json")
			for key, value := range saved {
				index := strings.LastIndex(key, "@")
				graphPatchEqual(t, graphMixedResult[any](t, call("show", key[:index], "--version", key[index+1:])), value)
			}
			graphMemoryReadRaw(t, bd, work, home, final.Properties.Body, "", "recall", final.ID, "--version", final.Revision)
			if call("show", issue.ID) != issueCurrent || call("show", target.ID) != targetCurrent {
				t.Fatal("deletion changed Issue survivors")
			}
			graphPatchEqual(t, graphMixedResult[graphstore.Record](t, call("show", survivor.ID)), survivor)
			graphPatchEqual(t, graphMixedResult[graphstore.LinkRecord](t, call("show", unowned.ID)), unowned)
			graphPatchEqual(t, graphMixedResult[graphstore.LinkRecord](t, call("show", dependency.Link.ID)), dependency.Link)
		})
	}
}
