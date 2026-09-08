package artifact

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"

	"github.com/ebnsina/transflux/internal/asset"
	"github.com/ebnsina/transflux/internal/db"
	"github.com/ebnsina/transflux/internal/job"
	"github.com/ebnsina/transflux/internal/storage"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type fixture struct {
	store   *Store
	jobs    *job.Store
	pool    *pgxpool.Pool
	s3      *storage.S3Store
	tenant  uuid.UUID
	jobID   uuid.UUID
	attempt uuid.UUID
	worker  uuid.UUID
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dbURL := os.Getenv("TRANSFLUX_TEST_DATABASE_URL")
	s3URL := os.Getenv("TRANSFLUX_TEST_S3_ENDPOINT")
	if dbURL == "" || s3URL == "" {
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

	s3, err := storage.NewS3(ctx, storage.Config{
		Endpoint: s3URL, Region: "us-east-1", Bucket: "transflux-test",
		AccessKey: os.Getenv("TRANSFLUX_TEST_S3_ACCESS_KEY"),
		SecretKey: os.Getenv("TRANSFLUX_TEST_S3_SECRET_KEY"),
		PathStyle: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s3.Verify(ctx, true); err != nil {
		t.Fatal(err)
	}

	f := &fixture{store: NewStore(pool, s3), jobs: job.NewStore(pool), pool: pool, s3: s3}
	f.tenant = uuid.Must(uuid.NewV7())
	if _, err := pool.Exec(ctx,
		`insert into tenants (id, name, status) values ($1, $2, 'active')`,
		f.tenant, "t-"+f.tenant.String()); err != nil {
		t.Fatal(err)
	}

	_, version, err := asset.NewStore(pool).Create(ctx, f.tenant, nil, nil, "managed")
	if err != nil {
		t.Fatal(err)
	}

	pipeID, pipeVer := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	pool.Exec(ctx, `insert into pipelines (id, tenant_id, name) values ($1,$2,$3)`,
		pipeID, f.tenant, "p-"+pipeID.String())
	pool.Exec(ctx, `insert into pipeline_versions (id, tenant_id, pipeline_id, version, definition)
		values ($1,$2,$3,1,'{}')`, pipeVer, f.tenant, pipeID)

	j, err := f.jobs.Create(ctx, f.tenant, version.ID, pipeVer, "a-"+uuid.NewString(), 100,
		[]job.NewTask{{Key: "encode", Operation: "encode"}})
	if err != nil {
		t.Fatal(err)
	}
	f.jobID = j.ID

	f.worker = uuid.Must(uuid.NewV7())
	credential := []byte("cred-" + f.worker.String())
	if _, err := pool.Exec(ctx, `
		insert into workers (id, name, hostname, os, arch, cpu_cores, memory_bytes,
		                     disk_bytes, ffmpeg_version, protocol_version, state, credential_hash)
		values ($1,$2,$3,'linux','arm64',8,1,1,'6.1.2',1,'online',$4)`,
		f.worker, "w-"+f.worker.String(), "h", credential); err != nil {
		t.Fatal(err)
	}

	a, err := f.jobs.Lease(ctx, job.WorkerFacts{
		ID: f.worker, Name: "w", Arch: "arm64", Operations: []string{"encode"},
		MemoryBytes: 1 << 40, DiskFree: 1 << 40, SlotCapacity: map[string]int{"encode": 4},
	}, 60_000_000_000)
	if err != nil {
		t.Fatal(err)
	}
	f.attempt = a.AttemptID
	if err := f.jobs.Start(ctx, f.attempt, f.worker, 60_000_000_000); err != nil {
		t.Fatal(err)
	}
	return f
}

// putObject stores real bytes, because registration checks storage rather than
// trusting the report.
func (f *fixture) putObject(t *testing.T, key string, body []byte) {
	t.Helper()
	if _, err := f.s3.Put(context.Background(), key, bytes.NewReader(body),
		int64(len(body)), "video/mp4"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.s3.Delete(context.Background(), key) })
}

func (f *fixture) registration(key, label string, size int64) Registration {
	return Registration{
		Kind: "rendition", Label: label, StorageKey: key, SizeBytes: size,
		ChecksumAlgo: "sha256", Checksum: []byte("0123456789abcdef0123456789abcdef"),
		Media: json.RawMessage(`{"codec":"h264","width":1280,"height":720}`),
	}
}

func TestRegisterAndList(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	key := "test/" + uuid.NewString()
	body := []byte("pretend this is an encoded rendition")
	f.putObject(t, key, body)

	a, err := f.store.Register(ctx, f.tenant, f.jobID, f.attempt,
		f.registration(key, "720p_h264", int64(len(body))))
	if err != nil {
		t.Fatal(err)
	}
	if a.SizeBytes != int64(len(body)) {
		t.Errorf("size = %d, want %d", a.SizeBytes, len(body))
	}

	sets, err := f.store.Sets(ctx, f.tenant, f.jobID)
	if err != nil {
		t.Fatal(err)
	}
	if len(sets) != 1 || len(sets[0].Artifacts) != 1 {
		t.Fatalf("got %d sets; want one holding one artifact", len(sets))
	}
	// A set is incomplete until its job is done, so nothing should be
	// delivered from it yet.
	if sets[0].State != "building" {
		t.Errorf("set state = %s, want building", sets[0].State)
	}
	if sets[0].Version != 1 {
		t.Errorf("set version = %d, want 1", sets[0].Version)
	}
}

// A worker that retried after a dropped response must not get a conflict for
// work that already succeeded.
func TestRegisterIsIdempotent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	key := "test/" + uuid.NewString()
	f.putObject(t, key, []byte("output"))

	first, err := f.store.Register(ctx, f.tenant, f.jobID, f.attempt,
		f.registration(key, "720p_h264", 6))
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.store.Register(ctx, f.tenant, f.jobID, f.attempt,
		f.registration(key, "720p_h264", 6))
	if err != nil {
		t.Fatalf("a repeated registration was refused: %v", err)
	}
	if first.ID != second.ID {
		t.Errorf("a retry created a second artifact: %v then %v", first.ID, second.ID)
	}

	sets, _ := f.store.Sets(ctx, f.tenant, f.jobID)
	if len(sets[0].Artifacts) != 1 {
		t.Errorf("the set holds %d artifacts, want 1", len(sets[0].Artifacts))
	}
}

// Artifacts are immutable: two different outputs must never share one name.
func TestRegisterRefusesToOverwriteALabel(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	first := "test/" + uuid.NewString()
	second := "test/" + uuid.NewString()
	f.putObject(t, first, []byte("original"))
	f.putObject(t, second, []byte("different"))

	if _, err := f.store.Register(ctx, f.tenant, f.jobID, f.attempt,
		f.registration(first, "720p_h264", 8)); err != nil {
		t.Fatal(err)
	}
	_, err := f.store.Register(ctx, f.tenant, f.jobID, f.attempt,
		f.registration(second, "720p_h264", 9))
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("overwriting a label returned %v, want ErrConflict", err)
	}

	// The original must be untouched.
	sets, _ := f.store.Sets(ctx, f.tenant, f.jobID)
	if got := sets[0].Artifacts[0].StorageKey; got != first {
		t.Errorf("the artifact now points at %q, want the original %q", got, first)
	}
}

// A worker reporting an object it never uploaded would leave a record pointing
// at nothing, and the failure would surface later as a broken download.
func TestRegisterRequiresTheObjectToExist(t *testing.T) {
	f := newFixture(t)
	_, err := f.store.Register(context.Background(), f.tenant, f.jobID, f.attempt,
		f.registration("test/never-uploaded-"+uuid.NewString(), "720p_h264", 100))
	if !errors.Is(err, ErrMissingObject) {
		t.Errorf("registering a missing object returned %v, want ErrMissingObject", err)
	}
}

// Storage is believed over the report: a truncated upload the worker thought
// succeeded must not become an artifact.
func TestRegisterRefusesASizeMismatch(t *testing.T) {
	f := newFixture(t)
	key := "test/" + uuid.NewString()
	f.putObject(t, key, []byte("only twelve"))

	_, err := f.store.Register(context.Background(), f.tenant, f.jobID, f.attempt,
		f.registration(key, "720p_h264", 999999))
	if !errors.Is(err, ErrSizeMismatch) {
		t.Errorf("a size mismatch returned %v, want ErrSizeMismatch", err)
	}
}

func TestRegisterRejectsUnsafeLabels(t *testing.T) {
	f := newFixture(t)
	key := "test/" + uuid.NewString()
	f.putObject(t, key, []byte("x"))

	for _, label := range []string{"../escape", "a/b", "", "with space"} {
		if _, err := f.store.Register(context.Background(), f.tenant, f.jobID, f.attempt,
			f.registration(key, label, 1)); !errors.Is(err, storage.ErrBadKey) {
			t.Errorf("label %q returned %v, want ErrBadKey", label, err)
		}
	}
}

func TestSetLifecycle(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	key := "test/" + uuid.NewString()
	f.putObject(t, key, []byte("output"))
	if _, err := f.store.Register(ctx, f.tenant, f.jobID, f.attempt,
		f.registration(key, "720p_h264", 6)); err != nil {
		t.Fatal(err)
	}

	if err := f.store.MarkComplete(ctx, f.tenant, f.jobID); err != nil {
		t.Fatal(err)
	}
	sets, _ := f.store.Sets(ctx, f.tenant, f.jobID)
	if sets[0].State != "complete" || sets[0].CompletedAt == nil {
		t.Errorf("set = %+v, want complete with a timestamp", sets[0])
	}

	// A completed set must not be reopened by a later failure report.
	if err := f.store.MarkFailed(ctx, f.tenant, f.jobID); err != nil {
		t.Fatal(err)
	}
	sets, _ = f.store.Sets(ctx, f.tenant, f.jobID)
	if sets[0].State != "complete" {
		t.Errorf("a completed set was reopened as %s", sets[0].State)
	}
}

func TestArtifactsAreTenantScoped(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	key := "test/" + uuid.NewString()
	f.putObject(t, key, []byte("output"))
	a, err := f.store.Register(ctx, f.tenant, f.jobID, f.attempt,
		f.registration(key, "720p_h264", 6))
	if err != nil {
		t.Fatal(err)
	}

	other := uuid.Must(uuid.NewV7())
	f.pool.Exec(ctx, `insert into tenants (id, name, status) values ($1,$2,'active')`,
		other, "t-"+other.String())

	if _, err := f.store.Get(ctx, other, a.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-tenant get returned %v, want ErrNotFound", err)
	}
	sets, err := f.store.Sets(ctx, other, f.jobID)
	if err != nil {
		t.Fatal(err)
	}
	if len(sets) != 0 {
		t.Errorf("another tenant saw %d artifact sets", len(sets))
	}
	if _, err := f.store.Get(ctx, f.tenant, a.ID); err != nil {
		t.Errorf("the owner lost access to their own artifact: %v", err)
	}
}
