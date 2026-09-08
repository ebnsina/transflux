// Package artifact records the immutable outputs of a job.
//
// Artifacts are never overwritten. A re-run produces a new set version
// alongside the old one, so a URL that resolved to some bytes yesterday still
// resolves to the same bytes today.
package artifact

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ebnsina/transflux/internal/storage"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound = errors.New("artifact not found")
	// ErrConflict means a different artifact already holds this label. Two
	// different outputs must never share one name.
	ErrConflict = errors.New("an artifact with this label already exists")
	// ErrMissingObject means the worker reported an object that storage does
	// not have. Registering it would produce a record pointing at nothing.
	ErrMissingObject = errors.New("the object is not in storage")
	ErrSizeMismatch  = errors.New("the stored object is a different size than reported")
)

type Artifact struct {
	ID           uuid.UUID       `json:"id"`
	SetID        uuid.UUID       `json:"artifact_set_id"`
	Kind         string          `json:"kind"`
	Label        string          `json:"label"`
	StorageKey   string          `json:"-"`
	SizeBytes    int64           `json:"size_bytes"`
	ChecksumAlgo string          `json:"checksum_algo,omitempty"`
	Checksum     []byte          `json:"checksum,omitempty"`
	Media        json.RawMessage `json:"media,omitempty"`
	CreatedAt    time.Time       `json:"created_at"`
}

type Set struct {
	ID          uuid.UUID  `json:"id"`
	JobID       uuid.UUID  `json:"job_id"`
	Version     int        `json:"version"`
	State       string     `json:"state"`
	Prefix      string     `json:"-"`
	CreatedAt   time.Time  `json:"created_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	Artifacts   []Artifact `json:"artifacts,omitempty"`
}

// Registration is what a worker reports about one output.
type Registration struct {
	Kind         string          `json:"kind"`
	Label        string          `json:"label"`
	StorageKey   string          `json:"storage_key"`
	SizeBytes    int64           `json:"size_bytes"`
	ChecksumAlgo string          `json:"checksum_algo,omitempty"`
	Checksum     []byte          `json:"checksum,omitempty"`
	Media        json.RawMessage `json:"media,omitempty"`
}

type Store struct {
	pool    *pgxpool.Pool
	storage storage.Store
}

func NewStore(pool *pgxpool.Pool, s storage.Store) *Store {
	return &Store{pool: pool, storage: s}
}

// Register records one output of a job.
//
// The object is checked in storage first. A worker reporting an artifact it did
// not actually upload would otherwise leave a record pointing at nothing, and
// the failure would surface later as a broken download rather than here.
//
// Registration is idempotent: a worker that retried after a dropped response
// gets the same artifact back rather than a conflict.
func (s *Store) Register(ctx context.Context, tenantID, jobID, attemptID uuid.UUID,
	reg Registration) (Artifact, error) {

	if _, err := storage.Segment(reg.Label); err != nil {
		return Artifact{}, fmt.Errorf("%w: %s", storage.ErrBadKey, reg.Label)
	}

	info, err := s.storage.Head(ctx, reg.StorageKey)
	if errors.Is(err, storage.ErrNotFound) {
		return Artifact{}, ErrMissingObject
	}
	if err != nil {
		return Artifact{}, fmt.Errorf("check object: %w", err)
	}
	// Trust storage over the report. A truncated upload that the worker
	// believed succeeded must not become an artifact.
	if reg.SizeBytes > 0 && info.Size != reg.SizeBytes {
		return Artifact{}, fmt.Errorf("%w: stored %d, reported %d",
			ErrSizeMismatch, info.Size, reg.SizeBytes)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Artifact{}, err
	}
	defer tx.Rollback(ctx)

	set, err := s.currentSet(ctx, tx, tenantID, jobID)
	if err != nil {
		return Artifact{}, err
	}

	var existing Artifact
	err = tx.QueryRow(ctx, `
		select id, artifact_set_id, kind, label, storage_key, size_bytes,
		       checksum_algo, checksum, media, created_at
		  from artifacts where artifact_set_id = $1 and label = $2`,
		set.ID, reg.Label,
	).Scan(&existing.ID, &existing.SetID, &existing.Kind, &existing.Label,
		&existing.StorageKey, &existing.SizeBytes, &existing.ChecksumAlgo,
		&existing.Checksum, &existing.Media, &existing.CreatedAt)

	if err == nil {
		// Same output reported twice: the retry of a dropped response.
		if existing.StorageKey == reg.StorageKey {
			return existing, nil
		}
		// A different object under a name that is already taken. Artifacts are
		// immutable, so this is a conflict rather than an update.
		return Artifact{}, fmt.Errorf("%w: %q", ErrConflict, reg.Label)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Artifact{}, err
	}

	a := Artifact{
		ID: uuid.Must(uuid.NewV7()), SetID: set.ID, Kind: reg.Kind, Label: reg.Label,
		StorageKey: reg.StorageKey, SizeBytes: info.Size,
		ChecksumAlgo: reg.ChecksumAlgo, Checksum: reg.Checksum, Media: reg.Media,
	}
	err = tx.QueryRow(ctx, `
		insert into artifacts (id, tenant_id, artifact_set_id, task_attempt_id, kind,
		                       label, storage_key, size_bytes, checksum_algo, checksum, media)
		values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		returning created_at`,
		a.ID, tenantID, set.ID, attemptID, a.Kind, a.Label, a.StorageKey,
		a.SizeBytes, nullable(a.ChecksumAlgo), a.Checksum, nullJSON(a.Media),
	).Scan(&a.CreatedAt)
	if err != nil {
		return Artifact{}, fmt.Errorf("register artifact: %w", err)
	}

	return a, tx.Commit(ctx)
}

// currentSet returns the job's open artifact set, creating the first one on
// demand. A re-run of a job opens a new version rather than adding to the old.
func (s *Store) currentSet(ctx context.Context, tx pgx.Tx, tenantID, jobID uuid.UUID) (Set, error) {
	var set Set
	err := tx.QueryRow(ctx, `
		select id, job_id, version, state, storage_prefix, created_at
		  from artifact_sets
		 where job_id = $1 and tenant_id = $2
		 order by version desc limit 1`, jobID, tenantID,
	).Scan(&set.ID, &set.JobID, &set.Version, &set.State, &set.Prefix, &set.CreatedAt)
	if err == nil {
		return set, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Set{}, err
	}

	set = Set{ID: uuid.Must(uuid.NewV7()), JobID: jobID, Version: 1, State: "building"}
	set.Prefix = fmt.Sprintf("t/%s/jobs/%s/v%d", tenantID, jobID, set.Version)
	err = tx.QueryRow(ctx, `
		insert into artifact_sets (id, tenant_id, job_id, version, state, storage_prefix)
		values ($1,$2,$3,$4,'building',$5) returning created_at`,
		set.ID, tenantID, jobID, set.Version, set.Prefix).Scan(&set.CreatedAt)
	return set, err
}

// MarkComplete closes a job's artifact set. Until this, the set is incomplete
// by definition and nothing should be delivered from it.
func (s *Store) MarkComplete(ctx context.Context, tenantID, jobID uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `
		update artifact_sets set state = 'complete', completed_at = now()
		 where job_id = $1 and tenant_id = $2 and state in ('building', 'validating')`,
		jobID, tenantID)
	return err
}

// MarkFailed records that a set will never be completed, so it is not mistaken
// for one still in progress.
func (s *Store) MarkFailed(ctx context.Context, tenantID, jobID uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `
		update artifact_sets set state = 'failed'
		 where job_id = $1 and tenant_id = $2 and state in ('building', 'validating')`,
		jobID, tenantID)
	return err
}

// Sets returns a job's artifact sets, newest version first, with their
// artifacts.
func (s *Store) Sets(ctx context.Context, tenantID, jobID uuid.UUID) ([]Set, error) {
	rows, err := s.pool.Query(ctx, `
		select id, job_id, version, state, storage_prefix, created_at, completed_at
		  from artifact_sets where job_id = $1 and tenant_id = $2
		 order by version desc`, jobID, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	sets := []Set{}
	for rows.Next() {
		var set Set
		if err := rows.Scan(&set.ID, &set.JobID, &set.Version, &set.State,
			&set.Prefix, &set.CreatedAt, &set.CompletedAt); err != nil {
			return nil, err
		}
		sets = append(sets, set)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for i := range sets {
		sets[i].Artifacts, err = s.artifacts(ctx, tenantID, sets[i].ID)
		if err != nil {
			return nil, err
		}
	}
	return sets, nil
}

func (s *Store) artifacts(ctx context.Context, tenantID, setID uuid.UUID) ([]Artifact, error) {
	rows, err := s.pool.Query(ctx, `
		select id, artifact_set_id, kind, label, storage_key, size_bytes,
		       checksum_algo, checksum, media, created_at
		  from artifacts where artifact_set_id = $1 and tenant_id = $2 order by label`,
		setID, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Artifact{}
	for rows.Next() {
		var a Artifact
		var algo *string
		if err := rows.Scan(&a.ID, &a.SetID, &a.Kind, &a.Label, &a.StorageKey,
			&a.SizeBytes, &algo, &a.Checksum, &a.Media, &a.CreatedAt); err != nil {
			return nil, err
		}
		if algo != nil {
			a.ChecksumAlgo = *algo
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ForJob returns the artifacts of a job's current set, for a validate task to
// fetch and check.
func (s *Store) ForJob(ctx context.Context, tenantID, jobID uuid.UUID) ([]Artifact, error) {
	var setID uuid.UUID
	err := s.pool.QueryRow(ctx, `
		select id from artifact_sets where job_id = $1 and tenant_id = $2
		 order by version desc limit 1`, jobID, tenantID).Scan(&setID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.artifacts(ctx, tenantID, setID)
}

// Get returns one artifact, scoped to its tenant.
func (s *Store) Get(ctx context.Context, tenantID, id uuid.UUID) (Artifact, error) {
	var a Artifact
	var algo *string
	err := s.pool.QueryRow(ctx, `
		select id, artifact_set_id, kind, label, storage_key, size_bytes,
		       checksum_algo, checksum, media, created_at
		  from artifacts where id = $1 and tenant_id = $2`, id, tenantID,
	).Scan(&a.ID, &a.SetID, &a.Kind, &a.Label, &a.StorageKey, &a.SizeBytes,
		&algo, &a.Checksum, &a.Media, &a.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Artifact{}, ErrNotFound
	}
	if algo != nil {
		a.ChecksumAlgo = *algo
	}
	return a, err
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func nullJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return []byte(raw)
}
