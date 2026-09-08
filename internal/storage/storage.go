// Package storage abstracts S3-compatible object storage.
//
// No SDK type crosses this interface (ADR-005), so the implementation stays
// replaceable and the domain never learns which vendor holds the bytes.
package storage

import (
	"context"
	"errors"
	"io"
	"time"
)

var (
	ErrNotFound = errors.New("object not found")
	ErrBadKey   = errors.New("invalid object key")
)

// ObjectInfo is what we know about a stored object without reading it.
type ObjectInfo struct {
	Key         string
	Size        int64
	ETag        string
	ContentType string
	Modified    time.Time
}

// Part is one piece of a multipart upload. ETag comes from the storage
// provider and must be echoed back on completion.
type Part struct {
	Number int32
	ETag   string
	Size   int64
}

// Store is the whole storage surface. Deliberately small: anything larger is a
// vendor feature we would then depend on.
type Store interface {
	Head(ctx context.Context, key string) (ObjectInfo, error)
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	Put(ctx context.Context, key string, body io.Reader, size int64, contentType string) (ObjectInfo, error)
	Delete(ctx context.Context, key string) error

	// Presigned URLs let clients upload and workers fetch without proxying
	// bytes through the control plane.
	PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error)
	PresignPut(ctx context.Context, key string, ttl time.Duration) (string, error)
	// PresignGetForWorker signs for the network workers are on, which is often
	// not the one customers are on.
	PresignGetForWorker(ctx context.Context, key string, ttl time.Duration) (string, error)

	// Multipart is the resumable-upload primitive; we do not reinvent it.
	CreateMultipart(ctx context.Context, key, contentType string) (uploadID string, err error)
	PresignUploadPart(ctx context.Context, key, uploadID string, part int32, ttl time.Duration) (string, error)
	ListParts(ctx context.Context, key, uploadID string) ([]Part, error)
	CompleteMultipart(ctx context.Context, key, uploadID string, parts []Part) (ObjectInfo, error)
	AbortMultipart(ctx context.Context, key, uploadID string) error
}
