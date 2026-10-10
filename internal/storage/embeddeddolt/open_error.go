package embeddeddolt

import (
	"errors"
	"fmt"
	"os"
)

// OpenPermissionError distinguishes a denied embedded open from transient lock
// contention. Readonly blocks Beads mutations, not the driver's filesystem writes.
type OpenPermissionError struct{ Err error }

func (e *OpenPermissionError) Error() string {
	return fmt.Sprintf("embedded Dolt open denied by filesystem permissions: %v", e.Err)
}
func (e *OpenPermissionError) Unwrap() error { return e.Err }
func (e *OpenPermissionError) Hint() string {
	return "Embedded Dolt requires filesystem write access to open storage files, even with --readonly. Retrying with unchanged permissions or sandbox rules will not help. Use a workspace with the required existing permissions, or an already-configured server-backed workspace."
}

func classifyOpenError(err error) error {
	if !errors.Is(err, os.ErrPermission) {
		return err
	}
	var denied *OpenPermissionError
	if errors.As(err, &denied) {
		return err
	}
	return &OpenPermissionError{Err: err}
}
