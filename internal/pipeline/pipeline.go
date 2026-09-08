// Package pipeline turns a named preset into the concrete task graph a job
// runs. Presets are defined in code for now; caller-defined pipelines are a
// later concern, and the validation they need is the reason to wait.
package pipeline

import (
	"context"
	"errors"
	"fmt"

	"github.com/ebnsina/transflux/internal/job"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrUnknownPreset = errors.New("unknown pipeline")

// Preset is a built-in pipeline. The definition is stored with each version so
// a job stays reproducible after a preset is changed.
type Preset struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// Tasks is a fixed graph. A preset with a Ladder instead has its tasks
	// built from the source, so a rung never asks to upscale.
	Tasks  []job.NewTask `json:"-"`
	Ladder []Rung        `json:"-"`
}

// NeedsProbe reports whether this preset can only be planned once the source
// has been inspected.
func (p Preset) NeedsProbe() bool { return len(p.Ladder) > 0 }

// Rung is one output of a ladder.
type Rung struct {
	Label  string
	Width  int
	Height int
	CRF    int
	// MaxrateBPS caps a difficult scene so a client's connection is not asked
	// for more than the rung promises.
	MaxrateBPS int
}

// presets are the pipelines callers may name today.
var presets = map[string]Preset{
	"probe": {
		Name:        "probe",
		Description: "Inspect the source and record its tracks.",
		Tasks: []job.NewTask{
			{Key: "probe", Operation: "probe"},
		},
	},
	"transcode-h264": {
		Name:        "transcode-h264",
		Description: "Transcode to a single 720p H.264 rendition with AAC audio.",
		Ladder: []Rung{
			{Label: "720p_h264", Width: 1280, Height: 720, CRF: 23, MaxrateBPS: 4_000_000},
		},
	},
}

func Presets() []Preset {
	out := make([]Preset, 0, len(presets))
	for _, p := range presets {
		out = append(out, p)
	}
	return out
}

type Store struct{ pool *pgxpool.Pool }

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// Resolve returns the pipeline version for a preset, creating it on first use.
// Each tenant gets its own rows so that a caller-defined pipeline of the same
// name later is not a special case.
func (s *Store) Resolve(ctx context.Context, tenantID uuid.UUID, name string) (uuid.UUID, Preset, error) {
	preset, ok := presets[name]
	if !ok {
		return uuid.Nil, Preset{}, fmt.Errorf("%w: %q", ErrUnknownPreset, name)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, Preset{}, err
	}
	defer tx.Rollback(ctx)

	var pipelineID uuid.UUID
	err = tx.QueryRow(ctx,
		`select id from pipelines where tenant_id = $1 and name = $2`,
		tenantID, name).Scan(&pipelineID)
	if errors.Is(err, pgx.ErrNoRows) {
		pipelineID = uuid.Must(uuid.NewV7())
		if _, err := tx.Exec(ctx,
			`insert into pipelines (id, tenant_id, name) values ($1, $2, $3)`,
			pipelineID, tenantID, name); err != nil {
			return uuid.Nil, Preset{}, err
		}
	} else if err != nil {
		return uuid.Nil, Preset{}, err
	}

	var versionID uuid.UUID
	err = tx.QueryRow(ctx, `
		select id from pipeline_versions
		 where pipeline_id = $1 order by version desc limit 1`, pipelineID).Scan(&versionID)
	if errors.Is(err, pgx.ErrNoRows) {
		versionID = uuid.Must(uuid.NewV7())
		definition := map[string]any{
			"preset":      preset.Name,
			"description": preset.Description,
			"tasks":       taskNames(preset),
		}
		if _, err := tx.Exec(ctx, `
			insert into pipeline_versions (id, tenant_id, pipeline_id, version, definition)
			values ($1, $2, $3, 1, $4)`, versionID, tenantID, pipelineID, definition); err != nil {
			return uuid.Nil, Preset{}, err
		}
	} else if err != nil {
		return uuid.Nil, Preset{}, err
	}

	return versionID, preset, tx.Commit(ctx)
}

func taskNames(p Preset) []string {
	out := make([]string, 0, len(p.Tasks)+len(p.Ladder))
	for _, t := range p.Tasks {
		out = append(out, t.Operation)
	}
	for _, r := range p.Ladder {
		out = append(out, "encode:"+r.Label)
	}
	return out
}
