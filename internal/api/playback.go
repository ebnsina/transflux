package api

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/ebnsina/transflux/internal/artifact"
	"github.com/ebnsina/transflux/internal/auth"
	"github.com/ebnsina/transflux/internal/playback"
	"github.com/ebnsina/transflux/internal/storage"
)

// handleCreatePlaybackLink issues a short-lived link for watching a package.
//
// Deciding who may watch is ours; delivering the bytes is not. The caller
// authenticates as usual and gets back a link they can hand to a player, which
// has no API key of its own.
func (s *Server) handleCreatePlaybackLink(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	id, ok := pathUUID(r, "id")
	if !ok {
		notFound(w)
		return
	}

	art, err := s.d.Artifacts.Get(r.Context(), p.TenantID, id)
	if errors.Is(err, artifact.ErrNotFound) {
		notFound(w)
		return
	}
	if err != nil {
		internalError(w, r, "get artifact", err)
		return
	}
	// Only a playlist can be played. A rendition is a file to download.
	if art.Kind != "manifest" || !strings.HasSuffix(art.StorageKey, ".m3u8") {
		writeError(w, http.StatusConflict, "not_playable",
			"Only a streaming playlist can be played. Download this one instead.")
		return
	}

	token, expires, err := s.d.Playback.Mint(art.SetID, p.TenantID, s.d.PlaybackTTL)
	if err != nil {
		internalError(w, r, "mint playback token", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"url":                playback.Path(token, "master.m3u8"),
		"expires_at":         expires,
		"expires_in_seconds": int(s.d.PlaybackTTL.Seconds()),
	})
}

// handlePlayback serves a playlist to a player holding a link.
//
// There is no API key here: the link is the authorisation. Only playlists are
// served, and only after being rewritten so their references resolve —
// segments point straight at storage, because putting every viewer's bandwidth
// through the control plane would make this a CDN, which it is not.
func (s *Server) handlePlayback(w http.ResponseWriter, r *http.Request) {
	claims, err := s.d.Playback.Verify(r.PathValue("token"))
	switch {
	case errors.Is(err, playback.ErrExpired):
		writeError(w, http.StatusGone, "link_expired",
			"This playback link has expired. Ask for a new one.")
		return
	case err != nil:
		// A bad link and an unknown one are the same answer: a player has no
		// business learning which.
		writeError(w, http.StatusForbidden, "link_invalid", "This playback link is not valid.")
		return
	}

	file := r.PathValue("file")
	if !playback.SafeFile(file) || !strings.HasSuffix(file, ".m3u8") {
		notFound(w)
		return
	}

	set, err := s.d.Artifacts.Set(r.Context(), claims.TenantID, claims.ArtifactSetID)
	if errors.Is(err, artifact.ErrNotFound) {
		notFound(w)
		return
	}
	if err != nil {
		internalError(w, r, "load artifact set", err)
		return
	}
	// A set that is not complete is not deliverable, whatever link exists.
	if set.State != "complete" {
		writeError(w, http.StatusConflict, "not_ready", "This is not ready to play yet.")
		return
	}

	body, err := s.readObject(r, set.Prefix+"/"+file)
	if errors.Is(err, storage.ErrNotFound) {
		notFound(w)
		return
	}
	if err != nil {
		internalError(w, r, "read playlist", err)
		return
	}

	rewritten, err := playback.RewriteHLS(r.Context(), body, r.PathValue("token"), set.Prefix,
		s.d.Storage.PresignGet, s.d.PlaybackTTL)
	if err != nil {
		internalError(w, r, "rewrite playlist", err)
		return
	}

	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	// The signed URLs inside expire, so a cached copy would outlive them.
	w.Header().Set("Cache-Control", "no-store")
	w.Write([]byte(rewritten))
}

// readObject reads a small object into memory. Only playlists reach this, and
// the limit is what stops something unexpected doing so.
func (s *Server) readObject(r *http.Request, key string) (string, error) {
	reader, err := s.d.Storage.Get(r.Context(), key)
	if err != nil {
		return "", err
	}
	defer reader.Close()

	body, err := io.ReadAll(io.LimitReader(reader, 4<<20))
	return string(body), err
}
