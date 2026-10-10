package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/beads"
)

const (
	claudeHookStop           = "Stop"
	claudeStopMemoryReminder = "Before you finish: if this session produced anything a later session in this project will need, save it now with `bd remember \"<fact>\"`. If there is nothing to save, just finish."

	// claudeTranscriptFingerprintBytes bounds how much of the transcript's
	// head the marker fingerprints, so an in-place rewrite that keeps a
	// newline at the saved offset still forces a rescan.
	claudeTranscriptFingerprintBytes = 4096
)

var (
	claudeHookMarkerDirOverride   string
	errClaudeTranscriptUnreadable = errors.New("read Claude transcript")
)

type claudeHookInput struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	CWD            string `json:"cwd"`
	HookEventName  string `json:"hook_event_name"`
	StopHookActive bool   `json:"stop_hook_active"`
}

type claudeStopDecision struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

type claudeTranscriptEntry struct {
	Type    string `json:"type"`
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

var claudeHookCmd = &cobra.Command{
	Use:          "claude-hook <event>",
	Hidden:       true,
	Short:        "Run an internal Claude Code lifecycle hook",
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	Annotations:  map[string]string{skipStoreAnnotation: "1"},
	RunE: func(cmd *cobra.Command, args []string) error {
		if graphPreviewActive {
			if err := admitGraphPreviewClaudeStop(cmd, args); err != nil {
				return err
			}
		}
		return runClaudeHook(cmd.Context(), args[0], os.Stdin, os.Stdout)
	},
}

func init() {
	rootCmd.AddCommand(claudeHookCmd)
}

func runClaudeHook(_ context.Context, event string, stdin io.Reader, stdout io.Writer) error {
	var input claudeHookInput
	if err := json.NewDecoder(stdin).Decode(&input); err != nil && err != io.EOF {
		return err
	}
	if input.HookEventName != "" {
		event = input.HookEventName
	}
	if !strings.EqualFold(event, claudeHookStop) {
		return fmt.Errorf("unsupported Claude hook event %q", event)
	}
	return claudeHookStopReminder(input, stdout)
}

func claudeHookStopReminder(input claudeHookInput, stdout io.Writer) error {
	if input.TranscriptPath == "" {
		return errors.New("claude Stop hook input has no transcript_path")
	}
	if !claudeHookInBeadsWorkspace(input.CWD) {
		// Match bd prime: outside a beads workspace the hook is a silent no-op.
		return nil
	}
	markerPath := agentHookMarkerPath(claudeHookMarkerBaseDir(), input.SessionID, input.TranscriptPath)
	offset, fingerprint, seenBefore, markerErr := readClaudeStopMarker(markerPath)
	end, endFingerprint, usedTools, scanErr := claudeTranscriptToolUseSince(input.TranscriptPath, offset, fingerprint)
	if errors.Is(scanErr, errClaudeTranscriptUnreadable) {
		return errors.Join(markerErr, scanErr)
	}
	if err := writeClaudeStopMarker(markerPath, end, endFingerprint); err != nil {
		return errors.Join(markerErr, scanErr, err)
	}
	if err := errors.Join(markerErr, scanErr); err != nil {
		return err
	}
	if input.StopHookActive || (seenBefore && !usedTools) {
		return nil
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(claudeStopDecision{
		Decision: "block",
		Reason:   claudeStopMemoryReminder,
	})
}

// claudeHookInBeadsWorkspace reports whether the session's working
// directory (or the process cwd when Claude Code omits it) is inside a beads
// workspace.
func claudeHookInBeadsWorkspace(cwd string) bool {
	if cwd == "" {
		return beads.FindBeadsDir() != ""
	}
	return beads.FindBeadsDirFrom(cwd) != ""
}

// claudeTranscriptToolUseSince scans the transcript from offset for tool use.
// It rescans from the start when the saved offset no longer lines up with the
// file: the file shrank, the offset is not at a line boundary, or the head of
// the file no longer matches fingerprint (an empty fingerprint always
// rescans). It returns the offset just past the last complete line and the
// fingerprint of the head of the file up to that offset.
func claudeTranscriptToolUseSince(path string, offset int64, fingerprint string) (int64, string, bool, error) {
	file, err := os.Open(path) // #nosec G304 -- path is the transcript Claude Code names for this session
	if err != nil {
		return 0, "", false, fmt.Errorf("%w: %w", errClaudeTranscriptUnreadable, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return 0, "", false, fmt.Errorf("%w: %w", errClaudeTranscriptUnreadable, err)
	}
	if offset > info.Size() || fingerprint == "" {
		offset = 0
	}
	if offset > 0 {
		prev := make([]byte, 1)
		if _, err := file.ReadAt(prev, offset-1); err != nil {
			return 0, "", false, fmt.Errorf("%w: %w", errClaudeTranscriptUnreadable, err)
		}
		if prev[0] != '\n' {
			offset = 0
		}
	}
	if offset > 0 {
		current, err := claudeTranscriptFingerprint(file, offset)
		if err != nil {
			return 0, "", false, err
		}
		if current != fingerprint {
			offset = 0
		}
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return 0, "", false, fmt.Errorf("%w: %w", errClaudeTranscriptUnreadable, err)
	}
	reader := bufio.NewReader(file)
	end := offset
	usedTools := false
	var parseErrs []error
	for {
		line, err := reader.ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			endFingerprint, err := claudeTranscriptFingerprint(file, end)
			if err != nil {
				return 0, "", false, err
			}
			return end, endFingerprint, usedTools, errors.Join(parseErrs...)
		}
		if err != nil {
			return 0, "", false, fmt.Errorf("%w: %w", errClaudeTranscriptUnreadable, err)
		}
		lineOffset := end
		end += int64(len(line))
		hasTool, err := claudeTranscriptLineUsesTool(bytes.TrimSpace(line))
		if err != nil {
			parseErrs = append(parseErrs, fmt.Errorf("parse Claude transcript %s at byte %d: %w", path, lineOffset, err))
			continue
		}
		usedTools = usedTools || hasTool
	}
}

// claudeTranscriptFingerprint hashes the first min(offset,
// claudeTranscriptFingerprintBytes) bytes of the transcript.
func claudeTranscriptFingerprint(file *os.File, offset int64) (string, error) {
	n := min(offset, claudeTranscriptFingerprintBytes)
	head := make([]byte, n)
	if _, err := file.ReadAt(head, 0); err != nil {
		return "", fmt.Errorf("%w: %w", errClaudeTranscriptUnreadable, err)
	}
	sum := sha256.Sum256(head)
	return hex.EncodeToString(sum[:]), nil
}

func claudeTranscriptLineUsesTool(line []byte) (bool, error) {
	if len(line) == 0 {
		return false, nil
	}
	var entry claudeTranscriptEntry
	if err := json.Unmarshal(line, &entry); err != nil {
		return false, err
	}
	content := bytes.TrimSpace(entry.Message.Content)
	if entry.Type != "assistant" || len(content) == 0 || content[0] != '[' {
		return false, nil
	}
	var blocks []struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(content, &blocks); err != nil {
		return false, err
	}
	for _, block := range blocks {
		if block.Type == "tool_use" {
			return true, nil
		}
	}
	return false, nil
}

// readClaudeStopMarker reads "<offset> <fingerprint>". A legacy offset-only
// marker is accepted with an empty fingerprint, which forces a rescan.
func readClaudeStopMarker(path string) (int64, string, bool, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path is derived by agentHookMarkerPath under the bd cache dir
	if errors.Is(err, os.ErrNotExist) {
		return 0, "", false, nil
	}
	if err != nil {
		return 0, "", true, fmt.Errorf("read Claude Stop hook marker: %w", err)
	}
	content := strings.TrimSpace(string(data))
	corrupt := fmt.Errorf("corrupt Claude Stop hook marker %s: %q", path, content)
	fields := strings.Fields(content)
	if len(fields) == 0 || len(fields) > 2 {
		return 0, "", true, corrupt
	}
	offset, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil || offset < 0 {
		return 0, "", true, corrupt
	}
	if len(fields) == 1 {
		return offset, "", true, nil
	}
	fingerprint := fields[1]
	if decoded, err := hex.DecodeString(fingerprint); err != nil || len(decoded) != sha256.Size {
		return 0, "", true, corrupt
	}
	return offset, fingerprint, true, nil
}

func writeClaudeStopMarker(path string, offset int64, fingerprint string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("write Claude Stop hook marker: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("write Claude Stop hook marker: %w", err)
	}
	_, writeErr := tmp.WriteString(strconv.FormatInt(offset, 10) + " " + fingerprint + "\n")
	closeErr := tmp.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return errors.Join(fmt.Errorf("write Claude Stop hook marker: %w", err), os.Remove(tmp.Name()))
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return errors.Join(fmt.Errorf("write Claude Stop hook marker: %w", err), os.Remove(tmp.Name()))
	}
	return nil
}

func claudeHookMarkerBaseDir() string {
	return agentHookMarkerBaseDir("claude-hooks", claudeHookMarkerDirOverride)
}
