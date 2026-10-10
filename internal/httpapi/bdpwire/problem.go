package bdpwire

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// ProblemFamily is the suffix of a problem's `type`: ProblemTypePrefix
// followed by the family. BDP defines a small set of stable families and the
// required `code` identifies the exact normative condition within one.
type ProblemFamily string

// The Read-profile problem families.
const (
	FamilyRequest        ProblemFamily = "request"
	FamilyAuthentication ProblemFamily = "authentication"
	FamilyAuthorization  ProblemFamily = "authorization"
	FamilyNotFound       ProblemFamily = "not-found"
	FamilyConflict       ProblemFamily = "conflict"
	FamilyGone           ProblemFamily = "gone"
	FamilySize           ProblemFamily = "size"
	FamilyRateLimit      ProblemFamily = "rate-limit"
	FamilyUnavailable    ProblemFamily = "unavailable"
)

type problemRow struct {
	family ProblemFamily
	status int
	retry  RetryDisposition
}

// readProblemTable is the closed Read problem table: each code fixes its
// family, HTTP status and retry disposition. It is the spec's table and the
// bundle's `readProblem` conditional rows in one place, and
// schema_parity_test.go derives the rows from the vendored bundle and
// compares them to this map in both directions, so it cannot drift from the
// pin unnoticed.
var readProblemTable = map[ReadProblemCode]problemRow{
	CodeRevisionUnknown:         {FamilyNotFound, 404, RetryAfterStateChange},
	CodeRevisionUnretained:      {FamilyConflict, 409, RetryAfterStateChange},
	CodeRevisionReorganized:     {FamilyGone, 410, RetryAfterStateChange},
	CodeRevisionNotTracked:      {FamilyConflict, 409, RetryAfterStateChange},
	CodeRevisionUnrepresentable: {FamilyConflict, 409, RetryAfterStateChange},
	CodeMalformedRequest:        {FamilyRequest, 400, RetryNever},
	CodeInvalidParameter:        {FamilyRequest, 400, RetryNever},
	CodeUnauthenticated:         {FamilyAuthentication, 401, RetryAfterStateChange},
	CodeForbidden:               {FamilyAuthorization, 403, RetryAfterStateChange},
	CodeResourceNotFound:        {FamilyNotFound, 404, RetryAfterStateChange},
	CodeResourcePruned:          {FamilyGone, 410, RetryNever},
	CodeResourceErased:          {FamilyGone, 410, RetryNever},
	CodeForeignView:             {FamilyConflict, 409, RetryAfterStateChange},
	CodeCursorExpired:           {FamilyGone, 410, RetryAfterStateChange},
	CodeRequestTooLarge:         {FamilySize, 413, RetryNever},
	CodeLimitExceeded:           {FamilySize, 413, RetryNever},
	CodeRateLimited:             {FamilyRateLimit, 429, RetryAfterDelay},
	CodeTemporarilyUnavailable:  {FamilyUnavailable, 503, RetryAfterDelay},
}

// Family returns the code's problem family, or "" for a code outside the
// table.
func (c ReadProblemCode) Family() ProblemFamily {
	return readProblemTable[c].family
}

// Type returns the code's problem `type`: ProblemTypePrefix plus the family.
// For a code outside the table it returns the bare prefix, which no valid
// problem carries.
func (c ReadProblemCode) Type() string {
	return ProblemTypePrefix + string(c.Family())
}

// Status returns the HTTP status the code fixes, or 0 outside the table.
func (c ReadProblemCode) Status() int {
	return readProblemTable[c].status
}

// Retry returns the retry disposition the code fixes, or "" outside the
// table.
func (c ReadProblemCode) Retry() RetryDisposition {
	return readProblemTable[c].retry
}

// ReadProblem is the `readProblem` envelope: RFC 9457 Problem Details plus
// the BDP members Code and Retry, which every BDP problem carries. It is the
// body of unsuccessful Read responses except HTTP-native 405, 406, 412 and
// 500 refusals. Conditional 304 responses are also bodyless. None of those
// native responses carries a BDP problem envelope.
//
// It is the one OPEN envelope in the Read profile: RFC 9457 extension members
// are allowed, so unknown members are carried through in Extensions rather
// than rejected, except the code-specific exclusions on resource-erased and
// the five History revision diagnoses.
// Every other envelope is closed.
type ReadProblem struct {
	Missing *HistoryMissing `json:"missing,omitempty"`
	// Type is the problem family URL: ProblemTypePrefix + Code.Family().
	Type string `json:"type"`
	// Title, Detail and Instance are the ordinary RFC 9457 members.
	// DECISION: an empty one is omitted rather than sent as "" (plain strings
	// with omitempty, not pointers), which is what the RFC intends for a
	// member with nothing to say; a document carrying an explicit "" does not
	// round-trip that member, and the bundle gives no meaning to one.
	Title string `json:"title,omitempty"`
	// Status is optional on a direct problem but, when present, MUST equal
	// the HTTP status — which Code fixes, so 0 here means "not sent".
	Status   int    `json:"status,omitempty"`
	Detail   string `json:"detail,omitempty"`
	Instance string `json:"instance,omitempty"`
	// Code is the member a client dispatches on.
	Code ReadProblemCode `json:"code"`
	// Retry is fixed by Code (Code.Retry()); it travels so a generic client
	// need not carry the table.
	Retry RetryDisposition `json:"retry"`
	// ArchivedAt MAY accompany CodeResourcePruned only — a Reference, possibly
	// pinned, naming where the content went, recorded and echoed like any
	// Reference and never validated or dereferenced. The bundle forbids it on
	// every other code (Validate enforces that).
	ArchivedAt *Reference `json:"archivedAt,omitempty"`
	// Extensions carries every member the bundle does not name, byte for
	// byte. It is nil when there are none. An extension may not reuse a
	// protocol-owned member name; MarshalJSON refuses one that does.
	Extensions map[string]json.RawMessage `json:"-"`
}

// NewReadProblem returns a problem for code with Type, Status and Retry
// filled in from the table. Title, Detail, Instance, ArchivedAt and
// Extensions are the caller's. DECISION: Status is filled in although the
// spec makes it optional on a direct problem, so the body is self-describing
// in a log; a caller that wants it off the wire clears it.
func NewReadProblem(code ReadProblemCode) ReadProblem {
	return ReadProblem{
		Type:   code.Type(),
		Status: code.Status(),
		Code:   code,
		Retry:  code.Retry(),
	}
}

// Validate checks the problem against the closed table: Code is a member,
// Type and Retry are the ones Code fixes, Status (when sent) is the one Code
// fixes, ArchivedAt appears only with CodeResourcePruned, and resource-erased
// carries no pointer extension.
func (p ReadProblem) Validate() error {
	if err := p.validateWireConditions(); err != nil {
		return err
	}
	row, ok := readProblemTable[p.Code]
	if !ok {
		return fmt.Errorf("bdpwire: unknown read problem code %q", p.Code)
	}
	if want := ProblemTypePrefix + string(row.family); p.Type != want {
		return fmt.Errorf("bdpwire: problem type %q for code %q, want %q", p.Type, p.Code, want)
	}
	if p.Retry != row.retry {
		return fmt.Errorf("bdpwire: problem retry %q for code %q, want %q", p.Retry, p.Code, row.retry)
	}
	if p.Status != 0 && p.Status != row.status {
		return fmt.Errorf("bdpwire: problem status %d for code %q, want %d", p.Status, p.Code, row.status)
	}
	if p.ArchivedAt != nil && p.Code != CodeResourcePruned {
		return fmt.Errorf("bdpwire: archivedAt is allowed only with %q, not %q", CodeResourcePruned, p.Code)
	}
	return nil
}

// readProblemMembersOnly is ReadProblem without its methods, so the default
// struct codec handles the named members; Extensions is excluded by its tag.
type readProblemMembersOnly ReadProblem

// readProblemMembers is the set of protocol-owned member names, read off the
// struct tags once so it cannot disagree with the struct.
var readProblemMembers = func() map[string]bool {
	members := map[string]bool{}
	rt := reflect.TypeOf(readProblemMembersOnly{})
	for i := 0; i < rt.NumField(); i++ {
		name, _, _ := strings.Cut(rt.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" {
			members[name] = true
		}
	}
	return members
}()

// MarshalJSON writes the named members and then the extensions. DECISION:
// member order on the wire is not significant to any consumer (RFC 8259
// objects are unordered and the matrix compares by pointer), so with
// extensions present the members come out in key order rather than in a
// preserved order the type would have to carry.
func (p ReadProblem) MarshalJSON() ([]byte, error) {
	if err := validateGoCarrier(p); err != nil {
		return nil, err
	}
	if err := p.validateWireConditions(); err != nil {
		return nil, err
	}
	named, err := json.Marshal(readProblemMembersOnly(p))
	if err != nil {
		return nil, err
	}
	if len(p.Extensions) == 0 {
		return named, nil
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(named, &members); err != nil {
		return nil, err
	}
	for name, value := range p.Extensions {
		if readProblemMembers[name] {
			return nil, fmt.Errorf("bdpwire: extension member %q reuses a protocol-owned problem member", name)
		}
		members[name] = value
	}
	return json.Marshal(members)
}

// UnmarshalJSON splits the object into the named members, decoded under the
// closed rule (exact names, no null, a wrong type is an error, an ArchivedAt
// object is held to the closed pinned-reference shape, type/code/retry
// present), and the rest, kept verbatim in Extensions. It is decodeProblem
// (decode.go).
func (p *ReadProblem) UnmarshalJSON(data []byte) error {
	return decodeProblem(data, p, "problem")
}

// The wire-condition guard preserves ordinary RFC 9457 extensions but rejects the
// condition-specific pointer on erased resources, even when its value is null.
// The codecs also apply this narrow guard because pointer otherwise passes
// through the open Extensions carrier. Named-field semantic conditions, such
// as archivedAt being exclusive to resource-pruned, remain Validate checks;
// these codecs do not claim complete conditional-schema validation.
// problemForbiddenMembers is mirrored by a schema-derived conditional gate.
func problemForbiddenMembers(code ReadProblemCode) map[string]bool {
	forbidden := map[string]bool{}
	if code != CodeResourcePruned {
		forbidden["archivedAt"] = true
	}
	if code != CodeRevisionUnretained {
		forbidden["missing"] = true
	}
	if code == CodeResourceErased {
		forbidden["pointer"] = true
	}
	switch code {
	case CodeRevisionUnknown, CodeRevisionUnretained, CodeRevisionReorganized, CodeRevisionNotTracked, CodeRevisionUnrepresentable:
		for _, name := range []string{"window", "mayChangeAfterSync", "resource", "properties", "ownedLinks", "changeContext", "archivedAt", "pointer"} {
			forbidden[name] = true
		}
	}
	return forbidden
}
func (p ReadProblem) validateWireConditions() error {
	if p.Code == CodeRevisionUnretained && p.Missing == nil {
		return fmt.Errorf("bdpwire: revision-unretained requires missing")
	}
	if p.Missing != nil {
		if p.Code != CodeRevisionUnretained {
			return fmt.Errorf("bdpwire: missing is forbidden for %q", p.Code)
		}
		if err := p.Missing.Validate(); err != nil {
			return err
		}
	}
	// The inherited archivedAt condition stays at explicit Validate, except
	// new History diagnoses whose narrow wire exclusion is unconditional.
	for name := range problemForbiddenMembers(p.Code) {
		if _, ok := p.Extensions[name]; ok {
			return fmt.Errorf("bdpwire: %s is forbidden with %q", name, p.Code)
		}
	}
	switch p.Code {
	case CodeRevisionUnknown, CodeRevisionUnretained, CodeRevisionReorganized, CodeRevisionNotTracked, CodeRevisionUnrepresentable:
		if p.ArchivedAt != nil {
			return fmt.Errorf("bdpwire: archivedAt forbidden on History diagnosis")
		}
	}
	return nil
}
