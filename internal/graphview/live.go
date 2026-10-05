package graphview

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

const livePollInterval = 5 * time.Second
const liveQueryDeadline = 30 * time.Second

// LiveHandler coalesces browser reads into one bounded query per interval. It
// caches only a presentation snapshot in RAM, never a second database/index.
type LiveHandler struct {
	load func(context.Context) (Page, error)
	mu   sync.Mutex
	next time.Time
	body []byte
	etag string
	err  error
}

func NewLiveHandler(load func(context.Context) (Page, error)) *LiveHandler {
	return &LiveHandler{load: load}
}

func (h *LiveHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if r.Method != http.MethodGet || r.URL.RawQuery != "" {
		http.Error(w, "unsupported viewer request", http.StatusBadRequest)
		return
	}
	if r.URL.Path == "/viewer" {
		// Public shell carries no workspace identity, paths, records or credentials.
		var out bytes.Buffer
		err := WriteHTML(&out, Page{Title: "Beads · visor automático", Knowledge: true, Nodes: []Node{}, Links: []Edge{}, Live: &LiveConfig{Endpoint: "/viewer/graph", PollMS: int(livePollInterval / time.Millisecond)}})
		if err != nil {
			http.Error(w, "viewer render failed", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(out.Bytes())
		return
	}
	if r.URL.Path != "/viewer/graph" {
		http.NotFound(w, r)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if r.Context().Err() != nil {
		return
	}
	if time.Now().After(h.next) {
		ctx, cancel := context.WithTimeout(r.Context(), liveQueryDeadline)
		page, err := h.load(ctx)
		cancel()
		if err == nil {
			var body []byte
			body, err = json.Marshal(page)
			if err == nil {
				sum := sha256.Sum256(body)
				h.body = body
				h.etag = `"` + hex.EncodeToString(sum[:]) + `"`
			}
		}
		h.err = err
		h.next = time.Now().Add(livePollInterval)
	}
	if h.err != nil {
		http.Error(w, "graph unavailable; retry shortly", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("ETag", h.etag)
	if r.Header.Get("If-None-Match") == h.etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(h.body)
}
