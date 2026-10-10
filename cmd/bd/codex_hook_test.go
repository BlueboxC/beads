package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func stubCodexHookPrime(t *testing.T, fn func(memoriesOnly bool) (string, error)) {
	t.Helper()
	orig := codexHookExecPrime
	codexHookExecPrime = func(_ context.Context, _ string, memoriesOnly bool) (string, error) {
		return fn(memoriesOnly)
	}
	t.Cleanup(func() { codexHookExecPrime = orig })
}

func TestCodexHookSessionStartInjectsPrimeContext(t *testing.T) {
	stubCodexHookPrime(t, func(memoriesOnly bool) (string, error) {
		if memoriesOnly {
			t.Fatal("SessionStart should request full prime output")
		}
		return "BEADS PRIME\nbd ready --json\n", nil
	})

	var out bytes.Buffer
	input := `{"session_id":"s1","cwd":"/repo","hook_event_name":"SessionStart","source":"startup"}`
	if err := runCodexHook(context.Background(), codexHookSessionStart, strings.NewReader(input), &out); err != nil {
		t.Fatalf("runCodexHook: %v", err)
	}

	var got codexHookResponse
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("parse output: %v\n%s", err, out.String())
	}
	if got.HookSpecificOutput.HookEventName != codexHookSessionStart {
		t.Fatalf("hook event = %q", got.HookSpecificOutput.HookEventName)
	}
	if !strings.Contains(got.HookSpecificOutput.AdditionalContext, "bd ready --json") {
		t.Fatalf("expected prime output in additionalContext: %#v", got)
	}
}

func TestCodexHookPreCompactWarnsWhenMemoriesUnavailable(t *testing.T) {
	stubCodexHookPrime(t, func(memoriesOnly bool) (string, error) {
		if !memoriesOnly {
			t.Fatal("PreCompact should request memories-only prime output")
		}
		return "", errors.New("workspace unavailable")
	})

	var out bytes.Buffer
	input := `{"session_id":"s1","cwd":"/repo","hook_event_name":"PreCompact","trigger":"manual"}`
	if err := runCodexHook(context.Background(), codexHookPreCompact, strings.NewReader(input), &out); err != nil {
		t.Fatalf("runCodexHook: %v", err)
	}

	var got codexHookResponse
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("parse output: %v\n%s", err, out.String())
	}
	if !strings.Contains(got.SystemMessage, "Beads context check failed") {
		t.Fatalf("expected warning systemMessage, got %#v", got)
	}
}

func TestCodexHookPostCompactMarksAndUserPromptRefreshesOnce(t *testing.T) {
	dir := t.TempDir()
	codexHookMarkerDirOverride = dir
	t.Cleanup(func() { codexHookMarkerDirOverride = "" })

	calls := 0
	stubCodexHookPrime(t, func(memoriesOnly bool) (string, error) {
		calls++
		if memoriesOnly {
			t.Fatal("UserPromptSubmit refresh should request full prime output")
		}
		return "REFRESHED BEADS CONTEXT\n", nil
	})

	input := codexHookInput{SessionID: "s1", CWD: filepath.Join("repo", "sub"), HookEventName: codexHookPostCompact}
	postJSON, _ := json.Marshal(input)
	if err := runCodexHook(context.Background(), codexHookPostCompact, bytes.NewReader(postJSON), ioDiscard{}); err != nil {
		t.Fatalf("PostCompact hook: %v", err)
	}
	marker := codexHookRefreshMarkerPath(input)
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("expected refresh marker: %v", err)
	}

	input.HookEventName = codexHookUserPromptSubmit
	promptJSON, _ := json.Marshal(input)
	var out bytes.Buffer
	if err := runCodexHook(context.Background(), codexHookUserPromptSubmit, bytes.NewReader(promptJSON), &out); err != nil {
		t.Fatalf("UserPromptSubmit hook: %v", err)
	}
	if calls != 1 {
		t.Fatalf("prime calls = %d, want 1", calls)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("expected refresh marker removed, stat err=%v", err)
	}
	if !strings.Contains(out.String(), "REFRESHED BEADS CONTEXT") {
		t.Fatalf("expected refreshed context, got %s", out.String())
	}

	out.Reset()
	if err := runCodexHook(context.Background(), codexHookUserPromptSubmit, bytes.NewReader(promptJSON), &out); err != nil {
		t.Fatalf("second UserPromptSubmit hook: %v", err)
	}
	if calls != 1 {
		t.Fatalf("refresh should run once, prime calls = %d", calls)
	}
	if out.Len() != 0 {
		t.Fatalf("expected no second refresh output, got %s", out.String())
	}
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }

func TestCodexHookSessionStartSatisfiesPendingRefresh(t *testing.T) {
	for _, trigger := range []string{"manual", "auto"} {
		t.Run(trigger, func(t *testing.T) {
			codexHookMarkerDirOverride = t.TempDir()
			t.Cleanup(func() { codexHookMarkerDirOverride = "" })
			calls := 0
			stubCodexHookPrime(t, func(memoriesOnly bool) (string, error) {
				calls++
				return "EXISTING MODULE AND SOLUTION\n", nil
			})
			input := codexHookInput{SessionID: "s1", CWD: "/repo", Trigger: trigger}
			data, _ := json.Marshal(input)
			if err := runCodexHook(context.Background(), codexHookPostCompact, bytes.NewReader(data), io.Discard); err != nil {
				t.Fatal(err)
			}
			var start bytes.Buffer
			if err := runCodexHook(context.Background(), codexHookSessionStart, bytes.NewReader(data), &start); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(start.String(), "EXISTING MODULE AND SOLUTION") {
				t.Fatalf("SessionStart lost context: %s", start.String())
			}
			var prompt bytes.Buffer
			if err := runCodexHook(context.Background(), codexHookUserPromptSubmit, bytes.NewReader(data), &prompt); err != nil {
				t.Fatal(err)
			}
			if prompt.Len() != 0 || calls != 1 {
				t.Fatalf("context was injected twice: prime calls=%d, prompt=%s", calls, prompt.String())
			}
		})
	}
}

func TestCodexHookFailedProjectionPreservesPendingRefresh(t *testing.T) {
	for _, event := range []string{codexHookSessionStart, codexHookUserPromptSubmit} {
		for _, failure := range []string{"prime", "empty", "output"} {
			t.Run(event+"/"+failure, func(t *testing.T) {
				codexHookMarkerDirOverride = t.TempDir()
				t.Cleanup(func() { codexHookMarkerDirOverride = "" })
				first := true
				stubCodexHookPrime(t, func(memoriesOnly bool) (string, error) {
					if first && failure == "prime" {
						return "", errors.New("store temporarily unavailable")
					}
					if first && failure == "empty" {
						return " \n", nil
					}
					return "RECOVERED CONTEXT\n", nil
				})
				input := codexHookInput{SessionID: "s1", CWD: "/repo"}
				data, _ := json.Marshal(input)
				if err := runCodexHook(context.Background(), codexHookPostCompact, bytes.NewReader(data), io.Discard); err != nil {
					t.Fatal(err)
				}
				var failed bytes.Buffer
				var writer io.Writer = &failed
				if failure == "output" {
					writer = codexHookErrorWriter{}
				}
				err := runCodexHook(context.Background(), event, bytes.NewReader(data), writer)
				if failure == "output" && err == nil {
					t.Fatal("output error was ignored")
				}
				if failure != "output" && err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(codexHookRefreshMarkerPath(input)); err != nil {
					t.Fatalf("failed projection discarded the pending refresh: %v", err)
				}
				first = false
				var recovered bytes.Buffer
				if err := runCodexHook(context.Background(), codexHookUserPromptSubmit, bytes.NewReader(data), &recovered); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(recovered.String(), "RECOVERED CONTEXT") {
					t.Fatalf("retry lost context: %s", recovered.String())
				}
				recovered.Reset()
				if err := runCodexHook(context.Background(), codexHookUserPromptSubmit, bytes.NewReader(data), &recovered); err != nil {
					t.Fatal(err)
				}
				if recovered.Len() != 0 {
					t.Fatalf("successful retry injected again: %s", recovered.String())
				}
			})
		}
	}
}

func TestCodexHookSessionStartKeepsOtherRefreshMarkers(t *testing.T) {
	codexHookMarkerDirOverride = t.TempDir()
	t.Cleanup(func() { codexHookMarkerDirOverride = "" })
	stubCodexHookPrime(t, func(memoriesOnly bool) (string, error) { return "CONTEXT\n", nil })
	pending := codexHookInput{SessionID: "s1", CWD: "/repo"}
	data, _ := json.Marshal(pending)
	if err := runCodexHook(context.Background(), codexHookPostCompact, bytes.NewReader(data), io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, other := range []codexHookInput{{SessionID: "s2", CWD: "/repo"}, {SessionID: "s1", CWD: "/other"}} {
		otherData, _ := json.Marshal(other)
		if err := runCodexHook(context.Background(), codexHookSessionStart, bytes.NewReader(otherData), io.Discard); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(codexHookRefreshMarkerPath(pending)); err != nil {
			t.Fatalf("another session/workspace consumed this refresh: %v", err)
		}
	}
}

type codexHookErrorWriter struct{}

func (codexHookErrorWriter) Write([]byte) (int, error) { return 0, errors.New("output unavailable") }

func TestCodexHookUnavailableStartupQueuesNextPrompt(t *testing.T) {
	codexHookMarkerDirOverride = t.TempDir()
	t.Cleanup(func() { codexHookMarkerDirOverride = "" })
	calls := 0
	stubCodexHookPrime(t, func(bool) (string, error) {
		calls++
		if calls == 1 {
			return "", errors.New("memory read unavailable")
		}
		return "RECOVERED SOLVED MODULE\n", nil
	})
	input := codexHookInput{SessionID: "startup", CWD: "/repo"}
	data, _ := json.Marshal(input)
	var out bytes.Buffer
	if err := runCodexHook(context.Background(), codexHookSessionStart, bytes.NewReader(data), &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(codexHookRefreshMarkerPath(input)); err != nil {
		t.Fatalf("failed startup lost refresh: %v", err)
	}
	if !strings.Contains(out.String(), "refresh retained") {
		t.Fatal(out.String())
	}
	out.Reset()
	if err := runCodexHook(context.Background(), codexHookUserPromptSubmit, bytes.NewReader(data), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "RECOVERED SOLVED MODULE") {
		t.Fatal(out.String())
	}
	out.Reset()
	if err := runCodexHook(context.Background(), codexHookUserPromptSubmit, bytes.NewReader(data), &out); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 || calls != 2 {
		t.Fatal("recovered startup injected twice")
	}
}
