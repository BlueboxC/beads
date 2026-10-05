package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKnowledgeCapacityPreservesMoreThan64Assertions(t *testing.T) {
	_, reader, record, memories := proposalFixture(t)
	for i := 0; i < 80; i++ {
		item := record
		item.ID = fmt.Sprintf("existing-%03d", i)
		if err := SaveRecord(context.Background(), memories, item); err != nil {
			t.Fatal(err)
		}
	}
	state := reader.Refresh(Decode(memories.plane))
	if len(state.Records) != 80 || len(state.Warnings) != 0 {
		t.Fatalf("lost existing assertions: %d, %v", len(state.Records), state.Warnings)
	}
	proposal := submit(t, reader, memories, record, "")
	accepted := review(t, reader, memories, proposal.ID, "accept")
	if _, err := SaveReview(context.Background(), memories, accepted); err != nil {
		t.Fatal(err)
	}
	state = reader.Refresh(Decode(memories.plane))
	if len(state.Records) != 81 || state.Proposals[0].Status != "accepted" {
		t.Fatalf("lost promotion after 64 assertions: %+v", state)
	}
	if len(Context(state)) > 6500 {
		t.Fatal("capacity expanded injected context")
	}
}

func TestKnowledgeCapacityPreservesMoreThan64PendingProposals(t *testing.T) {
	_, reader, record, memories := proposalFixture(t)
	for i := 0; i < 80; i++ {
		item := record
		item.ID = fmt.Sprintf("proposal-%03d", i)
		submit(t, reader, memories, item, "")
	}
	state := reader.Refresh(Decode(memories.plane))
	if len(state.Proposals) != 80 || len(state.Records) != 0 {
		t.Fatal("pending ledger lost proposals or promoted without review")
	}
}

func TestKnowledgeCapacityPreservesMoreThan128Reviews(t *testing.T) {
	_, reader, record, memories := proposalFixture(t)
	for i := 0; i < 160; i++ {
		item := record
		item.ID = fmt.Sprintf("rejected-%03d", i)
		proposal := submit(t, reader, memories, item, "")
		decision := review(t, reader, memories, proposal.ID, "reject")
		if _, err := SaveReview(context.Background(), memories, decision); err != nil {
			t.Fatal(err)
		}
	}
	state := reader.Refresh(Decode(memories.plane))
	if len(state.Proposals) != 160 || len(state.Records) != 0 || len(state.Warnings) != 0 {
		t.Fatal("review history lost or rejected solutions promoted")
	}
	for _, proposal := range state.Proposals {
		if proposal.Status != "rejected" || len(proposal.Reviews) != 1 {
			t.Fatal("review history lost after 128 entries")
		}
	}
}

func TestKnowledgeLargeSourceHashAndFreshReadsRemainBounded(t *testing.T) {
	root := t.TempDir()
	content := strings.Repeat("x", 3*1024*1024) + "tail"
	writeSource(t, root, "catalog.json", content)
	reader, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	record, err := reader.Bind(Record{ID: "large-source", Kind: "module", Summary: "Reviewed catalog", Scope: "recorded", Sources: []Source{{Path: "catalog.json"}}})
	if err != nil {
		t.Fatal(err)
	}
	expected := sha256.Sum256([]byte(content))
	if record.Sources[0].SHA256 != hex.EncodeToString(expected[:]) {
		t.Fatal("hash does not cover complete large source")
	}
	state := State{Records: []View{{Record: record}}}
	if reader.Refresh(state).Records[0].Validity != "current" {
		t.Fatal("initial source rejected")
	}
	writeSource(t, root, "catalog.json", content+"changed-after-first-read")
	changed := reader.Refresh(state).Records[0]
	if changed.Validity != "needs_review" || changed.Sources[0].SHA256 != record.Sources[0].SHA256 {
		t.Fatal("snapshot cache hid later drift or renewed evidence")
	}
	writeSource(t, root, "catalog.json", content)
	writeSource(t, root, "DOX.md", "# Introduced contract\n")
	contractChanged := reader.Refresh(state).Records[0]
	if contractChanged.Validity != "needs_review" || !strings.Contains(strings.Join(contractChanged.Changed, ","), "DOX.md") {
		t.Fatal("snapshot cache hid a newly introduced DOX contract")
	}
	file, err := os.Create(filepath.Join(root, "too-large.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(int64(MaxFileBytes) + 1); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Read("too-large.txt"); err == nil {
		t.Fatal("source size bound removed")
	}
}
