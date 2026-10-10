package graphread

// Selector semantics and conformance cases are adapted from gastownhall/bdp
// packages/server/src/read-selector.ts at
// 5b4eb46c7f21f2272f4da5a230f042ce753cb304. This module deliberately has no HTTP
// mappings. Paths see only the BDP selection projection, never response metadata.

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"unicode/utf8"
)

// SelectorLimits are explicit operational bounds, with no implicit defaults.
// AST depth and node accounting include each singular path segment and group.
type SelectorLimits struct{ Bytes, Depth, Nodes int }

// SelectorError classifies parsing and bounds failures without protocol mappings.
// Offset is a UTF-8 byte offset, or -1 when the failure is not source-local.
// Limit and Actual are zero for syntax and unsupported-feature failures.
type SelectorError struct {
	Code                  string
	Message               string
	Offset, Limit, Actual int
}

func (e *SelectorError) Error() string { return e.Message }

// Selector is an immutable, compiled Read Selector, safe for concurrent matches.
// Parsing and evaluation are iterative, including when callers set large limits.
type Selector struct{ nodes []selectorNode }

type selectorSegment struct {
	name    string
	index   int64
	isIndex bool
}
type selectorNode struct {
	kind         string
	segments     []selectorSegment
	literal      any
	left, right  int
	depth, count int
}

// CompileSelector parses and bounds the RFC 9535 subset used by BDP Read.
func CompileSelector(source string, limits SelectorLimits) (*Selector, error) {
	if limits.Bytes <= 0 || limits.Depth <= 0 || limits.Nodes <= 0 {
		return nil, fmt.Errorf("Selector limits must be positive integers")
	}
	if len(source) > limits.Bytes {
		return nil, selectorBound("source-bytes-limit-exceeded", -1, limits.Bytes, len(source))
	}
	if !utf8.ValidString(source) {
		return nil, selectorFailure("syntax", "Selector source must be valid UTF-8", 0)
	}
	tokens, err := selectorTokenize(source)
	if err != nil {
		return nil, err
	}
	return selectorParse(tokens, limits)
}

func selectorFailure(code, message string, offset int) error {
	return &SelectorError{Code: code, Message: fmt.Sprintf("%s at byte offset %d", message, offset), Offset: offset}
}
func selectorSyntax(message string, offset int) error {
	return selectorFailure("syntax", message, offset)
}
func selectorUnsupported(message string, offset int) error {
	return selectorFailure("unsupported-feature", message, offset)
}
func selectorBound(code string, offset, limit, actual int) error {
	return &SelectorError{Code: code, Message: fmt.Sprintf("Selector %s: actual %d exceeds limit %d", code, actual, limit), Offset: offset, Limit: limit, Actual: actual}
}
func selectorCheckSize(depth, nodes int, limits SelectorLimits, offset int) error {
	if depth > limits.Depth {
		return selectorBound("ast-depth-limit-exceeded", offset, limits.Depth, depth)
	}
	if nodes > limits.Nodes {
		return selectorBound("ast-nodes-limit-exceeded", offset, limits.Nodes, nodes)
	}
	return nil
}

type selectorValue struct {
	value   any
	present bool
	boolean bool
}

// Matches evaluates a complete JSON-decoded Resource without changing it.
// Candidate numbers may be float64 or json.Number. Missing and JSON null differ.
// Stored References are compared exactly, with no URI normalization or unpinning.
func (s *Selector) Matches(candidate map[string]any) bool {
	values := make([]selectorValue, len(s.nodes))
	for i, n := range s.nodes {
		switch n.kind {
		case "path":
			values[i].value, values[i].present = selectorResolve(n.segments, candidate)
			values[i].boolean = values[i].present
		case "literal":
			values[i] = selectorValue{value: n.literal, present: true}
		case "!":
			values[i].boolean = !values[n.left].boolean
		case "group":
			values[i].boolean = values[n.left].boolean
		case "&&":
			values[i].boolean = values[n.left].boolean && values[n.right].boolean
		case "||":
			values[i].boolean = values[n.left].boolean || values[n.right].boolean
		default:
			values[i].boolean = selectorCompare(n.kind, values[n.left], values[n.right])
		}
	}
	return len(values) > 0 && values[len(values)-1].boolean
}

func selectorResolve(segments []selectorSegment, candidate map[string]any) (any, bool) {
	var value any = candidate
	for position, segment := range segments {
		if segment.isIndex {
			array, ok := value.([]any)
			if !ok {
				return nil, false
			}
			index := segment.index
			if index < 0 {
				index += int64(len(array))
			}
			if index < 0 || index >= int64(len(array)) {
				return nil, false
			}
			value = array[index]
			continue
		}
		if position == 0 {
			switch segment.name {
			case "id", "type", "source", "target", "properties":
			default:
				return nil, false
			}
		}
		object, ok := value.(map[string]any)
		if !ok {
			return nil, false
		}
		value, ok = object[segment.name]
		if !ok {
			return nil, false
		}
	}
	return value, true
}

func selectorNumber(value any) (float64, bool) {
	var number float64
	switch v := value.(type) {
	case float64:
		number = v
	case json.Number:
		parsed, err := strconv.ParseFloat(string(v), 64)
		if err != nil {
			return 0, false
		}
		number = parsed
	default:
		return 0, false
	}
	return number, !math.IsInf(number, 0) && !math.IsNaN(number)
}

func selectorEqual(left, right any) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	if l, ok := selectorNumber(left); ok {
		r, ok := selectorNumber(right)
		return ok && l == r
	}
	switch l := left.(type) {
	case string:
		r, ok := right.(string)
		return ok && l == r
	case bool:
		r, ok := right.(bool)
		return ok && l == r
	}
	return false
}

func selectorCompare(operator string, left, right selectorValue) bool {
	if operator == "==" || operator == "!=" {
		equal := left.present && right.present && selectorEqual(left.value, right.value)
		if operator == "!=" {
			return !equal
		}
		return equal
	}
	if !left.present || !right.present {
		return false
	}
	if l, ok := selectorNumber(left.value); ok {
		r, ok := selectorNumber(right.value)
		if !ok {
			return false
		}
		switch operator {
		case "<":
			return l < r
		case "<=":
			return l <= r
		case ">":
			return l > r
		case ">=":
			return l >= r
		}
	}
	l, lok := left.value.(string)
	r, rok := right.value.(string)
	if !lok || !rok || !utf8.ValidString(l) || !utf8.ValidString(r) {
		return false
	}
	// Lexicographic UTF-8 order equals Unicode scalar order for valid strings.
	switch operator {
	case "<":
		return l < r
	case "<=":
		return l <= r
	case ">":
		return l > r
	case ">=":
		return l >= r
	}
	return false
}
