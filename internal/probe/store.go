package probe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("probe not found")

type Store struct{ pool *pgxpool.Pool }

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// Record stores a probe and its tracks, replacing any earlier probe of the same
// asset version. Re-probing is how a parsing improvement reaches existing
// assets, so it must be repeatable rather than an error.
func (s *Store) Record(ctx context.Context, tenantID, versionID uuid.UUID, raw []byte) (Result, error) {
	res, err := Parse(raw)
	if err != nil {
		return Result{}, fmt.Errorf("parse probe output: %w", err)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Result{}, err
	}
	defer tx.Rollback(ctx)

	// Tracks cascade from the probe, so replacing it cannot leave orphans.
	if _, err := tx.Exec(ctx,
		`delete from probes where asset_version_id = $1 and tenant_id = $2`,
		versionID, tenantID); err != nil {
		return Result{}, err
	}

	probeID := uuid.Must(uuid.NewV7())
	if _, err := tx.Exec(ctx, `
		insert into probes (id, tenant_id, asset_version_id, container,
		                    duration_ms, bitrate_bps, size_bytes, raw)
		values ($1,$2,$3,$4,$5,$6,$7,$8)`,
		probeID, tenantID, versionID, nullable(res.Container),
		nullZero(res.DurationMS), nullZero(res.BitrateBPS), nullZero(res.SizeBytes),
		raw); err != nil {
		return Result{}, fmt.Errorf("record probe: %w", err)
	}

	for _, t := range res.Tracks {
		if _, err := tx.Exec(ctx, `
			insert into tracks (id, tenant_id, asset_version_id, probe_id, kind, stream_index,
			                    codec, duration_ms, bitrate_bps, language, title,
			                    is_default, is_forced, role,
			                    width, height, fps_num, fps_den, is_vfr, pixel_format,
			                    bit_depth, rotation_degrees,
			                    color_primaries, color_transfer, color_matrix, color_range,
			                    hdr_format, mastering_display, content_light,
			                    channels, channel_layout, sample_rate_hz, subtitle_format)
			values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,
			        $21,$22,$23,$24,$25,$26,$27,$28,$29,$30,$31,$32,$33)`,
			uuid.Must(uuid.NewV7()), tenantID, versionID, probeID, t.Kind, t.StreamIndex,
			nullable(t.Codec), nullZero(t.DurationMS), nullZero(t.BitrateBPS),
			nullable(t.Language), nullable(t.Title), t.IsDefault, t.IsForced, nullable(t.Role),
			nullZeroInt(t.Width), nullZeroInt(t.Height), nullZeroInt(t.FPSNum), nullZeroInt(t.FPSDen),
			t.IsVFR, nullable(t.PixelFmt), nullZeroInt(t.BitDepth), t.Rotation,
			nullable(t.ColorPrimaries), nullable(t.ColorTransfer), nullable(t.ColorMatrix),
			nullable(t.ColorRange), nullable(t.HDRFormat), nullJSON(t.MasteringDisplay), nullJSON(t.ContentLight),
			nullZeroInt(t.Channels), nullable(t.ChannelLayout), nullZeroInt(t.SampleRateHz),
			nullable(t.SubtitleFormat)); err != nil {
			return Result{}, fmt.Errorf("record track %d: %w", t.StreamIndex, err)
		}
	}

	// The version is only ready once we know what it contains.
	if _, err := tx.Exec(ctx,
		`update asset_versions set status = 'ready' where id = $1 and tenant_id = $2`,
		versionID, tenantID); err != nil {
		return Result{}, err
	}

	return res, tx.Commit(ctx)
}

// Tracks returns what an asset version contains.
func (s *Store) Tracks(ctx context.Context, tenantID, versionID uuid.UUID) ([]Track, error) {
	rows, err := s.pool.Query(ctx, `
		select kind, stream_index, codec, duration_ms, bitrate_bps, language, title,
		       is_default, is_forced, role, width, height, fps_num, fps_den, is_vfr,
		       pixel_format, bit_depth, rotation_degrees,
		       color_primaries, color_transfer, color_matrix, color_range, hdr_format,
		       channels, channel_layout, sample_rate_hz, subtitle_format
		  from tracks where asset_version_id = $1 and tenant_id = $2
		 order by stream_index`, versionID, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tracks := []Track{}
	for rows.Next() {
		var t Track
		var codec, lang, title, role, pixFmt, prim, trc, matrix, rng, layout, subFmt, hdr *string
		var duration, bitrate *int64
		var width, height, fpsNum, fpsDen, bitDepth, channels, sampleRate *int
		if err := rows.Scan(&t.Kind, &t.StreamIndex, &codec, &duration, &bitrate, &lang, &title,
			&t.IsDefault, &t.IsForced, &role, &width, &height, &fpsNum, &fpsDen, &t.IsVFR,
			&pixFmt, &bitDepth, &t.Rotation, &prim, &trc, &matrix, &rng, &hdr,
			&channels, &layout, &sampleRate, &subFmt); err != nil {
			return nil, err
		}
		t.Codec, t.Language, t.Title, t.Role = deref(codec), deref(lang), deref(title), deref(role)
		t.PixelFmt, t.ChannelLayout, t.SubtitleFormat = deref(pixFmt), deref(layout), deref(subFmt)
		t.ColorPrimaries, t.ColorTransfer = deref(prim), deref(trc)
		t.ColorMatrix, t.ColorRange, t.HDRFormat = deref(matrix), deref(rng), deref(hdr)
		t.DurationMS, t.BitrateBPS = derefI64(duration), derefI64(bitrate)
		t.Width, t.Height = derefInt(width), derefInt(height)
		t.FPSNum, t.FPSDen = derefInt(fpsNum), derefInt(fpsDen)
		t.BitDepth, t.Channels, t.SampleRateHz = derefInt(bitDepth), derefInt(channels), derefInt(sampleRate)
		tracks = append(tracks, t)
	}
	return tracks, rows.Err()
}

// Get returns the probe summary for a version.
func (s *Store) Get(ctx context.Context, tenantID, versionID uuid.UUID) (Result, error) {
	var (
		res       Result
		container *string
		duration  *int64
		bitrate   *int64
		size      *int64
	)
	err := s.pool.QueryRow(ctx, `
		select container, duration_ms, bitrate_bps, size_bytes
		  from probes where asset_version_id = $1 and tenant_id = $2`, versionID, tenantID,
	).Scan(&container, &duration, &bitrate, &size)
	if errors.Is(err, pgx.ErrNoRows) {
		return Result{}, ErrNotFound
	}
	if err != nil {
		return Result{}, err
	}
	res.Container = deref(container)
	res.DurationMS, res.BitrateBPS, res.SizeBytes = derefI64(duration), derefI64(bitrate), derefI64(size)

	res.Tracks, err = s.Tracks(ctx, tenantID, versionID)
	return res, err
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func nullZero(n int64) *int64 {
	if n == 0 {
		return nil
	}
	return &n
}

func nullZeroInt(n int) *int {
	if n == 0 {
		return nil
	}
	return &n
}

func nullJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return []byte(raw)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func derefI64(n *int64) int64 {
	if n == nil {
		return 0
	}
	return *n
}

func derefInt(n *int) int {
	if n == nil {
		return 0
	}
	return *n
}
