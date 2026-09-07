# Transflux — Architecture

A media processing and protection platform: source media in, validated /
packaged / encrypted delivery artifacts out. Not a CDN, not a player, not an
analytics platform.

Status: design. No application code yet.

---

## 1. Boundaries

Inside: ingest, resumable upload, asset model, storage abstraction, probe,
encode planning, transcode, distributed workers, scheduling, chunking, audio,
subtitles, thumbnails, packaging (HLS/DASH/CMAF), encryption, DRM metadata,
validation/QC, artifact management, job orchestration, observability, tenancy.

Outside (integration boundaries only): CDN, player, playback/QoE analytics,
SSAI, billing, CRM, identity provider, DRM license server infrastructure.

We emit CDN-friendly, player-compatible artifacts. We do not deliver or play
them.

---

## 2. Planes

```
CONTROL PLANE (modular monolith, one binary)
  api · auth · tenancy · assets · uploads · jobs · pipelines · tasks
  scheduler · worker registry · artifacts · protection · audit
        │
        │  worker protocol (versioned HTTPS/JSON, worker-initiated)
        ▼
DATA PLANE (independent binary, N instances, heterogeneous)
  probe · encode · chunk · audio · thumbnail · subtitle
  package · encrypt · validate      → FFmpeg subprocesses
        │
        ▼
OBJECT STORAGE (S3-compatible)  →  external CDN / origin
```

Two binaries: `transflux` (control plane) and `transflux-worker`. Nothing else
is required to run the system beyond PostgreSQL and an S3-compatible bucket.

Neither binary knows how it is deployed. No orchestrator API is called from
application code. Compose, systemd, Nomad and Kubernetes are all "start the
binary with these env vars".

### Why worker-initiated (pull), not control-plane push

Workers may sit behind NAT, on a laptop, in another cloud, on a spot instance
that dies mid-task. Requiring inbound reachability to a worker makes the fleet
a network topology problem. Workers dial out, long-poll for a lease, and push
progress. The control plane never needs a route to a worker.

Cost: the scheduler cannot "assign" — it must answer "what should *this*
worker do next". See §7.

---

## 3. Modules (control plane)

Each is a Go package under `internal/`, owning its own tables, exposing a Go
interface, and talking to peers only through those interfaces. No package
imports another's SQL. This is the extraction seam if a module ever needs to
become a service — none does today.

```
internal/
  api/          HTTP surface, versioned /v1, auth middleware, no business logic
  auth/         API keys, tenant resolution, scopes
  tenant/       tenants, quotas, limits
  asset/        assets, asset versions, source files, tracks, probe results
  upload/       resumable uploads, parts, completion verification
  pipeline/     pipeline definitions + versions, task graph expansion
  job/          jobs, tasks, task attempts, state machine, cancellation
  sched/        capability matching, scoring, leasing, expiry sweeper
  worker/       registry, capabilities, heartbeats, lifecycle
  artifact/     artifact sets, artifacts, versioning, registration
  protect/      protection policies, keys, KIDs, DRM metadata, providers
  storage/      S3-compatible object storage abstraction
  audit/        append-only audit events
  obs/          logging, metrics, tracing helpers
worker/         data plane: task loop, executors, ffmpeg wrappers
cmd/transflux/  control plane binary
cmd/transflux-worker/
```

Rule: `sched` may read `worker` capabilities and `job` task requirements; it
may not reach into `asset`. The task spec carries everything the data plane
needs — the worker never queries the domain.

---

## 4. Domain model

The core distinction the schema must not blur:

- **Asset** is media. Immutable source. Never mutated by a job.
- **Job** is one operation against one asset version. Many jobs per asset.
- **Pipeline** describes what must happen. **Task** is one executable unit.
- **Task attempt** is one execution of a task. Tasks have many attempts.
- **Artifact** is an immutable output, versioned, never overwritten.

```
Tenant ─┬─ User
        ├─ APIKey
        └─ Asset ─── AssetVersion ─── SourceFile
                          │
                          ├── Probe ─── Track (video|audio|subtitle)
                          │
                          └── Job ─── Task ─── TaskAttempt
                                       │           │
                                       │           └── Chunk / ChunkAttempt
                                       │
                                       └── ArtifactSet ─── Artifact
                                                              │
                                                        Package/Manifest
                                                              │
                                                    EncryptionKey (KID, version)
```

Full DDL proposal: `docs/schema.sql`. Every tenant-scoped table carries
`tenant_id` and is indexed on it first.

---

## 5. Task state machine

```
PENDING ──► QUEUED ──► LEASED ──► RUNNING ──► SUCCEEDED
              ▲          │           │
              │          │           ├──► FAILED ──► QUEUED   (retryable, budget left)
              │          │           └──► CANCELLED
              │          │
              │          └──► EXPIRED ─┐
              └────────────────────────┘

PENDING: dependencies unmet.
QUEUED:  dependencies met, schedulable.
LEASED:  a worker holds a lease; not yet reported started.
RUNNING: worker reported started; heartbeats renew the lease.
Terminal: SUCCEEDED, CANCELLED, FAILED (retry budget exhausted or permanent).
```

Transitions are enforced in one place (`internal/job`), by a table of legal
`(from, to)` pairs, applied inside a transaction with the row locked. No other
package writes `tasks.state`. Illegal transition = error, not a silent no-op —
it means a bug or a duplicate worker report, and we want to see it.

`FAILED → QUEUED` only when the error class is retryable (§13) and
`attempt_count < max_attempts`.

---

## 6. Worker protocol (v1)

Versioned: `POST /worker/v1/...`. The control plane accepts N-1 and N. A
worker whose `protocol_version` the control plane does not support is told to
drain, not killed mid-task.

The protocol carries **structured task specs, never shell commands.** The
worker builds its own FFmpeg argv from the spec. A compromised or spoofed
control plane must not be able to run arbitrary commands on a worker.

```
POST /worker/v1/register    → worker_id, capabilities, hardware, versions
POST /worker/v1/lease       → long-poll; returns 0 or 1 task spec
POST /worker/v1/tasks/{id}/started
POST /worker/v1/tasks/{id}/heartbeat  → {progress, metrics}; renews lease;
                                        response may carry {cancel: true}
POST /worker/v1/tasks/{id}/artifacts  → register an output (idempotent)
POST /worker/v1/tasks/{id}/complete   → success | failure{class, reason, logs}
POST /worker/v1/heartbeat             → worker-level liveness + resource report
```

Task spec shape:

```
task_id, attempt_id, protocol_version, operation,
inputs[]  {storage_ref, size, checksum, presigned_get}
outputs   {prefix, presigned_put policy}
config    operation-specific structured config (encode/package/protect/...)
chunk     {index, start_pts, end_pts, keyframe boundaries} | null
limits    {wall_timeout, cpu, memory, disk}
```

Cancellation is delivered on the heartbeat response, not pushed. Worst-case
cancellation latency is one heartbeat interval (10s). Good enough; avoids
inbound connectivity to workers.

Idempotency: every mutating worker call carries `attempt_id`. Reports from a
stale attempt (lease already expired and reassigned) are recorded for audit and
rejected — the winning attempt is the one holding the lease.

---

## 7. Scheduler

Explainable, no ML, no heuristics that cannot be printed.

Worker asks "what next". The scheduler:

1. **Hard constraints** (SQL, indexed): task type supported, codec/encoder
   present, architecture compatible, GPU present if required, memory ≥
   requirement, free disk ≥ requirement, free slot for the workload class,
   tenant not over concurrency quota.
2. **Rank** feasible tasks for this worker:

```
score = priority_weight
      × capability_fit      prefer the *least* capable worker that can do it,
                            so GPU nodes are not consumed by 360p AAC work
      × capacity_headroom
      × fairness            tenant's running tasks vs. fair share
      × locality            worker already holds this asset's source/chunks
      × age                 queue-time bump, prevents starvation
```

3. **Lease** the winner: `SELECT ... FOR UPDATE SKIP LOCKED`, insert a
   `task_attempt`, set `LEASED`, write the reason string.

Every lease stores its explanation:

```
worker-17 ← task 9c1e (encode/h265/1080p)
  + hevc_nvenc available          + 72% CPU headroom
  + arch match (x86_64)           + 14GB free memory
  + 2 free hevc slots             + fair-share 0.81
  - not the least-capable fit (no CPU-only worker with hevc idle)
```

This is stored on the attempt and exposed in the API. When scheduling looks
wrong, we read why instead of guessing.

**Resource accounting is per workload class, not per CPU core.** A 32-core box
is not "32 encodes". Workers declare slot capacity per class (h264: 4,
hevc: 2, av1: 1, probe: 8, package: 4, thumbnail: 8) with defaults derived
from cores/memory and overridable per host. Real measurements replace the
defaults once we have them (§ TESTING).

---

## 8. Media pipeline

```
Upload ─► verify ─► Probe ─► Plan ─► [Encode ×N] ─► Package ─► Protect ─► Validate ─► ArtifactSet complete
                                     [Thumbnail]
                                     [Subtitle]
                                     [Audio ×N]
```

- **Plan** is a control-plane task, not a worker task: it reads the probe
  result and the requested ladder, and expands the pipeline into concrete
  encode/package/protect tasks with a dependency graph. Planning decisions are
  persisted, so a job is reproducible and inspectable.
- **Encode** produces mezzanine/variant media. **Package** produces segments
  and manifests. These are separate tasks and separate binaries' invocations —
  FFmpeg encoding logic must never generate manifests inline.
- **Chunking** (P1) splits on keyframe/GOP boundaries derived from the probe,
  never byte ranges. Chunk boundaries are computed once, persisted, and are
  deterministic. Chunked encode requires closed GOPs and fixed keyframe
  intervals in the output config; when the source or the requested config makes
  that unsafe, the planner emits a single whole-file encode task and records
  why. Unsafe cases documented in `docs/MEDIA_PIPELINE.md`.
- **Structured encoding config, not command strings.** Codec, profile, level,
  resolution, fps, rate control, bitrate/CRF, GOP, keyframe interval, pixel
  format, preset, tune, b-frames, refs, colour/HDR metadata, audio codec/
  bitrate/rate/channels. The FFmpeg argv is a rendering of that struct, built
  worker-side from an allowlisted vocabulary. No user string ever reaches argv
  unescaped.
- **HDR/colour is modelled from day one** even though P0 encodes SDR H.264:
  transfer, primaries, matrix, range, mastering display and content light
  level are probe fields and encode-config fields. Defaulting everything to
  BT.709 in the schema is the mistake that is expensive to undo.

---

## 9. Protection

Four levels, modelled explicitly. Not a `drm_enabled` boolean.

1. Private storage + authenticated access.
2. Signed, short-lived delivery URLs / tokens.
3. Media encryption (AES-128 / SAMPLE-AES / CENC / CBCS).
4. Full DRM (Widevine / FairPlay / PlayReady signalling + external licensing).

We own: the encryption workflow, KIDs, key lifecycle and rotation, key
metadata, PSSH construction, protected packaging, manifest signalling, and the
policy record that an external license server reads. We do **not** own the
license server.

```
Content ─► KID ─► ContentKey ─► Encrypted media ─► PSSH/manifest signalling
                      │
                      └── KeyProvider (local-dev | KMS | external DRM vendor)
```

Content keys are wrapped at rest, fetched by the worker through a scoped,
single-attempt-bound endpoint, held in memory only, and never written to logs,
API responses, error messages, task specs at rest, or artifacts. Redaction is
enforced in the logging layer, not by convention.

Playback authorization (is this user entitled?) is our concern and stays
separate from license issuance (what does the DRM system permit?).

Deferred but not designed out: offline/persistent licenses, forensic
watermarking (an integration point in the pipeline, not an implementation).

---

## 10. Storage

S3-compatible only, behind an interface with five methods (get, put,
multipart, head, delete, presign). No vendor SDK types cross the interface.
MinIO for dev, any of S3/R2/B2/Spaces in production.

Artifacts are immutable and content-addressed by prefix:
`{tenant}/{asset}/{job}/{artifact_set_version}/...`. Nothing is ever
overwritten; a re-run writes a new artifact set version.

**Assume the database and the bucket will disagree.** A reconciler walks both
directions on a schedule: DB rows whose objects are missing are marked
`orphaned_db`; objects with no DB row older than the safety window are marked
`orphaned_object`. Neither is deleted automatically on first detection —
findings are recorded, aged, and only then collected. A failed query must never
be able to trigger a delete.

Garbage collection targets: abandoned uploads, temp chunks, failed job
outputs, superseded artifact versions past retention. Every deletion is
audited.

---

## 11. Security

Threat model summary (full: `docs/SECURITY.md`, to be written alongside P0).

| Threat | Control |
|---|---|
| Malicious media / FFmpeg CVE | Workers are the blast radius, not the control plane. Non-root, read-only rootfs, dropped caps, no network for the FFmpeg process, per-task tmpdir, wall/CPU/memory/disk/pid limits, seccomp. |
| Decompression bomb, resource exhaustion | Hard limits on duration, resolution, track count, output size; enforced from probe before planning, and again as process limits. |
| Command injection | Structured specs → allowlisted argv construction. No shell. `exec.Command` with explicit args, never `sh -c`. |
| Path traversal | Storage keys are generated server-side from IDs; user-supplied names are metadata only, never path components. |
| Worker impersonation / stolen credentials | Per-worker bootstrap token → short-lived worker credential, rotated, revocable, scoped to that worker's leases only. A worker can only act on attempts it holds. |
| Cross-tenant access | `tenant_id` on every scoped query, enforced at the repository layer, plus an integration test suite that attempts cross-tenant reads on every endpoint. |
| Key leakage | Wrapped at rest, memory-only in workers, redaction in the log layer, never in specs persisted to disk, never in audit events. |
| Upload/API abuse | Per-tenant quotas: storage, concurrent jobs, upload rate, request rate. |

The control plane never invokes FFmpeg. That is the single most valuable
isolation property in the system.

---

## 12. Failure modes

| Failure | Detection | Recovery |
|---|---|---|
| Worker crash mid-encode | heartbeat stops | lease expires → attempt marked EXPIRED → task requeued |
| Network partition | heartbeat fails worker-side | worker self-cancels its FFmpeg process on lease-loss; control plane requeues; duplicate output is discarded on artifact registration (attempt must hold the lease) |
| Control plane restart | — | all state is in Postgres; no in-memory scheduling state; workers retry and resume long-poll |
| Postgres restart | conn errors | bounded retry with backoff; workers keep running current tasks and buffer reports |
| Storage failure | put/get errors | classified transient → retry with backoff; task fails after budget and is retryable |
| Duplicate task execution | two attempts, one lease | only the lease-holding attempt can register artifacts; loser's output is GC'd |
| Interrupted upload | part gaps at completion | completion verifies every part + checksum + size; no job may start on an unverified source |
| Poison media (fails every worker) | repeated failures, same error class | classified permanent after first non-transient failure; no retry; job fails with a diagnosable reason |
| Slow-loris task | wall timeout | worker kills the process group; attempt fails as timeout (retryable, bounded) |
| Successful FFmpeg, broken output | — | validation task; artifact set is only `complete` after validation passes |

Exit code 0 is not success. Nothing is delivered until validated (§ OUTPUT
VALIDATION in `docs/TESTING.md`).

---

## 13. Retry classification

Errors are classified, never blanket-retried.

| Class | Retry | Example |
|---|---|---|
| `transient` | yes, backoff | storage 5xx, network timeout, worker crash, lease expiry |
| `resource` | yes, maybe elsewhere | out of disk, OOM — retry with a higher requirement |
| `permanent_input` | no | unsupported/corrupt source, no decodable track |
| `permanent_config` | no | invalid encoding config, unsupported requested codec |
| `infrastructure` | yes, bounded | DB unavailable, key provider unavailable |
| `unknown` | yes, once | anything unclassified — and it gets a bug report |

Bounded attempts, exponential backoff with jitter, per-class caps.

---

## 14. Observability

Structured logs (`slog`, JSON, with tenant/asset/job/task/attempt/worker IDs on
every line, and key redaction). Prometheus metrics on `/metrics`. Tracing
plumbed through context, exporter off by default.

Control plane: API latency, DB latency, queue depth by task type, lease
latency, throughput, failure and retry rates by class, scheduler
feasible-worker counts (a task with zero feasible workers is an alert, not a
mystery).

Worker: CPU/memory/disk/GPU, encoding FPS, realtime factor, task duration,
process failures.

Pipeline: per-stage duration, output size, compression ratio, input/output
bytes.

Per-task resource accounting (CPU-seconds, wall time, peak memory, GPU time,
bytes in/out, codec, resolution) is recorded on the attempt. That enables
cost-per-asset analysis later. We track resource usage; we do not build
billing.

---

## 15. Deployment

Start: Docker Compose — reverse proxy, control plane, Postgres, MinIO, N
workers. Workers on other machines run the same image under systemd or plain
Docker with two env vars (control plane URL, bootstrap token). That reaches
"multiple GPU/ARM worker machines" without an orchestrator.

Adding a worker never requires a control-plane change, a schema change, or a
config change: it registers itself and declares its capabilities.

Kubernetes/Nomad remain available and remain irrelevant to application code.
Comparison: `docs/adr/ADR-008-deployment.md`.

---

## 16. What we are deliberately not building

CDN, player, playback/QoE analytics, billing, SSAI, multi-region active-active,
Kubernetes, Kafka, Redis, service mesh, distributed cache, ML scheduling,
custom codecs, DRM license server, forensic watermarking implementation, live
ingest, per-title encoding, GPU autoscaling.

Extension points exist for: live (separate domain, never modelled as a giant
VOD job), per-title (the planner already owns ladder selection), watermarking
(a pipeline stage), timed metadata (ID3/emsg/SCTE-35 fields reserved in the
packaging model).

---

## 17. Decisions

| ADR | Decision |
|---|---|
| 001 | Control plane in **Go** |
| 002 | **PostgreSQL** + pgx + sqlc |
| 003 | **Own task table**, no River/Temporal |
| 004 | **FFmpeg CLI subprocess**, not libav bindings |
| 005 | **S3-compatible** behind a narrow interface |
| 006 | **Worker-pull HTTPS/JSON**, versioned, structured specs |
| 007 | **Constraint + score scheduler**, explanations persisted |
| 008 | **Docker Compose** first; orchestrator-agnostic binaries |
| 009 | **DRM-aware, not a DRM vendor**; pluggable key/DRM providers |
| 010 | **Admin UI deferred**; API-first, Svelte + Vite SPA at P1 |
