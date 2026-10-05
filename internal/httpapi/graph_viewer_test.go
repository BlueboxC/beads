package httpapi

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The exclusive viewer must reuse Host/auth defenses without exposing any of
// the normal issue-write surface or claiming its v0 capabilities.
func TestGraphViewerUsesTransportGuardsAndExcludesIssueAPI(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("operator-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	auth, err := NewTokenFileAuth(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	called := 0
	viewer := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called++; _, _ = io.WriteString(w, "viewer") })
	srv, err := Listen(Config{Addr: "127.0.0.1:0", Auth: auth, GraphViewer: viewer, Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	for _, tc := range []struct {
		method, path, token, host string
		want                      int
	}{
		{"GET", "/viewer", "", "", 200}, {"GET", "/viewer/graph", "", "", 401},
		{"GET", "/viewer/graph", "operator-token", "", 200},
		{"GET", "/viewer/graph", "operator-token", "evil.example", 400},
		{"POST", "/v0/beads/issues", "operator-token", "", 404},
		{"GET", "/v0/beads/context", "operator-token", "", 404},
	} {
		req, err := http.NewRequest(tc.method, "http://"+srv.Addr()+tc.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if tc.token != "" {
			req.Header.Set("Authorization", "Bearer "+tc.token)
		}
		if tc.host != "" {
			req.Host = tc.host
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Fatalf("%s %s = %d", tc.method, tc.path, resp.StatusCode)
		}
	}
	if called != 2 {
		t.Fatalf("unexpected viewer access %d", called)
	}
	if _, err := Listen(Config{Addr: "127.0.0.1:0", GraphViewer: viewer, Provider: &fakeProvider{}}); err == nil || !strings.Contains(err.Error(), "combined") {
		t.Fatal("viewer accepted a write API source")
	}
}
