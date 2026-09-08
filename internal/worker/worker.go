// Package worker owns the worker registry: registration, declared
// capabilities, heartbeats and lifecycle.
//
// Workers are fleet infrastructure rather than tenant resources — one worker
// serves every tenant — so nothing here is tenant-scoped. Isolation is enforced
// when work is leased.
package worker

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound     = errors.New("worker not found")
	ErrUnauthorized = errors.New("unauthorized")
	ErrBadProtocol  = errors.New("unsupported worker protocol version")
)

// ProtocolVersion is the worker protocol this control plane speaks. It accepts
// N-1 so a fleet can be upgraded without a flag day.
const (
	ProtocolVersion    = 1
	MinProtocolVersion = 1
)

const credentialPrefix = "tfw_"

// State is the worker lifecycle.
const (
	StateOnline    = "online"
	StateDraining  = "draining" // finishing current work, taking no more
	StateOffline   = "offline"  // heartbeats stopped
	StateUnhealthy = "unhealthy"
)

// Capabilities is what a worker declares it can do. The scheduler gates on
// these rather than on machine identity, so a heterogeneous fleet needs no
// central inventory.
type Capabilities struct {
	Operations []string `json:"operations"`
	Encoders   []string `json:"encoders"`
	Decoders   []string `json:"decoders"`
	Containers []string `json:"containers"`
	PixelFmts  []string `json:"pixel_formats"`
	Packagers  []string `json:"packagers"`
	Encryption []string `json:"encryption"`
	ASRModels  []string `json:"asr_models"`
}

// Registration is what a worker reports about itself at startup.
type Registration struct {
	Name            string       `json:"name"`
	Hostname        string       `json:"hostname"`
	OS              string       `json:"os"`
	Arch            string       `json:"arch"`
	CPUCores        int          `json:"cpu_cores"`
	MemoryBytes     int64        `json:"memory_bytes"`
	DiskBytes       int64        `json:"disk_bytes"`
	GPUModel        string       `json:"gpu_model"`
	GPUMemoryBytes  int64        `json:"gpu_memory_bytes"`
	FFmpegVersion   string       `json:"ffmpeg_version"`
	ProtocolVersion int          `json:"protocol_version"`
	Capabilities    Capabilities `json:"capabilities"`
	// SlotCapacity is how much of each workload class this host can run at
	// once. Declared, never derived from core count: a 32-core box is not 32
	// concurrent AV1 encodes.
	SlotCapacity map[string]int `json:"slot_capacity"`
}

type Worker struct {
	ID              uuid.UUID      `json:"id"`
	Name            string         `json:"name"`
	Hostname        string         `json:"hostname"`
	OS              string         `json:"os"`
	Arch            string         `json:"arch"`
	CPUCores        int            `json:"cpu_cores"`
	MemoryBytes     int64          `json:"memory_bytes"`
	DiskBytes       int64          `json:"disk_bytes"`
	GPUModel        *string        `json:"gpu_model,omitempty"`
	FFmpegVersion   string         `json:"ffmpeg_version"`
	ProtocolVersion int            `json:"protocol_version"`
	Capabilities    Capabilities   `json:"capabilities"`
	SlotCapacity    map[string]int `json:"slot_capacity"`
	State           string         `json:"state"`
	LastHeartbeatAt time.Time      `json:"last_heartbeat_at"`
	RegisteredAt    time.Time      `json:"registered_at"`
}

// Report is what a worker sends on each heartbeat.
type Report struct {
	CPUPct          float32        `json:"cpu_pct"`
	MemoryUsedBytes int64          `json:"memory_used_bytes"`
	DiskFreeBytes   int64          `json:"disk_free_bytes"`
	GPUPct          float32        `json:"gpu_pct"`
	SlotsInUse      map[string]int `json:"slots_in_use"`
	Healthy         bool           `json:"healthy"`
}

type Store struct {
	pool *pgxpool.Pool
	// bootstrapHash authenticates registration only. It is shared across the
	// fleet, so it is exchanged immediately for a per-worker credential and is
	// never accepted for anything else.
	bootstrapHash []byte
}

func NewStore(pool *pgxpool.Pool, bootstrapToken string) *Store {
	sum := sha256.Sum256([]byte(bootstrapToken))
	return &Store{pool: pool, bootstrapHash: sum[:]}
}

// Register enrols a worker and returns a credential unique to it. Re-registering
// under the same name keeps the id and rotates the credential, so a restart or
// redeploy does not leak a fleet entry, and a stolen credential stops working
// once that worker restarts.
func (s *Store) Register(ctx context.Context, bootstrapToken string, r Registration) (Worker, string, error) {
	if !s.checkBootstrap(bootstrapToken) {
		return Worker{}, "", ErrUnauthorized
	}
	if r.ProtocolVersion < MinProtocolVersion || r.ProtocolVersion > ProtocolVersion {
		return Worker{}, "", fmt.Errorf("%w: worker speaks v%d, control plane accepts v%d-v%d",
			ErrBadProtocol, r.ProtocolVersion, MinProtocolVersion, ProtocolVersion)
	}
	if strings.TrimSpace(r.Name) == "" {
		return Worker{}, "", errors.New("worker name is required")
	}

	secret, hash, err := newCredential()
	if err != nil {
		return Worker{}, "", err
	}

	caps, err := json.Marshal(r.Capabilities)
	if err != nil {
		return Worker{}, "", err
	}
	slots, err := json.Marshal(r.SlotCapacity)
	if err != nil {
		return Worker{}, "", err
	}

	var w Worker
	err = s.pool.QueryRow(ctx, `
		insert into workers (id, name, hostname, os, arch, cpu_cores, memory_bytes,
		                     disk_bytes, gpu_model, gpu_memory_bytes, ffmpeg_version,
		                     protocol_version, capabilities, slot_capacity, state,
		                     credential_hash, last_heartbeat_at)
		values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,'online',$15, now())
		on conflict (name) do update set
			hostname = excluded.hostname, os = excluded.os, arch = excluded.arch,
			cpu_cores = excluded.cpu_cores, memory_bytes = excluded.memory_bytes,
			disk_bytes = excluded.disk_bytes, gpu_model = excluded.gpu_model,
			gpu_memory_bytes = excluded.gpu_memory_bytes,
			ffmpeg_version = excluded.ffmpeg_version,
			protocol_version = excluded.protocol_version,
			capabilities = excluded.capabilities,
			slot_capacity = excluded.slot_capacity,
			state = 'online',
			credential_hash = excluded.credential_hash,
			last_heartbeat_at = now()
		returning id, name, state, registered_at, last_heartbeat_at`,
		uuid.Must(uuid.NewV7()), r.Name, r.Hostname, r.OS, r.Arch, r.CPUCores,
		r.MemoryBytes, r.DiskBytes, nullable(r.GPUModel), nullableInt(r.GPUMemoryBytes),
		r.FFmpegVersion, r.ProtocolVersion, caps, slots, hash,
	).Scan(&w.ID, &w.Name, &w.State, &w.RegisteredAt, &w.LastHeartbeatAt)
	if err != nil {
		return Worker{}, "", fmt.Errorf("register worker: %w", err)
	}

	if _, err := s.pool.Exec(ctx, `
		insert into worker_resources (worker_id) values ($1)
		on conflict (worker_id) do update set updated_at = now(), healthy = true`, w.ID); err != nil {
		return Worker{}, "", err
	}

	w.Capabilities, w.SlotCapacity = r.Capabilities, r.SlotCapacity
	w.ProtocolVersion = r.ProtocolVersion
	return w, secret, nil
}

// Authenticate resolves a worker credential to the worker that holds it. A
// credential is scoped to exactly one worker and identifies who is calling and
// nothing more — it carries no tenant and grants no access to media.
func (s *Store) Authenticate(ctx context.Context, secret string) (Worker, error) {
	if !strings.HasPrefix(secret, credentialPrefix) {
		return Worker{}, ErrUnauthorized
	}
	sum := sha256.Sum256([]byte(secret))

	var (
		w      Worker
		stored []byte
	)
	err := s.pool.QueryRow(ctx, `
		select id, name, state, credential_hash, last_heartbeat_at
		  from workers where credential_hash = $1`, sum[:],
	).Scan(&w.ID, &w.Name, &w.State, &stored, &w.LastHeartbeatAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Worker{}, ErrUnauthorized
	}
	if err != nil {
		return Worker{}, err
	}
	if subtle.ConstantTimeCompare(sum[:], stored) != 1 {
		return Worker{}, ErrUnauthorized
	}
	return w, nil
}

// Heartbeat renews liveness and records utilisation. It returns the worker's
// current state so a worker learns it has been asked to drain without needing
// an inbound connection.
func (s *Store) Heartbeat(ctx context.Context, workerID uuid.UUID, rep Report) (string, error) {
	slots, err := json.Marshal(rep.SlotsInUse)
	if err != nil {
		return "", err
	}

	var state string
	err = s.pool.QueryRow(ctx, `
		update workers set last_heartbeat_at = now(),
		    -- A worker that reports itself unhealthy is taken out of scheduling
		    -- but keeps its lease; draining is not overridden by a heartbeat.
		    state = case
		        when state = 'draining' then 'draining'
		        when $2 then 'online'
		        else 'unhealthy'
		    end
		 where id = $1
		 returning state`, workerID, rep.Healthy).Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}

	_, err = s.pool.Exec(ctx, `
		insert into worker_resources (worker_id, cpu_pct, memory_used_bytes,
		                              disk_free_bytes, gpu_pct, slots_in_use, healthy, updated_at)
		values ($1,$2,$3,$4,$5,$6,$7, now())
		on conflict (worker_id) do update set
			cpu_pct = excluded.cpu_pct, memory_used_bytes = excluded.memory_used_bytes,
			disk_free_bytes = excluded.disk_free_bytes, gpu_pct = excluded.gpu_pct,
			slots_in_use = excluded.slots_in_use, healthy = excluded.healthy,
			updated_at = now()`,
		workerID, rep.CPUPct, rep.MemoryUsedBytes, rep.DiskFreeBytes,
		rep.GPUPct, slots, rep.Healthy)
	return state, err
}

// SetState is the administrative lever: drain a worker before maintenance, or
// bring a drained one back.
func (s *Store) SetState(ctx context.Context, workerID uuid.UUID, state string) error {
	switch state {
	case StateOnline, StateDraining, StateOffline:
	default:
		return fmt.Errorf("cannot set worker state to %q", state)
	}
	tag, err := s.pool.Exec(ctx, `update workers set state = $2 where id = $1`, workerID, state)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkStaleOffline takes workers offline once their heartbeats stop. Their
// leases expire separately: losing a worker and reclaiming its work are
// deliberately different mechanisms, so a slow heartbeat does not abandon work
// that is still running.
func (s *Store) MarkStaleOffline(ctx context.Context, after time.Duration) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		update workers set state = 'offline'
		 where state <> 'offline'
		   and last_heartbeat_at < now() - $1::interval`,
		fmt.Sprintf("%d milliseconds", after.Milliseconds()))
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// Facts returns what the scheduler needs to match this worker against tasks:
// declared capability plus current free disk. Free disk comes from the last
// heartbeat, falling back to total disk when a worker has not reported yet.
func (s *Store) Facts(ctx context.Context, id uuid.UUID) (Worker, int64, error) {
	w, err := s.Get(ctx, id)
	if err != nil {
		return Worker{}, 0, err
	}
	var free *int64
	err = s.pool.QueryRow(ctx,
		`select disk_free_bytes from worker_resources where worker_id = $1`, id).Scan(&free)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Worker{}, 0, err
	}
	if free == nil || *free <= 0 {
		return w, w.DiskBytes, nil
	}
	return w, *free, nil
}

func (s *Store) Get(ctx context.Context, id uuid.UUID) (Worker, error) {
	w, err := s.scanOne(ctx, `where w.id = $1`, id)
	return w, err
}

func (s *Store) List(ctx context.Context) ([]Worker, error) {
	rows, err := s.pool.Query(ctx, selectWorker+` order by w.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	workers := []Worker{}
	for rows.Next() {
		w, err := scanWorker(rows)
		if err != nil {
			return nil, err
		}
		workers = append(workers, w)
	}
	return workers, rows.Err()
}

const selectWorker = `
	select w.id, w.name, w.hostname, w.os, w.arch, w.cpu_cores, w.memory_bytes,
	       w.disk_bytes, w.gpu_model, w.ffmpeg_version, w.protocol_version,
	       w.capabilities, w.slot_capacity, w.state, w.last_heartbeat_at, w.registered_at
	  from workers w`

func (s *Store) scanOne(ctx context.Context, where string, args ...any) (Worker, error) {
	rows, err := s.pool.Query(ctx, selectWorker+" "+where, args...)
	if err != nil {
		return Worker{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		return Worker{}, ErrNotFound
	}
	return scanWorker(rows)
}

func scanWorker(rows pgx.Rows) (Worker, error) {
	var (
		w     Worker
		caps  []byte
		slots []byte
	)
	if err := rows.Scan(&w.ID, &w.Name, &w.Hostname, &w.OS, &w.Arch, &w.CPUCores,
		&w.MemoryBytes, &w.DiskBytes, &w.GPUModel, &w.FFmpegVersion, &w.ProtocolVersion,
		&caps, &slots, &w.State, &w.LastHeartbeatAt, &w.RegisteredAt); err != nil {
		return Worker{}, err
	}
	if err := json.Unmarshal(caps, &w.Capabilities); err != nil {
		return Worker{}, err
	}
	if err := json.Unmarshal(slots, &w.SlotCapacity); err != nil {
		return Worker{}, err
	}
	return w, nil
}

func (s *Store) checkBootstrap(token string) bool {
	sum := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(sum[:], s.bootstrapHash) == 1
}

// newCredential mints 256 bits of CSPRNG output. As with API keys, the input is
// high entropy and generated by us, so SHA-256 is the right store — a password
// KDF would only add latency to every heartbeat.
func newCredential() (secret string, hash []byte, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", nil, err
	}
	secret = credentialPrefix + base64.RawURLEncoding.EncodeToString(buf)
	sum := sha256.Sum256([]byte(secret))
	return secret, sum[:], nil
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func nullableInt(v int64) *int64 {
	if v == 0 {
		return nil
	}
	return &v
}
