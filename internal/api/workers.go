package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/ebnsina/transflux/internal/auth"
	"github.com/ebnsina/transflux/internal/worker"
)

// ── worker protocol v1 ────────────────────────────────────────────────────
//
// Worker-initiated over HTTPS (ADR-006): the control plane never needs a route
// to a worker, so a GPU box behind NAT in another cloud joins by setting two
// environment variables.

type registerResponse struct {
	Worker            worker.Worker `json:"worker"`
	Credential        string        `json:"credential"`
	ProtocolVersion   int           `json:"protocol_version"`
	HeartbeatInterval int           `json:"heartbeat_interval_seconds"`
}

func (s *Server) handleWorkerRegister(w http.ResponseWriter, r *http.Request) {
	token, ok := bearer(r)
	if !ok {
		unauthorized(w, r)
		return
	}
	var reg worker.Registration
	if !decode(w, r, &reg) {
		return
	}

	wk, credential, err := s.d.Workers.Register(r.Context(), token, reg)
	switch {
	case errors.Is(err, worker.ErrUnauthorized):
		unauthorized(w, r)
	case errors.Is(err, worker.ErrBadProtocol):
		// Tell the worker plainly rather than failing it mid-task later.
		writeError(w, http.StatusBadRequest, "unsupported_protocol", err.Error())
	case err != nil:
		internalError(w, r, "register worker", err)
	default:
		// The credential is returned exactly once, and re-registering rotates
		// it, so a leaked one stops working when that worker restarts.
		writeJSON(w, http.StatusCreated, registerResponse{
			Worker:            wk,
			Credential:        credential,
			ProtocolVersion:   worker.ProtocolVersion,
			HeartbeatInterval: int(s.d.HeartbeatInterval.Seconds()),
		})
	}
}

func (s *Server) handleWorkerHeartbeat(w http.ResponseWriter, r *http.Request) {
	wk, ok := s.authenticateWorker(w, r)
	if !ok {
		return
	}
	var rep worker.Report
	if !decode(w, r, &rep) {
		return
	}

	state, err := s.d.Workers.Heartbeat(r.Context(), wk.ID, rep)
	if err != nil {
		internalError(w, r, "worker heartbeat", err)
		return
	}
	// The response carries the worker's state, which is how a drain reaches a
	// worker we cannot dial.
	writeJSON(w, http.StatusOK, map[string]any{
		"state":                      state,
		"heartbeat_interval_seconds": int(s.d.HeartbeatInterval.Seconds()),
	})
}

func (s *Server) authenticateWorker(w http.ResponseWriter, r *http.Request) (worker.Worker, bool) {
	secret, ok := bearer(r)
	if !ok {
		unauthorized(w, r)
		return worker.Worker{}, false
	}
	wk, err := s.d.Workers.Authenticate(r.Context(), secret)
	if errors.Is(err, worker.ErrUnauthorized) {
		unauthorized(w, r)
		return worker.Worker{}, false
	}
	if err != nil {
		internalError(w, r, "authenticate worker", err)
		return worker.Worker{}, false
	}
	return wk, true
}

func bearer(r *http.Request) (string, bool) {
	scheme, token, found := strings.Cut(r.Header.Get("Authorization"), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	return token, token != ""
}

// ── administration ────────────────────────────────────────────────────────

func (s *Server) handleListWorkers(w http.ResponseWriter, r *http.Request) {
	workers, err := s.d.Workers.List(r.Context())
	if err != nil {
		internalError(w, r, "list workers", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workers": workers})
}

func (s *Server) handleGetWorker(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(r, "id")
	if !ok {
		notFound(w)
		return
	}
	wk, err := s.d.Workers.Get(r.Context(), id)
	if errors.Is(err, worker.ErrNotFound) {
		notFound(w)
		return
	}
	if err != nil {
		internalError(w, r, "get worker", err)
		return
	}
	writeJSON(w, http.StatusOK, wk)
}

// handleSetWorkerState drains a worker before maintenance, or returns a drained
// one to service. Draining lets it finish what it holds rather than killing it.
func (s *Server) handleSetWorkerState(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(r, "id")
	if !ok {
		notFound(w)
		return
	}
	var req struct {
		State string `json:"state"`
	}
	if !decode(w, r, &req) {
		return
	}

	switch err := s.d.Workers.SetState(r.Context(), id, req.State); {
	case errors.Is(err, worker.ErrNotFound):
		notFound(w)
	case err != nil:
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// workerScopes is the admin gate for fleet endpoints. Workers are shared
// infrastructure, so fleet visibility is an operator concern rather than a
// tenant one.
func adminScoped(h http.HandlerFunc) http.Handler {
	return auth.RequireScope(auth.ScopeAdmin, forbidden)(h)
}
