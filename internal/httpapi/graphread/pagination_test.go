package graphread

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// Cases adapted from the pinned BDP pagination reference: immutable snapshot,
// stable replay, authority fences, expiry, and capacity that cannot evict pages.
func pageTestAuthority(t *testing.T, change func(*PaginationOptions)) *Pagination {
	t.Helper()
	options := DefaultPaginationOptions("https://example.test/team/")
	options.DefaultPageItems = 1
	if change != nil {
		change(&options)
	}
	p, err := NewPagination(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}
func pageTestInput(items ...string) FirstPageInput {
	input := FirstPageInput{AuthorizationView: "alice-policy-1", ScopeEpoch: "epoch-1", Projection: "beads", ContinuationURL: "https://example.test/team/beads/?type=https%3A%2F%2Fexample.test%2Ftype&limit=1"}
	for _, item := range items {
		input.Items = append(input.Items, json.RawMessage(item))
	}
	return input
}
func pageTestToken(t *testing.T, page Page) string {
	t.Helper()
	if page.Next == nil {
		t.Fatal("missing continuation")
	}
	u, err := url.Parse(*page.Next)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query().Get("cursor")
}
func pageTestContinue(token string) ContinuationInput {
	return ContinuationInput{Token: token, AuthorizationView: "alice-policy-1", ScopeEpoch: "epoch-1", Projection: "beads"}
}
func pageTestError(t *testing.T, err error, code string) {
	t.Helper()
	var outcome *PaginationError
	if !errors.As(err, &outcome) || outcome.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}

func TestPaginationSnapshotReplayAndMutation(t *testing.T) {
	p := pageTestAuthority(t, nil)
	input := pageTestInput(`{"id":"a","revision":"old"}`, `{"id":"b","revision":"old"}`, `{"id":"c","revision":"old"}`)
	first, err := p.FirstPage(input)
	if err != nil {
		t.Fatal(err)
	}
	token := pageTestToken(t, first)
	input.Items[1][1] = 'x'
	first.Items[0][1] = 'x'
	*first.Next = "overwritten"
	second, err := p.ContinuePage(pageTestContinue(token))
	if err != nil {
		t.Fatal(err)
	}
	if string(second.Items[0]) != `{"id":"b","revision":"old"}` {
		t.Fatal("input mutation escaped")
	}
	replay, err := p.ContinuePage(pageTestContinue(token))
	if err != nil || !reflect.DeepEqual(second, replay) {
		t.Fatalf("replay changed: %v", err)
	}
	second.Items[0][1] = 'x'
	again, err := p.ContinuePage(pageTestContinue(token))
	if err != nil || !reflect.DeepEqual(again, replay) {
		t.Fatal("output mutation escaped")
	}
	lastToken := pageTestToken(t, replay)
	terminal, err := p.ContinuePage(pageTestContinue(lastToken))
	if err != nil || terminal.Next != nil || string(terminal.Items[0]) != `{"id":"c","revision":"old"}` {
		t.Fatalf("terminal: %+v %v", terminal, err)
	}
	// A newer source snapshot does not change already-issued pages.
	if _, err = p.FirstPage(pageTestInput(`{"id":"b","revision":"new"}`)); err != nil {
		t.Fatal(err)
	}
	old, err := p.ContinuePage(pageTestContinue(token))
	if err != nil || !reflect.DeepEqual(old, replay) {
		t.Fatal("new state changed old snapshot")
	}
}

func TestPaginationEmptyAndLimits(t *testing.T) {
	p := pageTestAuthority(t, nil)
	empty, err := p.FirstPage(pageTestInput())
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(empty)
	if string(data) != `{"items":[],"next":null}` {
		t.Fatal(string(data))
	}
	for _, limit := range []int{-1, 1001} {
		input := pageTestInput(`1`)
		input.Limit = limit
		_, err := p.FirstPage(input)
		pageTestError(t, err, "invalid-limit")
	}
	for _, limit := range []int{0, 1, 1000} {
		input := pageTestInput(`1`)
		input.Limit = limit
		page, err := p.FirstPage(input)
		if err != nil || page.Next != nil {
			t.Fatalf("limit %d: %v", limit, err)
		}
	}
}

func TestPaginationFencesAndEpochRelease(t *testing.T) {
	p := pageTestAuthority(t, func(o *PaginationOptions) { o.MaxSnapshots = 1 })
	first, err := p.FirstPage(pageTestInput(`1`, `2`))
	if err != nil {
		t.Fatal(err)
	}
	token := pageTestToken(t, first)
	for _, field := range []string{"view", "projection"} {
		input := pageTestContinue(token)
		code := "foreign-view"
		if field == "view" {
			input.AuthorizationView = "bob"
		} else {
			input.Projection = "links"
			code = "foreign-projection"
		}
		_, err = p.ContinuePage(input)
		pageTestError(t, err, code)
		if _, err = p.ContinuePage(pageTestContinue(token)); err != nil {
			t.Fatal("foreign request consumed valid cursor", err)
		}
	}
	fresh := pageTestInput(`3`, `4`)
	fresh.ScopeEpoch = "epoch-2"
	if _, err = p.FirstPage(fresh); err != nil {
		t.Fatal("epoch did not release old capacity", err)
	}
	_, err = p.ContinuePage(pageTestContinue(token))
	pageTestError(t, err, "cursor-expired")
	// A restore observed on continuation also invalidates that snapshot.
	freshPage, err := p.FirstPage(pageTestInput(`5`, `6`))
	if err != nil {
		t.Fatal(err)
	}
	input := pageTestContinue(pageTestToken(t, freshPage))
	input.ScopeEpoch = "epoch-3"
	_, err = p.ContinuePage(input)
	pageTestError(t, err, "cursor-expired")
	if len(p.snapshots) != 0 || p.retainedBytes != 0 {
		t.Fatal("epoch retained old state")
	}
}

func TestPaginationExpiryAndBackwardClock(t *testing.T) {
	p := pageTestAuthority(t, nil)
	now := time.Unix(10000, 0)
	p.now = func() time.Time { return now }
	first, err := p.FirstPage(pageTestInput(`1`, `2`, `3`))
	if err != nil {
		t.Fatal(err)
	}
	token := pageTestToken(t, first)
	now = now.Add(4 * time.Minute)
	second, err := p.ContinuePage(pageTestContinue(token))
	if err != nil {
		t.Fatal(err)
	}
	last := pageTestToken(t, second)
	if _, err = p.ContinuePage(pageTestContinue(last)); err != nil {
		t.Fatal(err)
	}
	now = now.Add(-3 * time.Minute)
	if _, err = p.ContinuePage(pageTestContinue(token)); err != nil {
		t.Fatal(err)
	}
	now = time.Unix(10000, 0).Add(5 * time.Minute)
	_, err = p.ContinuePage(pageTestContinue(last))
	pageTestError(t, err, "cursor-expired")
	if len(p.cursors) != 0 || p.retainedBytes != 0 {
		t.Fatal("expiry leaked state")
	}
	other := pageTestAuthority(t, nil)
	_, err = other.ContinuePage(pageTestContinue(token))
	pageTestError(t, err, "cursor-expired")
}

func TestPaginationCapacityNeverEvicts(t *testing.T) {
	p := pageTestAuthority(t, func(o *PaginationOptions) {
		o.MaxSnapshots = 1
		o.MaxCursorPositions = 2
		o.MaxCursorPositionsPerSnapshot = 2
	})
	first, err := p.FirstPage(pageTestInput(`1`, `2`, `3`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.FirstPage(pageTestInput(`4`, `5`))
	pageTestError(t, err, "capacity-exceeded")
	page := first
	for page.Next != nil {
		page, err = p.ContinuePage(pageTestContinue(pageTestToken(t, page)))
		if err != nil {
			t.Fatal(err)
		}
	}
	// Terminal access does not evict the snapshot or its original cursor.
	if _, err = p.ContinuePage(pageTestContinue(pageTestToken(t, first))); err != nil {
		t.Fatal(err)
	}
	if _, err = p.FirstPage(pageTestInput(`6`)); err != nil {
		t.Fatal("one-page response charged as retained state")
	}
}

func TestPaginationAdmissionBounds(t *testing.T) {
	cases := []struct {
		name    string
		options func(*PaginationOptions)
		items   []string
	}{
		{"item", func(o *PaginationOptions) { o.MaxSnapshotItems = 1 }, []string{`1`, `2`}},
		{"per-snapshot-bytes", func(o *PaginationOptions) { o.MaxSnapshotBytes = 2 }, []string{`"a"`}},
		{"cursor-positions", func(o *PaginationOptions) { o.MaxCursorPositionsPerSnapshot = 1 }, []string{`1`, `2`, `3`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := pageTestAuthority(t, tc.options)
			_, err := p.FirstPage(pageTestInput(tc.items...))
			pageTestError(t, err, "capacity-exceeded")
			if len(p.snapshots) != 0 || p.retainedBytes != 0 {
				t.Fatal("failed admission leaked state")
			}
		})
	}
	p := pageTestAuthority(t, func(o *PaginationOptions) { o.MaxSnapshotBytes = 4; o.MaxRetainedBytes = 4 })
	first, err := p.FirstPage(pageTestInput(`1`, `2`, `3`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.FirstPage(pageTestInput(`4`, `5`))
	pageTestError(t, err, "capacity-exceeded")
	if _, err = p.ContinuePage(pageTestContinue(pageTestToken(t, first))); err != nil {
		t.Fatal(err)
	}
}

func TestPaginationInvalidSnapshotAndFences(t *testing.T) {
	p := pageTestAuthority(t, nil)
	for _, item := range []string{"", "{", "NaN", "1 2"} {
		_, err := p.FirstPage(pageTestInput(item))
		pageTestError(t, err, "invalid-snapshot")
	}
	for _, field := range []string{"view", "epoch", "projection"} {
		input := pageTestInput(`1`, `2`)
		switch field {
		case "view":
			input.AuthorizationView = ""
		case "epoch":
			input.ScopeEpoch = " "
		case "projection":
			input.Projection = strings.Repeat("x", PaginationContextLimit+1)
		}
		_, err := p.FirstPage(input)
		pageTestError(t, err, "invalid-input")
	}
}

func TestPaginationURLConfinement(t *testing.T) {
	p := pageTestAuthority(t, nil)
	for _, value := range []string{"/team/beads/", "https://other.test/team/beads/", "https://user@example.test/team/beads/", "https://example.test/teammate/beads/", "https://example.test/team/../beads/", "https://example.test/team/%2e%2e/beads/", "https://example.test/team/beads/?cursor=x", "https://example.test/team/beads/#fragment", "https://example.test/team/beads/?%63ursor=x", "https://example.test/team/beads/?x=%GG"} {
		input := pageTestInput(`1`, `2`)
		input.ContinuationURL = value
		_, err := p.FirstPage(input)
		pageTestError(t, err, "invalid-input")
	}
	first, err := p.FirstPage(pageTestInput(`1`, `2`))
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(*first.Next)
	if u.Query().Get("limit") != "1" || u.Query().Get("type") != "https://example.test/type" {
		t.Fatal("projection parameters lost")
	}
}

func TestPaginationTokenFailureAtomicity(t *testing.T) {
	p := pageTestAuthority(t, nil)
	stable := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	p.token = func() (string, error) { return stable, nil }
	_, err := p.FirstPage(pageTestInput(`1`, `2`, `3`))
	pageTestError(t, err, "token-generation-failed")
	if len(p.cursors) != 0 || len(p.snapshots) != 0 || p.retainedBytes != 0 {
		t.Fatal("failed entropy leaked state")
	}
	p.token = func() (string, error) { return "", errors.New("entropy unavailable") }
	_, err = p.FirstPage(pageTestInput(`1`, `2`))
	pageTestError(t, err, "token-generation-failed")
	p.token = randomPageToken
	if _, err = p.FirstPage(pageTestInput(`1`, `2`, `3`)); err != nil {
		t.Fatal(err)
	}
}

func TestPaginationConcurrentReplay(t *testing.T) {
	p := pageTestAuthority(t, nil)
	first, err := p.FirstPage(pageTestInput(`1`, `2`, `3`))
	if err != nil {
		t.Fatal(err)
	}
	token := pageTestToken(t, first)
	expected, err := p.ContinuePage(pageTestContinue(token))
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for range 20 {
		workers.Go(func() {
			for range 20 {
				page, err := p.ContinuePage(pageTestContinue(token))
				if err != nil || !reflect.DeepEqual(page, expected) {
					t.Errorf("concurrent replay: %v", err)
					return
				}
				page.Items[0][0] = '9'
			}
		})
	}
	workers.Wait()
}

func TestPaginationCleanupCloseConfiguration(t *testing.T) {
	p := pageTestAuthority(t, nil)
	now := time.Unix(10000, 0)
	p.now = func() time.Time { return now }
	first, err := p.FirstPage(pageTestInput(`1`, `2`))
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(5 * time.Minute)
	if released := p.CleanupExpired(); released != 1 {
		t.Fatal(released)
	}
	if _, err = p.FirstPage(pageTestInput(`1`, `2`)); err != nil {
		t.Fatal(err)
	}
	p.Close()
	p.Close()
	if len(p.cursors) != 0 || p.retainedBytes != 0 {
		t.Fatal("close leaked state")
	}
	_, err = p.ContinuePage(pageTestContinue(pageTestToken(t, first)))
	pageTestError(t, err, "configuration-error")
	_, err = p.FirstPage(pageTestInput(`1`))
	pageTestError(t, err, "configuration-error")
	for _, scope := range []string{"bad", "https://example.test/team", "https://u@example.test/team/", "https://example.test/team/?q=a", "https://example.test/team/../"} {
		_, err := NewPagination(DefaultPaginationOptions(scope))
		pageTestError(t, err, "configuration-error")
	}
	bad := DefaultPaginationOptions("https://example.test/")
	bad.MaxSnapshots = 0
	_, err = NewPagination(bad)
	pageTestError(t, err, "configuration-error")
	bad = DefaultPaginationOptions("https://example.test/")
	bad.DefaultPageItems = bad.MaxPageItems + 1
	_, err = NewPagination(bad)
	pageTestError(t, err, "configuration-error")
	if fmt.Sprint(err) == "" {
		t.Fatal("error has no message")
	}
}

func TestPaginationCanonicalEncodedPathsAndLongProjection(t *testing.T) {
	for _, scope := range []string{"https://example.test/acme+/", "https://example.test/acme!/", "https://example.test/caf%C3%A9/"} {
		p, err := NewPagination(DefaultPaginationOptions(scope))
		if err != nil {
			t.Fatal(err)
		}
		defer p.Close()
		input := pageTestInput(`1`, `2`)
		input.Limit = 1
		input.ContinuationURL = scope + "beads/a!b?view=links&selector=" + strings.Repeat("a", 16<<10)
		input.Projection = strings.Repeat("p", 16<<10)
		page, err := p.FirstPage(input)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(*page.Next, scope+"beads/a!b?") {
			t.Fatal("canonical path changed", *page.Next)
		}
		continuation := pageTestContinue(pageTestToken(t, page))
		continuation.Projection = input.Projection
		if _, err = p.ContinuePage(continuation); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{"beads/a%2Fb", "beads/a%5Cb", "beads/café", "beads/caf%c3%a9", "beads/a%2Bb", "beads/%61", "beads//b", "beads/../b"} {
			input.ContinuationURL = scope + path + "?view=links"
			_, err = p.FirstPage(input)
			pageTestError(t, err, "invalid-input")
		}
	}
	for _, scope := range []string{"https://example.test/acme%2Fpart/", "https://example.test/acme%2B/", "https://example.test/café/"} {
		_, err := NewPagination(DefaultPaginationOptions(scope))
		pageTestError(t, err, "configuration-error")
	}
}

func TestPaginationContinuationTargetBound(t *testing.T) {
	p := pageTestAuthority(t, nil)
	input := pageTestInput(`1`, `2`)
	prefix := "https://example.test/team/beads/?selector="
	input.ContinuationURL = prefix + strings.Repeat("a", PaginationContextLimit-len(prefix))
	_, err := p.FirstPage(input)
	pageTestError(t, err, "invalid-input")
	input.ContinuationURL = prefix + strings.Repeat("a", PaginationContextLimit-len(prefix)-60)
	page, err := p.FirstPage(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(*page.Next) > PaginationContextLimit {
		t.Fatal("issued oversized target")
	}
}
