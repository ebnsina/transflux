package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/ebnsina/transflux/internal/asset"
	"github.com/ebnsina/transflux/internal/auth"
	"github.com/ebnsina/transflux/internal/probe"
	"github.com/ebnsina/transflux/internal/upload"
)

type createAssetRequest struct {
	ExternalID *string `json:"external_id"`
	Name       *string `json:"name"`
}

type assetResponse struct {
	asset.Asset
	Versions []versionResponse `json:"versions,omitempty"`
}

type versionResponse struct {
	asset.Version
	Media *probe.Result `json:"media,omitempty"`
}

func (s *Server) handleCreateAsset(w http.ResponseWriter, r *http.Request) {
	p, ok := auth.FromContext(r.Context())
	if !ok {
		internalError(w, r, "authenticated route without a principal", errNoPrincipal)
		return
	}
	var req createAssetRequest
	if !decode(w, r, &req) {
		return
	}

	a, v, err := s.d.Assets.Create(r.Context(), p.TenantID, req.ExternalID, req.Name, "managed")
	switch {
	case errors.Is(err, asset.ErrConflict):
		// external_id is the caller's own identifier, so a repeat is a
		// conflict they can act on rather than a server fault.
		writeError(w, http.StatusConflict, "asset_exists",
			"An asset with this external_id already exists.")
		return
	case err != nil:
		internalError(w, r, "create asset", err)
		return
	}
	writeJSON(w, http.StatusCreated, assetResponse{
		Asset: a, Versions: []versionResponse{{Version: v}}})
}

func (s *Server) handleListAssets(w http.ResponseWriter, r *http.Request) {
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

	assets, err := s.d.Assets.List(r.Context(), p.TenantID, limit)
	if err != nil {
		internalError(w, r, "list assets", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"assets": assets})
}

func (s *Server) handleGetAsset(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	id, ok := pathUUID(r, "id")
	if !ok {
		notFound(w)
		return
	}

	// Scoped by tenant, so another tenant's asset is simply not found.
	a, err := s.d.Assets.Get(r.Context(), p.TenantID, id)
	if errors.Is(err, asset.ErrNotFound) {
		notFound(w)
		return
	}
	if err != nil {
		internalError(w, r, "get asset", err)
		return
	}

	versions, err := s.d.Assets.Versions(r.Context(), p.TenantID, id)
	if err != nil {
		internalError(w, r, "list asset versions", err)
		return
	}

	out := make([]versionResponse, 0, len(versions))
	for _, v := range versions {
		vr := versionResponse{Version: v}
		// What the probe found, when there is one. Absent rather than empty
		// so an unprobed version is distinguishable from one with no tracks.
		if media, err := s.d.Probes.Get(r.Context(), p.TenantID, v.ID); err == nil {
			vr.Media = &media
		} else if !errors.Is(err, probe.ErrNotFound) {
			internalError(w, r, "load probe", err)
			return
		}
		out = append(out, vr)
	}
	writeJSON(w, http.StatusOK, assetResponse{Asset: a, Versions: out})
}

type createUploadRequest struct {
	AssetVersionID string `json:"asset_version_id"`
	SizeBytes      int64  `json:"size_bytes"`
	ContentType    string `json:"content_type"`
	ChecksumAlgo   string `json:"checksum_algo"`
	Checksum       []byte `json:"checksum"`
}

func (s *Server) handleCreateUpload(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	assetID, ok := pathUUID(r, "id")
	if !ok {
		notFound(w)
		return
	}
	var req createUploadRequest
	if !decode(w, r, &req) {
		return
	}

	versions, err := s.d.Assets.Versions(r.Context(), p.TenantID, assetID)
	if err != nil {
		internalError(w, r, "list asset versions", err)
		return
	}
	if len(versions) == 0 {
		notFound(w)
		return
	}

	// Default to the newest version; an explicit one must belong to this asset,
	// which also prevents uploading into another tenant's version by id.
	target := versions[len(versions)-1]
	if req.AssetVersionID != "" {
		found := false
		for _, v := range versions {
			if v.ID.String() == req.AssetVersionID {
				target, found = v, true
				break
			}
		}
		if !found {
			notFound(w)
			return
		}
	}

	up, parts, err := s.d.Uploads.Create(r.Context(), p.TenantID, target.ID,
		req.SizeBytes, req.ContentType, req.ChecksumAlgo, req.Checksum)
	switch {
	case errors.Is(err, upload.ErrBadSize):
		writeError(w, http.StatusBadRequest, "invalid_request",
			"size_bytes must be greater than zero.")
		return
	case errors.Is(err, upload.ErrTooLarge):
		writeError(w, http.StatusBadRequest, "too_large",
			"size_bytes exceeds the maximum object size.")
		return
	case err != nil:
		internalError(w, r, "create upload", err)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{"upload": up, "parts": parts})
}
