package graphstore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strings"

	graph "github.com/steveyegge/beads/graphops"
)

// VersionComparison is an experimental local projection, not a public History
// record or a declaration that the unsupported model fields are absent.
type VersionComparison struct {
	Resource    VersionComparisonResource `json:"resource"`
	From        VersionComparisonEndpoint `json:"from"`
	To          VersionComparisonEndpoint `json:"to"`
	Compared    []string                  `json:"compared"`
	Unsupported []string                  `json:"unsupported"`
	Changes     []VersionChange           `json:"changes"`
}
type VersionComparisonResource struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Source string `json:"source,omitempty"`
	Target string `json:"target,omitempty"`
}
type VersionComparisonEndpoint struct {
	Version     string      `json:"version"`
	Attribution Attribution `json:"attribution"`
}
type VersionChange struct {
	Area   string       `json:"area"`
	Member string       `json:"member,omitempty"`
	ID     string       `json:"id,omitempty"`
	From   VersionValue `json:"from"`
	To     VersionValue `json:"to"`
}
type VersionValue struct {
	Present bool            `json:"present"`
	Value   json.RawMessage `json:"value,omitempty"`
}

// CompareVersions acquires and validates both complete operands before any
// comparison, even when both requested tokens are equal. It performs no writes.
func (s *Store) CompareVersions(ctx context.Context, path, from, to string) (VersionComparison, error) {
	pair, err := s.ReadVersionPair(ctx, path, from, to)
	if err != nil {
		return VersionComparison{}, err
	}
	return compareVersionRecords(pair.From, pair.To)
}

type comparisonRecord struct {
	resource   VersionComparisonResource
	endpoint   VersionComparisonEndpoint
	kind       string
	properties map[string]json.RawMessage
	metadata   map[string]json.RawMessage
	owned      map[string]json.RawMessage
}

func comparisonProjection(value any) (comparisonRecord, error) {
	var r comparisonRecord
	var properties any
	var metadata json.RawMessage
	var owned []json.RawMessage
	switch v := value.(type) {
	case Record:
		r.resource = VersionComparisonResource{ID: v.ID, Type: v.Type}
		r.endpoint = VersionComparisonEndpoint{v.Version, v.Attribution}
		r.kind, properties, owned, metadata = "memory", v.Properties, v.Owned, v.Metadata
	case IssueRecord:
		if v.Properties == nil {
			return r, fmt.Errorf("%w: comparison Issue properties missing", ErrInvalidStore)
		}
		r.resource = VersionComparisonResource{ID: v.ID, Type: v.Type}
		r.endpoint = VersionComparisonEndpoint{v.Version, v.Attribution}
		r.kind, properties, owned, metadata = "issue", v.Properties, v.Owned, v.Metadata
	case LinkRecord:
		r.resource = VersionComparisonResource{ID: v.ID, Type: v.Type, Source: v.Source, Target: v.Target}
		r.endpoint = VersionComparisonEndpoint{v.Version, v.Attribution}
		r.kind, properties, metadata = "link", v.Properties, v.Metadata
	default:
		return r, fmt.Errorf("%w: unsupported retained comparison record", ErrInvalidStore)
	}
	raw, err := json.Marshal(properties)
	if err != nil {
		return r, fmt.Errorf("%w: comparison properties: %v", ErrInvalidStore, err)
	}
	if err := json.Unmarshal(raw, &r.properties); err != nil || r.properties == nil {
		return r, fmt.Errorf("%w: comparison properties must be an object", ErrInvalidStore)
	}
	canonicalMetadata, err := commonMetadata(metadata)
	if err != nil || !bytes.Equal(canonicalMetadata, metadata) || json.Unmarshal(canonicalMetadata, &r.metadata) != nil || r.metadata == nil {
		return r, fmt.Errorf("%w: comparison metadata must be a canonical object", ErrInvalidStore)
	}
	r.owned = make(map[string]json.RawMessage, len(owned))
	if r.kind != "link" && owned == nil {
		return r, fmt.Errorf("%w: comparison owned set missing", ErrInvalidStore)
	}
	for _, raw := range owned {
		var link struct {
			ID     string `json:"id"`
			Type   string `json:"type"`
			Source string `json:"source"`
			Target string `json:"target"`
		}
		if err := json.Unmarshal(raw, &link); err != nil || link.ID == "" || link.Type == "" || link.Source != r.resource.ID || link.Target == "" {
			return r, fmt.Errorf("%w: invalid comparison owned Link", ErrInvalidStore)
		}
		if _, exists := r.owned[link.ID]; exists {
			return r, fmt.Errorf("%w: duplicate comparison owned identity", ErrInvalidStore)
		}
		r.owned[link.ID] = bytes.Clone(raw)
	}
	return r, nil
}

// compareVersionRecords receives already validated retained records. JSON
// numbers are nevertheless compared exactly, without a binary64 conversion.
func compareVersionRecords(from, to any) (VersionComparison, error) {
	a, err := comparisonProjection(from)
	if err != nil {
		return VersionComparison{}, err
	}
	b, err := comparisonProjection(to)
	if err != nil {
		return VersionComparison{}, err
	}
	if a.kind != b.kind || a.resource != b.resource || a.resource.ID == "" || a.resource.Type == "" {
		return VersionComparison{}, fmt.Errorf("%w: comparison immutable identity differs", ErrInvalidStore)
	}
	result := VersionComparison{Resource: a.resource, From: a.endpoint, To: b.endpoint, Compared: []string{"properties", "metadata"}, Unsupported: []string{}, Changes: []VersionChange{}}
	if a.kind != "link" {
		result.Compared = append(result.Compared, "owned")
	}
	if a.kind == "memory" {
		result.Unsupported = append(result.Unsupported, "inception", "derivation")
	}
	for _, area := range []struct {
		name     string
		from, to map[string]json.RawMessage
	}{{"properties", a.properties, b.properties}, {"metadata", a.metadata, b.metadata}, {"owned", a.owned, b.owned}} {
		for _, key := range comparisonKeys(area.from, area.to) {
			before, haveBefore := area.from[key]
			after, haveAfter := area.to[key]
			if haveBefore && haveAfter {
				if area.name == "owned" {
					var x, y struct{ ID, Type, Source, Target string }
					if json.Unmarshal(before, &x) != nil || json.Unmarshal(after, &y) != nil || x != y {
						return VersionComparison{}, fmt.Errorf("%w: owned Link immutable identity differs", ErrInvalidStore)
					}
				}
				equal, err := comparisonJSONEqual(before, after)
				if err != nil {
					return VersionComparison{}, fmt.Errorf("%w: comparison value: %v", ErrInvalidStore, err)
				}
				if equal {
					continue
				}
			}
			change := VersionChange{Area: area.name, From: VersionValue{haveBefore, bytes.Clone(before)}, To: VersionValue{haveAfter, bytes.Clone(after)}}
			if area.name == "properties" || area.name == "metadata" {
				change.Member = key
			} else {
				change.ID = key
			}
			result.Changes = append(result.Changes, change)
		}
	}
	return result, nil
}

func comparisonKeys(a, b map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(a)+len(b))
	for key := range a {
		keys = append(keys, key)
	}
	for key := range b {
		if _, ok := a[key]; !ok {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return graph.CompareCodeUnits(keys[i], keys[j]) < 0 })
	return keys
}

func comparisonJSONEqual(a, b []byte) (bool, error) {
	decode := func(raw []byte) (any, error) {
		if !json.Valid(raw) {
			return nil, fmt.Errorf("invalid JSON")
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var value any
		err := decoder.Decode(&value)
		return value, err
	}
	av, err := decode(a)
	if err != nil {
		return false, err
	}
	bv, err := decode(b)
	if err != nil {
		return false, err
	}
	type pair struct{ a, b any }
	pending := []pair{{av, bv}}
	for len(pending) > 0 {
		p := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		switch a := p.a.(type) {
		case nil:
			if p.b != nil {
				return false, nil
			}
		case bool:
			if b, ok := p.b.(bool); !ok || a != b {
				return false, nil
			}
		case string:
			if b, ok := p.b.(string); !ok || a != b {
				return false, nil
			}
		case json.Number:
			b, ok := p.b.(json.Number)
			if !ok || comparisonNumber(a.String()) != comparisonNumber(b.String()) {
				return false, nil
			}
		case []any:
			b, ok := p.b.([]any)
			if !ok || len(a) != len(b) {
				return false, nil
			}
			for i := range a {
				pending = append(pending, pair{a[i], b[i]})
			}
		case map[string]any:
			b, ok := p.b.(map[string]any)
			if !ok || len(a) != len(b) {
				return false, nil
			}
			for key, value := range a {
				other, ok := b[key]
				if !ok {
					return false, nil
				}
				pending = append(pending, pair{value, other})
			}
		default:
			return false, fmt.Errorf("unsupported JSON value")
		}
	}
	return true, nil
}

// Normalize a syntactically valid JSON number into a signed coefficient and
// decimal exponent. Never expand the exponent or convert through floating point.
func comparisonNumber(value string) string {
	negative := strings.HasPrefix(value, "-")
	value = strings.TrimPrefix(value, "-")
	exponent := new(big.Int)
	if i := strings.IndexAny(value, "eE"); i >= 0 {
		exponent.SetString(value[i+1:], 10)
		value = value[:i]
	}
	if i := strings.IndexByte(value, '.'); i >= 0 {
		exponent.Sub(exponent, big.NewInt(int64(len(value)-i-1)))
		value = value[:i] + value[i+1:]
	}
	value = strings.TrimLeft(value, "0")
	if value == "" {
		return "0"
	}
	trimmed := strings.TrimRight(value, "0")
	exponent.Add(exponent, big.NewInt(int64(len(value)-len(trimmed))))
	if negative {
		trimmed = "-" + trimmed
	}
	return trimmed + "e" + exponent.String()
}
