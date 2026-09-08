// Package upload runs resumable uploads on top of the storage provider's
// multipart API. Bytes go straight from the client to object storage via
// presigned URLs; the control plane only tracks and verifies.
package upload

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ebnsina/transflux/internal/storage"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound     = errors.New("upload not found")
	ErrIncomplete   = errors.New("upload is missing parts")
	ErrSizeMismatch = errors.New("uploaded size does not match the declared size")
	ErrTooLarge     = errors.New("declared size exceeds the maximum")
	ErrBadSize      = errors.New("declared size must be positive")
	ErrNotOpen      = errors.New("upload is not in progress")
)

const (
	// S3 requires every part except the last to be at least 5MiB, and allows at
	// most 10000 parts. 8MiB keeps round trips reasonable for ordinary files.
	minPartSize = 8 << 20
	maxParts    = 10000
	// The provider ceiling for a single object.
	MaxUploadBytes = 5 << 40
	// How many presigned URLs to hand out at once. A client with thousands of
	// parts re-asks as it progresses, which also refreshes expiring URLs.
	maxURLsPerResponse = 100

	urlTTL    = time.Hour
	uploadTTL = 24 * time.Hour
)

type Upload struct {
	ID             uuid.UUID `json:"id"`
	AssetVersionID uuid.UUID `json:"asset_version_id"`
	SizeBytes      int64     `json:"size_bytes"`
	PartSize       int       `json:"part_size"`
	PartCount      int       `json:"part_count"`
	Status         string    `json:"status"`
	ExpiresAt      time.Time `json:"expires_at"`

	storageKey      string
	storageUploadID string
}

// PartURL is a presigned target for one part. The client PUTs the byte range
// [(Number-1)*PartSize, Number*PartSize) to URL.
type PartURL struct {
	Number int32  `json:"number"`
	URL    string `json:"url"`
}

// Status is what a client needs to resume: what we already hold, and where to
// send what we do not.
type Status struct {
	Upload
	Uploaded []int32   `json:"uploaded_parts"`
	Missing  []int32   `json:"missing_parts"`
	Parts    []PartURL `json:"parts"`
}

type Service struct {
	pool  *pgxpool.Pool
	store storage.Store
}

func NewService(pool *pgxpool.Pool, store storage.Store) *Service {
	return &Service{pool: pool, store: store}
}

// PartSizeFor keeps the part count within the provider limit for any legal
// object size, rather than failing on large files with a fixed part size.
func PartSizeFor(size int64) int {
	part := int64(minPartSize)
	for (size+part-1)/part > maxParts {
		part *= 2
	}
	return int(part)
}

func (s *Service) Create(ctx context.Context, tenantID, versionID uuid.UUID,
	size int64, contentType string, checksumAlgo string, checksum []byte) (Upload, []PartURL, error) {

	switch {
	case size <= 0:
		return Upload{}, nil, ErrBadSize
	case size > MaxUploadBytes:
		return Upload{}, nil, ErrTooLarge
	}

	partSize := PartSizeFor(size)
	u := Upload{
		ID:             uuid.Must(uuid.NewV7()),
		AssetVersionID: versionID,
		SizeBytes:      size,
		PartSize:       partSize,
		PartCount:      int((size + int64(partSize) - 1) / int64(partSize)),
		Status:         "in_progress",
		ExpiresAt:      time.Now().Add(uploadTTL),
		storageKey:     storage.UploadKey(tenantID, uuid.Must(uuid.NewV7())),
	}

	uploadID, err := s.store.CreateMultipart(ctx, u.storageKey, contentType)
	if err != nil {
		return Upload{}, nil, fmt.Errorf("create multipart: %w", err)
	}
	u.storageUploadID = uploadID

	_, err = s.pool.Exec(ctx, `
		insert into uploads (id, tenant_id, asset_version_id, storage_key, storage_upload_id,
		                     size_bytes, part_size, part_count, content_type,
		                     checksum_algo, checksum, status, expires_at)
		values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,'in_progress',$12)`,
		u.ID, tenantID, versionID, u.storageKey, u.storageUploadID,
		u.SizeBytes, u.PartSize, u.PartCount, nullable(contentType),
		nullable(checksumAlgo), checksum, u.ExpiresAt)
	if err != nil {
		// Do not leave a multipart upload accruing storage cost for a row that
		// does not exist.
		_ = s.store.AbortMultipart(context.WithoutCancel(ctx), u.storageKey, u.storageUploadID)
		return Upload{}, nil, fmt.Errorf("record upload: %w", err)
	}

	urls, err := s.presign(ctx, u, allParts(u.PartCount))
	return u, urls, err
}

// Status reports progress. The provider's ListParts is the authority, not any
// record of our own: a client can upload a part and die before telling us.
func (s *Service) Status(ctx context.Context, tenantID, uploadID uuid.UUID) (Status, error) {
	u, err := s.get(ctx, tenantID, uploadID)
	if err != nil {
		return Status{}, err
	}

	st := Status{Upload: u, Uploaded: []int32{}, Missing: []int32{}, Parts: []PartURL{}}
	if u.Status != "in_progress" {
		return st, nil
	}

	held, err := s.store.ListParts(ctx, u.storageKey, u.storageUploadID)
	if err != nil {
		return Status{}, fmt.Errorf("list parts: %w", err)
	}
	have := make(map[int32]bool, len(held))
	for _, p := range held {
		have[p.Number] = true
		st.Uploaded = append(st.Uploaded, p.Number)
	}
	for n := int32(1); n <= int32(u.PartCount); n++ {
		if !have[n] {
			st.Missing = append(st.Missing, n)
		}
	}

	st.Parts, err = s.presign(ctx, u, st.Missing)
	return st, err
}

// Complete verifies the upload and promotes it to the asset version's source.
// Nothing downstream may run until this has succeeded.
func (s *Service) Complete(ctx context.Context, tenantID, uploadID uuid.UUID) (Upload, error) {
	u, err := s.get(ctx, tenantID, uploadID)
	if err != nil {
		return Upload{}, err
	}
	// Completion is idempotent: a client that retries after a dropped response
	// must not get an error for work that already succeeded.
	if u.Status == "completed" {
		return u, nil
	}
	if u.Status != "in_progress" {
		return Upload{}, ErrNotOpen
	}

	held, err := s.store.ListParts(ctx, u.storageKey, u.storageUploadID)
	if err != nil {
		return Upload{}, fmt.Errorf("list parts: %w", err)
	}
	parts, err := verifyParts(held, u.PartCount, u.PartSize, u.SizeBytes)
	if err != nil {
		return Upload{}, err
	}

	info, err := s.store.CompleteMultipart(ctx, u.storageKey, u.storageUploadID, parts)
	if err != nil {
		return Upload{}, fmt.Errorf("complete multipart: %w", err)
	}
	// Trust the assembled object, not arithmetic over what the client claimed.
	if info.Size != u.SizeBytes {
		return Upload{}, fmt.Errorf("%w: stored %d, declared %d",
			ErrSizeMismatch, info.Size, u.SizeBytes)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Upload{}, err
	}
	defer tx.Rollback(ctx)

	// verified_at is set in the same transaction that records the source, so a
	// source row can never exist in an ambiguous state.
	_, err = tx.Exec(ctx, `
		insert into source_files (id, tenant_id, asset_version_id, origin, storage_key,
		                          size_bytes, content_type, checksum_algo, checksum, verified_at)
		select $1, $2, u.asset_version_id, 'managed', u.storage_key,
		       $3, u.content_type, u.checksum_algo, u.checksum, now()
		  from uploads u where u.id = $4`,
		uuid.Must(uuid.NewV7()), tenantID, info.Size, u.ID)
	if err != nil {
		return Upload{}, fmt.Errorf("record source: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`update uploads set status = 'completed', completed_at = now() where id = $1`, u.ID); err != nil {
		return Upload{}, err
	}
	if _, err := tx.Exec(ctx,
		`update asset_versions set status = 'source_ready' where id = $1`, u.AssetVersionID); err != nil {
		return Upload{}, err
	}
	if _, err := tx.Exec(ctx,
		`update assets set status = 'ready' where id = (
			select asset_id from asset_versions where id = $1)`, u.AssetVersionID); err != nil {
		return Upload{}, err
	}

	u.Status = "completed"
	return u, tx.Commit(ctx)
}

func (s *Service) Abort(ctx context.Context, tenantID, uploadID uuid.UUID) error {
	u, err := s.get(ctx, tenantID, uploadID)
	if err != nil {
		return err
	}
	if u.Status != "in_progress" {
		return ErrNotOpen
	}
	if err := s.store.AbortMultipart(ctx, u.storageKey, u.storageUploadID); err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `update uploads set status = 'aborted' where id = $1`, u.ID)
	return err
}

func (s *Service) get(ctx context.Context, tenantID, uploadID uuid.UUID) (Upload, error) {
	var u Upload
	err := s.pool.QueryRow(ctx, `
		select id, asset_version_id, storage_key, storage_upload_id,
		       size_bytes, part_size, part_count, status, expires_at
		  from uploads where id = $1 and tenant_id = $2`, uploadID, tenantID,
	).Scan(&u.ID, &u.AssetVersionID, &u.storageKey, &u.storageUploadID,
		&u.SizeBytes, &u.PartSize, &u.PartCount, &u.Status, &u.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Upload{}, ErrNotFound
	}
	return u, err
}

func (s *Service) presign(ctx context.Context, u Upload, numbers []int32) ([]PartURL, error) {
	if len(numbers) > maxURLsPerResponse {
		numbers = numbers[:maxURLsPerResponse]
	}
	urls := make([]PartURL, 0, len(numbers))
	for _, n := range numbers {
		url, err := s.store.PresignUploadPart(ctx, u.storageKey, u.storageUploadID, n, urlTTL)
		if err != nil {
			return nil, fmt.Errorf("presign part %d: %w", n, err)
		}
		urls = append(urls, PartURL{Number: n, URL: url})
	}
	return urls, nil
}

// verifyParts checks completeness and shape before assembly. A short part in
// the middle would silently corrupt the object, and the provider will not
// catch it.
func verifyParts(held []storage.Part, count, partSize int, size int64) ([]storage.Part, error) {
	byNumber := make(map[int32]storage.Part, len(held))
	for _, p := range held {
		byNumber[p.Number] = p
	}

	parts := make([]storage.Part, 0, count)
	var missing []int32
	for n := int32(1); n <= int32(count); n++ {
		p, ok := byNumber[n]
		if !ok {
			missing = append(missing, n)
			continue
		}
		want := int64(partSize)
		if int(n) == count {
			want = size - int64(partSize)*int64(count-1)
		}
		if p.Size != want {
			return nil, fmt.Errorf("%w: part %d is %d bytes, expected %d",
				ErrSizeMismatch, n, p.Size, want)
		}
		parts = append(parts, p)
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("%w: %v", ErrIncomplete, missing)
	}
	return parts, nil
}

func allParts(n int) []int32 {
	out := make([]int32, 0, n)
	for i := int32(1); i <= int32(n); i++ {
		out = append(out, i)
	}
	return out
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
