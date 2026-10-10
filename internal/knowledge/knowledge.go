// Package knowledge adds bounded, source-backed development memory to the fork.
// It uses memoryops without owning a database, executing documents or inferring
// that a recorded assertion has passed a test.
package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/steveyegge/beads/memoryops"
)

const (
	Prefix       = "@knowledge/"
	MaxSources   = 256
	MaxRecords   = 512
	MaxFileBytes = 16 * 1024 * 1024
)

// Source contains provenance and headings, not a second copy of a document.
type Source struct {
	Path      string   `json:"path"`
	SHA256    string   `json:"sha256"`
	Headings  []string `json:"headings,omitempty"`
	Contracts []string `json:"contracts,omitempty"`
}

// Record is an explicit assertion. Scope is the operator's evidence claim,
// never a level inferred from task status, file names or document contents.
type Record struct {
	ID         string   `json:"id"`
	Kind       string   `json:"kind"`
	Summary    string   `json:"summary"`
	Module     string   `json:"module,omitempty"`
	Issue      string   `json:"issue,omitempty"`
	BaseCommit string   `json:"base_commit,omitempty"`
	Problem    string   `json:"problem,omitempty"`
	Cause      string   `json:"cause,omitempty"`
	Solution   string   `json:"solution,omitempty"`
	Rejected   []string `json:"rejected,omitempty"`
	Scope      string   `json:"scope"`
	Evidence   string   `json:"evidence,omitempty"`
	Sources    []Source `json:"sources"`
	Related    []string `json:"related,omitempty"`
	Symbols    []string `json:"symbols,omitempty"`
}

type Catalog struct {
	Roots   []string `json:"roots"`
	Sources []Source `json:"sources"`
}

type View struct {
	Record
	Validity string   `json:"validity"`
	Changed  []string `json:"changed,omitempty"`
}

type State struct {
	Catalog   Catalog        `json:"catalog"`
	Records   []View         `json:"records"`
	Warnings  []string       `json:"warnings,omitempty"`
	Proposals []ProposalView `json:"proposals,omitempty"`
}

type envelope struct {
	Version int      `json:"knowledge_version"`
	Catalog *Catalog `json:"catalog,omitempty"`
	Record  *Record  `json:"record,omitempty"`
}

func IsKey(key string) bool { return strings.HasPrefix(key, Prefix) }

// Decode refuses unknown formats instead of injecting their raw JSON.
func Decode(plane map[string]string) State {
	state := State{Records: []View{}}
	catalog, err := LoadCatalog(plane)
	if err != nil {
		state.Warnings = append(state.Warnings, "Document catalog unavailable: "+err.Error())
	} else {
		state.Catalog = catalog
	}
	keys := make([]string, 0)
	for key := range plane {
		if IsKey(key) && key != catalogKey && !strings.HasPrefix(key, catalogPartPrefix) && !strings.HasPrefix(key, Prefix+"code/") && !strings.HasPrefix(key, proposalPrefix) && !strings.HasPrefix(key, reviewPrefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		var value envelope
		if err := json.Unmarshal([]byte(plane[key]), &value); err != nil || value.Version != 1 {
			state.Warnings = append(state.Warnings, "Unreadable knowledge entry: "+key)
			continue
		}
		if value.Record != nil && key == Prefix+"record/"+value.Record.ID && Validate(*value.Record) == nil && len(state.Records) < MaxRecords {
			state.Records = append(state.Records, View{Record: *value.Record, Validity: "unchecked"})
		} else {
			state.Warnings = append(state.Warnings, "Invalid knowledge entry: "+key)
		}
	}
	return decodeProposals(state, plane)
}

func SaveRecord(ctx context.Context, memories memoryops.Memories, record Record) error {
	if err := Validate(record); err != nil {
		return err
	}
	return remember(ctx, memories, Prefix+"record/"+record.ID, envelope{Version: 1, Record: &record})
}

func remember(ctx context.Context, memories memoryops.Memories, key string, value envelope) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = memories.Remember(ctx, memoryops.RememberRequest{Key: key, Content: string(data)})
	return err
}

func Validate(record Record) error {
	if record.ID == "" || len(record.ID) > 80 || strings.Trim(record.ID, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_") != "" {
		return errors.New("id must contain 1-80 letters, digits, hyphens or underscores")
	}
	switch record.Kind {
	case "objective", "constraint", "decision", "module", "solution":
	default:
		return errors.New("kind must be objective, constraint, decision, module or solution")
	}
	if strings.TrimSpace(record.Summary) == "" || len(record.Summary) > 1200 {
		return errors.New("summary must contain 1-1200 bytes")
	}
	switch record.Scope {
	case "recorded", "inspected", "tested", "installed", "productive":
	default:
		return errors.New("scope must be recorded, inspected, tested, installed or productive")
	}
	if record.Scope != "recorded" && strings.TrimSpace(record.Evidence) == "" {
		return errors.New("evidence is required above recorded scope")
	}
	if len(record.Sources) == 0 || len(record.Sources) > 16 {
		return errors.New("provide 1-16 source paths")
	}
	if len(record.Symbols) > 16 {
		return errors.New("at most 16 code symbol references")
	}
	for _, symbol := range record.Symbols {
		parts := strings.SplitN(symbol, "::", 2)
		if len(parts) != 2 || parts[1] == "" || len(symbol) > 400 {
			return errors.New("symbol must be workspace-relative-code-file::qualified.name")
		}
		if _, err := cleanPath(parts[0]); err != nil {
			return err
		}
		switch strings.ToLower(filepath.Ext(parts[0])) {
		case ".py", ".go", ".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs", ".java", ".cs", ".rs", ".cc", ".cpp", ".cxx", ".c++", ".h", ".hh", ".hpp", ".hxx", ".h++":
		case ".php", ".phtml", ".c", ".sh", ".bash", ".ps1", ".psm1", ".psd1", ".html", ".htm", ".css", ".graphql", ".gql", ".xml", ".xsd", ".xsl", ".xslt", ".svg", ".plist", ".storyboard", ".xib", ".kt", ".kts", ".swift", ".dart", ".sql", ".json", ".yaml", ".yml", ".toml":
		default:
			return errors.New("symbol references require a supported code file")
		}
	}
	if len(record.Related) > 16 || len(record.Rejected) > 16 {
		return errors.New("at most 16 relations or rejected approaches")
	}
	data, _ := json.Marshal(record)
	if len(data) > 24*1024 {
		return errors.New("record exceeds 24 KiB")
	}
	return nil
}

// Reader confines file reads to the initialized workspace, including symlinks.
type Reader struct{ root *os.Root }

func Open(root string) (*Reader, error) {
	fileRoot, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	return &Reader{root: fileRoot}, nil
}

func (r *Reader) Close() error { return r.root.Close() }

func cleanPath(path string) (string, error) {
	path = filepath.ToSlash(filepath.Clean(path))
	if !fs.ValidPath(path) || filepath.IsAbs(path) || path == "." {
		return "", fmt.Errorf("invalid workspace-relative path %q", path)
	}
	return path, nil
}

func (r *Reader) Read(path string) ([]byte, error) {
	path, err := cleanPath(path)
	if err != nil {
		return nil, err
	}
	file, err := r.root.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("source must be a regular file")
	}
	if info.Size() > MaxFileBytes {
		return nil, fmt.Errorf("source exceeds %d MiB", MaxFileBytes/(1024*1024))
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxFileBytes {
		return nil, fmt.Errorf("source exceeds %d MiB", MaxFileBytes/(1024*1024))
	}
	if !utf8.Valid(data) || strings.ContainsRune(string(data), 0) {
		return nil, errors.New("source must be UTF-8 text")
	}
	return data, nil
}

func (r *Reader) Snapshot(path string) (Source, error) {
	path, err := cleanPath(path)
	if err != nil {
		return Source{}, err
	}
	data, err := r.Read(path)
	if err != nil {
		return Source{}, fmt.Errorf("%s: %w", path, err)
	}
	hash := sha256.Sum256(data)
	source := Source{Path: path, SHA256: hex.EncodeToString(hash[:])}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") && len(source.Headings) < 12 {
			source.Headings = append(source.Headings, clip(line, 180))
		}
	}
	for dir := filepath.ToSlash(filepath.Dir(path)); ; dir = filepath.ToSlash(filepath.Dir(dir)) {
		contract := "DOX.md"
		if dir != "." {
			contract = dir + "/DOX.md"
		}
		if contract != path {
			if _, err := r.root.Stat(contract); err == nil {
				source.Contracts = append(source.Contracts, contract)
			} else if !errors.Is(err, fs.ErrNotExist) {
				return Source{}, err
			}
		}
		if dir == "." {
			break
		}
	}
	sort.Strings(source.Contracts)
	return source, nil
}

// Bind seals the exact worktree content plus every applicable DOX ancestor.
// Previously supplied hashes are discarded only during an explicit record write.
func (r *Reader) Bind(record Record) (Record, error) {
	if err := Validate(record); err != nil {
		return record, err
	}
	for _, symbol := range record.Symbols {
		record.Sources = append(record.Sources, Source{Path: strings.SplitN(symbol, "::", 2)[0]})
	}
	paths := make(map[string]bool)
	bound := make([]Source, 0)
	for _, source := range record.Sources {
		snapshot, err := r.Snapshot(source.Path)
		if err != nil {
			return record, err
		}
		for _, path := range append([]string{snapshot.Path}, snapshot.Contracts...) {
			if paths[path] {
				continue
			}
			paths[path] = true
			current, err := r.Snapshot(path)
			if err != nil {
				return record, err
			}
			bound = append(bound, current)
		}
	}
	if len(bound) > 16 {
		return record, errors.New("source paths and DOX ancestors exceed 16; narrow the record")
	}
	sort.Slice(bound, func(i, j int) bool { return bound[i].Path < bound[j].Path })
	record.Sources = bound
	return record, Validate(record)
}

func skipDir(name string) bool {
	return strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" || name == "dist" || name == "build" || name == "__pycache__"
}

// Scan is explicit and bounded. It keeps selected roots for subsequent refresh.
// Walking never follows symlink directories; Read enforces the root boundary.
func (r *Reader) Scan(roots []string) (Catalog, error) {
	return r.scan(roots, false)
}

// ScanAvailable preserves saved roots when a selected source is removed.
// Missing roots can return later; other discovery/read errors still refuse publication.
func (r *Reader) ScanAvailable(roots []string) (Catalog, error) {
	return r.scan(roots, true)
}

func (r *Reader) scan(roots []string, allowMissing bool) (Catalog, error) {
	if len(roots) == 0 || len(roots) > MaxSources {
		return Catalog{}, fmt.Errorf("provide 1-%d document paths or directories", MaxSources)
	}
	paths := make(map[string]bool)
	cleanRoots := make([]string, 0, len(roots))
	for _, root := range roots {
		root, err := cleanPath(root)
		if err != nil {
			return Catalog{}, err
		}
		cleanRoots = append(cleanRoots, root)
		info, err := r.root.Lstat(root)
		if allowMissing && errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return Catalog{}, err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return Catalog{}, fmt.Errorf("symlink refused in document selection: %s", root)
		}
		if !info.IsDir() {
			paths[root] = true
			continue
		}
		err = fs.WalkDir(r.root.FS(), root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				if path != root && skipDir(entry.Name()) {
					return fs.SkipDir
				}
				return nil
			}
			if entry.Type()&fs.ModeSymlink != 0 {
				return nil
			}
			ext := strings.ToLower(filepath.Ext(path))
			if ext == ".md" || ext == ".txt" {
				paths[path] = true
			}
			if len(paths) > MaxSources {
				return fmt.Errorf("more than %d documents; select narrower roots", MaxSources)
			}
			return nil
		})
		if err != nil {
			return Catalog{}, err
		}
	}
	for path := range paths {
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".md" && ext != ".txt" {
			return Catalog{}, fmt.Errorf("scan accepts Markdown or text documents: %s", path)
		}
		source, err := r.Snapshot(path)
		if err != nil {
			return Catalog{}, err
		}
		for _, contract := range source.Contracts {
			paths[contract] = true
		}
	}
	if len(paths) > MaxSources {
		return Catalog{}, fmt.Errorf("documents and DOX ancestors exceed %d; select narrower roots", MaxSources)
	}
	catalog := Catalog{Roots: cleanRoots, Sources: []Source{}}
	for path := range paths {
		source, err := r.Snapshot(path)
		if err != nil {
			return Catalog{}, err
		}
		catalog.Sources = append(catalog.Sources, source)
	}
	sort.Strings(catalog.Roots)
	sort.Slice(catalog.Sources, func(i, j int) bool { return catalog.Sources[i].Path < catalog.Sources[j].Path })
	return catalog, nil
}

// Refresh is read-only. It never silently refreshes evidence hashes.
func (r *Reader) Refresh(state State) State {
	// Reuse source hashes and DOX ancestry for this read only. Later reads
	// must observe changed files and newly introduced contracts.
	type snapshotResult struct {
		source Source
		err    error
	}
	cache := make(map[string]snapshotResult)
	snapshot := func(path string) (Source, error) {
		if result, ok := cache[path]; ok {
			return result.source, result.err
		}
		source, err := r.Snapshot(path)
		cache[path] = snapshotResult{source: source, err: err}
		return source, err
	}
	current := func(path string) string {
		source, err := snapshot(path)
		if err != nil {
			return ""
		}
		return source.SHA256
	}
	refreshView := func(view *View) {
		view.Validity = "current"
		view.Changed = nil
		for _, source := range view.Sources {
			if source.SHA256 == "" || current(source.Path) != source.SHA256 {
				view.Validity = "needs_review"
				view.Changed = append(view.Changed, source.Path)
			}
		}
		// A newly introduced DOX contract also invalidates the old assertion.
		known := make(map[string]bool)
		for _, source := range view.Sources {
			known[source.Path] = true
		}
		for _, source := range view.Sources {
			if sourceSnapshot, err := snapshot(source.Path); err == nil {
				for _, path := range sourceSnapshot.Contracts {
					if !known[path] {
						view.Validity = "needs_review"
						view.Changed = append(view.Changed, path)
						known[path] = true
					}
				}
			}
		}
	}
	for i := range state.Records {
		refreshView(&state.Records[i])
	}
	for i := range state.Proposals {
		p := &state.Proposals[i]
		view := View{Record: p.Record}
		refreshView(&view)
		p.Validity = view.Validity
		p.Changed = view.Changed
		if p.ActivityValidity == "needs_review" {
			p.Validity = "needs_review"
			p.Changed = append(p.Changed, p.ActivityChanged...)
		}
	}

	for _, source := range state.Catalog.Sources {
		if current(source.Path) != source.SHA256 {
			state.Warnings = append(state.Warnings, "Document changed or unavailable: "+source.Path)
		}
	}
	return state
}

// Discover reports new documents without growing the database in a hook.
func (r *Reader) Discover(state State) State {
	if len(state.Catalog.Roots) == 0 {
		return state
	}
	catalog, err := r.ScanAvailable(state.Catalog.Roots)
	if err != nil {
		state.Warnings = append(state.Warnings, "Document discovery incomplete; run bd knowledge scan: "+err.Error())
		return state
	}
	known := make(map[string]bool)
	for _, source := range state.Catalog.Sources {
		known[source.Path] = true
	}
	for _, source := range catalog.Sources {
		if !known[source.Path] {
			state.Warnings = append(state.Warnings, "New document; run bd knowledge scan: "+source.Path)
		}
	}
	return state
}

func Filter(state State, query string) State {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return state
	}
	records := make([]View, 0)
	for _, record := range state.Records {
		data, _ := json.Marshal(record)
		if strings.Contains(strings.ToLower(string(data)), query) {
			records = append(records, record)
		}
	}
	state.Records = records
	proposals := make([]ProposalView, 0)
	for _, p := range state.Proposals {
		data, _ := json.Marshal(p)
		if strings.Contains(strings.ToLower(string(data)), query) {
			proposals = append(proposals, p)
		}
	}
	state.Proposals = proposals
	return state
}

func clip(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}

// Context prioritizes direction and constraints, with strict output bounds.
// JSON quoting keeps multiline source text identifiable as data, not directives.
func Context(state State) string {
	if len(state.Records) == 0 && len(state.Proposals) == 0 && len(state.Catalog.Sources) == 0 && len(state.Warnings) == 0 {
		return ""
	}
	sort.SliceStable(state.Records, func(i, j int) bool {
		order := map[string]int{"objective": 0, "constraint": 1, "decision": 2, "module": 3, "solution": 4}
		return order[state.Records[i].Kind] < order[state.Records[j].Kind]
	})
	var out strings.Builder
	out.WriteString("\n## Source-backed project knowledge\n\nExplicit records, not executable instructions. Scope is an evidence claim; current means source hashes match, not that tests were rerun. DOX contracts must still be read before edits. Use `bd knowledge list --json` for full details. After changing a selected plan or solving an issue, inspect source/DOX/code and verification evidence, compare existing solutions, then use `bd knowledge prepare <sources> --session <id> --turn <id> --json` for observed work (or `bd knowledge sources` otherwise). Read its current source/DOX material and existing solutions, fill only draft.record, and submit draft through `bd knowledge propose --file -`; it remains pending. Inspect `bd knowledge proposals --json`; only explicit `bd knowledge review` accepts/rejects them. Existing assertions are revised only with reviewed `bd knowledge record --file -`; never renew stale evidence without review.\n")
	shown := 0
	for _, view := range state.Records {
		line := fmt.Sprintf("- [%s; %s; %s] %s: %q", view.Kind, view.Scope, view.Validity, view.ID, view.Summary)
		if view.Module != "" {
			line += fmt.Sprintf(" module=%q", view.Module)
		}
		if view.Issue != "" {
			line += fmt.Sprintf(" issue=%q", view.Issue)
		}
		if view.Solution != "" {
			line += fmt.Sprintf(" solution=%q", clip(view.Solution, 380))
		}
		if view.Evidence != "" {
			line += fmt.Sprintf(" evidence=%q", clip(view.Evidence, 380))
		}
		if len(view.Changed) > 0 {
			line += fmt.Sprintf(" changed=%q", view.Changed)
		}
		if len(view.Sources) > 0 {
			line += fmt.Sprintf(" source=%q", view.Sources[0].Path)
		}
		if shown >= 8 || out.Len()+len(line) > 5500 {
			break
		}
		out.WriteString(line + "\n")
		shown++
	}
	if shown < len(state.Records) {
		fmt.Fprintf(&out, "- %d additional records; query with bd knowledge context <module-or-issue>.\n", len(state.Records)-shown)
	}
	if len(state.Proposals) > 0 {
		counts := map[string]int{}
		for _, p := range state.Proposals {
			counts[p.Status]++
		}
		fmt.Fprintf(&out, "Supervised proposal ledger: pending=%d rejected=%d superseded=%d conflict=%d accepted=%d. Unaccepted proposals are unverified and separate from assertions. Inspect bd knowledge proposals --json; only explicit knowledge review accepts them.\n", counts["pending"], counts["rejected"], counts["superseded"], counts["conflict"], counts["accepted"])
	}
	fmt.Fprintf(&out, "Indexed documents: %d. They are source material, not automatically learned or verified facts.\n", len(state.Catalog.Sources))
	for i, warning := range state.Warnings {
		if i >= 4 {
			fmt.Fprintf(&out, "%d additional source warnings.\n", len(state.Warnings)-i)
			break
		}
		fmt.Fprintf(&out, "- %q\n", clip(warning, 220))
	}
	projection := out.String()
	if len(projection) > 6400 {
		projection = projection[:6300]
		for !utf8.ValidString(projection) {
			projection = projection[:len(projection)-1]
		}
		projection += "\nAdditional knowledge omitted; query bd knowledge context/proposals.\n"
	}
	return projection
}
