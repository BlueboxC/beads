package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
)

func TestEventsWatchRevokesExistingStream(t *testing.T) {
	auth, path, clock := newAuthForTest(t, "old-token\n")
	journal := &liveEventsJournal{}
	ts := watchServer(t, journal, func(s *Server) { s.auth = auth })
	ws := openWatch(t, ts, "/v0/beads/events:watch?since=0", http.Header{"Authorization": {"Bearer old-token"}})
	ws.next(t)
	writeTokenFile(t, path, "old-token\nnew-token-value\n")
	clock.advance(2 * time.Second)
	journal.commit(storage.EventsJournalRow{IssueID: "bd-rotation"})
	if got := ws.next(t); !strings.Contains(got, "bd-rotation") {
		t.Fatalf("rotation lost valid stream: %q", got)
	}
	writeTokenFile(t, path, "new-token-value\n")
	clock.advance(2 * time.Second)
	if mustVerify(t, auth, "old-token") {
		t.Fatal("revocation did not replace token set")
	}
	journal.commit(storage.EventsJournalRow{IssueID: "bd-after-revocation"})
	data, err := io.ReadAll(ws.frames)
	if err != nil {
		t.Fatalf("revoked stream did not close: %v", err)
	}
	if strings.Contains(string(data), "bd-after-revocation") {
		t.Errorf("revoked stream disclosed future event: %s", data)
	}
	ws2 := openWatch(t, ts, "/v0/beads/events:watch?since=1", http.Header{"Authorization": {"Bearer new-token-value"}})
	ws2.next(t)
	if got := ws2.next(t); !strings.Contains(got, "bd-after-revocation") {
		t.Errorf("retained token cannot read event: %q", got)
	}
}

func TestEventsWatchRevokesIdleStream(t *testing.T) {
	auth, path, clock := newAuthForTest(t, "old-token\n")
	journal := &liveEventsJournal{}
	ts := watchServer(t, journal, func(s *Server) { s.auth = auth })
	ws := openWatch(t, ts, "/v0/beads/events:watch?since=0", http.Header{"Authorization": {"Bearer old-token"}})
	ws.next(t)
	writeTokenFile(t, path, "new-token-value\n")
	clock.advance(2 * time.Second)
	if data, err := io.ReadAll(ws.frames); err != nil || len(data) != 0 {
		t.Fatalf("idle revoked stream remained open or sent data: %q, %v", data, err)
	}
}

type revokeOnRecordWriter struct {
	*httptest.ResponseRecorder
	revoke  func()
	records int
}

func (w *revokeOnRecordWriter) Write(p []byte) (int, error) {
	if strings.Contains(string(p), "data:") {
		w.records++
		if w.records == 1 {
			w.revoke()
		}
	}
	return w.ResponseRecorder.Write(p)
}

func TestEventsWatchRevokesWithinBacklogBatch(t *testing.T) {
	auth, path, clock := newAuthForTest(t, "old-token\n")
	s := &Server{auth: auth, log: newTestLogger(&lockedBuffer{})}
	req := httptest.NewRequest(http.MethodGet, "/v0/beads/events:watch?since=0", nil)
	req.Header.Set("Authorization", "Bearer old-token")
	page := storage.EventsJournalPage{Rows: []storage.EventsJournalRow{
		{Seq: 1, IssueID: "bd-first", IssueJSON: `{"id":"bd-first"}`},
		{Seq: 2, IssueID: "bd-revoked", IssueJSON: `{"id":"bd-revoked"}`},
	}}
	w := &revokeOnRecordWriter{ResponseRecorder: httptest.NewRecorder(), revoke: func() {
		writeTokenFile(t, path, "new-token-value\n")
		clock.advance(2 * time.Second)
	}}
	s.streamEvents(w, req, &liveEventsJournal{}, 0, page)
	if w.records != 1 || strings.Contains(w.Body.String(), "bd-revoked") {
		t.Fatalf("revoked stream kept draining current batch: %s", w.Body)
	}
}

func TestEventsWatchInvalidReloadKeepsLastGoodStream(t *testing.T) {
	auth, path, clock := newAuthForTest(t, "live-token\n")
	journal := &liveEventsJournal{}
	ts := watchServer(t, journal, func(s *Server) { s.auth = auth })
	ws := openWatch(t, ts, "/v0/beads/events:watch?since=0", http.Header{"Authorization": {"Bearer live-token"}})
	ws.next(t)
	writeTokenFile(t, path, "")
	clock.advance(2 * time.Second)
	journal.commit(storage.EventsJournalRow{IssueID: "bd-last-good"})
	if got := ws.next(t); !strings.Contains(got, "bd-last-good") {
		t.Errorf("last-good stream refused: %q", got)
	}
}
