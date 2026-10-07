package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"slices"
	"syscall"
)

// Required prime reads use a private exit-status protocol to preserve typed
// failure categories across re-exec without parsing or retaining stderr.
const primeMemoryFailureExitBase = 80

var primeMemoryFailureStages = []string{"store_open", "memory_accessor", "memory_list"}
var primeMemoryFailureReasons = []string{"unavailable", "deadline_exceeded", "canceled", "permission_denied", "connection_refused"}

type primeMemoryFailure struct {
	stage  string
	reason string
	err    error
}

func (e *primeMemoryFailure) Error() string { return primeErrorSummary(e.err) }
func (e *primeMemoryFailure) Unwrap() error { return e.err }

func newPrimeMemoryFailure(stage string, err error) *primeMemoryFailure {
	return &primeMemoryFailure{stage: stage, reason: primeMemoryFailureReason(err), err: err}
}

func primeMemoryFailureReason(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, os.ErrPermission):
		return "permission_denied"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection_refused"
	default:
		return "unavailable"
	}
}

func primeMemoryFailureExitCode(err error) (int, bool) {
	var failure *primeMemoryFailure
	if !errors.As(err, &failure) {
		return 0, false
	}
	stage := slices.Index(primeMemoryFailureStages, failure.stage)
	reason := slices.Index(primeMemoryFailureReasons, failure.reason)
	if stage < 0 || reason < 0 {
		return 0, false
	}
	return primeMemoryFailureExitBase + stage*len(primeMemoryFailureReasons) + reason, true
}

func primeMemorySubprocessFailure(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return newPrimeMemoryFailure("prime_process", errors.Join(err, ctx.Err()))
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		return newPrimeMemoryFailure("prime_process", err)
	}
	code := exit.ExitCode() - primeMemoryFailureExitBase
	if code < 0 || code >= len(primeMemoryFailureStages)*len(primeMemoryFailureReasons) {
		return newPrimeMemoryFailure("prime_process", err)
	}
	reason := primeMemoryFailureReasons[code%len(primeMemoryFailureReasons)]
	// Preserve standard errors.Is semantics as well as the original child exit.
	switch reason {
	case "deadline_exceeded":
		err = errors.Join(err, context.DeadlineExceeded)
	case "canceled":
		err = errors.Join(err, context.Canceled)
	case "permission_denied":
		err = errors.Join(err, os.ErrPermission)
	case "connection_refused":
		err = errors.Join(err, syscall.ECONNREFUSED)
	}
	return &primeMemoryFailure{stage: primeMemoryFailureStages[code/len(primeMemoryFailureReasons)], reason: reason, err: err}
}

func primeMemoryFailureStage(err error, fallback string) string {
	var failure *primeMemoryFailure
	if errors.As(err, &failure) {
		if slices.Contains(primeMemoryFailureStages, failure.stage) || failure.stage == "workspace_validation" || failure.stage == "prime_process" {
			return failure.stage
		}
	}
	return fallback
}
