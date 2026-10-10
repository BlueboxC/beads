package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

const (
	codexHookSessionStart     = "SessionStart"
	codexHookPreCompact       = "PreCompact"
	codexHookPostCompact      = "PostCompact"
	codexHookUserPromptSubmit = "UserPromptSubmit"
)

var codexHookMarkerDirOverride string

var codexHookExecPrime = func(ctx context.Context, cwd string, memoriesOnly bool) (string, error) {
	if memoriesOnly {
		return runBdPrimeInDir(ctx, cwd, "--memories-only")
	}
	return runBdPrimeInDir(ctx, cwd)
}

type codexHookInput struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	CWD            string `json:"cwd"`
	HookEventName  string `json:"hook_event_name"`
	Model          string `json:"model"`
	Trigger        string `json:"trigger"`
	Source         string `json:"source"`
	TurnID         string `json:"turn_id"`
}

type codexHookResponse struct {
	Continue           bool                    `json:"continue,omitempty"`
	SystemMessage      string                  `json:"systemMessage,omitempty"`
	HookSpecificOutput codexHookSpecificOutput `json:"hookSpecificOutput,omitempty"`
}

type codexHookSpecificOutput struct {
	HookEventName     string `json:"hookEventName,omitempty"`
	AdditionalContext string `json:"additionalContext,omitempty"`
}

var codexHookCmd = &cobra.Command{
	Use:    "codex-hook <event>",
	Hidden: true,
	Short:  "Run an internal Codex lifecycle hook",
	Args:   cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runCodexHook(cmd.Context(), args[0], os.Stdin, os.Stdout)
	},
}

func init() {
	rootCmd.AddCommand(codexHookCmd)
}

func runCodexHook(ctx context.Context, event string, stdin io.Reader, stdout io.Writer) (err error) {
	var input codexHookInput
	if err := json.NewDecoder(stdin).Decode(&input); err != nil && err != io.EOF {
		return err
	}
	if input.HookEventName != "" {
		event = input.HookEventName
	}

	switch event {
	case codexHookSessionStart, codexHookPreCompact, codexHookPostCompact, codexHookUserPromptSubmit:
	default:
		return fmt.Errorf("unsupported Codex hook event %q", event)
	}
	diagnostic := beginCodexHookDiagnostic(event, input)
	defer func() { diagnostic.finish(err) }()

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	switch event {
	case codexHookSessionStart:
		return codexHookInjectPrime(ctx, input, stdout, diagnostic)
	case codexHookPreCompact:
		return codexHookPreCompactCheck(ctx, stdout, diagnostic)
	case codexHookPostCompact:
		return codexHookMarkNeedsRefresh(input)
	case codexHookUserPromptSubmit:
		return codexHookMaybeRefresh(ctx, input, stdout, diagnostic)
	default:
		return fmt.Errorf("unsupported Codex hook event %q", event)
	}
}

func codexHookInjectPrime(ctx context.Context, input codexHookInput, stdout io.Writer, diagnostic *codexHookDiagnostic) error {
	out, err := diagnostic.prime(ctx, false)
	if err != nil {
		if markerErr := codexHookMarkNeedsRefresh(input); markerErr != nil {
			return markerErr
		}
		return diagnostic.output(writeCodexHookSystemMessage(stdout, fmt.Sprintf("Beads context unavailable at session start; refresh retained for the next prompt: %v", err)), "warning_written")
	}
	if strings.TrimSpace(out) == "" {
		return nil
	}
	// Queue before delivery: a failed cold stdout write must remain recoverable.
	_ = codexHookMarkNeedsRefresh(input) // Cache failure must not prevent a healthy delivery.
	if err := diagnostic.output(writeCodexHookAdditionalContext(stdout, codexHookSessionStart, out), "context_written"); err != nil {
		return err
	}
	// A successful start/resume already satisfies this workspace's pending refresh.
	_ = os.Remove(codexHookRefreshMarkerPath(input))
	return nil
}

func codexHookPreCompactCheck(ctx context.Context, stdout io.Writer, diagnostic *codexHookDiagnostic) error {
	if _, err := diagnostic.prime(ctx, true); err != nil {
		return diagnostic.output(writeCodexHookSystemMessage(stdout, fmt.Sprintf("Beads context check failed before compaction: %v", err)), "warning_written")
	}
	return nil
}

func codexHookMarkNeedsRefresh(input codexHookInput) error {
	return writeAgentHookMarker(codexHookRefreshMarkerPath(input))
}

func codexHookMaybeRefresh(ctx context.Context, input codexHookInput, stdout io.Writer, diagnostic *codexHookDiagnostic) error {
	path := codexHookRefreshMarkerPath(input)
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	out, err := diagnostic.prime(ctx, false)
	if err != nil {
		return diagnostic.output(writeCodexHookSystemMessage(stdout, fmt.Sprintf("Beads context refresh after compaction failed: %v", err)), "warning_written")
	}
	if strings.TrimSpace(out) == "" {
		return nil
	}
	if err := diagnostic.output(writeCodexHookAdditionalContext(stdout, codexHookUserPromptSubmit, out), "context_written"); err != nil {
		return err
	}
	_ = os.Remove(path)
	return nil
}

func codexHookRefreshMarkerPath(input codexHookInput) string {
	return agentHookMarkerPath(codexHookMarkerBaseDir(), input.SessionID, input.CWD)
}

func codexHookMarkerBaseDir() string {
	return agentHookMarkerBaseDir("codex-hooks", codexHookMarkerDirOverride)
}

func writeCodexHookAdditionalContext(stdout io.Writer, event, context string) error {
	return json.NewEncoder(stdout).Encode(codexHookResponse{
		Continue: true,
		HookSpecificOutput: codexHookSpecificOutput{
			HookEventName:     event,
			AdditionalContext: context,
		},
	})
}

func writeCodexHookSystemMessage(stdout io.Writer, message string) error {
	return json.NewEncoder(stdout).Encode(codexHookResponse{
		Continue:      true,
		SystemMessage: message,
	})
}
