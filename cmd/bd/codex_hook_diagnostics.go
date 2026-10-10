package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Diagnostics describe local hook execution, not Codex's subsequent admission
// of stdout into model context. Keep only the latest attempt for each event.
type codexHookDiagnostic struct {
	Version            int        `json:"version"`
	Event              string     `json:"event"`
	TurnID             string     `json:"turn_id,omitempty"`
	Source             string     `json:"source,omitempty"`
	Trigger            string     `json:"trigger,omitempty"`
	CWDMatchesInput    *bool      `json:"cwd_matches_input,omitempty"`
	StartedAt          time.Time  `json:"started_at"`
	CompletedAt        *time.Time `json:"completed_at,omitempty"`
	DurationMS         int64      `json:"duration_ms"`
	Phase              string     `json:"phase"`
	PrimeResult        string     `json:"prime_result"`
	PrimeFailureStage  string     `json:"prime_failure_stage,omitempty"`
	PrimeFailureReason string     `json:"prime_failure_reason,omitempty"`
	BinaryBuild        string     `json:"binary_build,omitempty"`
	OutputResult       string     `json:"output_result"`
	ContextBytes       int        `json:"context_bytes,omitempty"`
	ContextSHA256      string     `json:"context_sha256,omitempty"`
	RefreshPending     bool       `json:"refresh_pending"`
	HookFailed         bool       `json:"hook_failed"`

	cwd    string
	path   string
	marker string
}

func beginCodexHookDiagnostic(event string, input codexHookInput) *codexHookDiagnostic {
	marker := codexHookRefreshMarkerPath(input)
	d := &codexHookDiagnostic{
		cwd: input.CWD, Version: 1, Event: event, StartedAt: time.Now().UTC(), Phase: "running",
		PrimeResult: "not_requested", OutputResult: "none", marker: marker,
		path: strings.TrimSuffix(marker, ".refresh") + "." + event + ".json",
	}
	if len(Build) >= 7 && len(Build) <= 40 && strings.Trim(Build, "0123456789abcdef") == "" {
		d.BinaryBuild = Build
	}
	if id, err := uuid.Parse(input.TurnID); err == nil {
		d.TurnID = id.String()
	}
	switch input.Source {
	case "startup", "resume", "clear", "compact":
		d.Source = input.Source
	}
	switch input.Trigger {
	case "manual", "auto":
		d.Trigger = input.Trigger
	}
	if cwd, err := os.Getwd(); err == nil && filepath.IsAbs(input.CWD) {
		matches := filepath.Clean(cwd) == filepath.Clean(input.CWD)
		d.CWDMatchesInput = &matches
	}
	_, err := os.Stat(marker)
	d.RefreshPending = err == nil
	d.save()
	return d
}

func (d *codexHookDiagnostic) prime(ctx context.Context, memoriesOnly bool) (string, error) {
	out, err := codexHookExecPrime(ctx, d.cwd, memoriesOnly)
	if err != nil {
		d.PrimeFailureStage = primeMemoryFailureStage(err, "unknown")
		d.PrimeFailureReason = primeMemoryFailureReason(err)
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded):
		d.PrimeResult = "timed_out"
	case errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled):
		d.PrimeResult = "canceled"
	case err != nil:
		d.PrimeResult = "failed"
	case strings.TrimSpace(out) == "":
		d.PrimeResult = "empty"
	default:
		d.PrimeResult = "loaded"
		if !memoriesOnly {
			d.ContextBytes = len(out)
			sum := sha256.Sum256([]byte(out))
			d.ContextSHA256 = hex.EncodeToString(sum[:])
		}
	}
	return out, err
}

func (d *codexHookDiagnostic) output(err error, success string) error {
	d.OutputResult = success
	if err != nil {
		d.OutputResult = "failed"
	}
	return err
}

func (d *codexHookDiagnostic) finish(err error) {
	now := time.Now().UTC()
	d.CompletedAt = &now
	d.DurationMS = now.Sub(d.StartedAt).Milliseconds()
	d.Phase = "completed"
	d.HookFailed = err != nil
	_, markerErr := os.Stat(d.marker)
	d.RefreshPending = markerErr == nil
	d.save()
	if (d.Event == codexHookSessionStart && d.Source == "compact") || d.HookFailed || d.PrimeResult == "failed" || d.PrimeResult == "timed_out" || d.PrimeResult == "canceled" {
		d.retainAttempt()
	}
}

// Keep sixteen compact/failure attempts per session/workspace, without text.
// Latest-event overwrites must not erase the evidence needed after a gap.
func (d *codexHookDiagnostic) retainAttempt() {
	dir := strings.TrimSuffix(d.marker, ".refresh") + ".attempts"
	if os.MkdirAll(dir, 0o700) != nil {
		return
	}
	name := d.StartedAt.Format("20060102T150405.000000000Z") + "-" + d.Event + ".json"
	copy := *d
	copy.path = filepath.Join(dir, name)
	copy.save()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	names := []string{}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names[:max(0, len(names)-16)] {
		_ = os.Remove(filepath.Join(dir, name))
	}
}

// Diagnostic cache failures are advisory and must never change hook delivery.
// Atomic replacement keeps concurrent readers from observing partial JSON.
func (d *codexHookDiagnostic) save() {
	data, err := json.Marshal(d)
	if err != nil || os.MkdirAll(filepath.Dir(d.path), 0o700) != nil {
		return
	}
	f, err := os.CreateTemp(filepath.Dir(d.path), ".diagnostic-*")
	if err != nil {
		return
	}
	defer func() { _ = os.Remove(f.Name()) }()
	_, writeErr := f.Write(append(data, '\n'))
	closeErr := f.Close()
	if writeErr == nil && closeErr == nil {
		_ = os.Rename(f.Name(), d.path)
	}
}
