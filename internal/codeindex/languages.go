package codeindex

import (
	"bytes"
	"compress/gzip"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

//go:embed TYPESCRIPT-LICENSE
var typescriptLicense string

//go:embed TYPESCRIPT-NOTICE
var typescriptNotice string

func ParserLicenses() string {
	return "TypeScript 5.9.3 (Apache-2.0)\n" + typescriptLicense + "\n" + typescriptNotice + "\n" + treeLicenses
}

//go:embed go_ast.go
var goParserSource string

//go:embed script_ast.js
var scriptParser string

//go:embed typescript.js.gz
var typescriptBundle []byte

// ScanOptions preserves explicit selections and operator-owned interpreters.
type ScanOptions struct {
	Languages         []string
	Python, Node      string
	Rebuild           bool
	AllowMissingRoots bool // Maintenance retains selections whose files/directories were deleted.
}

func languageOf(path string) string {
	if filepath.Ext(path) == ".C" {
		return "cpp"
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".py":
		return "python"
	case ".java":
		return "java"
	case ".cs":
		return "csharp"
	case ".rs":
		return "rust"
	case ".cc", ".cpp", ".cxx", ".c++", ".h", ".hh", ".hpp", ".hxx", ".h++":
		return "cpp"
	case ".go":
		return "go"
	case ".ts", ".tsx", ".mts", ".cts":
		return "typescript"
	case ".js", ".jsx", ".mjs", ".cjs":
		return "javascript"
	}
	return ""
}
func fileLanguage(file File) string {
	if file.Language != "" {
		return file.Language
	}
	return languageOf(file.Path)
}
func languagesFor(index Index) []string {
	if len(index.Languages) > 0 {
		return index.Languages
	}
	return []string{"python"}
}
func normalizeLanguages(values []string) ([]string, error) {
	seen := map[string]bool{}
	for _, v := range values {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "all":
			for _, name := range []string{"python", "go", "javascript", "typescript", "java", "csharp", "rust", "cpp"} {
				seen[name] = true
			}
			continue
		case "c#", "cs", "c-sharp":
			v = "csharp"
		case "c++", "cxx":
			v = "cpp"
		default:
			v = strings.ToLower(strings.TrimSpace(v))
		}
		if !treeLanguage(v) && v != "python" && v != "go" && v != "typescript" && v != "javascript" {
			return nil, fmt.Errorf("unsupported language %q", v)
		}
		seen[v] = true
	}
	result := make([]string, 0, len(seen))
	for v := range seen {
		result = append(result, v)
	}
	sort.Strings(result)
	if len(result) == 0 {
		return nil, errors.New("select at least one language")
	}
	return result, nil
}
func parserHash(language string) string {
	switch language {
	case "python":
		return digest([]byte(parserScript))
	case "java", "csharp", "rust", "cpp":
		return treeParserHash(language)
	case "go":
		return digest([]byte(goParserSource + runtime.Version()))
	default:
		return digest(append([]byte(scriptParser), typescriptBundle...))
	}
}
func manifestParser(languages []string, version int) string {
	if version < 3 {
		return parserHash("python")
	}
	var s strings.Builder
	for _, language := range languages {
		s.WriteString(language + ":" + parserHash(language) + "\n")
	}
	return digest([]byte(s.String()))
}
func parseScripts(ctx context.Context, executable string, inputs []parserInput) (parserOutput, error) {
	if executable == "" {
		executable = "node"
	}
	path, err := exec.LookPath(executable)
	if err != nil {
		return parserOutput{}, errors.New("operator-owned Node.js is required to parse JavaScript/TypeScript; select --node")
	}
	reader, err := gzip.NewReader(bytes.NewReader(typescriptBundle))
	if err != nil {
		return parserOutput{}, err
	}
	compiler, err := io.ReadAll(io.LimitReader(reader, 16*1024*1024+1))
	_ = reader.Close()
	if err != nil {
		return parserOutput{}, err
	}
	if len(compiler) > 16*1024*1024 {
		return parserOutput{}, errors.New("compiler bundle exceeds bound")
	}
	input, err := json.Marshal(struct {
		Compiler string        `json:"compiler"`
		Files    []parserInput `json:"files"`
	}{string(compiler), inputs})
	if err != nil {
		return parserOutput{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--no-addons", "--no-global-search-paths", "--max-old-space-size=512", "--eval", scriptParser)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "SYSTEMROOT=" + os.Getenv("SYSTEMROOT")}
	cmd.Stdin = bytes.NewReader(input)
	var stdout, stderr boundedBuffer
	stdout.limit, stderr.limit = maxJSONBytes, 4096
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return parserOutput{}, fmt.Errorf("isolated JS/TS AST parser failed: %w", err)
	}
	var output parserOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		return output, errors.New("invalid JS/TS parser output")
	}
	if len(output.Files) != len(inputs) {
		return output, errors.New("JS/TS parser returned incomplete files")
	}
	for i, file := range output.Files {
		if file.Path != inputs[i].Path {
			return output, errors.New("JS/TS parser path mismatch")
		}
	}
	return output, nil
}
