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
	Name        string
	Description string
	Tasks       []job.NewTask
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
func (s *Store) Resolve(ctx context.Context, tenantID uuid.UUID, name string) (uuid.UUID, []job.NewTask, error) {
	preset, ok := presets[name]
	if !ok {
		return uuid.Nil, nil, fmt.Errorf("%w: %q", ErrUnknownPreset, name)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, nil, err
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
			return uuid.Nil, nil, err
		}
	} else if err != nil {
		return uuid.Nil, nil, err
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
			"tasks":       taskNames(preset.Tasks),
		}
		if _, err := tx.Exec(ctx, `
			insert into pipeline_versions (id, tenant_id, pipeline_id, version, definition)
			values ($1, $2, $3, 1, $4)`, versionID, tenantID, pipelineID, definition); err != nil {
			return uuid.Nil, nil, err
		}
	} else if err != nil {
		return uuid.Nil, nil, err
	}

	return versionID, preset.Tasks, tx.Commit(ctx)
}

func taskNames(tasks []job.NewTask) []string {
	out := make([]string, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, t.Operation)
	}
	return out
}
