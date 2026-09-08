// Package api is the HTTP surface. It owns routing, the error envelope and
// nothing else: business logic lives in the domain packages.
package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/ebnsina/transflux/internal/auth"
)

type Server struct {
	auth auth.Authenticator
	ping func(context.Context) error
}

func New(a auth.Authenticator, ping func(context.Context) error) *Server {
	return &Server{auth: a, ping: ping}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Liveness: the process is up. Never touches the database, so a database
	// blip does not get the container restarted into the same blip.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// Readiness: dependencies are usable, so take me out of rotation if not.
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := s.ping(r.Context()); err != nil {
			slog.WarnContext(r.Context(), "readiness check failed", "err", err)
			writeError(w, http.StatusServiceUnavailable, "database_unavailable",
				"Database is not reachable.")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// Everything under /v1 is authenticated. Mounting the middleware on the
	// prefix rather than per route means a new endpoint cannot be added
	// unauthenticated by forgetting a wrapper.
	v1 := http.NewServeMux()
	v1.HandleFunc("GET /v1/me", s.handleMe)

	mux.Handle("/v1/", auth.Middleware(s.auth, unauthorized, serverError)(v1))
	return mux
}

// handleMe echoes the authenticated identity. It exists so a caller can verify
// a key and see the tenant it resolves to without mutating anything.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	p, ok := auth.FromContext(r.Context())
	if !ok {
		serverError(w, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"tenant_id":  p.TenantID,
		"api_key_id": p.KeyID,
		"scopes":     p.Scopes,
	})
}

// ── error envelope ────────────────────────────────────────────────────────

type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("write response", "err", err)
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	var b errorBody
	b.Error.Code = code
	b.Error.Message = message
	writeJSON(w, status, b)
}

// Authentication failures are deliberately indistinguishable: unknown,
// malformed, revoked and expired keys all produce this exact response.
func unauthorized(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusUnauthorized, "unauthorized",
		"A valid API key is required.")
}

func forbidden(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusForbidden, "forbidden",
		"This API key lacks the required scope.")
}

func serverError(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusInternalServerError, "internal_error",
		"Something went wrong on our side.")
}
