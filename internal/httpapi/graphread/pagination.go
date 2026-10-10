package graphread

// Snapshot/cursor semantics follow gastownhall/bdp packages/server/src/read-pagination.ts
// at 5b4eb46c7f21f2272f4da5a230f042ce753cb304. This internal implementation
// stores owned JSON bytes rather than JavaScript objects. It does not advertise
// a BDP profile or supply the authority's epoch or authorization policy.

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"sync"
	"time"

	graph "github.com/steveyegge/beads/graphops"
)

// Page is one immutable-in-authority snapshot page. Returned bytes are caller-owned.
type Page struct {
	Items []json.RawMessage `json:"items"`
	Next  *string           `json:"next"`
}

// PaginationContextLimit is a provisional 32 KiB bound for the encoded
// continuation URL and each authority fence, including a Selector-bearing
// projection. It is an internal operational bound, not a protocol limit.
const PaginationContextLimit = 32 << 10

// PaginationOptions bounds retained payload and metadata independently. Expiry
// uses lazy cleanup; idle retained state stays bounded and Close releases it.
type PaginationOptions struct {
	Scope                                                           string
	DefaultPageItems, MaxPageItems                                  int
	MaxSnapshots, MaxCursorPositions, MaxCursorPositionsPerSnapshot int
	MaxSnapshotItems, MaxSnapshotBytes, MaxRetainedBytes            int
	CursorTTL                                                       time.Duration
}

// DefaultPaginationOptions returns provisional operational bounds, not wire contracts.
func DefaultPaginationOptions(scope string) PaginationOptions {
	return PaginationOptions{Scope: scope, DefaultPageItems: 100, MaxPageItems: 1000,
		MaxSnapshots: 32, MaxCursorPositions: 4096, MaxCursorPositionsPerSnapshot: 999,
		MaxSnapshotItems: 1000, MaxSnapshotBytes: 8 << 20, MaxRetainedBytes: 32 << 20, CursorTTL: 5 * time.Minute}
}

// FirstPageInput must contain the complete, authorized, filtered, deterministically
// ordered set. Epoch and view must come from the authority, never client input.
// A writer token is not a Scope epoch. Limit zero selects the configured default.
type FirstPageInput struct {
	Items                                                      []json.RawMessage
	Limit                                                      int
	AuthorizationView, ScopeEpoch, Projection, ContinuationURL string
}

// ContinuationInput supplies the request's current authority-derived fences.
type ContinuationInput struct {
	Token, AuthorizationView, ScopeEpoch, Projection string
}

// PaginationError is a local outcome; the HTTP layer owns Problem mapping.
type PaginationError struct{ Code, Reason string }

func (e *PaginationError) Error() string  { return "pagination: " + e.Code + ": " + e.Reason }
func pageError(code, reason string) error { return &PaginationError{Code: code, Reason: reason} }

type pageSnapshot struct {
	items                   []json.RawMessage
	limit, bytes            int
	view, epoch, projection string
	continuation            url.URL
	expires                 time.Time
	tokens                  []string
}
type pageCursor struct {
	snapshot *pageSnapshot
	offset   int
}

// Pagination is one process-local cursor authority. Restart loses all cursors;
// unknown cursors return cursor-expired, never a silently refreshed snapshot.
// Capacity pressure never evicts an unexpired snapshot, even after its final page.
// Callers must serialize epoch transitions with their authority restore boundary.
type Pagination struct {
	mu            sync.Mutex
	options       PaginationOptions
	scope         *url.URL
	snapshots     map[*pageSnapshot]struct{}
	cursors       map[string]pageCursor
	retainedBytes int
	epoch         string
	closed        bool
	now           func() time.Time
	token         func() (string, error)
	lastTime      time.Time
}

// NewPagination creates an isolated cursor authority with explicit resource bounds.
func NewPagination(options PaginationOptions) (*Pagination, error) {
	if len(options.Scope) > PaginationContextLimit || graph.ValidateScopeURL(options.Scope) != nil {
		return nil, pageError("configuration-error", "Scope must be a canonical HTTP URL ending in slash")
	}
	options.Scope = strings.Clone(options.Scope)
	scope, err := url.Parse(options.Scope)
	if err != nil {
		return nil, pageError("configuration-error", "Scope URL cannot be parsed")
	}
	for _, n := range []int{options.DefaultPageItems, options.MaxPageItems, options.MaxSnapshots, options.MaxCursorPositions, options.MaxCursorPositionsPerSnapshot, options.MaxSnapshotItems, options.MaxSnapshotBytes, options.MaxRetainedBytes} {
		if n <= 0 {
			return nil, pageError("configuration-error", "all capacity bounds must be positive")
		}
	}
	if options.DefaultPageItems > options.MaxPageItems || options.MaxCursorPositionsPerSnapshot > options.MaxCursorPositions || options.MaxSnapshotBytes > options.MaxRetainedBytes || options.CursorTTL <= 0 {
		return nil, pageError("configuration-error", "inconsistent pagination bounds")
	}
	return &Pagination{options: options, scope: scope, snapshots: make(map[*pageSnapshot]struct{}), cursors: make(map[string]pageCursor), now: time.Now, token: randomPageToken}, nil
}

func randomPageToken() (string, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value[:]), nil
}

func validPageURL(u *url.URL) bool {
	return u != nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.User == nil && u.Fragment == "" && u.Opaque == ""
}

// Validate encoded segments without rewriting their canonical spelling.
func cleanPagePath(u *url.URL) bool {
	path := strings.TrimSuffix(strings.TrimPrefix(u.EscapedPath(), "/"), "/")
	if path == "" {
		return u.EscapedPath() == "/"
	}
	for _, part := range strings.Split(path, "/") {
		if graph.ValidateCanonicalSegment(part) != nil {
			return false
		}
	}
	return true
}

func pageIdentity(s string) bool {
	return len(s) <= PaginationContextLimit && strings.TrimSpace(s) != ""
}

func (p *Pagination) continuation(value string) (*url.URL, error) {
	if len(value) > PaginationContextLimit {
		return nil, pageError("invalid-input", "continuation URL exceeds operational bound")
	}
	u, err := url.Parse(strings.Clone(value))
	base, _, _ := strings.Cut(value, "?")
	if err != nil || !validPageURL(u) || !cleanPagePath(u) || u.Scheme != p.scope.Scheme || u.Host != p.scope.Host || !strings.HasPrefix(value, p.options.Scope) || base != u.Scheme+"://"+u.Host+u.EscapedPath() {
		return nil, pageError("invalid-input", "continuation URL must be canonical and confined to Scope")
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || query.Has("cursor") {
		return nil, pageError("invalid-input", "continuation URL must not contain a cursor")
	}
	// Include canonical query encoding and token overhead in the same target
	// budget before any continuation can be issued.
	next := *u
	query.Set("cursor", strings.Repeat("A", 43))
	next.RawQuery = query.Encode()
	if len(next.String()) > PaginationContextLimit {
		return nil, pageError("invalid-input", "continuation target exceeds operational bound")
	}
	return u, nil
}

// ValidateLimit validates a limit before the caller performs an expensive read.
func (p *Pagination) ValidateLimit(limit int) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return 0, pageError("configuration-error", "pagination is closed")
	}
	return p.limit(limit)
}
func (p *Pagination) limit(limit int) (int, error) {
	if limit == 0 {
		limit = p.options.DefaultPageItems
	}
	if limit < 1 || limit > p.options.MaxPageItems {
		return 0, pageError("invalid-limit", "page size outside configured range")
	}
	return limit, nil
}

func (p *Pagination) clock() time.Time {
	now := p.now()
	if now.Before(p.lastTime) {
		return p.lastTime
	}
	p.lastTime = now
	return now
}

func (p *Pagination) FirstPage(input FirstPageInput) (Page, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return Page{}, pageError("configuration-error", "pagination is closed")
	}
	now := p.clock()
	p.cleanup(now)
	limit, err := p.limit(input.Limit)
	if err != nil {
		return Page{}, err
	}
	if !pageIdentity(input.AuthorizationView) || !pageIdentity(input.ScopeEpoch) || !pageIdentity(input.Projection) {
		return Page{}, pageError("invalid-input", "authority fences must be nonempty bounded identities")
	}
	continuation, err := p.continuation(input.ContinuationURL)
	if err != nil {
		return Page{}, err
	}
	if len(input.Items) > p.options.MaxSnapshotItems {
		return Page{}, pageError("capacity-exceeded", "snapshot item bound")
	}
	bytes := 0
	for _, item := range input.Items {
		if len(item) > p.options.MaxSnapshotBytes-bytes {
			return Page{}, pageError("capacity-exceeded", "snapshot byte bound")
		}
		bytes += len(item)
		if !json.Valid(item) {
			return Page{}, pageError("invalid-snapshot", "items must contain complete JSON values")
		}
	}
	if p.epoch != "" && p.epoch != input.ScopeEpoch {
		p.releaseAll()
	}
	p.epoch = strings.Clone(input.ScopeEpoch)
	positions := 0
	if len(input.Items) > limit {
		positions = (len(input.Items) - 1) / limit
	}
	if positions > 0 && (positions > p.options.MaxCursorPositionsPerSnapshot || len(p.snapshots) >= p.options.MaxSnapshots || positions > p.options.MaxCursorPositions-len(p.cursors) || bytes > p.options.MaxRetainedBytes-p.retainedBytes) {
		return Page{}, pageError("capacity-exceeded", "active snapshot capacity exhausted")
	}
	snapshot := &pageSnapshot{items: clonePageItems(input.Items), limit: limit, bytes: bytes, view: strings.Clone(input.AuthorizationView), epoch: p.epoch, projection: strings.Clone(input.Projection), continuation: *continuation, expires: now.Add(p.options.CursorTTL)}
	if positions == 0 {
		return p.page(snapshot, 0), nil
	}
	// Mint every future position before admission. Entropy/collision failure cannot
	// strand a snapshot after a successful first response.
	tokens := make(map[string]struct{}, positions)
	for range positions {
		token, err := p.uniqueToken(tokens)
		if err != nil {
			return Page{}, err
		}
		tokens[token] = struct{}{}
		snapshot.tokens = append(snapshot.tokens, token)
	}
	p.snapshots[snapshot] = struct{}{}
	p.retainedBytes += bytes
	for i, token := range snapshot.tokens {
		p.cursors[token] = pageCursor{snapshot: snapshot, offset: (i + 1) * limit}
	}
	return p.page(snapshot, 0), nil
}

func (p *Pagination) uniqueToken(pending map[string]struct{}) (string, error) {
	for range 8 {
		token, err := p.token()
		if err != nil {
			return "", pageError("token-generation-failed", "random source failed")
		}
		if len(token) != 43 {
			continue
		}
		if _, err := base64.RawURLEncoding.DecodeString(token); err != nil {
			continue
		}
		if _, exists := pending[token]; exists {
			continue
		}
		if _, exists := p.cursors[token]; !exists {
			return token, nil
		}
	}
	return "", pageError("token-generation-failed", "opaque token collision or invalid entropy")
}

func (p *Pagination) ContinuePage(input ContinuationInput) (Page, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return Page{}, pageError("configuration-error", "pagination is closed")
	}
	now := p.clock()
	cursor, exists := p.cursors[input.Token]
	if exists && !now.Before(cursor.snapshot.expires) {
		p.release(cursor.snapshot)
		p.cleanup(now)
		return Page{}, pageError("cursor-expired", "expired")
	}
	p.cleanup(now)
	if !exists {
		return Page{}, pageError("cursor-expired", "unknown")
	}
	if !pageIdentity(input.ScopeEpoch) || !pageIdentity(input.AuthorizationView) || !pageIdentity(input.Projection) {
		return Page{}, pageError("invalid-input", "authority fences must be nonempty bounded identities")
	}
	s := cursor.snapshot
	if input.ScopeEpoch != s.epoch {
		p.release(s)
		return Page{}, pageError("cursor-expired", "epoch-changed")
	}
	if input.AuthorizationView != s.view {
		return Page{}, pageError("foreign-view", "authorization projection changed")
	}
	if input.Projection != s.projection {
		return Page{}, pageError("foreign-projection", "collection projection changed")
	}
	return p.page(s, cursor.offset), nil
}

func (p *Pagination) page(s *pageSnapshot, offset int) Page {
	end := offset + min(s.limit, len(s.items)-offset)
	result := Page{Items: clonePageItems(s.items[offset:end])}
	if end < len(s.items) {
		u := s.continuation
		query := u.Query()
		query.Set("cursor", s.tokens[end/s.limit-1])
		u.RawQuery = query.Encode()
		next := u.String()
		result.Next = &next
	}
	return result
}
func clonePageItems(items []json.RawMessage) []json.RawMessage {
	result := make([]json.RawMessage, len(items))
	for i, item := range items {
		result[i] = append(json.RawMessage(nil), item...)
	}
	return result
}
func (p *Pagination) release(s *pageSnapshot) {
	if _, exists := p.snapshots[s]; !exists {
		return
	}
	delete(p.snapshots, s)
	p.retainedBytes -= s.bytes
	for _, token := range s.tokens {
		delete(p.cursors, token)
	}
}
func (p *Pagination) releaseAll() {
	for s := range p.snapshots {
		p.release(s)
	}
}
func (p *Pagination) cleanup(now time.Time) int {
	released := 0
	for s := range p.snapshots {
		if !now.Before(s.expires) {
			p.release(s)
			released++
		}
	}
	return released
}

// CleanupExpired returns the number of snapshots released. It is safe after Close.
func (p *Pagination) CleanupExpired() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cleanup(p.clock())
}

// Close releases all retained state and refuses subsequent paging requests.
func (p *Pagination) Close() { p.mu.Lock(); defer p.mu.Unlock(); p.closed = true; p.releaseAll() }
