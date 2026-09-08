package api

import (
	"errors"
	"net/http"

	"github.com/ebnsina/transflux/internal/artifact"
	"github.com/ebnsina/transflux/internal/auth"
	"github.com/ebnsina/transflux/internal/job"
	"github.com/ebnsina/transflux/internal/storage"
)

// handleRegisterArtifact records one output of a task.
//
// Registration is separate from completion because a task can produce several
// outputs and should be able to report each as it finishes, rather than holding
// them all until the end.
func (s *Server) handleRegisterArtifact(w http.ResponseWriter, r *http.Request) {
	wk, attemptID, ok := s.workerAttempt(w, r)
	if !ok {
		return
	}

	// Lease-bound: a worker whose lease was taken away cannot write results for
	// a task that now belongs to someone else.
	ctx, err := s.d.Jobs.AuthorizeAttempt(r.Context(), attemptID, wk.ID)
	if errors.Is(err, job.ErrStaleAttempt) {
		staleAttempt(w)
		return
	}
	if err != nil {
		internalError(w, r, "authorize attempt", err)
		return
	}

	var reg artifact.Registration
	if !decode(w, r, &reg) {
		return
	}

	a, err := s.d.Artifacts.Register(r.Context(), ctx.TenantID, ctx.JobID, attemptID, reg)
	switch {
	case errors.Is(err, artifact.ErrMissingObject):
		writeError(w, http.StatusConflict, "object_missing",
			"That object is not in storage. Upload it before registering it.")
	case errors.Is(err, artifact.ErrSizeMismatch):
		writeError(w, http.StatusConflict, "size_mismatch", err.Error())
	case errors.Is(err, artifact.ErrConflict):
		writeError(w, http.StatusConflict, "artifact_exists", err.Error())
	case errors.Is(err, storage.ErrBadKey):
		writeError(w, http.StatusBadRequest, "invalid_label", err.Error())
	case err != nil:
		internalError(w, r, "register artifact", err)
	default:
		writeJSON(w, http.StatusCreated, map[string]any{"artifact": a})
	}
}

// handleWorkerUploadURL mints an upload URL for one file of a task's output.
//
// A packager produces files whose names it only discovers as it runs — a
// manifest, an init segment and a chunk per rendition per interval — so they
// cannot all be signed in advance. The control plane still decides where they
// go: the worker names a path relative to its own task, and the key is built
// here. A worker never chooses an absolute key, and never holds a storage
// credential.
func (s *Server) handleWorkerUploadURL(w http.ResponseWriter, r *http.Request) {
	wk, attemptID, ok := s.workerAttempt(w, r)
	if !ok {
		return
	}
	ctx, err := s.d.Jobs.AuthorizeAttempt(r.Context(), attemptID, wk.ID)
	if errors.Is(err, job.ErrStaleAttempt) {
		staleAttempt(w)
		return
	}
	if err != nil {
		internalError(w, r, "authorize attempt", err)
		return
	}

	var req struct {
		Path string `json:"path"`
	}
	if !decode(w, r, &req) {
		return
	}

	key, err := storage.JobOutputKey(ctx.TenantID, ctx.JobID, 1, req.Path)
	if errors.Is(err, storage.ErrBadKey) {
		writeError(w, http.StatusBadRequest, "invalid_path", err.Error())
		return
	}
	if err != nil {
		internalError(w, r, "build output key", err)
		return
	}

	url, err := s.d.Storage.PresignPutForWorker(r.Context(), key, s.d.SourceURLTTL)
	if err != nil {
		internalError(w, r, "sign upload url", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"url": url, "storage_key": key})
}

func (s *Server) handleListArtifacts(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	id, ok := pathUUID(r, "id")
	if !ok {
		notFound(w)
		return
	}
	if _, err := s.d.Jobs.Get(r.Context(), p.TenantID, id); err != nil {
		notFound(w)
		return
	}

	sets, err := s.d.Artifacts.Sets(r.Context(), p.TenantID, id)
	if err != nil {
		internalError(w, r, "list artifacts", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"artifact_sets": sets})
}

// handleDownloadArtifact issues a short-lived signed URL rather than serving
// the bytes. Media does not pass through the control plane, and a permanent
// public URL is never the only way to reach protected content.
func (s *Server) handleDownloadArtifact(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	id, ok := pathUUID(r, "id")
	if !ok {
		notFound(w)
		return
	}

	a, err := s.d.Artifacts.Get(r.Context(), p.TenantID, id)
	if errors.Is(err, artifact.ErrNotFound) {
		notFound(w)
		return
	}
	if err != nil {
		internalError(w, r, "get artifact", err)
		return
	}

	url, err := s.d.Storage.PresignGet(r.Context(), a.StorageKey, s.d.DownloadURLTTL)
	if err != nil {
		internalError(w, r, "sign download url", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"url":                url,
		"expires_in_seconds": int(s.d.DownloadURLTTL.Seconds()),
		"size_bytes":         a.SizeBytes,
	})
}
