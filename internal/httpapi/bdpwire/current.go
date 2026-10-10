package bdpwire

// The normative current Read envelopes require metadata even though their
// retained base envelopes can omit it for records written before this member
// existed. These separate DTOs keep that distinction visible to schema parity.
type CurrentBeadRecord struct {
	ChangeContext *ChangeContext         `json:"changeContext,omitempty"`
	ID            string                 `json:"id"`
	Type          string                 `json:"type"`
	Revision      string                 `json:"revision"`
	Attribution   *Attribution           `json:"attribution,omitempty"`
	Properties    Properties             `json:"properties"`
	Metadata      Metadata               `json:"metadata"`
	Links         *CurrentLinkCollection `json:"links,omitempty"`
	OwnedLinks    CurrentOwnedLinks      `json:"ownedLinks,omitzero"`
}

type CurrentLinkRecord struct {
	ChangeContext *ChangeContext `json:"changeContext,omitempty"`
	ID            string         `json:"id"`
	Type          string         `json:"type"`
	Revision      string         `json:"revision"`
	Attribution   *Attribution   `json:"attribution,omitempty"`
	Source        Reference      `json:"source"`
	Target        Reference      `json:"target"`
	Properties    Properties     `json:"properties"`
	Metadata      Metadata       `json:"metadata"`
}

type CurrentBeadCollection struct {
	Items CurrentBeadRecords `json:"items"`
	Next  *string            `json:"next"`
}

type CurrentLinkCollection struct {
	Items CurrentLinkRecords `json:"items"`
	Next  *string            `json:"next"`
}
