package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage/graphstore"
)

// graphProjectCompleteRecords changes only the CLI representation. The store
// still reads and writes its original snapshots, including their version field.
func graphProjectCompleteRecords(result any) (json.RawMessage, error) {
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	return graphProjectRecordJSON(raw)
}

func graphProjectRecordJSON(raw json.RawMessage) (json.RawMessage, error) {
	return graphProjectRecordJSONIn(raw, false)
}

func graphProjectRecordJSONIn(raw json.RawMessage, ownedComparisonValue bool) (json.RawMessage, error) {
	value := bytes.TrimSpace(raw)
	if len(value) == 0 {
		return raw, nil
	}
	switch value[0] {
	case '{':
		var members map[string]json.RawMessage
		if err := json.Unmarshal(value, &members); err != nil {
			return nil, err
		}
		if graphIsCompleteRecord(members) {
			delete(members, "version")
		}
		if attribution, ok := members["attribution"]; ok {
			projected, present, err := graphProjectCarriedAttribution(attribution)
			if err != nil {
				return nil, err
			}
			if present {
				members["attribution"] = projected
			} else {
				delete(members, "attribution")
			}
		}
		ownedChange := string(members["area"]) == `"owned"`
		for key, member := range members {
			// These are caller-authored values, not graph record wrappers. An
			// object inside them may legitimately have a member named version.
			switch key {
			case "properties", "metadata", "attribution":
				continue
			case "value":
				if !ownedComparisonValue {
					continue
				}
			}
			projected, err := graphProjectRecordJSONIn(member, ownedComparisonValue || (ownedChange && (key == "from" || key == "to")))
			if err != nil {
				return nil, err
			}
			members[key] = projected
		}
		return json.Marshal(members)
	case '[':
		var items []json.RawMessage
		if err := json.Unmarshal(value, &items); err != nil {
			return nil, err
		}
		for i, item := range items {
			projected, err := graphProjectRecordJSONIn(item, ownedComparisonValue)
			if err != nil {
				return nil, err
			}
			items[i] = projected
		}
		return json.Marshal(items)
	default:
		return raw, nil
	}
}

// Only storage attribution has actor/status/recordedAt. Caller-authored
// properties and metadata never reach this function, even if they contain an
// unrelated member named attribution.
func graphProjectCarriedAttribution(raw json.RawMessage) (json.RawMessage, bool, error) {
	value := bytes.TrimSpace(raw)
	if bytes.Equal(value, []byte("null")) {
		return nil, false, nil
	}
	if len(value) == 0 || value[0] != '{' {
		// Native Issue version rows carry a scalar attribution label,
		// independent of the BDP carried-attribution envelope.
		return raw, true, nil
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil {
		return nil, false, fmt.Errorf("%w: decode stored attribution: %v", graphstore.ErrInvalidStore, err)
	}
	if _, ok := members["actor"]; !ok {
		return raw, true, nil
	}
	var actor, status string
	if err := json.Unmarshal(members["actor"], &actor); err != nil {
		return nil, false, fmt.Errorf("%w: decode stored attribution actor: %v", graphstore.ErrInvalidStore, err)
	}
	if err := json.Unmarshal(members["status"], &status); err != nil {
		return nil, false, fmt.Errorf("%w: decode stored attribution status: %v", graphstore.ErrInvalidStore, err)
	}
	if actor == "" && (status == "unknown" || status == "") {
		return nil, false, nil
	}
	if actor == "" {
		return nil, false, fmt.Errorf("%w: stored attribution has no actor", graphstore.ErrInvalidStore)
	}
	switch status {
	case "claimed":
		members["basis"] = json.RawMessage(`"writer-supplied"`)
	case "unknown":
		// Defensive reader path for imported historical records with an actor but no attested basis.
		members["basis"] = json.RawMessage(`"unknown"`)
	default:
		return nil, false, fmt.Errorf("%w: unsupported stored attribution status %q", graphstore.ErrInvalidStore, status)
	}
	delete(members, "status")
	projected, err := json.Marshal(members)
	return projected, true, err
}

func graphPublicAttributionBasis(status string) (string, error) {
	switch status {
	case "claimed":
		return "writer-supplied", nil
	case "unknown":
		// Defensive reader path for imported historical records with an actor but no attested basis.
		return "unknown", nil
	default:
		return "", fmt.Errorf("%w: unsupported stored attribution status %q", graphstore.ErrInvalidStore, status)
	}
}

func graphIsCompleteRecord(members map[string]json.RawMessage) bool {
	for _, key := range []string{"id", "type", "revision", "version", "properties"} {
		if _, present := members[key]; !present {
			return false
		}
	}
	var id string
	if err := json.Unmarshal(members["id"], &id); err != nil {
		return false
	}
	return (strings.HasPrefix(id, "https://") || strings.HasPrefix(id, "http://")) &&
		(strings.Contains(id, "/beads/") || strings.Contains(id, "/links/"))
}
