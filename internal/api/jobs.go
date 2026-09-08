package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/ebnsina/transflux/internal/asset"
	"github.com/ebnsina/transflux/internal/auth"
	"github.com/ebnsina/transflux/internal/job"
	"github.com/ebnsina/transflux/internal/pipeline"
	"github.com/ebnsina/transflux/internal/probe"
	"github.com/ebnsina/transflux/internal/storage"
	"github.com/google/uuid"
)

type createJobRequest struct {
	AssetVersionID string `json:"asset_version_id"`
	Pipeline       string `json:"pipeline"`
	IdempotencyKey string `json:"idempotency_key"`
	Priority       int    `json:"priority"`
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
	var pipelineVersionID uuid.UUID
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

	preset, err := func() (pipeline.Preset, error) {
		id, preset, err := s.d.Pipelines.Resolve(r.Context(), p.TenantID, req.Pipeline)
		pipelineVersionID = id
		return preset, err
	}()
	if errors.Is(err, pipeline.ErrUnknownPreset) {
		writeError(w, http.StatusBadRequest, "unknown_pipeline", err.Error())
		return
	}
	if err != nil {
		internalError(w, r, "resolve pipeline", err)
		return
	}

	// A ladder is planned against what the source actually is, so a rung never
	// asks to upscale. That means the source has to have been probed first.
	var media probe.Result
	if preset.NeedsProbe() {
		media, err = s.d.Probes.Get(r.Context(), p.TenantID, versionID)
		if errors.Is(err, probe.ErrNotFound) {
			writeError(w, http.StatusConflict, "probe_required",
				"This pipeline plans against the source's tracks. Run the probe pipeline first.")
			return
		}
		if err != nil {
			internalError(w, r, "load probe", err)
			return
		}
	}

	built, err := pipeline.Plan(preset, *source.StorageKey, media)
	if err != nil {
		writeError(w, http.StatusBadRequest, "cannot_plan", err.Error())
		return
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

func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())

	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 200 {
			writeError(w, http.StatusBadRequest, "invalid_request",
				"limit must be an integer between 1 and 200.")
			return
		}
		limit = n
	}

	jobs, err := s.d.Jobs.List(r.Context(), p.TenantID, limit)
	if err != nil {
		internalError(w, r, "list jobs", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
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

	body := map[string]any{"job": j, "tasks": tasks}

	// Every execution, with the scheduler's reasoning and what it cost. This
	// is what makes "why did this take an hour" a query rather than an
	// investigation.
	if attempts, err := s.d.Jobs.Attempts(r.Context(), p.TenantID, id); err == nil {
		body["attempts"] = attempts
	}
	// What was checked, and what it said. A failed job should not require
	// reading worker logs to find out why.
	if report, err := s.d.Validations.ForJob(r.Context(), p.TenantID, id); err == nil &&
		len(report.Checks) > 0 {
		body["validation"] = report
	}
	writeJSON(w, http.StatusOK, body)
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

// resolveSpec turns stored references into things a worker can actually use:
// URLs signed for the network workers are on, minted at the moment the work is
// handed out rather than when the job was created.
func (s *Server) resolveSpec(r *http.Request, a job.Assignment) (json.RawMessage, error) {
	var ref struct {
		SourceKey   string `json:"source_key"`
		OutputLabel string `json:"output_label"`
	}
	if err := json.Unmarshal(a.Spec, &ref); err != nil {
		return a.Spec, nil
	}

	var fields map[string]any
	if err := json.Unmarshal(a.Spec, &fields); err != nil {
		return nil, err
	}

	if ref.SourceKey != "" {
		url, err := s.d.Storage.PresignGetForWorker(r.Context(), ref.SourceKey, s.d.SourceURLTTL)
		if err != nil {
			return nil, err
		}
		fields["input_url"] = url
	}

	// A task that works on earlier outputs is given the artifacts that were
	// actually registered, rather than the ones the plan expected: for
	// validation the difference between the two is exactly what it is there to
	// notice, and for packaging it is what makes it package what exists.
	if wants, _ := fields["needs_artifacts"].(bool); wants {
		registered, err := s.d.Artifacts.ForJob(r.Context(), a.TenantID, a.JobID)
		if err != nil {
			return nil, err
		}
		list := make([]map[string]any, 0, len(registered))
		for _, art := range registered {
			url, err := s.d.Storage.PresignGetForWorker(r.Context(), art.StorageKey, s.d.SourceURLTTL)
			if err != nil {
				return nil, err
			}
			entry := map[string]any{
				"label": art.Label, "url": url, "size_bytes": art.SizeBytes,
				"checksum_algo": art.ChecksumAlgo, "checksum": art.Checksum,
			}
			// The recorded shape, so a packager can order renditions by
			// picture size rather than guessing from the label.
			var media struct {
				Width  int `json:"width"`
				Height int `json:"height"`
			}
			if len(art.Media) > 0 && json.Unmarshal(art.Media, &media) == nil {
				entry["width"], entry["height"] = media.Width, media.Height
			}
			list = append(list, entry)
		}
		fields["artifacts"] = list
	}

	if ref.OutputLabel != "" {
		// The output key is derived here rather than stored, so it cannot
		// disagree with the job it belongs to. Version 1 for now; a re-run
		// writes a new set rather than over this one.
		key, err := storage.JobArtifactKey(a.TenantID, a.JobID, 1, ref.OutputLabel)
		if err != nil {
			return nil, err
		}
		url, err := s.d.Storage.PresignPutForWorker(r.Context(), key, s.d.SourceURLTTL)
		if err != nil {
			return nil, err
		}
		fields["output_key"] = key
		fields["output_url"] = url
	}

	return json.Marshal(fields)
}
