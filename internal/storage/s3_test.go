package storage

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

// testStore talks to a real S3-compatible endpoint. Presigning, multipart and
// ETag semantics cannot be meaningfully faked, so there is no mock here.
func testStore(t *testing.T) *S3Store {
	t.Helper()
	endpoint := os.Getenv("TRANSFLUX_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("set TRANSFLUX_TEST_S3_ENDPOINT to run storage tests")
	}
	s, err := NewS3(context.Background(), Config{
		Endpoint:  endpoint,
		Region:    "us-east-1",
		Bucket:    "transflux-test",
		AccessKey: os.Getenv("TRANSFLUX_TEST_S3_ACCESS_KEY"),
		SecretKey: os.Getenv("TRANSFLUX_TEST_S3_SECRET_KEY"),
		PathStyle: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Verify(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRoundTrip(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	key := "test/" + uuid.Must(uuid.NewV7()).String()
	body := []byte("the quick brown fox")

	if _, err := s.Put(ctx, key, bytes.NewReader(body), int64(len(body)), "text/plain"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Delete(ctx, key) })

	info, err := s.Head(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size != int64(len(body)) {
		t.Errorf("size = %d, want %d", info.Size, len(body))
	}
	if info.ContentType != "text/plain" {
		t.Errorf("content type = %q, want text/plain", info.ContentType)
	}

	r, err := s.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got, _ := io.ReadAll(r)
	if !bytes.Equal(got, body) {
		t.Errorf("body = %q, want %q", got, body)
	}

	if err := s.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	// A missing object is a normal outcome and must be distinguishable.
	if _, err := s.Head(ctx, key); !errors.Is(err, ErrNotFound) {
		t.Errorf("head after delete = %v, want ErrNotFound", err)
	}
	if _, err := s.Get(ctx, key); !errors.Is(err, ErrNotFound) {
		t.Errorf("get after delete = %v, want ErrNotFound", err)
	}
	// Deleting what is already gone must not error: garbage collection retries.
	if err := s.Delete(ctx, key); err != nil {
		t.Errorf("second delete = %v, want nil", err)
	}
}

func TestPresign(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	key := "test/" + uuid.Must(uuid.NewV7()).String()
	body := []byte("presigned payload")
	t.Cleanup(func() { s.Delete(ctx, key) })

	// A client must be able to upload without credentials and without the
	// bytes passing through the control plane.
	putURL, err := s.PresignPut(ctx, key, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPut, putURL, bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("presigned PUT status = %d", resp.StatusCode)
	}

	getURL, err := s.PresignGet(ctx, key, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	resp, err = http.Get(getURL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(got, body) {
		t.Errorf("presigned GET body = %q, want %q", got, body)
	}

	// An expired URL must stop working: this is the whole point of signing.
	expired, err := s.PresignGet(ctx, key, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second)
	resp, err = http.Get(expired)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Error("an expired presigned URL still served the object")
	}
}

func TestMultipartResume(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	key := "test/" + uuid.Must(uuid.NewV7()).String()

	// S3 requires every part except the last to be at least 5MiB.
	const partSize = 5 << 20
	chunk1 := make([]byte, partSize)
	chunk2 := []byte("the tail of the object")
	rand.Read(chunk1)

	uploadID, err := s.CreateMultipart(ctx, key, "video/mp4")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.AbortMultipart(ctx, key, uploadID); s.Delete(ctx, key) })

	upload := func(part int32, data []byte) {
		t.Helper()
		url, err := s.PresignUploadPart(ctx, key, uploadID, part, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		req, _ := http.NewRequest(http.MethodPut, url, bytes.NewReader(data))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("part %d status = %d", part, resp.StatusCode)
		}
	}

	upload(1, chunk1)

	// Simulate an interrupted upload: the client vanished after part 1 and we
	// must discover what is actually stored rather than trust our own record.
	parts, err := s.ListParts(ctx, key, uploadID)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 1 || parts[0].Number != 1 || parts[0].Size != partSize {
		t.Fatalf("after interruption ListParts = %+v, want one 5MiB part", parts)
	}

	upload(2, chunk2)

	parts, err = s.ListParts(ctx, key, uploadID)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 {
		t.Fatalf("ListParts returned %d parts, want 2", len(parts))
	}

	info, err := s.CompleteMultipart(ctx, key, uploadID, parts)
	if err != nil {
		t.Fatal(err)
	}
	want := int64(len(chunk1) + len(chunk2))
	if info.Size != want {
		t.Errorf("assembled size = %d, want %d", info.Size, want)
	}

	r, err := s.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got, _ := io.ReadAll(r)
	if !bytes.Equal(got, append(chunk1, chunk2...)) {
		t.Error("assembled object does not match what was uploaded")
	}
}

func TestAbortMultipart(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	key := "test/" + uuid.Must(uuid.NewV7()).String()

	uploadID, err := s.CreateMultipart(ctx, key, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AbortMultipart(ctx, key, uploadID); err != nil {
		t.Fatal(err)
	}
	// An aborted upload must leave no object behind for garbage collection.
	if _, err := s.Head(ctx, key); !errors.Is(err, ErrNotFound) {
		t.Errorf("head after abort = %v, want ErrNotFound", err)
	}
}
