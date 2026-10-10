// Package graphpatch evaluates the private preview's bounded Property Change.
// Semantics follow BDP 53bdbd03136875f952af184fce7b3c7af8f74e96,
// docs/specs/bdp.md (Property changes), packages/server/src/resource-evaluator.ts
// (patch), and packages/protocol/src/read-update-values.ts (PropertyChange).
// This package does not validate effective Types, authorize writes, mint versions,
// or implement a BDP mutation/profile. Its limits are local preview protections.
package graphpatch

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/steveyegge/beads/graphops"
)

const (
	MaxInputBytes      = 1 << 20
	MaxOperations      = 256
	MaxPointerBytes    = 4096
	MaxDepth           = 64
	MaxDocumentBytes   = MaxInputBytes
	MaxPointerSegments = MaxDepth
	// MaxEvaluationBytes bounds cumulative document/value work across operations.
	// It is a conservative work proxy, not a measurement or exact heap limit.
	MaxEvaluationBytes = 16 << 20
)

var (
	ErrInvalid = errors.New("invalid property change")
	ErrLimit   = errors.New("property change preview limit exceeded")
)

// Patch is an admitted, immutable operation list. Parse captures all caller data;
// Apply can be called repeatedly or concurrently without modifying the Patch.
type Patch struct{ operations []operation }
type operation struct {
	kind  string
	path  []string
	value *node
}

// Parse admits an ordered array of closed add/replace/remove operation objects.
// Numeric, Unicode and duplicate-member admission comes from graphops, not
// encoding/json's lossy defaults. The envelope adds two levels above a value.
func Parse(raw []byte) (*Patch, error) {
	if len(raw) > MaxInputBytes {
		return nil, fmt.Errorf("%w: input bytes", ErrLimit)
	}
	if err := checkDepth(raw, MaxDepth+2); err != nil {
		return nil, err
	}
	canonical, err := graphops.CanonicalizeJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if len(canonical) > MaxInputBytes {
		return nil, fmt.Errorf("%w: canonical input bytes", ErrLimit)
	}
	if len(canonical) == 0 || canonical[0] != '[' {
		return nil, fmt.Errorf("%w: expected operation array", ErrInvalid)
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(canonical, &entries); err != nil {
		return nil, fmt.Errorf("%w: operation array", ErrInvalid)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("%w: at least one operation is required", ErrInvalid)
	}
	if len(entries) > MaxOperations {
		return nil, fmt.Errorf("%w: operation count", ErrLimit)
	}
	p := &Patch{operations: make([]operation, 0, len(entries))}
	for i, entry := range entries {
		op, err := parseOperation(entry)
		if err != nil {
			return nil, fmt.Errorf("operation %d: %w", i, err)
		}
		p.operations = append(p.operations, op)
	}
	return p, nil
}

func parseOperation(raw []byte) (operation, error) {
	var fields map[string]json.RawMessage
	if len(raw) == 0 || raw[0] != '{' || json.Unmarshal(raw, &fields) != nil {
		return operation{}, fmt.Errorf("%w: expected operation object", ErrInvalid)
	}
	var kind, path string
	if err := json.Unmarshal(fields["op"], &kind); err != nil || (kind != "add" && kind != "replace" && kind != "remove") {
		return operation{}, fmt.Errorf("%w: only add, replace and remove are supported", ErrInvalid)
	}
	pathRaw, exists := fields["path"]
	if !exists || len(pathRaw) == 0 || pathRaw[0] != '"' || json.Unmarshal(pathRaw, &path) != nil {
		return operation{}, fmt.Errorf("%w: path must be a string", ErrInvalid)
	}
	count := 3
	if kind == "remove" {
		count = 2
	}
	if len(fields) != count {
		return operation{}, fmt.Errorf("%w: unexpected or missing operation member", ErrInvalid)
	}
	for key := range fields {
		if key != "op" && key != "path" && (key != "value" || kind == "remove") {
			return operation{}, fmt.Errorf("%w: unexpected operation member", ErrInvalid)
		}
	}
	tokens, err := pointer(path)
	if err != nil {
		return operation{}, err
	}
	op := operation{kind: kind, path: tokens}
	if kind != "remove" {
		value, exists := fields["value"]
		if !exists {
			return operation{}, fmt.Errorf("%w: value is required", ErrInvalid)
		}
		op.value, err = parseNode(value, 0)
		if err != nil {
			return operation{}, err
		}
	}
	return op, nil
}

func pointer(path string) ([]string, error) {
	if len(path) > MaxPointerBytes {
		return nil, fmt.Errorf("%w: pointer bytes", ErrLimit)
	}
	if path == "" {
		return nil, nil
	}
	if path[0] != '/' {
		return nil, fmt.Errorf("%w: pointer must be empty or begin with /", ErrInvalid)
	}
	tokens := strings.Split(path[1:], "/")
	if len(tokens) > MaxDepth {
		return nil, fmt.Errorf("%w: pointer depth", ErrLimit)
	}
	for i, token := range tokens {
		for j := 0; j < len(token); j++ {
			if token[j] == '~' {
				j++
				if j == len(token) || (token[j] != '0' && token[j] != '1') {
					return nil, fmt.Errorf("%w: malformed pointer escape", ErrInvalid)
				}
			}
		}
		tokens[i] = strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
	}
	return tokens, nil
}

// Apply evaluates against the supplied predecessor without modifying it. Missing
// root (after remove) is separate from JSON null. Only the completed result must
// be an object; intermediate roots may have another kind or be absent.
func (p *Patch) Apply(before graphops.Properties) (graphops.Properties, error) {
	if p == nil || len(p.operations) == 0 {
		return graphops.Properties{}, fmt.Errorf("%w: uninitialized patch", ErrInvalid)
	}
	raw := before.Bytes()
	if len(raw) > MaxInputBytes {
		return graphops.Properties{}, fmt.Errorf("%w: current properties bytes", ErrLimit)
	}
	if err := checkDepth(raw, MaxDepth); err != nil {
		return graphops.Properties{}, err
	}
	current, err := parseNode(raw, 0)
	if err != nil {
		return graphops.Properties{}, err
	}
	evaluationBytes := 0
	for i, op := range p.operations {
		// Charge before edit can copy containers. Even small operation lists may
		// repeatedly traverse a large current value; per-document limits alone
		// do not bound that cumulative work tightly enough for this preview.
		cost := 0
		if current != nil {
			cost += current.size
		}
		if op.value != nil {
			cost += op.value.size
		}
		if cost > MaxEvaluationBytes-evaluationBytes {
			return graphops.Properties{}, fmt.Errorf("operation %d: %w: cumulative evaluation bytes", i, ErrLimit)
		}
		evaluationBytes += cost
		current, err = edit(current, op.path, op)
		if err != nil {
			return graphops.Properties{}, fmt.Errorf("operation %d: %w", i, err)
		}
	}
	if current == nil || current.object == nil {
		return graphops.Properties{}, fmt.Errorf("%w: resulting properties must be an object", ErrInvalid)
	}
	result, err := graphops.NewProperties(current.encode())
	if err != nil {
		return graphops.Properties{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return result, nil
}

// checkDepth bounds nesting BEFORE graphops' recursive canonicalizer. Quoted
// delimiters and escaped quotes do not affect depth; graphops checks syntax.
func checkDepth(raw []byte, limit int) error {
	depth := 0
	quoted, escaped := false, false
	for _, b := range raw {
		if quoted {
			if escaped {
				escaped = false
			} else if b == '\\' {
				escaped = true
			} else if b == '"' {
				quoted = false
			}
			continue
		}
		switch b {
		case '"':
			quoted = true
		case '{', '[':
			depth++
			if depth > limit {
				return fmt.Errorf("%w: value depth", ErrLimit)
			}
		case '}', ']':
			depth--
		}
	}
	return nil
}

// node is immutable. Edits copy only the path's containers. Cached canonical
// size/depth reject oversized results before serializing them. Nodes supplied
// by Patch are safe to share across evaluations because no edit mutates a node.
type node struct {
	object      map[string]*node
	array       []*node
	scalar      []byte
	size, depth int
}

func parseNode(raw []byte, parents int) (*node, error) {
	n := &node{size: len(raw)}
	if len(raw) > MaxInputBytes {
		return nil, fmt.Errorf("%w: value bytes", ErrLimit)
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("%w: missing value", ErrInvalid)
	}
	switch raw[0] {
	case '{':
		if parents >= MaxDepth {
			return nil, fmt.Errorf("%w: value depth", ErrLimit)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return nil, fmt.Errorf("%w: object", ErrInvalid)
		}
		n.object = make(map[string]*node, len(fields))
		n.depth = 1
		for key, value := range fields {
			child, err := parseNode(value, parents+1)
			if err != nil {
				return nil, err
			}
			n.object[key] = child
			n.depth = max(n.depth, 1+child.depth)
		}
	case '[':
		if parents >= MaxDepth {
			return nil, fmt.Errorf("%w: value depth", ErrLimit)
		}
		var values []json.RawMessage
		if err := json.Unmarshal(raw, &values); err != nil {
			return nil, fmt.Errorf("%w: array", ErrInvalid)
		}
		n.array = make([]*node, len(values))
		n.depth = 1
		for i, value := range values {
			child, err := parseNode(value, parents+1)
			if err != nil {
				return nil, err
			}
			n.array[i] = child
			n.depth = max(n.depth, 1+child.depth)
		}
	default:
		n.scalar = bytes.Clone(raw)
	}
	return n, nil
}

func edit(current *node, path []string, op operation) (*node, error) {
	if len(path) == 0 {
		if op.kind != "add" && current == nil {
			return nil, fmt.Errorf("%w: target does not exist", ErrInvalid)
		}
		if op.kind == "remove" {
			return nil, nil
		}
		return op.value, nil
	}
	if current == nil {
		return nil, fmt.Errorf("%w: parent does not exist", ErrInvalid)
	}
	key := path[0]
	switch {
	case current.object != nil:
		old, exists := current.object[key]
		if len(path) > 1 && !exists {
			return nil, fmt.Errorf("%w: parent does not exist", ErrInvalid)
		}
		if len(path) == 1 && op.kind != "add" && !exists {
			return nil, fmt.Errorf("%w: target does not exist", ErrInvalid)
		}
		child, err := edit(old, path[1:], op)
		if err != nil {
			return nil, err
		}
		// The previous document and every value are independently bounded. Precheck
		// byte growth before copying even the bounded parent map.
		size := current.size
		if exists {
			size -= old.size
		}
		if child != nil {
			size += child.size
		}
		if !exists && child != nil {
			size += len(quoted(key)) + 1
			if len(current.object) > 0 {
				size++
			}
		}
		if exists && child == nil {
			size -= len(quoted(key)) + 1
			if len(current.object) > 1 {
				size--
			}
		}
		if size > MaxInputBytes {
			return nil, fmt.Errorf("%w: working properties bytes", ErrLimit)
		}
		next := &node{object: make(map[string]*node, len(current.object)), size: size, depth: 1}
		for k, v := range current.object {
			if k != key {
				next.object[k] = v
			}
		}
		if child != nil {
			next.object[key] = child
		}
		for _, v := range next.object {
			next.depth = max(next.depth, 1+v.depth)
		}
		return bounded(next)
	case current.array != nil:
		index, err := arrayIndex(key, len(current.array), len(path) == 1 && op.kind == "add")
		if err != nil {
			return nil, err
		}
		insert := len(path) == 1 && op.kind == "add"
		var old *node
		if !insert {
			old = current.array[index]
		}
		child, err := edit(old, path[1:], op)
		if err != nil {
			return nil, err
		}
		size := current.size
		if old != nil {
			size -= old.size
		}
		if child != nil {
			size += child.size
		}
		if insert && len(current.array) > 0 {
			size++
		}
		if child == nil && len(current.array) > 1 {
			size--
		}
		if size > MaxInputBytes {
			return nil, fmt.Errorf("%w: working properties bytes", ErrLimit)
		}
		count := len(current.array)
		if insert {
			count++
		}
		if child == nil {
			count--
		}
		next := &node{array: make([]*node, 0, count), size: size, depth: 1}
		next.array = append(next.array, current.array[:index]...)
		if child != nil {
			next.array = append(next.array, child)
		}
		tail := index
		if !insert {
			tail++
		}
		next.array = append(next.array, current.array[tail:]...)
		for _, v := range next.array {
			next.depth = max(next.depth, 1+v.depth)
		}
		return bounded(next)
	default:
		return nil, fmt.Errorf("%w: parent is not a container", ErrInvalid)
	}
}

func bounded(n *node) (*node, error) {
	if n.depth > MaxDepth {
		return nil, fmt.Errorf("%w: working properties depth", ErrLimit)
	}
	return n, nil
}
func arrayIndex(key string, length int, adding bool) (int, error) {
	if key == "-" && adding {
		return length, nil
	}
	if key == "" || (len(key) > 1 && key[0] == '0') {
		return 0, fmt.Errorf("%w: invalid array index", ErrInvalid)
	}
	for _, c := range key {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("%w: invalid array index", ErrInvalid)
		}
	}
	index, err := strconv.ParseUint(key, 10, 64)
	if err != nil || index > uint64(length) || (!adding && index == uint64(length)) {
		return 0, fmt.Errorf("%w: array index does not exist", ErrInvalid)
	}
	return int(index), nil
}

func quoted(s string) []byte {
	// s has already passed graphops' Unicode admission. Canonicalization avoids
	// encoding/json's HTML/U+2028 escaping changing the cached byte-size budget.
	raw, _ := json.Marshal(s)
	canonical, _ := graphops.CanonicalizeJSON(raw)
	return canonical
}
func (n *node) encode() []byte {
	out := make([]byte, 0, n.size)
	return n.appendJSON(out)
}
func (n *node) appendJSON(out []byte) []byte {
	switch {
	case n.object != nil:
		out = append(out, '{')
		first := true
		for key, value := range n.object {
			if !first {
				out = append(out, ',')
			}
			first = false
			out = append(out, quoted(key)...)
			out = append(out, ':')
			out = value.appendJSON(out)
		}
		return append(out, '}')
	case n.array != nil:
		out = append(out, '[')
		for i, value := range n.array {
			if i > 0 {
				out = append(out, ',')
			}
			out = value.appendJSON(out)
		}
		return append(out, ']')
	default:
		return append(out, n.scalar...)
	}
}
