package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/steveyegge/beads/internal/activity"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/memoryops"
)

// Recording double only for immutable write/no-op checks; real Dolt process
// persistence and atomic assertion/review wiring have a separate integration test.
type proposalMemory struct {
	memoryops.Memories
	plane  map[string]string
	writes int
}

func (m *proposalMemory) Remember(_ context.Context, r memoryops.RememberRequest) (memoryops.RememberResult, error) {
	m.plane[r.Key] = r.Content
	m.writes++
	return memoryops.RememberResult{}, nil
}
func (m *proposalMemory) Recall(_ context.Context, r memoryops.RecallRequest) (memoryops.RecallResult, error) {
	value, ok := m.plane[r.Key]
	return memoryops.RecallResult{Value: value, Found: ok}, nil
}
func proposalFixture(t *testing.T) (string, *Reader, Record, *proposalMemory) {
	t.Helper()
	root := t.TempDir()
	writeSource(t, root, "DOX.md", "# Contract\n")
	writeSource(t, root, "docs/plan.md", "# Existing solution\n")
	reader, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	record, err := reader.Bind(Record{ID: "freshness", Kind: "solution", Summary: "Preserve evidence after a repair", Problem: "Old evidence reused", Cause: "No mutation version check", Solution: "Reject evidence older than the latest mutation", Scope: "recorded", Sources: []Source{{Path: "docs/plan.md"}}})
	if err != nil {
		t.Fatal(err)
	}
	return root, reader, record, &proposalMemory{plane: map[string]string{}}
}
func submit(t *testing.T, reader *Reader, m *proposalMemory, record Record, supersedes string) Proposal {
	t.Helper()
	p, err := reader.PrepareProposal(record, supersedes, reader.Refresh(Decode(m.plane)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SaveProposal(context.Background(), m, p); err != nil {
		t.Fatal(err)
	}
	return p
}
func review(t *testing.T, reader *Reader, m *proposalMemory, id, decision string) Review {
	t.Helper()
	scope, evidence := "", ""
	if decision == "accept" {
		scope = "inspected"
		evidence = "Read source; tests not rerun"
	}
	item, err := PrepareReview(reader.Refresh(Decode(m.plane)), id, decision, "supervisor", "Inspected implementation", scope, evidence)
	if err != nil {
		t.Fatal(err)
	}
	return item
}
func TestProposalReviewAtomicPromotionAndRetries(t *testing.T) {
	_, reader, record, m := proposalFixture(t)
	p := submit(t, reader, m, record, "")
	record.Sources[0], record.Sources[1] = record.Sources[1], record.Sources[0]
	again := submit(t, reader, m, record, "")
	if p.ID != again.ID || m.writes != 1 {
		t.Fatal("duplicate proposal or write")
	}
	state := reader.Refresh(Decode(m.plane))
	if len(state.Records) != 0 || state.Proposals[0].Status != "pending" || !strings.Contains(Context(state), "unverified") {
		t.Fatal("pending solution promoted")
	}
	item := review(t, reader, m, p.ID, "accept")
	if changed, err := SaveReview(context.Background(), m, item); err != nil || !changed {
		t.Fatalf("review write: %v", err)
	}
	state = reader.Refresh(Decode(m.plane))
	if len(state.Records) != 1 || state.Records[0].Scope != "inspected" || state.Proposals[0].Status != "accepted" {
		t.Fatalf("promotion lost: %+v", state)
	}
	// One ledger value, never a partially published separate record/status pair.
	if len(m.plane) != 2 {
		t.Fatal("acceptance split across keys")
	}
	retry := review(t, reader, m, p.ID, "accept")
	if changed, err := SaveReview(context.Background(), m, retry); err != nil || changed || m.writes != 2 {
		t.Fatal("review retry wrote or duplicated")
	}
	filtered := Filter(state, "freshness")
	if len(filtered.Proposals) != 1 || len(filtered.Records) != 1 {
		t.Fatal("topic query lost proposal")
	}
	graph := BuildGraph(state)
	found := false
	for _, edge := range graph.Edges {
		if edge.Kind == "reviewed-as" {
			found = true
		}
	}
	if !found {
		t.Fatal("native graph lost promotion provenance")
	}
}
func TestProposalsRequireInspectedHashesAndChangedContractsRefuseAcceptance(t *testing.T) {
	root, reader, record, m := proposalFixture(t)
	missing := record
	missing.Sources = missing.Sources[1:]
	if _, err := reader.PrepareProposal(missing, "", State{}); err == nil {
		t.Fatal("missing DOX hash accepted")
	}
	p := submit(t, reader, m, record, "")
	writeSource(t, root, "docs/DOX.md", "# New contract\n")
	state := reader.Refresh(Decode(m.plane))
	if _, err := PrepareReview(state, p.ID, "accept", "reviewer", "Read old sources", "tested", "Historical tests"); err == nil {
		t.Fatal("new DOX silently rebound")
	}
	if state.Proposals[0].Validity != "needs_review" || state.Proposals[0].Scope != "recorded" {
		t.Fatal("source change upgraded proposal")
	}
	if err := os.Remove(filepath.Join(root, "docs/DOX.md")); err != nil {
		t.Fatal(err)
	}
	writeSource(t, root, "docs/plan.md", "# Changed plan\n")
	if _, err := reader.PrepareProposal(record, "", state); err == nil {
		t.Fatal("old inspection hash accepted")
	}
	fresh, err := reader.Bind(record)
	if err != nil {
		t.Fatal(err)
	}
	replacement := submit(t, reader, m, fresh, p.ID)
	state = reader.Refresh(Decode(m.plane))
	statuses := map[string]string{}
	for _, v := range state.Proposals {
		statuses[v.ID] = v.Status
	}
	if statuses[p.ID] != "superseded" || statuses[replacement.ID] != "pending" || len(state.Records) != 0 {
		t.Fatal("replacement erased history or accepted learning")
	}
}
func TestConflictingConcurrentReviewsAndTopicsWithholdPromotion(t *testing.T) {
	_, reader, record, m := proposalFixture(t)
	p := submit(t, reader, m, record, "")
	// Both decisions read the same pending snapshot before either writes.
	accept := review(t, reader, m, p.ID, "accept")
	reject := review(t, reader, m, p.ID, "reject")
	if _, err := SaveReview(context.Background(), m, accept); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveReview(context.Background(), m, reject); err != nil {
		t.Fatal(err)
	}
	state := Decode(m.plane)
	if len(state.Records) != 0 || state.Proposals[0].Status != "conflict" || len(state.Proposals[0].Reviews) != 2 {
		t.Fatal("last writer hid conflicting review")
	}
	// Two proposals for one topic may also race; neither is an arbitrary winner.
	_, reader2, record2, m2 := proposalFixture(t)
	a := submit(t, reader2, m2, record2, "")
	record2.Summary = "Different proposed solution"
	b := submit(t, reader2, m2, record2, "")
	ar := review(t, reader2, m2, a.ID, "accept")
	br := review(t, reader2, m2, b.ID, "accept")
	_, _ = SaveReview(context.Background(), m2, ar)
	_, _ = SaveReview(context.Background(), m2, br)
	state = Decode(m2.plane)
	if len(state.Records) != 0 || state.Proposals[0].Status != "conflict" || state.Proposals[1].Status != "conflict" {
		t.Fatal("duplicate accepted topic arbitrarily promoted")
	}
}
func TestExistingSolutionsRemainAuthoritativeAndRejectionPersists(t *testing.T) {
	_, reader, record, m := proposalFixture(t)
	p := submit(t, reader, m, record, "")
	item := review(t, reader, m, p.ID, "accept")
	legacy := record
	legacy.Summary = "Previously solved and reviewed"
	legacy.Scope = "tested"
	legacy.Evidence = "Historical passing tests"
	if err := SaveRecord(context.Background(), m, legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareReview(reader.Refresh(Decode(m.plane)), p.ID, "accept", "reviewer", "New proposal", "inspected", "Read"); err == nil {
		t.Fatal("existing solution replaced")
	}
	// An acceptance racing with an explicit legacy record still cannot overwrite it.
	_, _ = SaveReview(context.Background(), m, item)
	state := Decode(m.plane)
	if len(state.Records) != 1 || state.Records[0].Summary != legacy.Summary || state.Records[0].Scope != "tested" {
		t.Fatal("old solution lost")
	}
	record.ID = "another-topic"
	p2 := submit(t, reader, m, record, "")
	reject := review(t, reader, m, p2.ID, "reject")
	_, _ = SaveReview(context.Background(), m, reject)
	state = Decode(m.plane)
	for _, v := range state.Proposals {
		if v.ID == p2.ID && v.Status != "rejected" {
			t.Fatal("rejection lost")
		}
	}
	if len(state.Records) != 1 {
		t.Fatal("rejected proposal promoted")
	}
}
func TestProposalBoundsMalformedEntriesAndContext(t *testing.T) {
	_, reader, record, m := proposalFixture(t)
	p := submit(t, reader, m, record, "")
	m.plane[proposalPrefix+"bad"] = "{malformed"
	state := Decode(m.plane)
	if len(state.Proposals) != 1 || len(state.Warnings) != 1 || strings.Contains(Context(state), "{malformed") {
		t.Fatal("malformed proposal leaked")
	}
	// Oversized valid-looking ledger must fail closed for promotions.
	item := review(t, reader, m, p.ID, "accept")
	_, _ = SaveReview(context.Background(), m, item)
	for i := 0; i < MaxProposals; i++ {
		copy := p
		copy.Record.ID = fmt.Sprintf("topic-%d", i)
		copy.ID = proposalID(copy)
		data, _ := json.Marshal(proposalEnvelope{Version: 1, Proposal: &copy})
		m.plane[proposalPrefix+copy.ID] = string(data)
	}
	state = Decode(m.plane)
	if len(state.Records) != 0 || len(state.Proposals) > MaxProposals {
		t.Fatal("overflow promoted incomplete ledger")
	}
	if len(Context(state)) > 6500 {
		t.Fatal("context exceeds bound")
	}
}

func TestActivityProposalOriginsPreserveLegacyIDsAndBlockStaleAcceptance(t *testing.T) {
	_, reader, record, m := proposalFixture(t)
	legacy, err := reader.PrepareProposal(record, "", State{})
	if err != nil {
		t.Fatal(err)
	}
	expected := "p-" + digest(struct {
		Record     Record `json:"record"`
		Supersedes string `json:"supersedes,omitempty"`
	}{canonical(legacy.Record), ""})
	if legacy.ID != expected {
		t.Fatal("existing proposal identity changed")
	}
	event, err := activity.Build(activity.Input{SessionID: "chat", TurnID: "turn", CWD: "/repo", Event: "Stop", LastAssistantMessage: "Report a solution; still requires source review"}, "/repo", time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(event)
	key := activity.Prefix + "event/" + event.ID
	m.plane[key] = string(raw)
	refs, err := activity.SelectReferences(m.plane, []string{event.ID})
	if err != nil {
		t.Fatal(err)
	}
	p, err := reader.PrepareActivityProposal(record, "", State{}, refs)
	if err != nil || p.ID == legacy.ID {
		t.Fatal("origin omitted from identity", err)
	}
	if _, err := SaveProposal(context.Background(), m, p); err != nil {
		t.Fatal(err)
	}
	state := reader.Refresh(Decode(m.plane))
	if len(state.Records) != 0 || state.Proposals[0].ActivityValidity != "current" {
		t.Fatal("reported handoff promoted", state)
	}
	delete(m.plane, key)
	stale := reader.Refresh(Decode(m.plane))
	if stale.Proposals[0].Validity != "needs_review" || len(stale.Proposals[0].Changed) != 1 {
		t.Fatal("source refresh hid absent origin", stale)
	}
	if _, err := PrepareReview(stale, p.ID, "accept", "supervisor", "Sources read", "inspected", "No tests rerun"); err == nil {
		t.Fatal("missing origin accepted")
	}
	if _, err := PrepareReview(stale, p.ID, "reject", "supervisor", "Origin absent", "", ""); err != nil {
		t.Fatal("cannot reject missing origin", err)
	}
	m.plane[key] = string(raw)
	item := review(t, reader, m, p.ID, "accept")
	if _, err := SaveReview(context.Background(), m, item); err != nil {
		t.Fatal(err)
	}
	delete(m.plane, key)
	accepted := reader.Refresh(Decode(m.plane))
	if len(accepted.Records) != 1 || accepted.Records[0].Scope != "inspected" || accepted.Proposals[0].Status != "accepted" || accepted.Proposals[0].ActivityValidity != "needs_review" {
		t.Fatal("journal loss erased reviewed assertion", accepted)
	}
}
