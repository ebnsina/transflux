package storage

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestSegmentRejectsTraversal(t *testing.T) {
	bad := []string{
		"", ".", "..", "../etc/passwd", "a/b", `a\b`, "/absolute",
		strings.Repeat("x", 129), "with space", "semi;colon", "null\x00byte",
		"new\nline", "quote'", "star*", "tilde~", "percent%20",
	}
	for _, s := range bad {
		if _, err := Segment(s); !errors.Is(err, ErrBadKey) {
			t.Errorf("Segment(%q) was accepted", s)
		}
	}

	good := []string{"1080p_h264", "hls-master.m3u8", "thumb.0001.jpg", "a", strings.Repeat("x", 128)}
	for _, s := range good {
		if got, err := Segment(s); err != nil || got != s {
			t.Errorf("Segment(%q) = %q, %v; want it accepted unchanged", s, got, err)
		}
	}
}

func TestKeysAreScopedToTenant(t *testing.T) {
	tenant := uuid.Must(uuid.NewV7())
	asset := uuid.Must(uuid.NewV7())
	job := uuid.Must(uuid.NewV7())

	// Every key must start with the tenant, so a prefix listing can never cross
	// a tenant boundary and lifecycle rules can be written per tenant.
	keys := []string{
		SourceKey(tenant, asset, uuid.Must(uuid.NewV7())),
		UploadKey(tenant, uuid.Must(uuid.NewV7())),
	}
	artifact, err := ArtifactKey(tenant, asset, job, 1, "1080p_h264")
	if err != nil {
		t.Fatal(err)
	}
	keys = append(keys, artifact)

	for _, k := range keys {
		if !strings.HasPrefix(k, "t/"+tenant.String()+"/") {
			t.Errorf("key %q is not scoped to the tenant", k)
		}
		if strings.Contains(k, "..") || strings.Contains(k, "//") {
			t.Errorf("key %q is not clean", k)
		}
	}

	// Artifact set versions must not collide: a re-run writes alongside.
	v1, _ := ArtifactKey(tenant, asset, job, 1, "master")
	v2, _ := ArtifactKey(tenant, asset, job, 2, "master")
	if v1 == v2 {
		t.Error("artifact set versions share a key")
	}

	if _, err := ArtifactKey(tenant, asset, job, 1, "../../escape"); !errors.Is(err, ErrBadKey) {
		t.Error("a traversing label produced a key")
	}
}
