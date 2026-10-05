package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/activity"
	"github.com/steveyegge/beads/memoryops"
)

const MaxProposals = 512
const MaxReviews = 1024
const proposalPrefix = Prefix + "proposal/"
const reviewPrefix = Prefix + "review/"

// Proposals and reviews are immutable, content-addressed entries. Acceptance
// carries its assertion in the same atomic memory write; no storage API or
// transaction spanning multiple keys is required.
type Proposal struct {
	ID string `json:"proposal_id"`
	Record
	Supersedes     string               `json:"supersedes,omitempty"`
	ActivityEvents []activity.Reference `json:"activity_events,omitempty"`
	CreatedAt      string               `json:"created_at"`
}
type Review struct {
	ID         string  `json:"review_id"`
	ProposalID string  `json:"proposal_id"`
	Decision   string  `json:"decision"`
	Reviewer   string  `json:"reviewer"`
	Reason     string  `json:"reason"`
	Accepted   *Record `json:"accepted,omitempty"`
	CreatedAt  string  `json:"created_at"`
}
type ProposalView struct {
	Proposal
	ActivityValidity string   `json:"activity_validity,omitempty"`
	ActivityChanged  []string `json:"activity_changed,omitempty"`
	Status           string   `json:"status"`
	Validity         string   `json:"validity"`
	Changed          []string `json:"changed,omitempty"`
	Reviews          []Review `json:"reviews,omitempty"`
}
type proposalEnvelope struct {
	Version  int       `json:"proposal_version"`
	Proposal *Proposal `json:"proposal,omitempty"`
	Review   *Review   `json:"review,omitempty"`
}

func digest(value any) string {
	data, _ := json.Marshal(value)
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}
func canonical(record Record) Record {
	// Copy slices so canonicalization cannot mutate an existing decoded record.
	record.Sources = append([]Source{}, record.Sources...)
	record.Symbols = append([]string{}, record.Symbols...)
	record.Related = append([]string{}, record.Related...)
	record.Rejected = append([]string{}, record.Rejected...)
	sort.Slice(record.Sources, func(i, j int) bool { return record.Sources[i].Path < record.Sources[j].Path })
	sort.Strings(record.Symbols)
	sort.Strings(record.Related)
	sort.Strings(record.Rejected)
	return record
}
func proposalID(p Proposal) string {
	return "p-" + digest(struct {
		Record         Record               `json:"record"`
		Supersedes     string               `json:"supersedes,omitempty"`
		ActivityEvents []activity.Reference `json:"activity_events,omitempty"`
	}{canonical(p.Record), p.Supersedes, p.ActivityEvents})
}
func reviewID(review Review) string {
	review.ID = ""
	review.CreatedAt = ""
	return "r-" + digest(review)
}
func validProposal(p Proposal) error {
	if err := activity.ValidateReferences(p.ActivityEvents); err != nil {
		return err
	}
	if err := Validate(p.Record); err != nil {
		return err
	}
	if p.Scope != "recorded" {
		return errors.New("proposals require recorded scope; review explicitly selects evidence scope")
	}
	if p.ID != proposalID(p) {
		return errors.New("proposal content digest mismatch")
	}
	for _, s := range p.Sources {
		hash, err := hex.DecodeString(s.SHA256)
		if err != nil || len(hash) != 32 {
			return errors.New("proposal sources require SHA-256")
		}
	}
	if _, err := time.Parse(time.RFC3339, p.CreatedAt); err != nil {
		return err
	}
	return nil
}
func validReview(review Review, p Proposal) error {
	if review.ID != reviewID(review) || review.ProposalID != p.ID {
		return errors.New("review content digest mismatch")
	}
	if strings.TrimSpace(review.Reviewer) == "" || len(review.Reviewer) > 200 || strings.TrimSpace(review.Reason) == "" || len(review.Reason) > 2000 {
		return errors.New("review requires reviewer (1-200 bytes) and reason (1-2000 bytes)")
	}
	if _, err := time.Parse(time.RFC3339, review.CreatedAt); err != nil {
		return err
	}
	switch review.Decision {
	case "accept":
		if review.Accepted == nil {
			return errors.New("accepted review must include its assertion")
		}
		if err := Validate(*review.Accepted); err != nil {
			return err
		}
		original := canonical(p.Record)
		accepted := canonical(*review.Accepted)
		original.Scope = accepted.Scope
		original.Evidence = accepted.Evidence
		if !reflect.DeepEqual(original, accepted) {
			return errors.New("review cannot change proposal content or source hashes")
		}
	case "reject":
		if review.Accepted != nil {
			return errors.New("rejection cannot carry an assertion")
		}
	default:
		return errors.New("decision must be accept or reject")
	}
	return nil
}

// Decode proposals after legacy records. Preserve every valid review rather
// than choosing a last writer; ambiguous acceptance never becomes context.
func decodeProposals(state State, plane map[string]string) State {
	state.Proposals = []ProposalView{}
	keys := make([]string, 0)
	for key := range plane {
		if strings.HasPrefix(key, proposalPrefix) || strings.HasPrefix(key, reviewPrefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	reviews := []Review{}
	overflow := false
	for _, key := range keys {
		var value proposalEnvelope
		if len(plane[key]) > 32*1024 || json.Unmarshal([]byte(plane[key]), &value) != nil || value.Version != 1 {
			state.Warnings = append(state.Warnings, "Unreadable proposal entry: "+key)
			continue
		}
		if p := value.Proposal; p != nil && value.Review == nil && key == proposalPrefix+p.ID && validProposal(*p) == nil {
			if len(state.Proposals) >= MaxProposals {
				overflow = true
				continue
			}
			view := ProposalView{Proposal: *p, Status: "pending", Validity: "unchecked"}
			if len(p.ActivityEvents) > 0 {
				view.ActivityValidity = "current"
				view.ActivityChanged = activity.ChangedReferences(plane, p.ActivityEvents)
				if len(view.ActivityChanged) > 0 {
					view.ActivityValidity = "needs_review"
				}
			}
			state.Proposals = append(state.Proposals, view)
		} else if v := value.Review; v != nil && value.Proposal == nil && key == reviewPrefix+v.ProposalID+"/"+v.ID {
			if len(reviews) >= MaxReviews {
				overflow = true
				continue
			}
			reviews = append(reviews, *v)
		} else {
			state.Warnings = append(state.Warnings, "Invalid proposal entry: "+key)
		}
	}
	byID := map[string]*ProposalView{}
	for i := range state.Proposals {
		byID[state.Proposals[i].ID] = &state.Proposals[i]
	}
	for _, review := range reviews {
		p := byID[review.ProposalID]
		if p == nil || validReview(review, p.Proposal) != nil {
			state.Warnings = append(state.Warnings, "Invalid or orphan proposal review: "+review.ID)
			continue
		}
		p.Reviews = append(p.Reviews, review)
	}
	topics := map[string][]*ProposalView{}
	for i := range state.Proposals {
		p := &state.Proposals[i]
		if overflow || len(p.Reviews) > 1 {
			p.Status = "conflict"
		} else if len(p.Reviews) == 1 {
			p.Status = "rejected"
			if p.Reviews[0].Decision == "accept" {
				p.Status = "accepted"
				topics[p.Record.ID] = append(topics[p.Record.ID], p)
			}
		}
	}
	for _, group := range topics {
		if len(group) > 1 {
			for _, p := range group {
				p.Status = "conflict"
			}
		}
	}
	for i := range state.Proposals {
		p := &state.Proposals[i]
		if p.Supersedes != "" {
			old := byID[p.Supersedes]
			if old == nil || old.Record.ID != p.Record.ID || old.ID == p.ID {
				p.Status = "conflict"
			} else if old.Status == "pending" {
				old.Status = "superseded"
			}
		}
	}
	existing := map[string]bool{}
	for _, v := range state.Records {
		existing[v.ID] = true
	}
	for _, p := range state.Proposals {
		if p.Status == "conflict" {
			state.Warnings = append(state.Warnings, "Proposal conflict requires explicit record review: "+p.ID)
			continue
		}
		if p.Status != "accepted" {
			continue
		}
		if existing[p.Record.ID] {
			state.Warnings = append(state.Warnings, "Existing assertion retained over accepted proposal: "+p.Record.ID)
			continue
		}
		if len(state.Records) >= MaxRecords {
			state.Warnings = append(state.Warnings, "Accepted proposal exceeds assertion limit: "+p.ID)
			continue
		}
		state.Records = append(state.Records, View{Record: *p.Reviews[0].Accepted, Validity: "unchecked"})
		existing[p.Record.ID] = true
	}
	if overflow {
		state.Warnings = append(state.Warnings, "Proposal/review limit exceeded; promotions withheld")
	}
	return state
}

// PrepareProposal verifies the supplied hashes of files actually inspected.
// Unlike record, proposing never silently replaces expected hashes with now.
func (r *Reader) PrepareProposal(record Record, supersedes string, state State) (Proposal, error) {
	return r.PrepareActivityProposal(record, supersedes, state, nil)
}

// PrepareActivityProposal keeps observed origins separate from source evidence.
func (r *Reader) PrepareActivityProposal(record Record, supersedes string, state State, refs []activity.Reference) (Proposal, error) {
	if err := activity.ValidateReferences(refs); err != nil {
		return Proposal{}, err
	}
	if record.Scope != "recorded" {
		return Proposal{}, errors.New("proposal scope must be recorded")
	}
	bound, err := r.Bind(record)
	if err != nil {
		return Proposal{}, err
	}
	expected := map[string]string{}
	for _, source := range record.Sources {
		path, err := cleanPath(source.Path)
		if err != nil {
			return Proposal{}, err
		}
		if _, ok := expected[path]; ok {
			return Proposal{}, errors.New("duplicate proposal source path")
		}
		expected[path] = source.SHA256
	}
	for _, source := range bound.Sources {
		if expected[source.Path] != source.SHA256 {
			return Proposal{}, fmt.Errorf("source %q changed or lacks inspected SHA-256; read source/DOX and use knowledge sources", source.Path)
		}
	}
	bound = canonical(bound)
	p := Proposal{Record: bound, Supersedes: supersedes, ActivityEvents: append([]activity.Reference(nil), refs...), CreatedAt: time.Now().UTC().Format(time.RFC3339)}
	p.ID = proposalID(p)
	for _, old := range state.Proposals {
		if old.ID == p.ID {
			return old.Proposal, nil
		}
	}
	if len(state.Proposals) >= MaxProposals {
		return p, fmt.Errorf("at most %d proposals; archive explicitly before proposing", MaxProposals)
	}
	if supersedes != "" {
		found := false
		for _, old := range state.Proposals {
			if old.ID == supersedes {
				found = true
				if old.Record.ID != p.Record.ID || old.Status == "accepted" || old.Status == "conflict" {
					return p, errors.New("supersedes requires a pending/rejected proposal for the same assertion ID")
				}
			}
		}
		if !found {
			return p, errors.New("superseded proposal must already exist")
		}
	}
	return p, validProposal(p)
}

func PrepareReview(state State, id, decision, reviewer, reason, scope, evidence string) (Review, error) {
	var p *ProposalView
	count := 0
	for i := range state.Proposals {
		count += len(state.Proposals[i].Reviews)
		if state.Proposals[i].ID == id {
			p = &state.Proposals[i]
		}
	}
	if p == nil {
		return Review{}, errors.New("proposal not found")
	}
	review := Review{ProposalID: id, Decision: decision, Reviewer: reviewer, Reason: reason, CreatedAt: time.Now().UTC().Format(time.RFC3339)}
	if decision == "accept" {
		if scope == "" {
			return review, errors.New("accept requires explicit --scope and supporting --evidence above recorded")
		}
		record := p.Record
		record.Scope = scope
		record.Evidence = evidence
		review.Accepted = &record
	} else if scope != "" || evidence != "" {
		return review, errors.New("scope/evidence apply only to acceptance")
	}
	review.ID = reviewID(review)
	if err := validReview(review, p.Proposal); err != nil {
		return review, err
	}
	for _, old := range p.Reviews {
		if old.ID == review.ID {
			return old, nil
		}
	}
	if p.Status != "pending" {
		return review, fmt.Errorf("proposal is %s; existing reviews are immutable", p.Status)
	}
	if count >= MaxReviews {
		return review, fmt.Errorf("at most %d reviews", MaxReviews)
	}
	if decision == "accept" {
		if p.Validity != "current" {
			return review, errors.New("proposal sources/DOX or activity origins changed; inspect and create a new proposal")
		}
		for _, existing := range state.Records {
			if existing.ID == p.Record.ID {
				return review, errors.New("assertion ID already exists; review it explicitly with knowledge record, never replace via proposal")
			}
		}
		if len(state.Records) >= MaxRecords {
			return review, fmt.Errorf("at most %d assertions", MaxRecords)
		}
	}
	return review, nil
}

// SaveImmutable skips identical retries and refuses replacing a different value.
// Content identities exclude timestamps; identical concurrent writes may only
// differ in the informational first-write time, never decision or assertion.
func saveImmutable(ctx context.Context, memories memoryops.Memories, key string, value proposalEnvelope) (bool, error) {
	old, err := memories.Recall(ctx, memoryops.RecallRequest{Key: key})
	if err != nil {
		return false, err
	}
	if old.Found {
		var prior proposalEnvelope
		if json.Unmarshal([]byte(old.Value), &prior) != nil || prior.Version != 1 {
			return false, errors.New("existing immutable entry is unreadable")
		}
		if prior.Proposal != nil && value.Proposal != nil && reflect.DeepEqual(canonical(prior.Proposal.Record), canonical(value.Proposal.Record)) && prior.Proposal.Supersedes == value.Proposal.Supersedes && reflect.DeepEqual(prior.Proposal.ActivityEvents, value.Proposal.ActivityEvents) {
			return false, nil
		}
		if prior.Review != nil && value.Review != nil && reviewID(*prior.Review) == reviewID(*value.Review) {
			return false, nil
		}
		return false, errors.New("refusing to replace an immutable proposal/review")
	}
	data, err := json.Marshal(value)
	if err != nil {
		return false, err
	}
	if len(data) > 32*1024 {
		return false, errors.New("proposal/review exceeds 32 KiB")
	}
	_, err = memories.Remember(ctx, memoryops.RememberRequest{Key: key, Content: string(data)})
	return err == nil, err
}
func SaveProposal(ctx context.Context, memories memoryops.Memories, p Proposal) (bool, error) {
	if err := validProposal(p); err != nil {
		return false, err
	}
	return saveImmutable(ctx, memories, proposalPrefix+p.ID, proposalEnvelope{Version: 1, Proposal: &p})
}
func SaveReview(ctx context.Context, memories memoryops.Memories, review Review) (bool, error) {
	return saveImmutable(ctx, memories, reviewPrefix+review.ProposalID+"/"+review.ID, proposalEnvelope{Version: 1, Review: &review})
}
