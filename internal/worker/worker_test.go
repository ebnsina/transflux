package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/ebnsina/transflux/internal/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const bootstrapToken = "test-bootstrap-token"

func testStore(t *testing.T) (*Store, *pgxpool.Pool) {
	t.Helper()
	url := os.Getenv("TRANSFLUX_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TRANSFLUX_TEST_DATABASE_URL to run integration tests")
	}
	ctx := context.Background()

	pool, err := db.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := db.Migrate(ctx, pool, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	return NewStore(pool, bootstrapToken), pool
}

func registration(name string) Registration {
	return Registration{
		Name: name, Hostname: name + ".local", OS: "linux", Arch: "arm64",
		CPUCores: 16, MemoryBytes: 32 << 30, DiskBytes: 500 << 30,
		FFmpegVersion: "9.0.1", ProtocolVersion: ProtocolVersion,
		Capabilities: Capabilities{
			Operations: []string{"probe", "encode"},
			Encoders:   []string{"libx264", "libx265"},
		},
		SlotCapacity: map[string]int{"h264": 4, "hevc": 2, "probe": 8},
	}
}

func TestRegisterAndAuthenticate(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	name := "w-" + uuid.NewString()

	w, credential, err := s.Register(ctx, bootstrapToken, registration(name))
	if err != nil {
		t.Fatal(err)
	}
	if w.State != StateOnline {
		t.Errorf("state = %s, want online", w.State)
	}

	got, err := s.Authenticate(ctx, credential)
	if err != nil {
		t.Fatalf("worker credential rejected: %v", err)
	}
	if got.ID != w.ID {
		t.Errorf("credential resolved to %v, want %v", got.ID, w.ID)
	}

	// Capabilities survive the round trip: the scheduler gates on these, so a
	// silently empty capability set would make a worker unusable.
	full, err := s.Get(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(full.Capabilities.Encoders) != 2 || full.Capabilities.Encoders[0] != "libx264" {
		t.Errorf("encoders = %v, want the declared pair", full.Capabilities.Encoders)
	}
	if full.SlotCapacity["h264"] != 4 || full.SlotCapacity["av1"] != 0 {
		t.Errorf("slot capacity = %v, want the declared map", full.SlotCapacity)
	}
}

func TestRegistrationRejections(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()

	if _, _, err := s.Register(ctx, "wrong-token", registration("w-"+uuid.NewString())); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("bad bootstrap token returned %v, want ErrUnauthorized", err)
	}
	if _, _, err := s.Register(ctx, "", registration("w-"+uuid.NewString())); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("empty bootstrap token returned %v, want ErrUnauthorized", err)
	}

	// A worker speaking a protocol we do not support must be told at
	// registration, not discovered mid-task.
	future := registration("w-" + uuid.NewString())
	future.ProtocolVersion = ProtocolVersion + 1
	if _, _, err := s.Register(ctx, bootstrapToken, future); !errors.Is(err, ErrBadProtocol) {
		t.Errorf("future protocol returned %v, want ErrBadProtocol", err)
	}

	unnamed := registration("   ")
	if _, _, err := s.Register(ctx, bootstrapToken, unnamed); err == nil {
		t.Error("a worker with no name was accepted")
	}
}

func TestCredentialRejections(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	_, credential, err := s.Register(ctx, bootstrapToken, registration("w-"+uuid.NewString()))
	if err != nil {
		t.Fatal(err)
	}

	for _, bad := range []string{"", "nope", "tfw_", credential + "x", credential[:len(credential)-1]} {
		if _, err := s.Authenticate(ctx, bad); !errors.Is(err, ErrUnauthorized) {
			t.Errorf("credential %q returned %v, want ErrUnauthorized", bad, err)
		}
	}
	// The bootstrap token must not work as a worker credential: it is shared
	// across the fleet and only ever authenticates registration.
	if _, err := s.Authenticate(ctx, bootstrapToken); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("the bootstrap token authenticated as a worker: %v", err)
	}
}

// A restarting worker must keep its identity, or the fleet list grows a row on
// every deploy. Its credential rotates, so a leaked one dies with the restart.
func TestReregisterKeepsIdentityAndRotatesCredential(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	name := "w-" + uuid.NewString()

	first, credential1, err := s.Register(ctx, bootstrapToken, registration(name))
	if err != nil {
		t.Fatal(err)
	}

	// It comes back with more capability than before, as after an upgrade.
	upgraded := registration(name)
	upgraded.Capabilities.Encoders = append(upgraded.Capabilities.Encoders, "libsvtav1")
	upgraded.SlotCapacity["av1"] = 1

	second, credential2, err := s.Register(ctx, bootstrapToken, upgraded)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID {
		t.Fatalf("re-registration created a new worker: %v then %v", first.ID, second.ID)
	}
	if credential1 == credential2 {
		t.Error("the credential was not rotated on re-registration")
	}
	if _, err := s.Authenticate(ctx, credential1); !errors.Is(err, ErrUnauthorized) {
		t.Error("the old credential still works after re-registration")
	}
	if _, err := s.Authenticate(ctx, credential2); err != nil {
		t.Errorf("the new credential does not work: %v", err)
	}

	full, _ := s.Get(ctx, second.ID)
	if len(full.Capabilities.Encoders) != 3 || full.SlotCapacity["av1"] != 1 {
		t.Errorf("upgraded capabilities were not recorded: %+v", full.Capabilities)
	}
}

// The slice's central property: a worker appears online, and goes offline once
// it stops heartbeating.
func TestHeartbeatAndGoingOffline(t *testing.T) {
	s, pool := testStore(t)
	ctx := context.Background()
	w, _, err := s.Register(ctx, bootstrapToken, registration("w-"+uuid.NewString()))
	if err != nil {
		t.Fatal(err)
	}

	state, err := s.Heartbeat(ctx, w.ID, Report{Healthy: true, CPUPct: 42,
		SlotsInUse: map[string]int{"h264": 1}})
	if err != nil {
		t.Fatal(err)
	}
	if state != StateOnline {
		t.Errorf("state = %s, want online", state)
	}

	// Nothing is stale yet, so a sweep must not touch it.
	if _, err := s.MarkStaleOffline(ctx, time.Hour); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(ctx, w.ID); got.State != StateOnline {
		t.Fatalf("a live worker was swept offline: %s", got.State)
	}

	// Simulate the heartbeats stopping.
	if _, err := pool.Exec(ctx,
		`update workers set last_heartbeat_at = now() - interval '5 minutes' where id = $1`,
		w.ID); err != nil {
		t.Fatal(err)
	}
	n, err := s.MarkStaleOffline(ctx, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if n < 1 {
		t.Fatal("the stale worker was not swept")
	}
	if got, _ := s.Get(ctx, w.ID); got.State != StateOffline {
		t.Errorf("state = %s, want offline", got.State)
	}

	// A heartbeat brings it straight back: recovery needs no operator action.
	if state, err := s.Heartbeat(ctx, w.ID, Report{Healthy: true}); err != nil || state != StateOnline {
		t.Errorf("heartbeat after offline gave (%q, %v), want online", state, err)
	}
}

func TestUnhealthyAndDraining(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	w, _, err := s.Register(ctx, bootstrapToken, registration("w-"+uuid.NewString()))
	if err != nil {
		t.Fatal(err)
	}

	// A worker that reports itself unhealthy stops being schedulable but is
	// not lost: it is still heartbeating.
	if state, _ := s.Heartbeat(ctx, w.ID, Report{Healthy: false}); state != StateUnhealthy {
		t.Errorf("state = %s, want unhealthy", state)
	}
	if state, _ := s.Heartbeat(ctx, w.ID, Report{Healthy: true}); state != StateOnline {
		t.Errorf("state = %s, want online after recovery", state)
	}

	if err := s.SetState(ctx, w.ID, StateDraining); err != nil {
		t.Fatal(err)
	}
	// A heartbeat must not undo a drain, or an operator could never take a
	// worker out of service. The response is how the worker learns of it.
	if state, _ := s.Heartbeat(ctx, w.ID, Report{Healthy: true}); state != StateDraining {
		t.Errorf("state = %s, want the drain to survive a heartbeat", state)
	}

	if err := s.SetState(ctx, w.ID, "banana"); err == nil {
		t.Error("an invalid state was accepted")
	}
	if err := s.SetState(ctx, uuid.Must(uuid.NewV7()), StateDraining); !errors.Is(err, ErrNotFound) {
		t.Errorf("draining an unknown worker returned %v, want ErrNotFound", err)
	}
}

func TestHeartbeatFromUnknownWorker(t *testing.T) {
	s, _ := testStore(t)
	if _, err := s.Heartbeat(context.Background(), uuid.Must(uuid.NewV7()), Report{Healthy: true}); !errors.Is(err, ErrNotFound) {
		t.Errorf("heartbeat from an unknown worker returned %v, want ErrNotFound", err)
	}
}
