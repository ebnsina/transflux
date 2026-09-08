package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/ebnsina/transflux/internal/auth"
	"github.com/ebnsina/transflux/internal/job"
	"github.com/ebnsina/transflux/internal/validate"
	"github.com/ebnsina/transflux/internal/worker"
	"github.com/google/uuid"
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

// ── task lifecycle ────────────────────────────────────────────────────────

// handleWorkerLease is a poll for work. An empty response is the normal answer
// for an idle fleet, so it is 204 rather than an error.
func (s *Server) handleWorkerLease(w http.ResponseWriter, r *http.Request) {
	wk, ok := s.authenticateWorker(w, r)
	if !ok {
		return
	}
	// A draining, unhealthy or offline worker takes no new work. Draining is
	// how a machine is emptied before maintenance without killing what it holds.
	if wk.State != worker.StateOnline {
		w.Header().Set("X-Worker-State", wk.State)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Capabilities come from the registry, not from the request: a worker
	// cannot widen what it is allowed to run by asking for more.
	full, diskFree, err := s.d.Workers.Facts(r.Context(), wk.ID)
	if err != nil {
		internalError(w, r, "worker facts", err)
		return
	}

	assignment, err := s.d.Jobs.Lease(r.Context(), job.WorkerFacts{
		ID:           full.ID,
		Name:         full.Name,
		Arch:         full.Arch,
		Operations:   full.Capabilities.Operations,
		Encoders:     full.Capabilities.Encoders,
		HasGPU:       full.GPUModel != nil,
		MemoryBytes:  full.MemoryBytes,
		DiskFree:     diskFree,
		SlotCapacity: full.SlotCapacity,
	}, s.d.LeaseTTL)
	switch {
	case errors.Is(err, job.ErrNoWork):
		w.WriteHeader(http.StatusNoContent)
	case err != nil:
		internalError(w, r, "lease task", err)
	default:
		// Signed now rather than when the job was created: a task can sit
		// queued for hours, and a URL minted then would already have expired.
		resolved, err := s.resolveSpec(r, assignment)
		if err != nil {
			internalError(w, r, "resolve task spec", err)
			return
		}
		assignment.Spec = resolved
		writeJSON(w, http.StatusOK, assignment)
	}
}

func (s *Server) handleWorkerStarted(w http.ResponseWriter, r *http.Request) {
	wk, attemptID, ok := s.workerAttempt(w, r)
	if !ok {
		return
	}
	switch err := s.d.Jobs.Start(r.Context(), attemptID, wk.ID, s.d.LeaseTTL); {
	case errors.Is(err, job.ErrStaleAttempt):
		staleAttempt(w)
	case err != nil:
		internalError(w, r, "start attempt", err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleWorkerProgress renews the lease and answers whether the task has been
// cancelled. Cancellation rides the heartbeat response because the control
// plane has no route to a worker (ADR-006).
func (s *Server) handleWorkerProgress(w http.ResponseWriter, r *http.Request) {
	wk, attemptID, ok := s.workerAttempt(w, r)
	if !ok {
		return
	}
	var req struct {
		ProgressPct float32 `json:"progress_pct"`
	}
	if !decode(w, r, &req) {
		return
	}

	cancelled, err := s.d.Jobs.Progress(r.Context(), attemptID, wk.ID, req.ProgressPct, s.d.LeaseTTL)
	switch {
	case errors.Is(err, job.ErrStaleAttempt):
		staleAttempt(w)
	case err != nil:
		internalError(w, r, "attempt progress", err)
	default:
		writeJSON(w, http.StatusOK, map[string]any{
			"cancel":            cancelled,
			"lease_ttl_seconds": int(s.d.LeaseTTL.Seconds()),
		})
	}
}

func (s *Server) handleWorkerComplete(w http.ResponseWriter, r *http.Request) {
	wk, attemptID, ok := s.workerAttempt(w, r)
	if !ok {
		return
	}
	var res job.Result
	if !decode(w, r, &res) {
		return
	}

	completion, err := s.d.Jobs.Complete(r.Context(), attemptID, wk.ID, res)
	switch {
	case errors.Is(err, job.ErrStaleAttempt):
		staleAttempt(w)
		return
	case err != nil:
		internalError(w, r, "complete attempt", err)
		return
	}

	// Operation-specific handling lives here rather than in the job package,
	// which knows nothing about media.
	// A validation report is recorded whether it passed or failed: a failed job
	// should say which check failed rather than only that something did.
	if completion.Operation == "validate" && len(res.Output) > 0 {
		var report validate.Report
		if err := json.Unmarshal(res.Output, &report); err != nil {
			slog.ErrorContext(r.Context(), "validation report is not readable", "err", err)
		} else if err := s.d.Validations.Record(r.Context(), completion.TenantID,
			completion.JobID, report); err != nil {
			slog.ErrorContext(r.Context(), "could not record validation", "err", err)
		}
	}

	if completion.Succeeded && completion.Operation == "probe" && len(res.Output) > 0 {
		if _, err := s.d.Probes.Record(r.Context(), completion.TenantID,
			completion.AssetVersionID, res.Output); err != nil {
			// The work itself succeeded; failing the worker's call would make
			// it redo an encode because we could not parse a probe.
			slog.ErrorContext(r.Context(), "could not record probe results",
				"asset_version_id", completion.AssetVersionID, "err", err)
		}
	}

	// An artifact set is incomplete by definition until its job is done, so
	// nothing should be delivered from it before this.
	switch completion.JobState {
	case job.JobSucceeded:
		if err := s.d.Artifacts.MarkComplete(r.Context(), completion.TenantID, completion.JobID); err != nil {
			slog.ErrorContext(r.Context(), "could not complete artifact set", "err", err)
		}
	case job.JobFailed, job.JobCancelled:
		if err := s.d.Artifacts.MarkFailed(r.Context(), completion.TenantID, completion.JobID); err != nil {
			slog.ErrorContext(r.Context(), "could not fail artifact set", "err", err)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"retrying": completion.Retrying})
}

func (s *Server) workerAttempt(w http.ResponseWriter, r *http.Request) (worker.Worker, uuid.UUID, bool) {
	wk, ok := s.authenticateWorker(w, r)
	if !ok {
		return worker.Worker{}, uuid.Nil, false
	}
	attemptID, ok := pathUUID(r, "attempt")
	if !ok {
		notFound(w)
		return worker.Worker{}, uuid.Nil, false
	}
	return wk, attemptID, true
}

// staleAttempt tells a worker its lease is gone so it stops work immediately,
// rather than finishing an encode nobody will accept.
func staleAttempt(w http.ResponseWriter) {
	writeError(w, http.StatusConflict, "stale_attempt",
		"This attempt no longer holds the lease. Stop work and discard any output.")
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
