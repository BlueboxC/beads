package embeddeddolt

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
	"testing"

	"github.com/dolthub/dolt/go/store/nbs"
)

func TestClassifyOpenPermission(t *testing.T) {
	for _, cause := range []error{os.ErrPermission, syscall.EACCES, syscall.EPERM,
		&os.PathError{Op: "openat", Path: "store/LOCK", Err: syscall.EACCES},
		errors.Join(errors.New("cleanup"), fmt.Errorf("open: %w", os.ErrPermission)),
	} {
		got := classifyOpenError(cause)
		var denied *OpenPermissionError
		if !errors.As(got, &denied) || !errors.Is(got, cause) || !errors.Is(got, os.ErrPermission) {
			t.Fatalf("lost typed permission cause: %v", got)
		}
		if classifyOpenError(got) != got {
			t.Fatal("wrapped twice")
		}
	}
}

func TestClassifyOpenLeavesOtherFailuresUnchanged(t *testing.T) {
	for _, err := range []error{nil, context.Canceled, context.DeadlineExceeded, nbs.ErrDatabaseLocked,
		errors.New("database is locked"), errors.New("Access is denied"), errors.New("permission denied"),
	} {
		if got := classifyOpenError(err); got != err {
			t.Fatalf("classified by error text: %v", got)
		}
	}
}
