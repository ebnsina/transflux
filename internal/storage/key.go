package storage

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/google/uuid"
)

// Object keys are always built here from identifiers we generate. A
// customer-supplied filename is metadata, never a path component — that is what
// keeps path traversal out of the storage layer entirely.

// SourceKey locates an asset version's uploaded source.
func SourceKey(tenant, asset, version uuid.UUID) string {
	return fmt.Sprintf("t/%s/assets/%s/versions/%s/source", tenant, asset, version)
}

// ArtifactKey locates one output within an immutable artifact set. The set
// version is in the path, so a re-run writes alongside rather than over.
func ArtifactKey(tenant, asset, job uuid.UUID, setVersion int, label string) (string, error) {
	safe, err := Segment(label)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("t/%s/assets/%s/jobs/%s/v%d/%s", tenant, asset, job, setVersion, safe), nil
}

// JobArtifactKey locates an output of a job. The set version is in the path so
// a re-run writes alongside rather than over: artifacts are immutable.
func JobArtifactKey(tenant, job uuid.UUID, setVersion int, label string) (string, error) {
	safe, err := Segment(label)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("t/%s/jobs/%s/v%d/%s", tenant, job, setVersion, safe), nil
}

// JobOutputKey locates one file of a job's output, named by a path relative to
// the job's own prefix.
//
// Each segment of the path is validated, so a worker cannot climb out of its
// job's area however it names a file. The prefix itself is built from
// identifiers we generate, never from anything a worker sends.
func JobOutputKey(tenant, job uuid.UUID, setVersion int, relative string) (string, error) {
	if relative == "" || len(relative) > 512 {
		return "", fmt.Errorf("%w: path must be 1-512 characters", ErrBadKey)
	}
	parts := strings.Split(relative, "/")
	if len(parts) > 4 {
		return "", fmt.Errorf("%w: path is too deeply nested", ErrBadKey)
	}
	for _, part := range parts {
		if _, err := Segment(part); err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("t/%s/jobs/%s/v%d/%s", tenant, job, setVersion,
		strings.Join(parts, "/")), nil
}

// UploadKey is where a multipart upload accumulates before it is verified and
// promoted to a source. Kept separate so an abandoned upload is trivially
// identifiable by prefix during garbage collection.
func UploadKey(tenant, upload uuid.UUID) string {
	return fmt.Sprintf("t/%s/uploads/%s", tenant, upload)
}

// Segment validates a caller-influenced path component. It rejects rather than
// sanitises: silently rewriting a label would make two different artifacts
// collide on one key.
func Segment(s string) (string, error) {
	if s == "" || len(s) > 128 {
		return "", fmt.Errorf("%w: label must be 1-128 characters", ErrBadKey)
	}
	if s == "." || s == ".." || strings.Contains(s, "/") || strings.Contains(s, `\`) {
		return "", fmt.Errorf("%w: label must not contain a path", ErrBadKey)
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.':
		default:
			if unicode.IsControl(r) {
				return "", fmt.Errorf("%w: label must not contain control characters", ErrBadKey)
			}
			return "", fmt.Errorf("%w: %q is not allowed in a label", ErrBadKey, r)
		}
	}
	return s, nil
}
