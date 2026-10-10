//go:build cgo

package graphstore

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// A workspace lists the same Issues whether it is read as Beads or as Issues,
// including the statuses it configures itself. The pinned flag is not part of
// this fixture: no writer admits it, and a store whose Issue row carries it
// differs from its retained state, which both listings refuse as invalid.
func TestListBeadsHidesWhatTheIssueListHides(t *testing.T) {
	for _, backend := range []string{"embedded", "server"} {
		t.Run(backend, func(t *testing.T) {
			ctx, _, s := issueListFixture(t, backend)
			setIssueListConfig(t, ctx, s, "status.custom", "triaged:active,archived:done,on-ice:frozen")
			if _, err := s.Create(ctx, CreateRequest{Path: "beads/plan", Title: "Plan", Body: "memory body", Actor: "test-author"}); err != nil {
				t.Fatal(err)
			}
			paths := []string{}
			for _, status := range []types.Status{types.StatusOpen, types.StatusClosed, types.StatusPinned, "triaged", "archived", "on-ice"} {
				request := plainIssue(string(status))
				request.Issue.Status = status
				path := "beads/" + string(status)
				if _, err := s.CreateIssue(ctx, path, request); err != nil {
					t.Fatal(err)
				}
				paths = append(paths, path)
			}

			listed := func(request BeadListRequest) []string {
				t.Helper()
				page, err := s.ListBeads(ctx, request)
				if err != nil {
					t.Fatal(err)
				}
				got := beadListPaths(t, page, s.ScopeURL())
				slices.Sort(got)
				return got
			}
			ordinary, err := s.ListIssues(ctx, publicops.ListRequest{})
			if err != nil {
				t.Fatal(err)
			}
			shownIssues := []string{}
			for _, item := range ordinary.Items {
				shownIssues = append(shownIssues, strings.TrimPrefix(item.ID, s.ScopeURL()))
			}
			slices.Sort(shownIssues)
			if want := []string{"beads/open", "beads/triaged"}; !slices.Equal(shownIssues, want) {
				t.Fatalf("the ordinary Issue list shows %v, want %v: the fixture no longer exercises every arm", shownIssues, want)
			}

			wantDefault := append([]string{"beads/plan"}, shownIssues...)
			slices.Sort(wantDefault)
			if got := listed(BeadListRequest{}); !slices.Equal(got, wantDefault) {
				t.Fatalf("default Bead list %v, want the Memory and the Issues the ordinary list shows %v", got, wantDefault)
			}
			everything := append([]string{"beads/plan"}, paths...)
			slices.Sort(everything)
			if got := listed(BeadListRequest{All: true}); !slices.Equal(got, everything) {
				t.Fatalf("--all Bead list %v, want %v", got, everything)
			}
			if got := listed(BeadListRequest{TypeURL: IssueTypeURL(s.ScopeURL())}); !slices.Equal(got, shownIssues) {
				t.Fatalf("Issue Type list %v, want %v", got, shownIssues)
			}
			if got := listed(BeadListRequest{TypeURL: MemoryTypeURL(s.ScopeURL())}); !slices.Equal(got, []string{"beads/plan"}) {
				t.Fatalf("Memory Type list %v", got)
			}
			for _, notABeadType := range []string{s.ScopeURL() + "types/uninstalled", DependencyTypeURL(s.ScopeURL())} {
				if _, err := s.ListBeads(ctx, BeadListRequest{TypeURL: notABeadType}); !errors.Is(err, ErrCapabilityUnavailable) {
					t.Fatalf("%s must refuse as unavailable, got %v", notABeadType, err)
				}
			}
		})
	}
}
