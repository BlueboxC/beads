package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/steveyegge/beads/issueops"
)

type externalContextReader struct{ issueops.Reader }

func (externalContextReader) Ready(ctx context.Context, _ issueops.ReadyRequest) (issueops.IssuePage, error) {
	resolve := issueops.ExternalResolverFromContext(ctx)
	if resolve == nil {
		return issueops.IssuePage{}, fmt.Errorf("resolver missing from request")
	}
	_, err := resolve(ctx, []string{"external:peer:cap"})
	return issueops.IssuePage{Items: []*issueops.IssueWithCounts{}}, err
}

func TestExternalResolverRequestContextRespectsAuth(t *testing.T) {
	var calls atomic.Int32
	ts := newTestServer(t, rolesConfig(Config{
		Reader: externalContextReader{}, Auth: authTokenFile(t, "real-token"),
		ExternalResolver: func(context.Context, []string) (map[string]bool, error) { calls.Add(1); return map[string]bool{}, nil },
	}))
	for _, authorized := range []bool{false, true} {
		req, err := http.NewRequest(http.MethodGet, ts.base+"/v0/beads/ready", nil)
		if err != nil {
			t.Fatal(err)
		}
		if authorized {
			req.Header.Set("Authorization", "Bearer real-token")
		}
		res, err := ts.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		want := http.StatusUnauthorized
		if authorized {
			want = http.StatusOK
		}
		if res.StatusCode != want {
			t.Fatalf("authorized=%v: status=%d", authorized, res.StatusCode)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("resolver calls = %d, want only authorized request", calls.Load())
	}
}
