package bdpwire

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
)

// History wire support does not authorize capability advertisement.
type ContextState string
type ContextTimeState string

const (
	ContextPresent          ContextState     = "present"
	ContextAbsent           ContextState     = "absent"
	ContextUndetermined     ContextState     = "undetermined"
	ContextTimePresent      ContextTimeState = "present"
	ContextTimeUndetermined ContextTimeState = "undetermined"
)

func (s ContextState) Valid() bool {
	return s == ContextPresent || s == ContextAbsent || s == ContextUndetermined
}
func (s ContextTimeState) Valid() bool {
	return s == ContextTimePresent || s == ContextTimeUndetermined
}

// Pointers retain the present empty message, distinct from no value.
type ContextTime struct {
	State ContextTimeState `json:"state"`
	Value *string          `json:"value,omitempty"`
}
type ContextString struct {
	State ContextState `json:"state"`
	Value *string      `json:"value,omitempty"`
}
type ContextMessage struct {
	State ContextState `json:"state"`
	Value *string      `json:"value,omitempty"`
}
type ChangeContext struct {
	CommittedAt ContextTime    `json:"committedAt"`
	Agent       ContextString  `json:"agent"`
	Message     ContextMessage `json:"message"`
}

func contextValue(state string, value *string, timeOnly, nonempty bool) error {
	if (timeOnly && !ContextTimeState(state).Valid()) || (!timeOnly && !ContextState(state).Valid()) {
		return fmt.Errorf("bdpwire: invalid context state %q", state)
	}
	if state == string(ContextPresent) {
		if value == nil || (nonempty && *value == "") {
			return fmt.Errorf("bdpwire: present context requires value")
		}
		return nil
	}
	if value != nil {
		return fmt.Errorf("bdpwire: context value forbidden for %q", state)
	}
	return nil
}
func (v ContextTime) Validate() error    { return contextValue(string(v.State), v.Value, true, false) }
func (v ContextString) Validate() error  { return contextValue(string(v.State), v.Value, false, true) }
func (v ContextMessage) Validate() error { return contextValue(string(v.State), v.Value, false, false) }
func (v ChangeContext) Validate() error {
	if err := v.CommittedAt.Validate(); err != nil {
		return err
	}
	if err := v.Agent.Validate(); err != nil {
		return err
	}
	return v.Message.Validate()
}

type HistoryCapability struct {
	Version int `json:"version"`
}

func (v HistoryCapability) Validate() error {
	if v.Version != 1 {
		return fmt.Errorf("bdpwire: History version must be 1")
	}
	return nil
}

// HistoricalBeadRecord excludes the current incident-Link aggregate.
// Parity derives this shape from beadRecord minus links at the pinned bundle.
type HistoricalBeadRecord struct {
	ID            string         `json:"id"`
	Type          string         `json:"type"`
	Revision      string         `json:"revision"`
	Attribution   *Attribution   `json:"attribution,omitempty"`
	Properties    Properties     `json:"properties"`
	Metadata      Metadata       `json:"metadata,omitzero"`
	OwnedLinks    OwnedLinks     `json:"ownedLinks,omitzero"`
	ChangeContext *ChangeContext `json:"changeContext,omitempty"`
}

// HistoricalLinkRecord has the exact ordinary Link shape and a distinct strict
// codec. Its underlying type keeps field types/tags in sync with LinkRecord.
// Ordinary LinkRecord's encoding/json entrypoint remains unchanged.
type HistoricalLinkRecord LinkRecord

type HistoryMissingKind string

const (
	MissingRecord     HistoryMissingKind = "record"
	MissingProperty   HistoryMissingKind = "property"
	MissingOwnedLinks HistoryMissingKind = "owned-links"
)

func (k HistoryMissingKind) Valid() bool {
	return k == MissingRecord || k == MissingProperty || k == MissingOwnedLinks
}

type HistoryMissingItem struct {
	Kind    HistoryMissingKind `json:"kind"`
	Pointer *string            `json:"pointer,omitempty"`
	Type    *string            `json:"type,omitempty"`
}

var historyPointer = regexp.MustCompile(`^(/([^/~]|~[01])*)*$`)
var historyPropertyPointer = regexp.MustCompile(`^/(properties(?:/|$)|ownedLinks/([^/~]|~[01])+/(0|[1-9][0-9]*)/properties(?:/|$))`)

func (v HistoryMissingItem) Validate() error {
	switch v.Kind {
	case MissingRecord:
		if v.Pointer != nil || v.Type != nil {
			return fmt.Errorf("bdpwire: record missing item carries extra members")
		}
	case MissingProperty:
		if v.Type != nil || v.Pointer == nil || !historyPointer.MatchString(*v.Pointer) || !historyPropertyPointer.MatchString(*v.Pointer) {
			return fmt.Errorf("bdpwire: invalid missing property pointer")
		}
	case MissingOwnedLinks:
		if v.Pointer != nil {
			return fmt.Errorf("bdpwire: owned-links item carries pointer")
		}
	default:
		return fmt.Errorf("bdpwire: invalid missing kind")
	}
	return nil
}

type HistoryMissingItems []HistoryMissingItem

func (v HistoryMissingItems) MarshalJSON() ([]byte, error) {
	if v == nil {
		return []byte("[]"), nil
	}
	return json.Marshal([]HistoryMissingItem(v))
}

type HistoryMissing struct {
	Complete bool                `json:"complete"`
	Items    HistoryMissingItems `json:"items"`
}

func (v HistoryMissing) Validate() error {
	if len(v.Items) > 64 || (v.Complete && len(v.Items) == 0) {
		return fmt.Errorf("bdpwire: invalid missing inventory size")
	}
	seen := map[string]bool{}
	for _, x := range v.Items {
		if err := x.Validate(); err != nil {
			return err
		}
		b, err := x.MarshalJSON()
		if err != nil {
			return err
		}
		k := string(b)
		if seen[k] {
			return fmt.Errorf("bdpwire: duplicate missing item")
		}
		seen[k] = true
	}
	return nil
}

type HistoryLineage string
type HistoryBody string
type HistoryParticipation string

const (
	HistoryCurrent      HistoryLineage       = "current"
	HistoryReplaced     HistoryLineage       = "replaced"
	HistoryComplete     HistoryBody          = "complete"
	HistoryIncomplete   HistoryBody          = "incomplete"
	HistoryTracked      HistoryParticipation = "tracked"
	HistoryNotTracked   HistoryParticipation = "not-tracked"
	HistoryUndetermined HistoryParticipation = "undetermined"
	HistoryPopulation                        = "all-retained"
)

func (v HistoryLineage) Valid() bool { return v == HistoryCurrent || v == HistoryReplaced }
func (v HistoryBody) Valid() bool    { return v == HistoryComplete || v == HistoryIncomplete }
func (v HistoryParticipation) Valid() bool {
	return v == HistoryTracked || v == HistoryNotTracked || v == HistoryUndetermined
}

type HistoryVersionRow struct {
	Revision      string         `json:"revision"`
	Attribution   *Attribution   `json:"attribution,omitempty"`
	ChangeContext *ChangeContext `json:"changeContext,omitempty"`
	Lineage       HistoryLineage `json:"lineage"`
	Body          HistoryBody    `json:"body"`
}

func (v HistoryVersionRow) Validate() error {
	if v.Revision == "" || !v.Lineage.Valid() || !v.Body.Valid() {
		return fmt.Errorf("bdpwire: invalid history version row")
	}
	if v.ChangeContext != nil {
		return v.ChangeContext.Validate()
	}
	return nil
}

type HistoryVersionRows []HistoryVersionRow

func (v HistoryVersionRows) MarshalJSON() ([]byte, error) {
	if v == nil {
		return []byte("[]"), nil
	}
	return json.Marshal([]HistoryVersionRow(v))
}

type HistoryWindow struct {
	Newest   *string `json:"newest"`
	Oldest   *string `json:"oldest"`
	Complete bool    `json:"complete"`
}

func (v HistoryWindow) Validate() error {
	if (v.Newest == nil) != (v.Oldest == nil) {
		return fmt.Errorf("bdpwire: history bounds must be paired")
	}
	if v.Newest != nil && (*v.Newest == "" || *v.Oldest == "") {
		return fmt.Errorf("bdpwire: empty history bound")
	}
	return nil
}

type HistoryVersionsPage struct {
	Subject       string               `json:"subject"`
	Population    string               `json:"population"`
	Participation HistoryParticipation `json:"participation"`
	Window        HistoryWindow        `json:"window"`
	Items         HistoryVersionRows   `json:"items"`
	Next          *string              `json:"next"`
}

func (v HistoryVersionsPage) Validate() error {
	if v.Population != HistoryPopulation || !v.Participation.Valid() {
		return fmt.Errorf("bdpwire: invalid history population/participation")
	}
	if err := v.Window.Validate(); err != nil {
		return err
	}
	if v.Participation != HistoryTracked && (len(v.Items) != 0 || v.Next != nil || v.Window.Newest != nil) {
		return fmt.Errorf("bdpwire: untracked history must have empty items and null bounds/next")
	}
	if len(v.Items) > 0 && v.Window.Newest == nil {
		return fmt.Errorf("bdpwire: history rows require nonnull bounds")
	}
	seen := map[string]bool{}
	for _, row := range v.Items {
		if err := row.Validate(); err != nil {
			return err
		}
		if seen[row.Revision] {
			return fmt.Errorf("bdpwire: duplicate history revision")
		}
		seen[row.Revision] = true
	}
	return nil
}

// decodeHistory uses an alias without methods to avoid recursively dispatching
// the same root. Nested public types retain their strict structural codecs.
func decodeHistory(data []byte, target any, validate func() error) error {
	raw, err := oneDocument(data)
	if err != nil {
		return err
	}
	if err := decodeStruct(raw, reflect.ValueOf(target).Elem(), "history"); err != nil {
		return err
	}
	return validate()
}
func marshalHistory(v any, validate func() error) ([]byte, error) {
	if err := validateGoCarrier(v); err != nil {
		return nil, err
	}
	if err := validate(); err != nil {
		return nil, err
	}
	return json.Marshal(v)
}

func historyDecoder(v reflect.Value) (func([]byte) error, bool) {
	if !v.CanAddr() {
		return nil, false
	}
	switch p := v.Addr().Interface().(type) {
	case *ContextTime:
		return p.UnmarshalJSON, true
	case *ContextString:
		return p.UnmarshalJSON, true
	case *ContextMessage:
		return p.UnmarshalJSON, true
	case *ChangeContext:
		return p.UnmarshalJSON, true
	case *HistoryCapability:
		return p.UnmarshalJSON, true
	case *HistoryMissingItem:
		return p.UnmarshalJSON, true
	case *HistoryMissing:
		return p.UnmarshalJSON, true
	case *HistoryVersionRow:
		return p.UnmarshalJSON, true
	case *HistoryWindow:
		return p.UnmarshalJSON, true
	case *HistoryVersionsPage:
		return p.UnmarshalJSON, true
	case *HistoricalBeadRecord:
		return p.UnmarshalJSON, true
	case *HistoricalLinkRecord:
		return p.UnmarshalJSON, true
	}
	return nil, false
}

type contextTimeWire ContextTime

func (v *ContextTime) UnmarshalJSON(data []byte) error {
	var decoded contextTimeWire
	if err := decodeHistory(data, &decoded, func() error { return ContextTime(decoded).Validate() }); err != nil {
		return err
	}
	*v = ContextTime(decoded)
	return nil
}
func (v ContextTime) MarshalJSON() ([]byte, error) {
	return marshalHistory(contextTimeWire(v), v.Validate)
}

type contextStringWire ContextString

func (v *ContextString) UnmarshalJSON(data []byte) error {
	var decoded contextStringWire
	if err := decodeHistory(data, &decoded, func() error { return ContextString(decoded).Validate() }); err != nil {
		return err
	}
	*v = ContextString(decoded)
	return nil
}
func (v ContextString) MarshalJSON() ([]byte, error) {
	return marshalHistory(contextStringWire(v), v.Validate)
}

type contextMessageWire ContextMessage

func (v *ContextMessage) UnmarshalJSON(data []byte) error {
	var decoded contextMessageWire
	if err := decodeHistory(data, &decoded, func() error { return ContextMessage(decoded).Validate() }); err != nil {
		return err
	}
	*v = ContextMessage(decoded)
	return nil
}
func (v ContextMessage) MarshalJSON() ([]byte, error) {
	return marshalHistory(contextMessageWire(v), v.Validate)
}

type changeContextWire ChangeContext

func (v *ChangeContext) UnmarshalJSON(data []byte) error {
	var decoded changeContextWire
	if err := decodeHistory(data, &decoded, func() error { return ChangeContext(decoded).Validate() }); err != nil {
		return err
	}
	*v = ChangeContext(decoded)
	return nil
}
func (v ChangeContext) MarshalJSON() ([]byte, error) {
	return marshalHistory(changeContextWire(v), v.Validate)
}

type historyCapabilityWire HistoryCapability

func (v *HistoryCapability) UnmarshalJSON(data []byte) error {
	var decoded historyCapabilityWire
	if err := decodeHistory(data, &decoded, func() error { return HistoryCapability(decoded).Validate() }); err != nil {
		return err
	}
	*v = HistoryCapability(decoded)
	return nil
}
func (v HistoryCapability) MarshalJSON() ([]byte, error) {
	return marshalHistory(historyCapabilityWire(v), v.Validate)
}

type historyMissingItemWire HistoryMissingItem

func (v *HistoryMissingItem) UnmarshalJSON(data []byte) error {
	var decoded historyMissingItemWire
	if err := decodeHistory(data, &decoded, func() error { return HistoryMissingItem(decoded).Validate() }); err != nil {
		return err
	}
	*v = HistoryMissingItem(decoded)
	return nil
}
func (v HistoryMissingItem) MarshalJSON() ([]byte, error) {
	return marshalHistory(historyMissingItemWire(v), v.Validate)
}

type historyMissingWire HistoryMissing

func (v *HistoryMissing) UnmarshalJSON(data []byte) error {
	var decoded historyMissingWire
	if err := decodeHistory(data, &decoded, func() error { return HistoryMissing(decoded).Validate() }); err != nil {
		return err
	}
	*v = HistoryMissing(decoded)
	return nil
}
func (v HistoryMissing) MarshalJSON() ([]byte, error) {
	return marshalHistory(historyMissingWire(v), v.Validate)
}

type historyVersionRowWire HistoryVersionRow

func (v *HistoryVersionRow) UnmarshalJSON(data []byte) error {
	var decoded historyVersionRowWire
	if err := decodeHistory(data, &decoded, func() error { return HistoryVersionRow(decoded).Validate() }); err != nil {
		return err
	}
	*v = HistoryVersionRow(decoded)
	return nil
}
func (v HistoryVersionRow) MarshalJSON() ([]byte, error) {
	return marshalHistory(historyVersionRowWire(v), v.Validate)
}

type historyWindowWire HistoryWindow

func (v *HistoryWindow) UnmarshalJSON(data []byte) error {
	var decoded historyWindowWire
	if err := decodeHistory(data, &decoded, func() error { return HistoryWindow(decoded).Validate() }); err != nil {
		return err
	}
	*v = HistoryWindow(decoded)
	return nil
}
func (v HistoryWindow) MarshalJSON() ([]byte, error) {
	return marshalHistory(historyWindowWire(v), v.Validate)
}

type historyVersionsPageWire HistoryVersionsPage

func (v *HistoryVersionsPage) UnmarshalJSON(data []byte) error {
	var decoded historyVersionsPageWire
	if err := decodeHistory(data, &decoded, func() error { return HistoryVersionsPage(decoded).Validate() }); err != nil {
		return err
	}
	*v = HistoryVersionsPage(decoded)
	return nil
}
func (v HistoryVersionsPage) MarshalJSON() ([]byte, error) {
	return marshalHistory(historyVersionsPageWire(v), v.Validate)
}
func (v HistoricalBeadRecord) Validate() error {
	if v.ChangeContext != nil {
		return v.ChangeContext.Validate()
	}
	return nil
}

type historicalBeadRecordWire HistoricalBeadRecord

func (v *HistoricalBeadRecord) UnmarshalJSON(data []byte) error {
	var decoded historicalBeadRecordWire
	if err := decodeHistory(data, &decoded, func() error { return HistoricalBeadRecord(decoded).Validate() }); err != nil {
		return err
	}
	*v = HistoricalBeadRecord(decoded)
	return nil
}
func (v HistoricalBeadRecord) MarshalJSON() ([]byte, error) {
	return marshalHistory(historicalBeadRecordWire(v), v.Validate)
}

func (v HistoricalLinkRecord) Validate() error {
	if v.ChangeContext != nil {
		return v.ChangeContext.Validate()
	}
	return nil
}

type historicalLinkRecordWire HistoricalLinkRecord

func (v *HistoricalLinkRecord) UnmarshalJSON(data []byte) error {
	var decoded historicalLinkRecordWire
	if err := decodeHistory(data, &decoded, func() error { return HistoricalLinkRecord(decoded).Validate() }); err != nil {
		return err
	}
	*v = HistoricalLinkRecord(decoded)
	return nil
}
func (v HistoricalLinkRecord) MarshalJSON() ([]byte, error) {
	return marshalHistory(historicalLinkRecordWire(v), v.Validate)
}
