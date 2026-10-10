package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const claudeHookMeasuredReminder = "Before you finish: if this session produced anything a later session in this project will need, save it now with `bd remember \"<fact>\"`. If there is nothing to save, just finish."

type claudeStopFixture struct {
	t          *testing.T
	transcript string
	workspace  string
}

func newClaudeStopFixture(t *testing.T) *claudeStopFixture {
	t.Helper()
	dir := t.TempDir()
	orig := claudeHookMarkerDirOverride
	claudeHookMarkerDirOverride = filepath.Join(dir, "markers")
	t.Cleanup(func() { claudeHookMarkerDirOverride = orig })
	workspace := filepath.Join(dir, "repo")
	if err := os.MkdirAll(filepath.Join(workspace, ".beads"), 0o700); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".beads", "metadata.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("create workspace metadata: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, "subdir"), 0o700); err != nil {
		t.Fatalf("create workspace subdir: %v", err)
	}
	transcript := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(transcript, nil, 0o600); err != nil {
		t.Fatalf("create transcript: %v", err)
	}
	return &claudeStopFixture{t: t, transcript: transcript, workspace: workspace}
}

func (f *claudeStopFixture) markerPath() string {
	return agentHookMarkerPath(claudeHookMarkerBaseDir(), "s1", f.transcript)
}

func (f *claudeStopFixture) append(lines ...string) {
	f.t.Helper()
	file, err := os.OpenFile(f.transcript, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		f.t.Fatalf("open transcript: %v", err)
	}
	defer file.Close()
	for _, line := range lines {
		if _, err := file.WriteString(line + "\n"); err != nil {
			f.t.Fatalf("append transcript: %v", err)
		}
	}
}

func (f *claudeStopFixture) stop(active bool) (string, error) {
	f.t.Helper()
	return f.stopIn(f.workspace, active)
}

func (f *claudeStopFixture) stopIn(cwd string, active bool) (string, error) {
	f.t.Helper()
	input, err := json.Marshal(map[string]any{
		"session_id":       "s1",
		"transcript_path":  f.transcript,
		"cwd":              cwd,
		"hook_event_name":  "Stop",
		"stop_hook_active": active,
	})
	if err != nil {
		f.t.Fatalf("marshal input: %v", err)
	}
	var out bytes.Buffer
	err = runClaudeHook(context.Background(), "stop", bytes.NewReader(input), &out)
	return out.String(), err
}

func (f *claudeStopFixture) mustStop(active bool) string {
	f.t.Helper()
	out, err := f.stop(active)
	if err != nil {
		f.t.Fatalf("runClaudeHook: %v", err)
	}
	return out
}

func userLine(text string) string {
	return fmt.Sprintf(`{"type":"user","message":{"role":"user","content":%q}}`, text)
}

func assistantTextLine(text string) string {
	return fmt.Sprintf(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":%q}]}}`, text)
}

func assistantToolLine(name string) string {
	return fmt.Sprintf(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"working"},{"type":"tool_use","id":"t1","name":%q,"input":{}}]}}`, name)
}

func toolResultLine() string {
	return `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}}`
}

func assertBlocksWithReminder(t *testing.T, out string) {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("parse output: %v\n%s", err, out)
	}
	if got["decision"] != "block" {
		t.Fatalf("decision = %v, want block", got["decision"])
	}
	if got["reason"] != claudeHookMeasuredReminder {
		t.Fatalf("reason = %q, want the measured reminder", got["reason"])
	}
	if !strings.Contains(out, "<fact>") {
		t.Fatalf("reminder must reach Claude Code unescaped: %s", out)
	}
	if len(got) != 2 {
		t.Fatalf("unexpected output fields: %v", got)
	}
}

func assertAllowsStop(t *testing.T, out string) {
	t.Helper()
	if strings.TrimSpace(out) != "" {
		t.Fatalf("expected no output, got %q", out)
	}
}

func TestClaudeHookStopRemindsAfterToolUse(t *testing.T) {
	f := newClaudeStopFixture(t)
	f.append(userLine("fix the build"), assistantToolLine("Bash"), toolResultLine(), assistantTextLine("done"))
	assertBlocksWithReminder(t, f.mustStop(false))
}

func TestClaudeHookStopRemindsAtFirstStopWithoutToolUse(t *testing.T) {
	f := newClaudeStopFixture(t)
	f.append(userLine("what is beads?"), assistantTextLine("an issue tracker"))
	assertBlocksWithReminder(t, f.mustStop(false))
}

func TestClaudeHookStopSilentOnLaterStopWithoutToolUse(t *testing.T) {
	f := newClaudeStopFixture(t)
	f.append(userLine("what is beads?"), assistantTextLine("an issue tracker"))
	assertBlocksWithReminder(t, f.mustStop(false))
	f.append(userLine("Stop hook feedback"), assistantTextLine("nothing to save"))
	assertAllowsStop(t, f.mustStop(true))

	f.append(userLine("and dolt?"), assistantTextLine("a versioned database"))
	assertAllowsStop(t, f.mustStop(false))
}

func TestClaudeHookStopFirstStopWhileHookActiveUsesTheFirstReminder(t *testing.T) {
	f := newClaudeStopFixture(t)
	f.append(userLine("hi"), assistantTextLine("hello"))
	assertAllowsStop(t, f.mustStop(true))

	f.append(userLine("thanks"), assistantTextLine("welcome"))
	assertAllowsStop(t, f.mustStop(false))
}

func TestClaudeHookStopSilentWhenStopHookActive(t *testing.T) {
	f := newClaudeStopFixture(t)
	f.append(userLine("fix the build"), assistantToolLine("Edit"), toolResultLine())
	assertAllowsStop(t, f.mustStop(true))
}

func TestClaudeHookStopDoesNotCountTheReminderResponse(t *testing.T) {
	f := newClaudeStopFixture(t)
	f.append(userLine("fix the build"), assistantToolLine("Edit"), toolResultLine(), assistantTextLine("done"))
	assertBlocksWithReminder(t, f.mustStop(false))

	f.append(userLine("Stop hook feedback"), assistantToolLine("Bash"), toolResultLine(), assistantTextLine("saved"))
	assertAllowsStop(t, f.mustStop(true))

	f.append(userLine("thanks, what next?"), assistantTextLine("nothing"))
	assertAllowsStop(t, f.mustStop(false))
}

func TestClaudeHookStopRemindsAgainAfterNewWork(t *testing.T) {
	f := newClaudeStopFixture(t)
	f.append(userLine("fix the build"), assistantToolLine("Edit"), toolResultLine())
	assertBlocksWithReminder(t, f.mustStop(false))
	assertAllowsStop(t, f.mustStop(true))

	f.append(userLine("now the tests"), assistantToolLine("Bash"), toolResultLine(), assistantTextLine("green"))
	assertBlocksWithReminder(t, f.mustStop(false))
}

func TestClaudeHookStopQuietTurnDoesNotResetPendingWork(t *testing.T) {
	f := newClaudeStopFixture(t)
	f.append(userLine("hi"), assistantTextLine("hello"))
	assertBlocksWithReminder(t, f.mustStop(false))
	assertAllowsStop(t, f.mustStop(true))
	f.append(userLine("how are you?"), assistantTextLine("fine"))
	assertAllowsStop(t, f.mustStop(false))

	f.append(userLine("fix it"), assistantToolLine("Write"), toolResultLine())
	assertBlocksWithReminder(t, f.mustStop(false))
}

func TestClaudeHookStopIgnoresPartialTrailingLine(t *testing.T) {
	f := newClaudeStopFixture(t)
	f.append(userLine("hi"), assistantTextLine("hello"))
	file, err := os.OpenFile(f.transcript, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open transcript: %v", err)
	}
	partial := assistantToolLine("Bash")
	if _, err := file.WriteString(partial[:len(partial)/2]); err != nil {
		t.Fatalf("write partial: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	assertBlocksWithReminder(t, f.mustStop(false))

	f.append(partial[len(partial)/2:])
	assertBlocksWithReminder(t, f.mustStop(false))
}

func TestClaudeHookStopRescansAfterTranscriptShrinks(t *testing.T) {
	f := newClaudeStopFixture(t)
	f.append(userLine("a long quiet chat"), assistantTextLine(strings.Repeat("x", 4096)))
	assertBlocksWithReminder(t, f.mustStop(false))

	if err := os.WriteFile(f.transcript, nil, 0o600); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	f.append(userLine("fix it"), assistantToolLine("Edit"))
	assertBlocksWithReminder(t, f.mustStop(false))
}

func TestClaudeHookStopRescansWhenTranscriptReplacedByLargerFile(t *testing.T) {
	f := newClaudeStopFixture(t)
	f.append(userLine("hi"), assistantTextLine("hello"))
	assertBlocksWithReminder(t, f.mustStop(false))

	if err := os.WriteFile(f.transcript, nil, 0o600); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	f.append(userLine(strings.Repeat("y", 7)), assistantToolLine("Edit"), assistantTextLine(strings.Repeat("z", 4096)))
	assertBlocksWithReminder(t, f.mustStop(false))
}

func TestClaudeHookCommandSilencesUsageOnError(t *testing.T) {
	if !claudeHookCmd.SilenceUsage {
		t.Fatal("claude-hook errors reach Claude Code as hook output; a usage dump buries the error")
	}
}

func TestClaudeHookStopSessionsDoNotShareState(t *testing.T) {
	f := newClaudeStopFixture(t)
	f.append(userLine("fix it"), assistantToolLine("Edit"))
	assertBlocksWithReminder(t, f.mustStop(false))

	input := fmt.Sprintf(`{"session_id":"s2","transcript_path":%q,"cwd":%q,"hook_event_name":"Stop"}`, f.transcript, f.workspace)
	var out bytes.Buffer
	if err := runClaudeHook(context.Background(), "stop", strings.NewReader(input), &out); err != nil {
		t.Fatalf("runClaudeHook: %v", err)
	}
	assertBlocksWithReminder(t, out.String())
}

func TestClaudeHookStopErrors(t *testing.T) {
	t.Run("missing transcript path", func(t *testing.T) {
		newClaudeStopFixture(t)
		err := runClaudeHook(context.Background(), "stop", strings.NewReader(`{"session_id":"s1","hook_event_name":"Stop"}`), &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "transcript_path") {
			t.Fatalf("expected transcript_path error, got %v", err)
		}
	})
	t.Run("unreadable transcript", func(t *testing.T) {
		f := newClaudeStopFixture(t)
		f.transcript = filepath.Join(t.TempDir(), "missing.jsonl")
		if _, err := f.stop(false); err == nil {
			t.Fatal("expected error for a missing transcript")
		}
	})
	t.Run("malformed transcript line", func(t *testing.T) {
		f := newClaudeStopFixture(t)
		f.append("not json")
		if _, err := f.stop(false); err == nil {
			t.Fatal("expected error for a malformed transcript line")
		}
	})
	t.Run("unsupported event", func(t *testing.T) {
		newClaudeStopFixture(t)
		err := runClaudeHook(context.Background(), "SubagentStop", strings.NewReader(`{}`), &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "unsupported") {
			t.Fatalf("expected unsupported event error, got %v", err)
		}
	})
	t.Run("corrupt marker", func(t *testing.T) {
		f := newClaudeStopFixture(t)
		f.append(assistantToolLine("Edit"))
		marker := agentHookMarkerPath(claudeHookMarkerBaseDir(), "s1", f.transcript)
		if err := os.MkdirAll(filepath.Dir(marker), 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(marker, []byte("garbage"), 0o600); err != nil {
			t.Fatalf("write marker: %v", err)
		}
		if _, err := f.stop(false); err == nil {
			t.Fatal("expected error for a corrupt marker")
		}
	})
}

func TestClaudeHookStopSurvivesDirectoryChange(t *testing.T) {
	f := newClaudeStopFixture(t)
	f.append(userLine("fix it"), assistantToolLine("Edit"))
	assertBlocksWithReminder(t, f.mustStop(false))

	f.append(userLine("thanks"), assistantTextLine("welcome"))
	out, err := f.stopIn(filepath.Join(f.workspace, "subdir"), false)
	if err != nil {
		t.Fatalf("runClaudeHook: %v", err)
	}
	assertAllowsStop(t, out)
}

func TestClaudeHookCommandSkipsStoreInit(t *testing.T) {
	if !commandOptsOutOfStore(claudeHookCmd) {
		t.Fatal("claude-hook must not open the beads store on every Stop")
	}
	if !firstRunNoticeSuppressedCommands["claude-hook"] {
		t.Fatal("claude-hook must suppress the first-run metrics notice")
	}
}

func TestClaudeHookStopRecoversAfterMalformedLine(t *testing.T) {
	f := newClaudeStopFixture(t)
	f.append(userLine("hi"), "not json", assistantTextLine("hello"))
	if _, err := f.stop(false); err == nil {
		t.Fatal("expected the malformed line to be reported")
	}
	assertAllowsStop(t, f.mustStop(false))

	f.append(userLine("fix it"), assistantToolLine("Edit"))
	assertBlocksWithReminder(t, f.mustStop(false))
}

func TestClaudeHookStopRecoversAfterCorruptMarker(t *testing.T) {
	f := newClaudeStopFixture(t)
	f.append(userLine("hi"), assistantTextLine("hello"))
	marker := agentHookMarkerPath(claudeHookMarkerBaseDir(), "s1", f.transcript)
	if err := os.MkdirAll(filepath.Dir(marker), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(marker, []byte("garbage"), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	if _, err := f.stop(false); err == nil {
		t.Fatal("expected the corrupt marker to be reported")
	}
	assertAllowsStop(t, f.mustStop(false))

	f.append(userLine("fix it"), assistantToolLine("Edit"))
	assertBlocksWithReminder(t, f.mustStop(false))
}

func TestClaudeHookStopScansBlankAndCRLFLines(t *testing.T) {
	f := newClaudeStopFixture(t)
	f.append(userLine("hi"), "", assistantTextLine("hello")+"\r", assistantToolLine("Bash")+"\r", assistantToolLine("Edit"))
	assertBlocksWithReminder(t, f.mustStop(false))
}

func TestClaudeHookStopSilentOutsideBeadsWorkspace(t *testing.T) {
	f := newClaudeStopFixture(t)
	f.append(userLine("fix it"), assistantToolLine("Edit"))
	out, err := f.stopIn(t.TempDir(), false)
	if err != nil {
		t.Fatalf("runClaudeHook: %v", err)
	}
	assertAllowsStop(t, out)
	if _, err := os.Stat(f.markerPath()); !os.IsNotExist(err) {
		t.Fatalf("expected no marker outside a beads workspace, stat err = %v", err)
	}
}

func TestClaudeHookStopRescansWhenTranscriptRewrittenInPlace(t *testing.T) {
	f := newClaudeStopFixture(t)
	f.append(userLine("hi"), assistantTextLine("hello"))
	assertBlocksWithReminder(t, f.mustStop(false))
	assertAllowsStop(t, f.mustStop(true))

	info, err := os.Stat(f.transcript)
	if err != nil {
		t.Fatalf("stat transcript: %v", err)
	}
	oldSize := int(info.Size())
	tool := assistantToolLine("Edit") + "\n"
	pad := oldSize - len(tool) - 1
	if pad < len(userLine("")) {
		pad = len(userLine(""))
	}
	filler := userLine(strings.Repeat("p", pad-len(userLine(""))))
	rewritten := tool + filler + "\n"
	if len(rewritten) < oldSize || rewritten[oldSize-1] != '\n' {
		t.Fatalf("fixture must keep a newline at the old offset %d (len %d)", oldSize, len(rewritten))
	}
	if err := os.WriteFile(f.transcript, []byte(rewritten), 0o600); err != nil {
		t.Fatalf("rewrite transcript: %v", err)
	}
	assertBlocksWithReminder(t, f.mustStop(false))
}

func TestClaudeHookStopAcceptsLegacyOffsetOnlyMarker(t *testing.T) {
	f := newClaudeStopFixture(t)
	f.append(userLine("hi"), assistantTextLine("hello"))
	marker := f.markerPath()
	if err := os.MkdirAll(filepath.Dir(marker), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(marker, []byte("123\n"), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	if _, err := f.stop(false); err != nil {
		t.Fatalf("legacy offset-only marker must not be reported as corrupt: %v", err)
	}
}
