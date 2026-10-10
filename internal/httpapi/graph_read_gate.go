package httpapi

import (
	"context"
	"net/http"

	"github.com/steveyegge/beads/internal/httpapi/bdpwire"
)

// graphReadRoute reuses the listener's deadline, credential verifier, request
// semaphore and logging without publishing a legacy Issue mutation route or
// passing graph requests through ServeMux's path normalization redirects.
func (s *Server) graphReadRoute() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := requestInfo(r.Context())
		rec.op = "bdp-read"
		ctx, cancel := context.WithTimeout(r.Context(), requestDeadline)
		defer cancel()
		r = r.WithContext(ctx)
		if s.auth != nil && !s.authorize(w, r, rec) {
			return
		}
		release, err := s.acquire(ctx, rec)
		if err != nil {
			w.Header().Set("Retry-After", "1")
			graphReadProblem(w, r, bdpwire.CodeTemporarilyUnavailable)
			return
		}
		defer release()
		s.cfg.GraphRead.ServeHTTP(w, r)
	})
}
