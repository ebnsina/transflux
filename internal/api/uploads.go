package api

import (
	"errors"
	"net/http"

	"github.com/ebnsina/transflux/internal/auth"
	"github.com/ebnsina/transflux/internal/upload"
)

func (s *Server) handleUploadStatus(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	id, ok := pathUUID(r, "id")
	if !ok {
		notFound(w)
		return
	}

	// Returns what storage actually holds plus presigned URLs for what is
	// missing, so resuming an interrupted upload is one request.
	st, err := s.d.Uploads.Status(r.Context(), p.TenantID, id)
	if errors.Is(err, upload.ErrNotFound) {
		notFound(w)
		return
	}
	if err != nil {
		internalError(w, r, "upload status", err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleCompleteUpload(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	id, ok := pathUUID(r, "id")
	if !ok {
		notFound(w)
		return
	}

	up, err := s.d.Uploads.Complete(r.Context(), p.TenantID, id)
	switch {
	case errors.Is(err, upload.ErrNotFound):
		notFound(w)
	case errors.Is(err, upload.ErrIncomplete):
		// 409, not 400: the request is well formed, the upload simply is not
		// finished. The client should fetch status and send what is missing.
		writeError(w, http.StatusConflict, "upload_incomplete", err.Error())
	case errors.Is(err, upload.ErrSizeMismatch):
		writeError(w, http.StatusConflict, "size_mismatch", err.Error())
	case errors.Is(err, upload.ErrNotOpen):
		writeError(w, http.StatusConflict, "upload_not_open",
			"This upload has been aborted or has expired.")
	case err != nil:
		internalError(w, r, "complete upload", err)
	default:
		writeJSON(w, http.StatusOK, map[string]any{"upload": up})
	}
}

func (s *Server) handleAbortUpload(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	id, ok := pathUUID(r, "id")
	if !ok {
		notFound(w)
		return
	}

	switch err := s.d.Uploads.Abort(r.Context(), p.TenantID, id); {
	case errors.Is(err, upload.ErrNotFound):
		notFound(w)
	case errors.Is(err, upload.ErrNotOpen):
		writeError(w, http.StatusConflict, "upload_not_open",
			"This upload has already been completed or aborted.")
	case err != nil:
		internalError(w, r, "abort upload", err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}
