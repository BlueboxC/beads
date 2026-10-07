// Package codeindex maintains an advisory structural code index in the existing
// memory plane. Rebuilding derived data never writes human knowledge or tasks.
package codeindex

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/mod/modfile"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/knowledge"
	"github.com/steveyegge/beads/memoryops"
)

const (
	Prefix        = "@knowledge/code/"
	manifestKey   = Prefix + "manifest"
	MaxFiles      = 4096
	MaxInputBytes = 32 * 1024 * 1024
	maxJSONBytes  = 8 * 1024 * 1024
	maxRowBytes   = 60 * 1024 // Fits the existing TEXT storage contract.
)

//go:embed python_ast.py
var parserScript string

type Symbol struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Line      int    `json:"line"`
	EndLine   int    `json:"end_line"`
	Parent    string `json:"parent"`
	Static    bool   `json:"static,omitempty"`
	Uncertain bool   `json:"uncertain,omitempty"`
}

type Import struct {
	Owner         string `json:"owner"`
	Name          string `json:"name"`
	Member        string `json:"member,omitempty"`
	Alias         string `json:"alias"`
	Level         int    `json:"level"`
	Line          int    `json:"line"`
	From          bool   `json:"from"`
	ExplicitAlias bool   `json:"explicit_alias,omitempty"`
	TypeOnly      bool   `json:"type_only,omitempty"`
	CommonJS      bool   `json:"common_js,omitempty"`
	Uncertain     bool   `json:"uncertain,omitempty"`
}

type Call struct {
	Owner string `json:"owner"`
	Name  string `json:"name"`
	Line  int    `json:"line"`
}

type File struct {
	Path       string              `json:"path"`
	Language   string              `json:"language,omitempty"`
	Parser     string              `json:"parser_sha256,omitempty"`
	Package    string              `json:"package,omitempty"`
	Exports    map[string]string   `json:"exports,omitempty"`
	SHA256     string              `json:"sha256"`
	Module     string              `json:"module"`
	Bytes      int                 `json:"bytes"`
	Contracts  []knowledge.Source  `json:"contracts"`
	Symbols    []Symbol            `json:"symbols"`
	Imports    []Import            `json:"imports"`
	Calls      []Call              `json:"calls"`
	Blocked    map[string][]string `json:"blocked"`
	ParseError string              `json:"parse_error,omitempty"`
	Validity   string              `json:"validity,omitempty"`
}

type Rename struct {
	From string `json:"from"`
	To   string `json:"to"`
}
type Changes struct {
	Parsed  int      `json:"parsed"`
	Reused  int      `json:"reused"`
	Added   []string `json:"added"`
	Changed []string `json:"changed"`
	Removed []string `json:"removed"`
	Renamed []Rename `json:"renamed"`
}
type Stats struct {
	Files       int `json:"files"`
	Symbols     int `json:"symbols"`
	Imports     int `json:"imports"`
	Calls       int `json:"calls"`
	ParseErrors int `json:"parse_errors"`
	InputBytes  int `json:"input_bytes"`
	StoredBytes int `json:"stored_bytes"`
}
type Index struct {
	Version    int      `json:"code_index_version"`
	Languages  []string `json:"languages,omitempty"`
	Node       string   `json:"node_version,omitempty"`
	TypeScript string   `json:"typescript_version,omitempty"`
	Parser     string   `json:"parser_sha256"`
	Python     string   `json:"python_version"`
	Roots      []string `json:"roots"`
	Exclude    []string `json:"exclude"`
	Files      []File   `json:"files"`
	Changes    Changes  `json:"changes"`
	Stats      Stats    `json:"stats"`
	Warnings   []string `json:"warnings,omitempty"`
}
type reference struct {
	Path string `json:"path"`
	Blob string `json:"blob"`
}
type manifest struct {
	Index
	References []reference `json:"references,omitempty"`
	Parts      []string    `json:"reference_parts,omitempty"`
}

func IsKey(key string) bool     { return strings.HasPrefix(key, Prefix) }
func digest(data []byte) string { value := sha256.Sum256(data); return hex.EncodeToString(value[:]) }

func pack(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	if len(data) > maxJSONBytes {
		return "", errors.New("code index JSON exceeds 8 MiB; narrow roots")
	}
	var buf bytes.Buffer
	writer := gzip.NewWriter(&buf)
	if _, err := writer.Write(data); err != nil {
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}
	encoded := "code-v1:" + base64.StdEncoding.EncodeToString(buf.Bytes())
	if len(encoded) > maxRowBytes {
		return "", errors.New("code index row exceeds 60 KiB; narrow roots or split a large module")
	}
	return encoded, nil
}

func unpack(value string, target any) error {
	if !strings.HasPrefix(value, "code-v1:") || len(value) > maxRowBytes {
		return errors.New("invalid code index encoding")
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(value, "code-v1:"))
	if err != nil {
		return err
	}
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer func() { _ = reader.Close() }()
	data, err = io.ReadAll(io.LimitReader(reader, maxJSONBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxJSONBytes {
		return errors.New("code index expands beyond 8 MiB")
	}
	return json.Unmarshal(data, target)
}

func Load(plane map[string]string) (Index, error) {
	return loadIndex(func(key string) (string, bool, error) {
		value, exists := plane[key]
		return value, exists, nil
	}, nil)
}

// LoadSelected reads routing metadata and only the requested current file blobs.
// All manifest paths and reference parts are validated before a file is trusted.
func LoadSelected(ctx context.Context, memories memoryops.Memories, paths []string) (Index, error) {
	if len(paths) == 0 || len(paths) > 16 {
		return Index{}, errors.New("select 1-16 code files")
	}
	selected := make(map[string]bool)
	for _, path := range paths {
		clean, err := safePath(path)
		if err != nil || clean != path {
			return Index{}, errors.New("invalid selected code path")
		}
		selected[path] = true
	}
	return loadIndex(func(key string) (string, bool, error) {
		result, err := memories.Recall(ctx, memoryops.RecallRequest{Key: key})
		return result.Value, result.Found, err
	}, selected)
}

func loadIndex(read func(string) (string, bool, error), selected map[string]bool) (Index, error) {
	value, exists, err := read(manifestKey)
	if err != nil {
		return Index{}, err
	}
	if !exists {
		return Index{}, nil
	}
	var entry manifest
	if err := unpack(value, &entry); err != nil {
		return Index{}, err
	}
	if (entry.Version != 1 && entry.Version != 2 && entry.Version != 3) || len(entry.References) > MaxFiles || len(entry.Parts) > MaxFiles/128+1 {
		return Index{}, errors.New("unsupported or oversized code index manifest")
	}
	if entry.Version == 3 {
		normalized, err := normalizeLanguages(entry.Languages)
		if err != nil || len(normalized) != len(entry.Languages) {
			return Index{}, errors.New("invalid code index languages")
		}
		entry.Languages = normalized
	}
	entry.Files = nil
	entry.Stats.StoredBytes = len(value)
	for _, part := range entry.Parts {
		encoded, exists, err := read(Prefix + "blob/" + part)
		if err != nil {
			return Index{}, err
		}
		if !exists || digest([]byte(encoded)) != part {
			return Index{}, errors.New("missing or corrupt code reference part")
		}
		var refs []reference
		if err := unpack(encoded, &refs); err != nil {
			return Index{}, err
		}
		if len(refs) > 128 || len(entry.References)+len(refs) > MaxFiles {
			return Index{}, errors.New("oversized code reference part")
		}
		entry.References = append(entry.References, refs...)
		entry.Stats.StoredBytes += len(encoded)
	}
	seen := make(map[string]bool)
	for _, ref := range entry.References {
		if _, err := safePath(ref.Path); err != nil || seen[ref.Path] {
			return Index{}, errors.New("invalid or duplicate code index path")
		}
		seen[ref.Path] = true
		if selected != nil && !selected[ref.Path] {
			continue
		}
		value, exists, err := read(Prefix + "blob/" + ref.Blob)
		if err != nil {
			return Index{}, err
		}
		if !exists || digest([]byte(value)) != ref.Blob {
			return Index{}, errors.New("missing or corrupt code index blob: " + ref.Path)
		}
		var file File
		if err := unpack(value, &file); err != nil {
			return Index{}, err
		}
		if file.Path != ref.Path || (entry.Version == 3 && (file.Language != languageOf(file.Path) || !slices.Contains(entry.Languages, file.Language))) {
			return Index{}, errors.New("code index path mismatch")
		}
		entry.Files = append(entry.Files, file)
		entry.Stats.StoredBytes += len(value)
	}
	return entry.Index, nil
}

// Immutable blobs precede the single atomic manifest write. Interrupted scans
// leave the previous manifest readable. Old blobs remain for rollback; never
// delete another concurrent reader's generation or human knowledge rows.
func Save(ctx context.Context, memories memoryops.Memories, plane map[string]string, index Index) (int, error) {
	entry := manifest{Index: index}
	entry.Files = nil
	blobs := make(map[string]string)
	stored := 0
	for _, file := range index.Files {
		file.Validity = ""
		encoded, err := pack(file)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", file.Path, err)
		}
		blob := digest([]byte(encoded))
		entry.References = append(entry.References, reference{Path: file.Path, Blob: blob})
		blobs[Prefix+"blob/"+blob] = encoded
		stored += len(encoded)
	}
	// References are bounded parts too; the final manifest stays within TEXT.
	if index.Version >= 2 {
		for start := 0; start < len(entry.References); start += 128 {
			end := min(start+128, len(entry.References))
			part, err := pack(entry.References[start:end])
			if err != nil {
				return 0, err
			}
			key := digest([]byte(part))
			blobs[Prefix+"blob/"+key] = part
			entry.Parts = append(entry.Parts, key)
			stored += len(part)
		}
		entry.References = nil
	}
	entry.Stats.StoredBytes = 0
	encoded, err := pack(entry)
	if err != nil {
		return 0, err
	}
	if atomic, ok := memories.(memoryops.AtomicMemories); ok {
		// A pruning writer must never race a reused blob against manifest publication.
		// The storage capability checks the prior generation and writes every required
		// row together, skipping equal values within that same transaction.
		blobs[manifestKey] = encoded
		_, err := atomic.Apply(ctx, memoryops.BatchRequest{
			Expected: map[string]string{manifestKey: plane[manifestKey]}, Remember: blobs,
		})
		return stored + len(encoded), err
	}
	keys := make([]string, 0, len(blobs))
	for key := range blobs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if plane[key] == blobs[key] {
			continue
		}
		if _, err := memories.Remember(ctx, memoryops.RememberRequest{Key: key, Content: blobs[key]}); err != nil {
			return 0, err
		}
	}
	if plane[manifestKey] != encoded {
		if _, err := memories.Remember(ctx, memoryops.RememberRequest{Key: manifestKey, Content: encoded}); err != nil {
			return 0, err
		}
	}
	return stored + len(encoded), nil
}

type Reader struct {
	root    *os.Root
	sources *knowledge.Reader
}

func Open(path string) (*Reader, error) {
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	sources, err := knowledge.Open(path)
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	return &Reader{root: root, sources: sources}, nil
}
func (r *Reader) Close() error { _ = r.sources.Close(); return r.root.Close() }
func safePath(path string) (string, error) {
	path = filepath.ToSlash(filepath.Clean(path))
	if !fs.ValidPath(path) || path == "." || filepath.IsAbs(path) {
		return "", fmt.Errorf("invalid workspace-relative path %q", path)
	}
	return path, nil
}

var ignoredDirs = map[string]bool{"node_modules": true, "vendor": true, "dist": true, "build": true, "backups": true, "backup": true, "__pycache__": true, "venv": true, "logs": true, "data": true, "workspace": true}

func excluded(path string, exclusions []string) bool {
	for _, component := range strings.Split(path, "/") {
		if strings.HasPrefix(component, ".") || ignoredDirs[strings.ToLower(component)] {
			return true
		}
	}
	for _, prefix := range exclusions {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

func (r *Reader) paths(roots, exclusions, languages []string, allowMissing bool) ([]string, error) {
	if len(roots) == 0 || len(roots) > MaxFiles {
		return nil, errors.New("provide 1-4096 code files or directories")
	}
	paths := make(map[string]bool)
	allowed := map[string]bool{}
	for _, language := range languages {
		allowed[language] = true
	}
	for _, selection := range roots {
		selection, err := safePath(selection)
		if err != nil {
			return nil, err
		}
		if excluded(selection, exclusions) {
			return nil, fmt.Errorf("selected root is excluded: %s", selection)
		}
		info, err := r.root.Lstat(selection)
		if allowMissing && errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !info.IsDir() && !allowed[languageOf(selection)] {
			return nil, fmt.Errorf("selected file is outside selected languages: %s", selection)
		}
		err = fs.WalkDir(r.root.FS(), selection, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if excluded(path, exclusions) {
				if entry.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if entry.Type()&fs.ModeSymlink != 0 {
				return fmt.Errorf("symlink refused in code selection: %s", path)
			}
			if !entry.IsDir() && allowed[languageOf(path)] {
				paths[path] = true
			}
			if len(paths) > MaxFiles {
				return fmt.Errorf("more than %d code files; narrow roots", MaxFiles)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	result := make([]string, 0, len(paths))
	for path := range paths {
		result = append(result, path)
	}
	sort.Strings(result)
	return result, nil
}

type parserInput struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}
type parserOutput struct {
	Python     string `json:"python"`
	Node       string `json:"node"`
	TypeScript string `json:"typescript"`
	Files      []File `json:"files"`
}
type boundedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *boundedBuffer) Write(data []byte) (int, error) {
	if b.Len()+len(data) > b.limit {
		return 0, errors.New("AST parser output exceeds bound")
	}
	return b.Buffer.Write(data)
}

func parse(ctx context.Context, executable string, inputs []parserInput) (parserOutput, error) {
	if executable == "" {
		executable = "python3"
	}
	path, err := exec.LookPath(executable)
	if err != nil {
		return parserOutput{}, errors.New("Python 3 is required for code scan; install/select an operator-owned interpreter")
	}
	data, err := json.Marshal(inputs)
	if err != nil {
		return parserOutput{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "-I", "-S", "-c", parserScript)
	cmd.Stdin = bytes.NewReader(data)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "SYSTEMROOT=" + os.Getenv("SYSTEMROOT")}
	var stdout, stderr boundedBuffer
	stdout.limit, stderr.limit = maxJSONBytes, 4096
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return parserOutput{}, fmt.Errorf("isolated AST parser failed: %w", err)
	}
	var result parserOutput
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return result, err
	}
	if len(result.Files) != len(inputs) {
		return result, errors.New("AST parser returned incomplete files")
	}
	for i, file := range result.Files {
		if file.Path != inputs[i].Path {
			return result, errors.New("AST parser path mismatch")
		}
	}
	return result, nil
}

func (r *Reader) Scan(ctx context.Context, roots, exclusions []string, previous Index, rebuild bool, python string) (Index, error) {
	return r.ScanWithOptions(ctx, roots, exclusions, previous, ScanOptions{Languages: []string{"python"}, Python: python, Rebuild: rebuild})
}

func (r *Reader) ScanWithOptions(ctx context.Context, roots, exclusions []string, previous Index, options ScanOptions) (Index, error) {
	if len(options.Languages) == 0 {
		options.Languages = languagesFor(previous)
	}
	languages, err := normalizeLanguages(options.Languages)
	if err != nil {
		return Index{}, err
	}
	version := 2
	if previous.Version >= 3 || len(languages) != 1 || languages[0] != "python" {
		version = 3
	}
	roots = append([]string(nil), roots...)
	exclusions = append([]string(nil), exclusions...)
	for i, path := range roots {
		clean, err := safePath(path)
		if err != nil {
			return Index{}, err
		}
		roots[i] = clean
	}
	for i, path := range exclusions {
		clean, err := safePath(path)
		if err != nil {
			return Index{}, err
		}
		exclusions[i] = clean
	}
	sort.Strings(roots)
	sort.Strings(exclusions)
	paths, err := r.paths(roots, exclusions, languages, options.AllowMissingRoots)
	if err != nil {
		return Index{}, err
	}
	result := Index{Version: version, Parser: manifestParser(languages, version), Python: previous.Python, Node: previous.Node, TypeScript: previous.TypeScript, Roots: roots, Exclude: exclusions, Files: []File{}}
	if version >= 3 {
		result.Languages = languages
	}
	old := make(map[string]File)
	for _, file := range previous.Files {
		old[file.Path] = file
	}
	inputs := map[string][]parserInput{}
	positions := map[string][]int{}
	contractCache := make(map[string]knowledge.Source)
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return Index{}, err
		}
		data, err := r.sources.Read(path)
		if err != nil {
			return Index{}, fmt.Errorf("%s: %w", path, err)
		}
		result.Stats.InputBytes += len(data)
		if result.Stats.InputBytes > MaxInputBytes {
			return Index{}, errors.New("code input exceeds 32 MiB; narrow roots")
		}
		language := languageOf(path)
		file := File{Path: path, SHA256: digest(data), Bytes: len(data), Module: strings.TrimSuffix(strings.ReplaceAll(path, "/", "."), ".py")}
		if language != "python" {
			prefix := "js:"
			if treeLanguage(language) {
				prefix = language + ":"
			}
			file.Module = prefix + strings.TrimSuffix(path, filepath.Ext(path))
		}
		if version >= 3 {
			file.Language = language
			file.Parser = parserHash(language)
		}
		if language == "python" {
			file.Module = strings.TrimSuffix(file.Module, ".__init__")
		}
		source, err := r.sources.Snapshot(path)
		if err != nil || source.SHA256 != file.SHA256 {
			return Index{}, fmt.Errorf("source changed during scan: %s", path)
		}
		for _, path := range source.Contracts {
			contract, found := contractCache[path]
			if !found {
				var err error
				contract, err = r.sources.Snapshot(path)
				if err != nil {
					return Index{}, err
				}
				contract.Headings = nil
				contractCache[path] = contract
			}
			file.Contracts = append(file.Contracts, contract)
		}
		prior, found := old[path]
		if !found {
			result.Changes.Added = append(result.Changes.Added, path)
		} else if prior.SHA256 != file.SHA256 {
			result.Changes.Changed = append(result.Changes.Changed, path)
		}
		priorParser := prior.Parser
		if priorParser == "" {
			priorParser = previous.Parser
		}
		if found && prior.SHA256 == file.SHA256 && priorParser == parserHash(language) && !options.Rebuild {
			file.Symbols, file.Imports, file.Calls, file.Blocked, file.ParseError = prior.Symbols, prior.Imports, prior.Calls, prior.Blocked, prior.ParseError
			file.Package, file.Exports = prior.Package, prior.Exports
			result.Changes.Reused++
		} else {
			inputs[language] = append(inputs[language], parserInput{Path: path, Content: string(data)})
			positions[language] = append(positions[language], len(result.Files))
		}
		result.Files = append(result.Files, file)
		delete(old, path)
	}
	for _, language := range languages {
		for start := 0; start < len(inputs[language]); start += 128 {
			if err := ctx.Err(); err != nil {
				return Index{}, err
			}
			end := min(start+128, len(inputs[language]))
			batch := inputs[language][start:end]
			var parsed parserOutput
			var err error
			switch language {
			case "python":
				parsed, err = parse(ctx, options.Python, batch)
			case "go":
				for _, input := range batch {
					parsed.Files = append(parsed.Files, parseGo(input))
				}
			case "xml":
				for _, input := range batch {
					parsed.Files = append(parsed.Files, parseXML(input))
				}
			default:
				if treeLanguage(language) {
					parsed, err = parseTrees(ctx, options.Node, language, batch)
				} else {
					parsed, err = parseScripts(ctx, options.Node, batch)
				}
			}
			if err != nil {
				return Index{}, err
			}
			if parsed.Python != "" {
				result.Python = parsed.Python
			}
			if parsed.Node != "" {
				result.Node = parsed.Node
				if parsed.TypeScript != "" {
					result.TypeScript = parsed.TypeScript
				}
			}
			for i, position := range positions[language][start:end] {
				file := &result.Files[position]
				ast := parsed.Files[i]
				file.Symbols, file.Imports, file.Calls, file.Blocked, file.ParseError = ast.Symbols, ast.Imports, ast.Calls, ast.Blocked, ast.ParseError
				file.Package, file.Exports = ast.Package, ast.Exports
			}
			result.Changes.Parsed += len(batch)
		}
	}
	// Bind Go package mapping to the nearest selected workspace go.mod as provenance.
	type moduleSnapshot struct {
		name   string
		source knowledge.Source
	}
	moduleCache := map[string]moduleSnapshot{}
	for i := range result.Files {
		file := &result.Files[i]
		if fileLanguage(*file) != "go" {
			continue
		}
		dir := path.Dir(file.Path)
		file.Module = "go-local:" + dir + "@" + file.Package
		for current := dir; ; current = path.Dir(current) {
			modpath := path.Join(current, "go.mod")
			info, staterr := r.root.Lstat(modpath)
			if staterr == nil {
				if !info.Mode().IsRegular() {
					return Index{}, errors.New("go.mod must be a confined regular file")
				}
				cached, found := moduleCache[modpath]
				if !found {
					data, readerr := r.sources.Read(modpath)
					if readerr != nil {
						return Index{}, readerr
					}
					mod, parseerr := modfile.ParseLax(modpath, data, nil)
					if parseerr != nil || mod.Module == nil {
						return Index{}, errors.New("invalid selected go.mod")
					}
					source, snapshoterr := r.sources.Snapshot(modpath)
					if snapshoterr != nil || source.SHA256 != digest(data) {
						return Index{}, errors.New("go.mod changed during scan")
					}
					source.Headings, source.Contracts = nil, nil
					cached = moduleSnapshot{name: mod.Module.Mod.Path, source: source}
					moduleCache[modpath] = cached
				}
				file.Contracts = append(file.Contracts, cached.source)
				suffix := strings.TrimPrefix(strings.TrimPrefix(dir, current), "/")
				if current == "." {
					suffix = dir
				}
				importpath := cached.name
				if suffix != "" && suffix != "." {
					importpath += "/" + suffix
				}
				file.Module = "go:" + importpath
				if strings.HasSuffix(file.Package, "_test") {
					file.Module += "@" + file.Package
				}
				break
			}
			if current == "." {
				break
			}
		}
	}
	for path := range old {
		result.Changes.Removed = append(result.Changes.Removed, path)
	}
	sort.Strings(result.Changes.Removed)
	addedPaths := make(map[string]bool)
	for _, path := range result.Changes.Added {
		addedPaths[path] = true
	}
	addedHashes, removedHashes := make(map[string][]string), make(map[string][]string)
	for _, file := range result.Files {
		if addedPaths[file.Path] {
			addedHashes[file.SHA256] = append(addedHashes[file.SHA256], file.Path)
		}
	}
	for path, file := range old {
		removedHashes[file.SHA256] = append(removedHashes[file.SHA256], path)
	}
	for hash, paths := range addedHashes {
		if len(paths) == 1 && len(removedHashes[hash]) == 1 {
			result.Changes.Renamed = append(result.Changes.Renamed, Rename{From: removedHashes[hash][0], To: paths[0]})
		}
	}
	sort.Slice(result.Changes.Renamed, func(i, j int) bool { return result.Changes.Renamed[i].To < result.Changes.Renamed[j].To })
	result.Stats.Files = len(result.Files)
	for _, file := range result.Files {
		result.Stats.Symbols += len(file.Symbols)
		result.Stats.Imports += len(file.Imports)
		result.Stats.Calls += len(file.Calls)
		if file.ParseError != "" {
			result.Stats.ParseErrors++
		}
	}
	// Recheck the captured worktree before publishing any derived rows.
	checked := r.Refresh(result)
	if len(checked.Warnings) > 0 {
		return Index{}, errors.New("source or DOX changed during scan; previous index retained")
	}
	return result, nil
}

// Refresh detects source, contract and discovery drift without parsing or writes.
func (r *Reader) Refresh(index Index) Index {
	return r.refresh(index, true)
}

// RefreshSelected checks loaded files and their provenance without discovering
// roots or reading unrelated sources. It never parses or publishes an index.
func (r *Reader) RefreshSelected(index Index) Index {
	return r.refresh(index, false)
}

func (r *Reader) refresh(index Index, discovery bool) Index {
	if index.Version == 0 {
		return index
	}
	parserChanged := index.Parser != manifestParser(languagesFor(index), index.Version)
	if parserChanged && (discovery || index.Version < 3) {
		index.Warnings = append(index.Warnings, "AST parser changed; run bd code scan")
	}
	cache := make(map[string]string)
	current := func(path string) string {
		if value, found := cache[path]; found {
			return value
		}
		source, err := r.sources.Snapshot(path)
		if err == nil {
			cache[path] = source.SHA256
		} else {
			cache[path] = ""
		}
		return cache[path]
	}
	known := make(map[string]bool)
	for i := range index.Files {
		file := &index.Files[i]
		file.Validity = "current"
		if !discovery && ((index.Version < 3 && parserChanged) || (index.Version >= 3 && file.Parser != parserHash(fileLanguage(*file)))) {
			file.Validity = "needs_review"
		}
		known[file.Path] = true
		if current(file.Path) != file.SHA256 {
			file.Validity = "needs_review"
		}
		contracts := make(map[string]bool)
		for _, source := range file.Contracts {
			contracts[source.Path] = true
			if current(source.Path) != source.SHA256 {
				file.Validity = "needs_review"
			}
		}
		if source, err := r.sources.Snapshot(file.Path); err == nil {
			for _, contract := range source.Contracts {
				if !contracts[contract] {
					file.Validity = "needs_review"
				}
			}
		}
		if file.Validity != "current" {
			index.Warnings = append(index.Warnings, "Code or DOX changed/unavailable: "+file.Path)
		}
	}
	if !discovery {
		return index
	}
	paths, err := r.paths(index.Roots, index.Exclude, languagesFor(index), true)
	if err != nil {
		index.Warnings = append(index.Warnings, "Code discovery incomplete: "+err.Error())
	} else {
		for _, path := range paths {
			if !known[path] {
				index.Warnings = append(index.Warnings, "New code file: "+path)
			}
		}
	}
	return index
}

func Summary(plane map[string]string) string {
	value, exists := plane[manifestKey]
	if !exists {
		return ""
	}
	var entry manifest
	if err := unpack(value, &entry); err != nil || (entry.Version != 1 && entry.Version != 2 && entry.Version != 3) {
		return "\nCode index unavailable; inspect with bd code status.\n"
	}
	label := "Python code index"
	if entry.Version >= 3 {
		label = "Code index (" + strings.Join(entry.Languages, ", ") + ")"
	}
	return fmt.Sprintf("\n%s snapshot: %d files, %d symbols, %d imports, %d call sites, %d parse errors. Inspect current hashes with bd code status; query bd code query <path-or-symbol>. Static references are not runtime proof. Rebuild preserves human records.\n", label, entry.Stats.Files, entry.Stats.Symbols, entry.Stats.Imports, entry.Stats.Calls, entry.Stats.ParseErrors)
}

// Knowledge joins the same confined source reader with explicit human records.
func (r *Reader) Knowledge(plane map[string]string) knowledge.State {
	return r.sources.Refresh(knowledge.Decode(plane))
}
