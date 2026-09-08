package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/ebnsina/transflux/internal/asset"
	"github.com/ebnsina/transflux/internal/auth"
	"github.com/ebnsina/transflux/internal/job"
	"github.com/ebnsina/transflux/internal/pipeline"
	"github.com/google/uuid"
)

type createJobRequest struct {
	AssetVersionID string `json:"asset_version_id"`
	Pipeline       string `json:"pipeline"`
	IdempotencyKey string `json:"idempotency_key"`
	Priority       int    `json:"priority"`
}

// sourceRef is what a task spec carries instead of a URL.
//
// A presigned URL has a lifetime, and a task can sit queued for hours behind a
// busy fleet. Storing the reference and signing at lease time means a URL is
// always fresh when a worker receives it.
type sourceRef struct {
	SourceKey string `json:"source_key"`
}

func (s *Server) handleCreateJob(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	var req createJobRequest
	if !decode(w, r, &req) {
		return
	}

	versionID, err := uuid.Parse(req.AssetVersionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request",
			"asset_version_id must be a valid id.")
		return
	}
	if _, err := s.d.Assets.GetVersion(r.Context(), p.TenantID, versionID); err != nil {
		notFound(w)
		return
	}

	// The gate: expensive work must never start on a source we have not
	// confirmed is complete.
	source, err := s.d.Assets.Source(r.Context(), p.TenantID, versionID)
	if errors.Is(err, asset.ErrNotFound) {
		writeError(w, http.StatusConflict, "source_missing",
			"This asset version has no source yet. Complete an upload first.")
		return
	}
	if err != nil {
		internalError(w, r, "load source", err)
		return
	}
	if source.VerifiedAt == nil || source.StorageKey == nil {
		writeError(w, http.StatusConflict, "source_unverified",
			"This source has not been verified. Complete its upload first.")
		return
	}

	pipelineVersionID, tasks, err := s.d.Pipelines.Resolve(r.Context(), p.TenantID, req.Pipeline)
	if errors.Is(err, pipeline.ErrUnknownPreset) {
		writeError(w, http.StatusBadRequest, "unknown_pipeline", err.Error())
		return
	}
	if err != nil {
		internalError(w, r, "resolve pipeline", err)
		return
	}

	spec, err := json.Marshal(sourceRef{SourceKey: *source.StorageKey})
	if err != nil {
		internalError(w, r, "build task spec", err)
		return
	}
	built := make([]job.NewTask, 0, len(tasks))
	for _, t := range tasks {
		t.Spec = json.RawMessage(spec)
		built = append(built, t)
	}

	priority := req.Priority
	if priority <= 0 {
		priority = 100
	}

	created, err := s.d.Jobs.Create(r.Context(), p.TenantID, versionID, pipelineVersionID,
		req.IdempotencyKey, priority, built)
	if err != nil {
		internalError(w, r, "create job", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"job": created})
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	id, ok := pathUUID(r, "id")
	if !ok {
		notFound(w)
		return
	}

	j, err := s.d.Jobs.Get(r.Context(), p.TenantID, id)
	if errors.Is(err, job.ErrNotFound) {
		notFound(w)
		return
	}
	if err != nil {
		internalError(w, r, "get job", err)
		return
	}
	tasks, err := s.d.Jobs.Tasks(r.Context(), p.TenantID, id)
	if err != nil {
		internalError(w, r, "list tasks", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"job": j, "tasks": tasks})
}

func (s *Server) handleCancelJob(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	id, ok := pathUUID(r, "id")
	if !ok {
		notFound(w)
		return
	}
	switch err := s.d.Jobs.Cancel(r.Context(), p.TenantID, id); {
	case errors.Is(err, job.ErrNotFound):
		notFound(w)
	case err != nil:
		internalError(w, r, "cancel job", err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) handleListPipelines(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"pipelines": pipeline.Presets()})
}

// resolveSpec turns the stored source reference into something a worker can
// fetch, signed for the network workers are on and only at the moment the work
// is handed out.
func (s *Server) resolveSpec(r *http.Request, spec json.RawMessage) (json.RawMessage, error) {
	var ref sourceRef
	if err := json.Unmarshal(spec, &ref); err != nil || ref.SourceKey == "" {
		return spec, nil
	}

	url, err := s.d.Storage.PresignGetForWorker(r.Context(), ref.SourceKey, s.d.SourceURLTTL)
	if err != nil {
		return nil, err
	}

	var fields map[string]any
	if err := json.Unmarshal(spec, &fields); err != nil {
		return nil, err
	}
	fields["input_url"] = url
	return json.Marshal(fields)
}
