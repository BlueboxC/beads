package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func readHookDiagnostic(t *testing.T, input codexHookInput, event string) map[string]any {
	t.Helper()
	marker := codexHookRefreshMarkerPath(input)
	path := strings.TrimSuffix(marker, ".refresh") + "." + event + ".json"
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 || len(data) > 2048 {
		t.Fatalf("diagnostic must be private and bounded: %v, bytes=%d", err, len(data))
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestCodexHookDiagnosticsDescribeAttemptWithoutContext(t *testing.T) {
	for _, tc := range []struct {
		name, primeResult, outputResult string
		primeError                      error
		context                         string
		writerFails                     bool
	}{
		{"delivered", "loaded", "context_written", nil, "PRIVATE SOLUTION\n", false},
		{"read_failed", "failed", "warning_written", errors.New("PRIVATE DATABASE ERROR"), "", false},
		{"timeout", "timed_out", "warning_written", context.DeadlineExceeded, "", false},
		{"typed_read_timeout", "timed_out", "warning_written", newPrimeMemoryFailure("memory_list", context.DeadlineExceeded), "", false},
		{"permission", "failed", "warning_written", newPrimeMemoryFailure("store_open", os.ErrPermission), "", false},
		{"empty", "empty", "none", nil, " \n", false},
		{"output_failed", "loaded", "failed", nil, "PRIVATE SOLUTION\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			codexHookMarkerDirOverride = t.TempDir()
			t.Cleanup(func() { codexHookMarkerDirOverride = "" })
			cwd, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			input := codexHookInput{SessionID: "PRIVATE SESSION", CWD: cwd}
			stubCodexHookPrime(t, func(bool) (string, error) {
				if readHookDiagnostic(t, input, codexHookSessionStart)["phase"] != "running" {
					t.Fatal("attempt was not recorded before prime")
				}
				return tc.context, tc.primeError
			})
			payload := `{"session_id":"PRIVATE SESSION","cwd":` + strconv.Quote(cwd) + `,"source":"compact","trigger":"auto","turn_id":"01a10cbb-8077-7a73-bdfb-a7c8785b1967","transcript_path":"PRIVATE TRANSCRIPT","model":"PRIVATE MODEL"}`
			var out bytes.Buffer
			var writer io.Writer = &out
			if tc.writerFails {
				writer = codexHookErrorWriter{}
			}
			err = runCodexHook(context.Background(), codexHookSessionStart, strings.NewReader(payload), writer)
			if (err != nil) != tc.writerFails {
				t.Fatalf("hook error=%v", err)
			}
			got := readHookDiagnostic(t, input, codexHookSessionStart)
			if got["phase"] != "completed" || got["prime_result"] != tc.primeResult || got["output_result"] != tc.outputResult || got["cwd_matches_input"] != true || got["source"] != "compact" || got["trigger"] != "auto" || got["turn_id"] != "01a10cbb-8077-7a73-bdfb-a7c8785b1967" || got["completed_at"] == nil {
				t.Fatalf("attempt outcome=%#v", got)
			}
			if tc.primeResult == "loaded" {
				sum := sha256.Sum256([]byte(tc.context))
				if got["context_sha256"] != hex.EncodeToString(sum[:]) || got["context_bytes"] != float64(len(tc.context)) {
					t.Fatalf("context metadata=%#v", got)
				}
			}
			if tc.primeError != nil {
				if got["prime_failure_stage"] != primeMemoryFailureStage(tc.primeError, "unknown") || got["prime_failure_reason"] != primeMemoryFailureReason(tc.primeError) {
					t.Fatalf("failure classification=%#v", got)
				}
			} else if got["prime_failure_stage"] != nil || got["prime_failure_reason"] != nil {
				t.Fatalf("healthy/no-op attempt retained a failure: %#v", got)
			}
			data, _ := json.Marshal(got)
			for _, secret := range []string{"PRIVATE", cwd} {
				if bytes.Contains(data, []byte(secret)) {
					t.Fatalf("diagnostic retained private text %q", secret)
				}
			}
			if got["refresh_pending"] != (tc.primeError != nil || tc.writerFails) {
				t.Fatalf("pending refresh=%#v", got)
			}
		})
	}
}

func TestCodexHookDiagnosticsKeepOnlyLatestPerEvent(t *testing.T) {
	codexHookMarkerDirOverride = t.TempDir()
	t.Cleanup(func() { codexHookMarkerDirOverride = "" })
	stubCodexHookPrime(t, func(bool) (string, error) { return "KEPT SOLUTION\n", nil })
	input := codexHookInput{SessionID: "s1", CWD: "/other"}
	payload := `{"session_id":"s1","cwd":"/other","source":"PRIVATE SOURCE","trigger":"PRIVATE TRIGGER","turn_id":"PRIVATE TURN"}`
	for _, event := range []string{codexHookPreCompact, codexHookPostCompact, codexHookSessionStart, codexHookUserPromptSubmit, codexHookSessionStart} {
		var out bytes.Buffer
		if err := runCodexHook(context.Background(), event, strings.NewReader(payload), &out); err != nil {
			t.Fatal(err)
		}
		got := readHookDiagnostic(t, input, event)
		if got["phase"] != "completed" || got["event"] != event || got["cwd_matches_input"] != false {
			t.Fatalf("attempt=%#v", got)
		}
		for _, key := range []string{"source", "trigger", "turn_id"} {
			if _, ok := got[key]; ok {
				t.Fatalf("unknown identity retained: %#v", got)
			}
		}
		if event == codexHookPostCompact && got["refresh_pending"] != true {
			t.Fatal("post compact lost marker")
		}
		if event == codexHookUserPromptSubmit && (got["prime_result"] != "not_requested" || got["output_result"] != "none" || out.Len() != 0) {
			t.Fatal("diagnostics changed no-op refresh")
		}
	}
	files, err := os.ReadDir(codexHookMarkerDirOverride)
	if err != nil || len(files) != 4 {
		t.Fatalf("not bounded per event: %v, files=%d", err, len(files))
	}
}

func TestCodexHookDiagnosticsFailureDoesNotPreventDelivery(t *testing.T) {
	codexHookMarkerDirOverride = filepath.Join(t.TempDir(), "not-a-directory")
	t.Cleanup(func() { codexHookMarkerDirOverride = "" })
	if err := os.WriteFile(codexHookMarkerDirOverride, []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	stubCodexHookPrime(t, func(bool) (string, error) { return "RECOVERED SOLUTION\n", nil })
	var out bytes.Buffer
	if err := runCodexHook(context.Background(), codexHookSessionStart, strings.NewReader(`{"session_id":"s1","cwd":"/repo"}`), &out); err != nil {
		t.Fatal(err)
	}
	var got codexHookResponse
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || got.HookSpecificOutput.AdditionalContext != "RECOVERED SOLUTION\n" {
		t.Fatalf("cache failure affected context: %v %s", err, out.String())
	}
}

func TestCodexHookDiagnosticsBuildLabelExcludesPrivateText(t *testing.T) {
	oldBuild := Build
	codexHookMarkerDirOverride = t.TempDir()
	t.Cleanup(func() { Build = oldBuild; codexHookMarkerDirOverride = "" })
	input := codexHookInput{SessionID: "s1", CWD: "/repo"}
	for _, tc := range []struct{ build, want string }{{"1bda5cc9d", "1bda5cc9d"}, {"PRIVATE BRANCH OR PATH", ""}, {strings.Repeat("a", 100), ""}, {"dev", ""}} {
		Build = tc.build
		d := beginCodexHookDiagnostic(codexHookPostCompact, input)
		d.finish(nil)
		got := readHookDiagnostic(t, input, codexHookPostCompact)
		if tc.want == "" {
			if got["binary_build"] != nil {
				t.Fatal("non-commit build label retained")
			}
		} else if got["binary_build"] != tc.want {
			t.Fatal("commit build label lost")
		}
	}
}
