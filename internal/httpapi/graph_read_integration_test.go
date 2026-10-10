//go:build cgo

package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/httpapi/bdpwire"
	"github.com/steveyegge/beads/internal/httpapi/graphread"
	"github.com/steveyegge/beads/internal/storage/graphstore"
	"github.com/steveyegge/beads/issueops"
)

// This exercises the actual listener, security gates and graph store. The CLI
// and independent public client are separately exercised by graph-bdp-read-smoke.
func TestGraphReadHTTPAuthorityAndSecurity(t *testing.T) {
	port, err := strconv.Atoi(os.Getenv("BEADS_GRAPH_TEST_SERVER_PORT"))
	if err != nil || port == 0 {
		t.Skip("ordinary shared-server Dolt port not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const scope = "https://authority.example/read/"
	options := graphstore.Options{Backend: "server", ServerHost: "127.0.0.1", ServerPort: port, ServerUser: "root", Database: fmt.Sprintf("graph_http_%d", time.Now().UnixNano()), Branch: "main", DataDir: filepath.Join(workspace, "dolt"), Binding: graphstore.Binding{WorkspaceID: workspace, ScopeURL: scope, AuthorityID: "0123456789abcdef0123456789abcdef", SchemaVersion: graphstore.SchemaVersion}}
	if err := graphstore.Init(ctx, options); err != nil {
		t.Fatal(err)
	}
	store, err := graphstore.OpenExisting(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for _, path := range []string{"beads/plan", "beads/other"} {
		request := graphstore.CreateRequest{Path: path, Title: "記憶"}
		if path == "beads/plan" {
			request.Metadata = json.RawMessage(`{"team":"docs"}`)
		}
		if _, err := store.Create(ctx, request); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.AddInformationalLink(ctx, graphstore.LinkCreateRequest{Path: "links/context", SourcePath: "beads/plan", TargetPath: "beads/other", UnconditionalSource: true, Properties: map[string]any{"note": "initial"}, Metadata: json.RawMessage(`{"origin":"manual"}`)}); err != nil {
		t.Fatal(err)
	}
	graph, err := NewGraphRead(graphread.New(store), scope)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(graph.Close)
	// Use a valid reader so mixed-source rejection cannot pass accidentally
	// through the invalid-reader fallback. Each incompatible source is isolated.
	if err := checkDatabaseSource(Config{GraphRead: graph}); err != nil {
		t.Fatalf("valid graph source refused: %v", err)
	}
	for name, cfg := range map[string]Config{
		"provider":       {GraphRead: graph, Provider: &fakeProvider{}},
		"legacy-role":    {GraphRead: graph, Reader: &roleReader{}},
		"journal-reader": {GraphRead: graph, EventsJournal: &roleEventsJournal{}},
		"journal-flag":   {GraphRead: graph, EventsJournalEnabled: true},
	} {
		t.Run("mixed-source-"+name, func(t *testing.T) {
			err := checkDatabaseSource(cfg)
			if err == nil || err.Error() != "httpapi: graph Read cannot be combined with legacy database sources" {
				t.Fatalf("mixed graph source was not explicitly refused: %v", err)
			}
		})
	}
	tokenFile := filepath.Join(workspace, "tokens")
	if err := os.WriteFile(tokenFile, []byte("first-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	auth, err := NewTokenFileAuth(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	auth.reloadGate = time.Nanosecond // exercise existing reload policy without a sleep
	srv := newTestServer(t, Config{GraphRead: graph, Auth: auth, AllowedHosts: []string{"transport.example"}})
	request := func(method, path, token, host string, headers http.Header) (*http.Response, []byte) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, method, srv.base+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header = headers.Clone()
		if req.Header == nil {
			req.Header = http.Header{}
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if host != "" {
			req.Host = host
		}
		response, err := srv.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if response.Header.Get("Cache-Control") != "private, no-store" {
			t.Fatal("authorization-dependent response is cacheable")
		}
		return response, body
	}
	assertProblem := func(response *http.Response, body []byte, code bdpwire.ReadProblemCode) {
		t.Helper()
		var problem bdpwire.ReadProblem
		if response.StatusCode != code.Status() || bdpwire.Unmarshal(body, &problem) != nil || problem.Code != code {
			t.Fatalf("want %s, got %d %s", code, response.StatusCode, body)
		}
	}
	response, body := request("GET", "/read/beads/plan", "", "", http.Header{"If-None-Match": {"*"}})
	assertProblem(response, body, bdpwire.CodeUnauthenticated)
	response, body = request("GET", "/read/beads/plan", "first-token", "evil.example", nil)
	assertProblem(response, body, bdpwire.CodeMalformedRequest)
	response, body = request("GET", "/read/beads/plan", "first-token", "transport.example", nil)
	var bead bdpwire.BeadRecord
	if response.StatusCode != 200 || bdpwire.Unmarshal(body, &bead) != nil || bead.ID != scope+"beads/plan" {
		t.Fatalf("transport alias reassigned identity: %d %s", response.StatusCode, body)
	}
	if string(bead.Metadata["team"]) != `"docs"` {
		t.Fatalf("HTTP current Memory omitted common metadata: %s", body)
	}
	response, body = request("GET", "/read/beads/other", "first-token", "", nil)
	var emptyMetadataBead bdpwire.BeadRecord
	if response.StatusCode != 200 || bdpwire.Unmarshal(body, &emptyMetadataBead) != nil || emptyMetadataBead.Metadata == nil || len(emptyMetadataBead.Metadata) != 0 {
		t.Fatalf("HTTP current Memory did not emit empty metadata object: %d %s", response.StatusCode, body)
	}
	response, body = request("GET", "/read/links/context", "first-token", "", nil)
	var directLink bdpwire.LinkRecord
	if response.StatusCode != 200 || bdpwire.Unmarshal(body, &directLink) != nil || string(directLink.Metadata["origin"]) != `"manual"` {
		t.Fatalf("HTTP current Link omitted common metadata: %d %s", response.StatusCode, body)
	}
	response, body = request("GET", "/read/beads/plan", "first-token", "transport.example", nil)
	getLength := len(body)
	response, body = request("HEAD", "/read/beads/plan", "first-token", "", nil)
	if response.StatusCode != 200 || len(body) != 0 || response.ContentLength != int64(getLength) {
		t.Fatalf("Unicode HEAD: %d %d %d", response.StatusCode, len(body), response.ContentLength)
	}
	for _, test := range []struct {
		path    string
		headers http.Header
		code    bdpwire.ReadProblemCode
	}{
		{"/read/beads/missing", http.Header{"Accept": {"image/png"}, "If-None-Match": {"*"}}, bdpwire.CodeResourceNotFound},
		{"/read/beads/?cursor=unknown", http.Header{"If-None-Match": {"*"}}, bdpwire.CodeCursorExpired},
		{"/read/beads/?limit=1&limit=2", nil, bdpwire.CodeInvalidParameter},
		{"/read/beads/?bad=%zz", nil, bdpwire.CodeInvalidParameter},
		{"/read/beads/plan?revision=old", nil, bdpwire.CodeInvalidParameter},
		{"/v0/beads/context", nil, bdpwire.CodeResourceNotFound},
	} {
		response, body = request("GET", test.path, "first-token", "", test.headers)
		assertProblem(response, body, test.code)
	}
	response, body = request("GET", "/read/beads/?limit=1", "first-token", "", nil)
	var first bdpwire.BeadCollection
	if response.StatusCode != 200 || bdpwire.Unmarshal(body, &first) != nil || first.Next == nil {
		t.Fatalf("first page: %d %s", response.StatusCode, body)
	}
	next, _ := url.Parse(*first.Next)
	// Atomic token rotation changes credential acceptance, not the full-workspace
	// authorization projection. Revoked credentials fail before cursor/conditions.
	newToken := filepath.Join(workspace, "tokens-new")
	if err := os.WriteFile(newToken, []byte("second-token-longer\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(newToken, tokenFile); err != nil {
		t.Fatal(err)
	}
	response, body = request("GET", next.RequestURI(), "first-token", "", http.Header{"If-None-Match": {"*"}})
	assertProblem(response, body, bdpwire.CodeUnauthenticated)
	response, body = request("GET", next.RequestURI(), "second-token-longer", "", nil)
	if response.StatusCode != 200 {
		t.Fatalf("accepted rotated token lost same projection: %d %s", response.StatusCode, body)
	}
	// Every aggregate must close over a single inventory even while writes race
	// the HTTP request. The owned and incident copy of a Link must agree.
	writerCtx, stopWriter := context.WithCancel(ctx)
	writerDone := make(chan error, 1)
	go func() {
		for i := 0; ; i++ {
			_, err := store.UpdateLink(writerCtx, graphstore.LinkUpdateRequest{Path: "links/context", Unconditional: true, UnconditionalSource: true, Properties: map[string]any{"note": strconv.Itoa(i)}})
			if err != nil {
				if writerCtx.Err() != nil {
					err = nil
				}
				writerDone <- err
				return
			}
		}
	}()
	stopAndWait := sync.OnceFunc(func() {
		stopWriter()
		if err := <-writerDone; err != nil {
			t.Errorf("concurrent writer: %v", err)
		}
	})
	defer stopAndWait()
	for range 12 {
		response, body = request("GET", "/read/beads/plan?include=links", "second-token-longer", "", nil)
		var aggregate bdpwire.BeadRecord
		if response.StatusCode != 200 || bdpwire.Unmarshal(body, &aggregate) != nil || aggregate.Links == nil || len(aggregate.Links.Items) != 1 {
			t.Fatalf("aggregate: %d %s", response.StatusCode, body)
		}
		owned := aggregate.OwnedLinks[graphstore.RelatedTypeURL(scope)]
		if len(owned) != 1 || owned[0].Revision != aggregate.Links.Items[0].Revision || string(owned[0].Properties["note"]) != string(aggregate.Links.Items[0].Properties["note"]) {
			t.Fatal("aggregate mixed two storage snapshots")
		}
		if string(owned[0].Metadata["origin"]) != `"manual"` || string(aggregate.Links.Items[0].Metadata["origin"]) != `"manual"` {
			t.Fatalf("HTTP aggregate omitted Link metadata: %s", body)
		}
	}
	stopAndWait()
	// The same canonical HTTP reader exposes native Issue edits. This exercises
	// real storage and listener calls, without inventing an HTTP Write endpoint.
	t.Run("issue-priority-assignment", func(t *testing.T) {
		issue, err := store.CreateIssue(ctx, "beads/work", issueops.CreateRequest{Issue: &issueops.Issue{Title: "HTTP work", Status: "open", IssueType: "task", Priority: 2}, Actor: "creator"})
		if err != nil {
			t.Fatal(err)
		}
		priority, assignee := 0, "agent.雪"
		edited, err := store.UpdateIssue(ctx, graphstore.UpdateIssueRequest{Path: "beads/work", Actor: "editor", ExpectedRevision: issue.Revision, Priority: &priority, Assignee: &assignee})
		if err != nil || !edited.Changed {
			t.Fatalf("Issue edit: %+v %v", edited, err)
		}
		before, err := store.CurrentSnapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		resp, raw := request("GET", "/read/beads/work", "second-token-longer", "", nil)
		var record bdpwire.BeadRecord
		if resp.StatusCode != 200 || bdpwire.Unmarshal(raw, &record) != nil || record.ID != edited.Issue.ID || record.Revision != edited.Issue.Revision || string(record.Properties["priority"]) != "0" || string(record.Properties["assignee"]) != `"agent.雪"` || record.Attribution == nil || record.Attribution.Principal != "editor" || record.Metadata == nil || len(record.Metadata) != 0 {
			t.Fatalf("canonical Issue edit not exposed: %d %s", resp.StatusCode, raw)
		}
		resp, raw = request("GET", "/read/beads/?limit=100", "second-token-longer", "", nil)
		var collection bdpwire.BeadCollection
		if resp.StatusCode != 200 || bdpwire.Unmarshal(raw, &collection) != nil || collection.Next != nil {
			t.Fatalf("current collection: %d %s", resp.StatusCode, raw)
		}
		found := 0
		for _, item := range collection.Items {
			if item.ID == record.ID {
				found++
				if !reflect.DeepEqual(item, record) {
					t.Fatal("collection and canonical Issue records differ")
				}
			}
		}
		if found != 1 {
			t.Fatalf("current Issue occurrences=%d", found)
		}
		if after, err := store.CurrentSnapshot(ctx); err != nil || !reflect.DeepEqual(after, before) {
			t.Fatalf("HTTP read changed graph state: %v", err)
		}
		assignee = ""
		cleared, err := store.UpdateIssue(ctx, graphstore.UpdateIssueRequest{Path: "beads/work", Actor: "editor", ExpectedRevision: edited.Issue.Revision, Assignee: &assignee})
		if err != nil || !cleared.Changed {
			t.Fatalf("clear: %+v %v", cleared, err)
		}
		resp, raw = request("GET", "/read/beads/work", "second-token-longer", "", nil)
		record = bdpwire.BeadRecord{}
		if resp.StatusCode != 200 || bdpwire.Unmarshal(raw, &record) != nil || record.Revision != cleared.Issue.Revision || string(record.Properties["priority"]) != "0" {
			t.Fatalf("cleared canonical Issue: %d %s", resp.StatusCode, raw)
		}
		if assignee, exists := record.Properties["assignee"]; exists && string(assignee) != `""` {
			t.Fatalf("cleared assignee still exposed: %s", assignee)
		}
	})
	t.Run("issue-append-notes", func(t *testing.T) {
		claimed, err := store.ClaimIssue(ctx, "beads/work", "notes-holder")
		if err != nil || !claimed.Changed {
			t.Fatalf("claim for notes: %+v %v", claimed, err)
		}
		read := func(headers http.Header) (*http.Response, bdpwire.BeadRecord) {
			t.Helper()
			resp, raw := request("GET", "/read/beads/work", "second-token-longer", "", headers)
			var value bdpwire.BeadRecord
			if resp.StatusCode != 200 || bdpwire.Unmarshal(raw, &value) != nil {
				t.Fatalf("notes canonical read: %d %s", resp.StatusCode, raw)
			}
			return resp, value
		}
		beforeHTTP, before := read(nil)
		etag := beforeHTTP.Header.Get("ETag")
		if etag == "" || before.Revision != claimed.Issue.Revision {
			t.Fatal("missing claim revision/ETag")
		}
		state, err := store.CurrentSnapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		empty := ""
		noop, err := store.UpdateIssue(ctx, graphstore.UpdateIssueRequest{Path: "beads/work", Actor: "notes-holder", ExpectedRevision: claimed.Issue.Revision, AppendNotes: &empty})
		if err != nil || noop.Changed || !reflect.DeepEqual(noop.Issue, claimed.Issue) {
			t.Fatalf("empty append noop: %+v %v", noop, err)
		}
		resp, raw := request("GET", "/read/beads/work", "second-token-longer", "", http.Header{"If-None-Match": {etag}})
		if resp.StatusCode != 304 || len(raw) != 0 || resp.Header.Get("ETag") != etag {
			t.Fatalf("noop changed ETag: %d %s", resp.StatusCode, raw)
		}
		if after, err := store.CurrentSnapshot(ctx); err != nil || !reflect.DeepEqual(after, state) {
			t.Fatalf("noop/HTTP read changed state: %v", err)
		}
		text := "Progress — 雪\r\nsecond line\t"
		appended, err := store.UpdateIssue(ctx, graphstore.UpdateIssueRequest{Path: "beads/work", Actor: "notes-holder", ExpectedRevision: noop.Issue.Revision, AppendNotes: &text})
		if err != nil || !appended.Changed {
			t.Fatalf("append: %+v %v", appended, err)
		}
		state, err = store.CurrentSnapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		afterHTTP, after := read(http.Header{"If-None-Match": {etag}})
		if afterHTTP.Header.Get("ETag") == "" || afterHTTP.Header.Get("ETag") == etag || after.Revision != appended.Issue.Revision || after.Revision == before.Revision {
			t.Fatal("append did not expose fresh revision/ETag")
		}
		want := before
		want.Revision, want.Attribution = after.Revision, after.Attribution
		want.Properties = bdpwire.Properties{}
		for key, value := range before.Properties {
			want.Properties[key] = value
		}
		want.Properties["notes"], _ = json.Marshal(text)
		want.Properties["updated_at"] = after.Properties["updated_at"]
		if after.Attribution == nil || after.Attribution.Principal != "notes-holder" || !reflect.DeepEqual(after, want) {
			t.Fatal("append changed other HTTP properties, lease or ownership")
		}
		resp, raw = request("GET", "/read/beads/work?view=properties", "second-token-longer", "", nil)
		var properties bdpwire.Properties
		if resp.StatusCode != 200 || bdpwire.Unmarshal(raw, &properties) != nil || !reflect.DeepEqual(properties, after.Properties) {
			t.Fatalf("appended properties: %d %s", resp.StatusCode, raw)
		}
		resp, raw = request("GET", "/read/beads/?limit=100", "second-token-longer", "", nil)
		var page bdpwire.BeadCollection
		if resp.StatusCode != 200 || bdpwire.Unmarshal(raw, &page) != nil || page.Next != nil {
			t.Fatalf("appended inventory: %d %s", resp.StatusCode, raw)
		}
		found := 0
		for _, item := range page.Items {
			if item.ID == after.ID {
				found++
				if !reflect.DeepEqual(item, after) {
					t.Fatal("appended inventory/current differ")
				}
			}
		}
		if found != 1 {
			t.Fatalf("appended Issue occurrences=%d", found)
		}
		if current, err := store.CurrentSnapshot(ctx); err != nil || !reflect.DeepEqual(current, state) {
			t.Fatalf("HTTP reads changed appended state: %v", err)
		}
	})
	// Retained bytes never substitute for a currently usable authority. Even a
	// condition that would otherwise return 304 must fail after storage closes.
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	response, body = request("GET", next.RequestURI(), "second-token-longer", "", http.Header{"If-None-Match": {"*"}})
	if response.StatusCode != http.StatusInternalServerError || len(body) != 0 {
		t.Fatalf("closed authority served retained page: %d %s", response.StatusCode, body)
	}
	if strings.Contains(srv.stderr.String(), "first-token") || strings.Contains(srv.stderr.String(), "second-token-longer") {
		t.Fatal("credential leaked to request logs")
	}
	if !strings.Contains(srv.stderr.String(), "db=graph-read") || strings.Contains(srv.stderr.String(), "capabilities=issue") {
		t.Fatal("startup reported a legacy source")
	}
	encoded, _ := json.Marshal(map[string]any{"checks": "real authority, Host, auth rotation, query/condition precedence, Unicode HEAD, aggregate concurrent snapshot", "passed": true})
	t.Logf("HTTP_RECEIPT %s", encoded)
}

func TestGraphReadRejectsMixedSources(t *testing.T) {
	for _, cfg := range []Config{
		{GraphRead: &GraphRead{}},
		{GraphRead: &GraphRead{}, Provider: &fakeProvider{}},
		{GraphRead: &GraphRead{}, EventsJournalEnabled: true},
	} {
		if _, err := Listen(cfg); err == nil {
			t.Fatal("invalid graph source bound a listener")
		}
	}
}
