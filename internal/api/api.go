// Package api is the HTTP surface. It owns routing, request decoding and the
// error envelope, and nothing else: business logic lives in the domain
// packages.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/ebnsina/transflux/internal/asset"
	"github.com/ebnsina/transflux/internal/auth"
	"github.com/ebnsina/transflux/internal/upload"
	"github.com/ebnsina/transflux/internal/worker"
	"github.com/google/uuid"
)

type Deps struct {
	Auth              auth.Authenticator
	Ping              func(context.Context) error
	Assets            *asset.Store
	Uploads           *upload.Service
	Workers           *worker.Store
	HeartbeatInterval time.Duration
}

type Server struct{ d Deps }

func New(d Deps) *Server { return &Server{d: d} }

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Liveness: the process is up. Never touches the database, so a database
	// blip does not get the container restarted into the same blip.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// Readiness: dependencies are usable, so take me out of rotation if not.
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := s.d.Ping(r.Context()); err != nil {
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

	v1.Handle("POST /v1/assets", scoped(auth.ScopeAssetsWrite, s.handleCreateAsset))
	v1.Handle("GET /v1/assets", scoped(auth.ScopeAssetsRead, s.handleListAssets))
	v1.Handle("GET /v1/assets/{id}", scoped(auth.ScopeAssetsRead, s.handleGetAsset))
	v1.Handle("POST /v1/assets/{id}/uploads", scoped(auth.ScopeAssetsWrite, s.handleCreateUpload))

	v1.Handle("GET /v1/uploads/{id}", scoped(auth.ScopeAssetsRead, s.handleUploadStatus))
	v1.Handle("POST /v1/uploads/{id}/complete", scoped(auth.ScopeAssetsWrite, s.handleCompleteUpload))
	v1.Handle("DELETE /v1/uploads/{id}", scoped(auth.ScopeAssetsWrite, s.handleAbortUpload))

	// The fleet is shared infrastructure, so its endpoints are an operator
	// concern rather than a tenant one.
	v1.Handle("GET /v1/workers", adminScoped(s.handleListWorkers))
	v1.Handle("GET /v1/workers/{id}", adminScoped(s.handleGetWorker))
	v1.Handle("POST /v1/workers/{id}/state", adminScoped(s.handleSetWorkerState))

	mux.Handle("/v1/", auth.Middleware(s.d.Auth, unauthorized, serverError)(v1))

	// The worker protocol authenticates with worker credentials, not API keys,
	// so it is mounted outside the /v1 tenant surface entirely.
	mux.HandleFunc("POST /worker/v1/register", s.handleWorkerRegister)
	mux.HandleFunc("POST /worker/v1/heartbeat", s.handleWorkerHeartbeat)

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

func scoped(scope string, h http.HandlerFunc) http.Handler {
	return auth.RequireScope(scope, forbidden)(h)
}

// ── request helpers ───────────────────────────────────────────────────────

// pathUUID parses an id from the path. A malformed id is a 404 rather than a
// 400: to the caller it is indistinguishable from an id that does not exist,
// which is also what a valid id belonging to another tenant returns.
func pathUUID(r *http.Request, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue(name))
	return id, err == nil
}

// decode reads a JSON body with a size cap, so a huge or malformed body cannot
// consume memory before validation runs.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request",
			"Request body is not valid JSON for this endpoint.")
		return false
	}
	return true
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

func notFound(w http.ResponseWriter) {
	writeError(w, http.StatusNotFound, "not_found", "No such resource.")
}

// internalError logs the cause and tells the caller nothing about it: error
// text can carry storage keys, SQL and other internals.
func internalError(w http.ResponseWriter, r *http.Request, msg string, err error) {
	slog.ErrorContext(r.Context(), msg, "err", err)
	serverError(w, r)
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

var errNoPrincipal = errors.New("no principal on an authenticated route")
