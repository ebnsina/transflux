package upload

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"testing"

	"github.com/ebnsina/transflux/internal/asset"
	"github.com/ebnsina/transflux/internal/db"
	"github.com/ebnsina/transflux/internal/storage"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type fixture struct {
	svc    *Service
	assets *asset.Store
	pool   *pgxpool.Pool
	tenant uuid.UUID
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dbURL := os.Getenv("TRANSFLUX_TEST_DATABASE_URL")
	s3 := os.Getenv("TRANSFLUX_TEST_S3_ENDPOINT")
	if dbURL == "" || s3 == "" {
		t.Skip("set TRANSFLUX_TEST_DATABASE_URL and TRANSFLUX_TEST_S3_ENDPOINT")
	}
	ctx := context.Background()

	pool, err := db.Open(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := db.Migrate(ctx, pool, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}

	store, err := storage.NewS3(ctx, storage.Config{
		Endpoint: s3, Region: "us-east-1", Bucket: "transflux-test",
		AccessKey: os.Getenv("TRANSFLUX_TEST_S3_ACCESS_KEY"),
		SecretKey: os.Getenv("TRANSFLUX_TEST_S3_SECRET_KEY"),
		PathStyle: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Verify(ctx, true); err != nil {
		t.Fatal(err)
	}

	f := &fixture{svc: NewService(pool, store), assets: asset.NewStore(pool), pool: pool}
	f.tenant = f.newTenant(t)
	return f
}

func (f *fixture) newTenant(t *testing.T) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	_, err := f.pool.Exec(context.Background(),
		`insert into tenants (id, name, status) values ($1, $2, 'active')`, id, "t-"+id.String())
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *fixture) newVersion(t *testing.T, tenant uuid.UUID) uuid.UUID {
	t.Helper()
	_, v, err := f.assets.Create(context.Background(), tenant, nil, nil, "managed")
	if err != nil {
		t.Fatal(err)
	}
	return v.ID
}

func putPart(t *testing.T, url string, data []byte) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPut, url, bytes.NewReader(data))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("part upload returned %d", resp.StatusCode)
	}
}

// The slice's central property: an interrupted upload resumes, and nothing is
// marked usable until every byte is accounted for.
func TestInterruptedUploadResumes(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	versionID := f.newVersion(t, f.tenant)

	// Two parts: one full, one short tail.
	body := make([]byte, minPartSize+4096)
	rand.Read(body)

	up, parts, err := f.svc.Create(ctx, f.tenant, versionID, int64(len(body)), "video/mp4", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if up.PartCount != 2 || len(parts) != 2 {
		t.Fatalf("part count = %d with %d urls, want 2 and 2", up.PartCount, len(parts))
	}

	// No source exists yet, so nothing downstream may run.
	if _, err := f.assets.Source(ctx, f.tenant, versionID); !errors.Is(err, asset.ErrNotFound) {
		t.Fatalf("a source existed before the upload completed: %v", err)
	}

	// The client uploads part 1 and then dies.
	putPart(t, parts[0].URL, body[:minPartSize])

	// Completing now must fail: the object would be truncated.
	if _, err := f.svc.Complete(ctx, f.tenant, up.ID); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("complete with a missing part returned %v, want ErrIncomplete", err)
	}

	// Resume: ask what is missing and get URLs for exactly that.
	st, err := f.svc.Status(ctx, f.tenant, up.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Uploaded) != 1 || st.Uploaded[0] != 1 {
		t.Errorf("uploaded = %v, want [1]", st.Uploaded)
	}
	if len(st.Missing) != 1 || st.Missing[0] != 2 {
		t.Fatalf("missing = %v, want [2]", st.Missing)
	}
	if len(st.Parts) != 1 || st.Parts[0].Number != 2 {
		t.Fatalf("resume urls = %+v, want one for part 2", st.Parts)
	}

	putPart(t, st.Parts[0].URL, body[minPartSize:])

	if _, err := f.svc.Complete(ctx, f.tenant, up.ID); err != nil {
		t.Fatal(err)
	}

	// Only now is there a verified source.
	src, err := f.assets.Source(ctx, f.tenant, versionID)
	if err != nil {
		t.Fatal(err)
	}
	if src.VerifiedAt == nil {
		t.Error("source is not marked verified")
	}
	if src.SizeBytes == nil || *src.SizeBytes != int64(len(body)) {
		t.Errorf("source size = %v, want %d", src.SizeBytes, len(body))
	}

	v, err := f.assets.GetVersion(ctx, f.tenant, versionID)
	if err != nil {
		t.Fatal(err)
	}
	if v.Status != "source_ready" {
		t.Errorf("version status = %q, want source_ready", v.Status)
	}

	// Completion is idempotent: a retried request after a dropped response must
	// not be an error.
	if _, err := f.svc.Complete(ctx, f.tenant, up.ID); err != nil {
		t.Errorf("second complete returned %v, want nil", err)
	}
}

// A client that declares one size and uploads another must be rejected, or the
// pipeline would run on a truncated file.
func TestCompleteRejectsSizeMismatch(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	versionID := f.newVersion(t, f.tenant)

	body := make([]byte, minPartSize+4096)
	rand.Read(body)

	// Declare the true size, then upload a short tail.
	up, parts, err := f.svc.Create(ctx, f.tenant, versionID, int64(len(body)), "video/mp4", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	putPart(t, parts[0].URL, body[:minPartSize])
	putPart(t, parts[1].URL, body[minPartSize:minPartSize+100])

	if _, err := f.svc.Complete(ctx, f.tenant, up.ID); !errors.Is(err, ErrSizeMismatch) {
		t.Fatalf("complete returned %v, want ErrSizeMismatch", err)
	}
	// And no source may have been recorded.
	if _, err := f.assets.Source(ctx, f.tenant, versionID); !errors.Is(err, asset.ErrNotFound) {
		t.Error("a source was recorded for a mismatched upload")
	}
}

func TestUploadsAreTenantScoped(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	other := f.newTenant(t)

	versionID := f.newVersion(t, f.tenant)
	up, _, err := f.svc.Create(ctx, f.tenant, versionID, 1024, "video/mp4", "", nil)
	if err != nil {
		t.Fatal(err)
	}

	// Knowing the id must not be enough.
	if _, err := f.svc.Status(ctx, other, up.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("status across tenants returned %v, want ErrNotFound", err)
	}
	if _, err := f.svc.Complete(ctx, other, up.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("complete across tenants returned %v, want ErrNotFound", err)
	}
	if err := f.svc.Abort(ctx, other, up.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("abort across tenants returned %v, want ErrNotFound", err)
	}
	// The owner is unaffected.
	if _, err := f.svc.Status(ctx, f.tenant, up.ID); err != nil {
		t.Errorf("owner lost access to their own upload: %v", err)
	}
}

func TestAbortLeavesNothingBehind(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	versionID := f.newVersion(t, f.tenant)

	up, _, err := f.svc.Create(ctx, f.tenant, versionID, 4096, "video/mp4", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Abort(ctx, f.tenant, up.ID); err != nil {
		t.Fatal(err)
	}
	// An aborted upload cannot then be completed.
	if _, err := f.svc.Complete(ctx, f.tenant, up.ID); !errors.Is(err, ErrNotOpen) {
		t.Errorf("complete after abort returned %v, want ErrNotOpen", err)
	}
	if err := f.svc.Abort(ctx, f.tenant, up.ID); !errors.Is(err, ErrNotOpen) {
		t.Errorf("second abort returned %v, want ErrNotOpen", err)
	}
}
