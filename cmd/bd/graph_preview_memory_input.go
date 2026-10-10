package main

import (
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"
)

// A disposable CLI acquisition budget, not a Memory Type or BDP limit.
const graphPreviewMemoryBodyLimit = 1 << 20

func rememberArgs(cmd *cobra.Command, args []string) error {
	// Cobra validates arguments before workspace admission. Explicit graph-only
	// flags must reach admission even with no positional body; legacy calls keep
	// their original arity. The graph handler validates source exclusivity.
	if rememberGraphFlagsChanged(cmd) {
		return nil
	}
	return cobra.ExactArgs(1)(cmd, args)
}

// Shared by argument and workspace admission: a graph-only flag must never
// fall into the legacy remember path or read input there, even without a body.
func rememberGraphFlagsChanged(cmd *cobra.Command) bool {
	for _, name := range []string{"id", "title", "body-file", "stdin", "update", "create-only", "if-revision", "metadata", "set-metadata", "unset-metadata", "properties"} {
		if cmd.Flags().Changed(name) {
			return true
		}
	}
	return false
}

func graphPreviewRememberBody(cmd *cobra.Command, args []string) (string, error) {
	fileSet, stdinSet := cmd.Flags().Changed("body-file"), cmd.Flags().Changed("stdin")
	count := len(args)
	if fileSet {
		count++
	}
	if stdinSet {
		count++
	}
	if count != 1 {
		return "", graphFailure("invalid_properties", "remember requires exactly one body source: positional text, --body-file FILE, or --stdin", 2)
	}
	if stdinSet {
		enabled, _ := cmd.Flags().GetBool("stdin")
		if !enabled {
			return "", graphFailure("invalid_properties", "--stdin must be true when supplied", 2)
		}
		return graphPreviewReadMemoryBody(cmd.InOrStdin())
	}
	if fileSet {
		path, _ := cmd.Flags().GetString("body-file")
		file, err := os.Open(path) // #nosec G304 -- explicit operator-selected Memory body file
		if err != nil {
			return "", graphFailure("invalid_properties", "cannot open Memory body file: "+err.Error(), 2)
		}
		body, readErr := graphPreviewReadMemoryBody(file)
		closeErr := file.Close()
		if readErr != nil {
			return "", readErr
		}
		if closeErr != nil {
			return "", graphFailure("invalid_properties", "cannot close Memory body file: "+closeErr.Error(), 2)
		}
		return body, nil
	}
	return graphPreviewReadMemoryBody(strings.NewReader(args[0]))
}

func graphPreviewReadMemoryBody(reader io.Reader) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(reader, graphPreviewMemoryBodyLimit+1))
	if err != nil {
		return "", graphFailure("invalid_properties", "cannot read Memory body: "+err.Error(), 2)
	}
	if len(raw) > graphPreviewMemoryBodyLimit {
		return "", graphFailure("capability_unavailable", "Memory body input exceeds the preview limit of 1048576 bytes", 5)
	}
	if !utf8.Valid(raw) {
		return "", graphFailure("invalid_properties", "Memory body must be valid UTF-8", 2)
	}
	return string(raw), nil
}
