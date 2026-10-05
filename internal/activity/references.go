package activity

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
)

const MaxReferences = 16

// Reference pins an observed event, including its original observation time.
// It identifies reported provenance, never verified evidence or authority.
type Reference struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
	TurnID    string `json:"turn_id,omitempty"`
	SHA256    string `json:"sha256"`
}

func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func decodeEvent(key, raw string) (Event, error) {
	var e Event
	if len(raw) > MaxRecord || json.Unmarshal([]byte(raw), &e) != nil || e.Version != 1 || !validDigest(e.ID) || key != Prefix+"event/"+e.ID || !validIdentity(e.SessionID, false) || !validIdentity(e.TurnID, true) {
		return e, errors.New("invalid activity envelope")
	}
	if _, err := time.Parse(time.RFC3339Nano, e.ObservedAt); err != nil {
		return e, errors.New("invalid observation time")
	}
	switch e.Kind {
	case "operation":
		if e.TurnID == "" || !validIdentity(e.ToolUseID, false) || e.Tool == "" {
			return e, errors.New("operation lacks identity")
		}
	case "turn_end":
		if e.TurnID == "" || e.SummaryScope != "reported" {
			return e, errors.New("invalid reported handoff")
		}
	case "session_end":
	default:
		return e, errors.New("unknown activity kind")
	}
	identity := e
	identity.ID = ""
	identity.ObservedAt = ""
	data, _ := json.Marshal(identity)
	if Digest(data) != e.ID {
		return e, errors.New("activity content digest mismatch")
	}
	return e, nil
}

func ValidateReferences(refs []Reference) error {
	if len(refs) > MaxReferences {
		return fmt.Errorf("at most %d activity events", MaxReferences)
	}
	previous := ""
	for _, ref := range refs {
		if !validDigest(ref.ID) || !validDigest(ref.SHA256) || !validIdentity(ref.SessionID, false) || !validIdentity(ref.TurnID, true) || ref.ID <= previous {
			return errors.New("invalid, duplicate or unordered activity references")
		}
		previous = ref.ID
	}
	return nil
}

// SelectReferences confines provenance to the selected project's memory plane.
func SelectReferences(plane map[string]string, ids []string) ([]Reference, error) {
	if len(ids) > MaxReferences {
		return nil, fmt.Errorf("at most %d activity events", MaxReferences)
	}
	refs := make([]Reference, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		if !validDigest(id) || seen[id] {
			return nil, errors.New("invalid or duplicate activity event ID")
		}
		seen[id] = true
		key := Prefix + "event/" + id
		raw, ok := plane[key]
		if !ok {
			return nil, fmt.Errorf("activity event %s is absent from this workspace", id)
		}
		e, err := decodeEvent(key, raw)
		if err != nil {
			return nil, fmt.Errorf("activity event %s: %w", id, err)
		}
		refs = append(refs, Reference{ID: id, SessionID: e.SessionID, TurnID: e.TurnID, SHA256: Digest([]byte(raw))})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].ID < refs[j].ID })
	return refs, nil
}

// ChangedReferences never renews origins; an absent or changed event needs review.
func ChangedReferences(plane map[string]string, refs []Reference) []string {
	var changed []string
	for _, ref := range refs {
		current, err := SelectReferences(plane, []string{ref.ID})
		if err != nil || current[0] != ref {
			changed = append(changed, "activity:"+ref.ID)
		}
	}
	return changed
}
