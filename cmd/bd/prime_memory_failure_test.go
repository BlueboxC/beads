package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/memoryops"
)

type primeFailureMemories struct {
	memoryops.Memories
	err error
}

func (m primeFailureMemories) List(context.Context, memoryops.ListRequest) (memoryops.ListResult, error) {
	return memoryops.ListResult{}, m.err
}

type primeFailureStore struct {
	storage.DoltStorage
	accessorErr, listErr error
}

func (s primeFailureStore) Memories() (memoryops.Memories, error) {
	return primeFailureMemories{err: s.listErr}, s.accessorErr
}

type primeFailureProvider struct {
	uow.UnitOfWorkProvider
	accessorErr, listErr error
}

func (p primeFailureProvider) Memories() (memoryops.Memories, error) {
	return primeFailureMemories{err: p.listErr}, p.accessorErr
}

func TestRequiredPrimeFailurePreservesReadStageAndTypedCause(t *testing.T) {
	oldStore, oldProvider, oldProxied := store, uowProvider, proxiedServerMode
	oldRequire, oldError := primeRequireMemoryLoad, primeMemoryLoadError
	oldOpen := primeProxiedProviderOpen
	t.Cleanup(func() {
		primeProxiedProviderOpen = oldOpen
		store, uowProvider, proxiedServerMode = oldStore, oldProvider, oldProxied
		primeRequireMemoryLoad, primeMemoryLoadError = oldRequire, oldError
	})
	primeRequireMemoryLoad = true
	for _, proxied := range []bool{false, true} {
		for _, stage := range primeMemoryFailureStages {
			t.Run(fmt.Sprintf("proxied=%v/%s", proxied, stage), func(t *testing.T) {
				primeMemoryLoadError = nil
				proxiedServerMode = proxied
				cause := fmt.Errorf("PRIVATE DATABASE: %w", os.ErrPermission)
				switch stage {
				case "store_open":
					if proxied {
						workspace := t.TempDir()
						if err := os.WriteFile(filepath.Join(workspace, "config.yaml"), nil, 0o600); err != nil {
							t.Fatal(err)
						}
						t.Setenv("BEADS_DIR", workspace)
						uowProvider = nil
						primeProxiedProviderOpen = func(context.Context, string) (uow.UnitOfWorkProvider, error) { return nil, cause }
					} else {
						stubPrimeStoreOpen(t, cause)
					}
				case "memory_accessor":
					store = primeFailureStore{accessorErr: cause}
					uowProvider = primeFailureProvider{accessorErr: cause}
				case "memory_list":
					store = primeFailureStore{listErr: cause}
					uowProvider = primeFailureProvider{listErr: cause}
				}
				formatMemoriesForPrime(false)
				if primeMemoryFailureStage(primeMemoryLoadError, "unknown") != stage || !errors.Is(primeMemoryLoadError, os.ErrPermission) {
					t.Fatalf("lost typed read failure: %v", primeMemoryLoadError)
				}
				if _, ok := primeMemoryFailureExitCode(primeMemoryLoadError); !ok {
					t.Fatal("read failure cannot cross the subprocess boundary")
				}
			})
		}
	}
}

func TestRequiredPrimeFailurePreservesStoreDeadline(t *testing.T) {
	oldRequire, oldError := primeRequireMemoryLoad, primeMemoryLoadError
	t.Cleanup(func() { primeRequireMemoryLoad, primeMemoryLoadError = oldRequire, oldError })
	primeRequireMemoryLoad = true
	stubPrimeStoreOpen(t, fmt.Errorf("PRIVATE DATABASE: %w", context.DeadlineExceeded))
	formatMemoriesForPrime(false)
	if !errors.Is(primeMemoryLoadError, context.DeadlineExceeded) || primeMemoryFailureStage(primeMemoryLoadError, "unknown") != "store_open" {
		t.Fatalf("lost store deadline: %v", primeMemoryLoadError)
	}
}

// The test subprocess exits with the internal required-read status, exercising
// the real OS exit boundary without parsing stderr (which can contain secrets).
func TestPrimeMemoryFailureExitTransport(t *testing.T) {
	if os.Getenv("BEADS_TEST_PRIME_FAILURE_CHILD") == "1" {
		stage := os.Getenv("BEADS_TEST_PRIME_FAILURE_STAGE")
		reason := os.Getenv("BEADS_TEST_PRIME_FAILURE_REASON")
		code, ok := primeMemoryFailureExitCode(&primeMemoryFailure{stage: stage, reason: reason})
		if !ok {
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "PRIVATE DATABASE timed out permission denied lock")
		os.Exit(code)
	}
	for _, stage := range primeMemoryFailureStages {
		for _, reason := range primeMemoryFailureReasons {
			t.Run(stage+"/"+reason, func(t *testing.T) {
				cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestPrimeMemoryFailureExitTransport$")
				cmd.Env = append(os.Environ(), "BEADS_TEST_PRIME_FAILURE_CHILD=1", "BEADS_TEST_PRIME_FAILURE_STAGE="+stage, "BEADS_TEST_PRIME_FAILURE_REASON="+reason)
				_, err := cmd.CombinedOutput()
				failure := primeMemorySubprocessFailure(context.Background(), err)
				if primeMemoryFailureStage(failure, "unknown") != stage || primeMemoryFailureReason(failure) != reason {
					t.Fatalf("typed category changed across process exit: %v", failure)
				}
			})
		}
	}
	// A parent cancellation wins over a child's exit status, without attributing
	// the parent deadline to a memory-plane stage.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := primeMemorySubprocessFailure(ctx, errors.New("PRIVATE PROCESS"))
	if !errors.Is(err, context.Canceled) || primeMemoryFailureStage(err, "unknown") != "prime_process" {
		t.Fatal("lost parent cancellation")
	}
	// Error text alone is never evidence of a typed permission/timeout/lock cause.
	err = primeMemorySubprocessFailure(context.Background(), errors.New("PRIVATE DATABASE permission denied deadline exceeded lock"))
	if primeMemoryFailureReason(err) != "unavailable" || strings.Contains(primeMemoryFailureStage(err, "unknown"), "PRIVATE") {
		t.Fatal("classified an untyped error by its text")
	}
	if primeMemoryFailureReason(fmt.Errorf("connect: %w", syscall.ECONNREFUSED)) != "connection_refused" {
		t.Fatal("lost typed connection refusal")
	}
}
