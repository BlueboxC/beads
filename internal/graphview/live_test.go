package graphview

import (
	"context"
	"errors"
	"github.com/steveyegge/beads/internal/activity"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLiveGraphCoalescesReadsAndRecoversWithoutStaleSuccess(t *testing.T) {
	calls := 0
	failed := false
	h := NewLiveHandler(func(ctx context.Context) (Page, error) {
		calls++
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > liveQueryDeadline {
			t.Error("query has no bound")
		}
		if failed {
			return Page{}, errors.New("private storage error")
		}
		return Page{Workspace: "/selected", Title: "changed"}, nil
	})
	get := func(etag string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/viewer/graph", nil)
		req.Header.Set("If-None-Match", etag)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	first := get("")
	if first.Code != 200 || calls != 1 {
		t.Fatal("initial query failed")
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Go(func() {
			if get(first.Header().Get("ETag")).Code != 304 {
				t.Error("unchanged snapshot transferred again")
			}
		})
	}
	wg.Wait()
	if calls != 1 {
		t.Fatal("concurrent browsers caused parallel database reads")
	}
	failed = true
	h.next = time.Time{}
	bad := get("")
	if bad.Code != 503 || strings.Contains(bad.Body.String(), "private storage") {
		t.Fatal("failure leaked detail or served stale success")
	}
	failed = false
	h.next = time.Time{}
	if get("").Code != 200 || calls != 3 {
		t.Fatal("reconnection failed")
	}
}

func TestLiveGraphShellAndRequestConfinement(t *testing.T) {
	calls := 0
	h := NewLiveHandler(func(context.Context) (Page, error) {
		calls++
		return Page{Workspace: "/private/project", Activity: &activity.View{Events: []activity.Event{{Summary: "private-handoff"}}}}, nil
	})
	for _, tc := range []struct {
		method, path string
		want         int
	}{{"GET", "/viewer", 200}, {"GET", "/viewer/graph?workspace=other", 400}, {"POST", "/viewer/graph", 400}, {"GET", "/foreign", 404}} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Code != tc.want {
			t.Fatalf("%s = %d", tc.path, w.Code)
		}
		if strings.Contains(w.Body.String(), "/private/project") || strings.Contains(w.Body.String(), "private-handoff") {
			t.Fatal("public shell disclosed workspace")
		}
	}
	if calls != 0 {
		t.Fatal("unsupported requests queried database")
	}
}
