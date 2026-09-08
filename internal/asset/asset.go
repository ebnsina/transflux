// Package asset owns assets, their versions and the source file each version
// pins. An asset is media; a job is an operation against it. Jobs never mutate
// an asset, and reference a version so they stay reproducible.
package asset

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound = errors.New("asset not found")
	ErrConflict = errors.New("asset already exists")
)

type Asset struct {
	ID         uuid.UUID `json:"id"`
	ExternalID *string   `json:"external_id,omitempty"`
	Name       *string   `json:"name,omitempty"`
	Status     string    `json:"status"`
	Lifecycle  string    `json:"lifecycle"`
	CreatedAt  time.Time `json:"created_at"`
}

type Version struct {
	ID        uuid.UUID `json:"id"`
	AssetID   uuid.UUID `json:"asset_id"`
	Version   int       `json:"version"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

type Source struct {
	ID          uuid.UUID  `json:"id"`
	Origin      string     `json:"origin"`
	StorageKey  *string    `json:"storage_key,omitempty"`
	SizeBytes   *int64     `json:"size_bytes,omitempty"`
	ContentType *string    `json:"content_type,omitempty"`
	VerifiedAt  *time.Time `json:"verified_at,omitempty"`
}

type Store struct{ pool *pgxpool.Pool }

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// Create makes an asset and its first version in one transaction. An asset
// without a version is not useful, and a half-created pair would have to be
// cleaned up by something.
func (s *Store) Create(ctx context.Context, tenantID uuid.UUID, externalID, name *string, lifecycle string) (Asset, Version, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Asset{}, Version{}, err
	}
	defer tx.Rollback(ctx)

	a := Asset{
		ID: uuid.Must(uuid.NewV7()), ExternalID: externalID, Name: name,
		Status: "draft", Lifecycle: lifecycle,
	}
	err = tx.QueryRow(ctx, `
		insert into assets (id, tenant_id, external_id, name, status, lifecycle)
		values ($1, $2, $3, $4, $5, $6) returning created_at`,
		a.ID, tenantID, externalID, name, a.Status, lifecycle).Scan(&a.CreatedAt)
	if isUniqueViolation(err) {
		return Asset{}, Version{}, ErrConflict
	}
	if err != nil {
		return Asset{}, Version{}, fmt.Errorf("create asset: %w", err)
	}

	v := Version{ID: uuid.Must(uuid.NewV7()), AssetID: a.ID, Version: 1, Status: "pending_source"}
	err = tx.QueryRow(ctx, `
		insert into asset_versions (id, tenant_id, asset_id, version, status)
		values ($1, $2, $3, $4, $5) returning created_at`,
		v.ID, tenantID, a.ID, v.Version, v.Status).Scan(&v.CreatedAt)
	if err != nil {
		return Asset{}, Version{}, fmt.Errorf("create asset version: %w", err)
	}

	return a, v, tx.Commit(ctx)
}

// Get is always tenant-scoped: the tenant comes from the authenticated
// principal, never from the request, so a known id from another tenant is
// indistinguishable from one that does not exist.
func (s *Store) Get(ctx context.Context, tenantID, id uuid.UUID) (Asset, error) {
	var a Asset
	err := s.pool.QueryRow(ctx, `
		select id, external_id, name, status, lifecycle, created_at
		  from assets where id = $1 and tenant_id = $2 and deleted_at is null`,
		id, tenantID).Scan(&a.ID, &a.ExternalID, &a.Name, &a.Status, &a.Lifecycle, &a.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Asset{}, ErrNotFound
	}
	return a, err
}

func (s *Store) List(ctx context.Context, tenantID uuid.UUID, limit int) ([]Asset, error) {
	rows, err := s.pool.Query(ctx, `
		select id, external_id, name, status, lifecycle, created_at
		  from assets where tenant_id = $1 and deleted_at is null
		 order by created_at desc limit $2`, tenantID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	assets := []Asset{}
	for rows.Next() {
		var a Asset
		if err := rows.Scan(&a.ID, &a.ExternalID, &a.Name, &a.Status, &a.Lifecycle, &a.CreatedAt); err != nil {
			return nil, err
		}
		assets = append(assets, a)
	}
	return assets, rows.Err()
}

func (s *Store) Versions(ctx context.Context, tenantID, assetID uuid.UUID) ([]Version, error) {
	rows, err := s.pool.Query(ctx, `
		select id, asset_id, version, status, created_at
		  from asset_versions where tenant_id = $1 and asset_id = $2 order by version`,
		tenantID, assetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	versions := []Version{}
	for rows.Next() {
		var v Version
		if err := rows.Scan(&v.ID, &v.AssetID, &v.Version, &v.Status, &v.CreatedAt); err != nil {
			return nil, err
		}
		versions = append(versions, v)
	}
	return versions, rows.Err()
}

func (s *Store) GetVersion(ctx context.Context, tenantID, versionID uuid.UUID) (Version, error) {
	var v Version
	err := s.pool.QueryRow(ctx, `
		select id, asset_id, version, status, created_at
		  from asset_versions where id = $1 and tenant_id = $2`,
		versionID, tenantID).Scan(&v.ID, &v.AssetID, &v.Version, &v.Status, &v.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Version{}, ErrNotFound
	}
	return v, err
}

// Source returns the verified source for a version, or ErrNotFound. Callers
// about to schedule work must check VerifiedAt rather than mere existence.
func (s *Store) Source(ctx context.Context, tenantID, versionID uuid.UUID) (Source, error) {
	var src Source
	err := s.pool.QueryRow(ctx, `
		select id, origin, storage_key, size_bytes, content_type, verified_at
		  from source_files where asset_version_id = $1 and tenant_id = $2`,
		versionID, tenantID).Scan(&src.ID, &src.Origin, &src.StorageKey,
		&src.SizeBytes, &src.ContentType, &src.VerifiedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Source{}, ErrNotFound
	}
	return src, err
}

func isUniqueViolation(err error) bool {
	var pgErr interface{ SQLState() string }
	return errors.As(err, &pgErr) && pgErr.SQLState() == "23505"
}
