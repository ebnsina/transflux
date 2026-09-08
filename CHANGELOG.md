# Changelog

All notable user-facing changes to Transflux. Format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); this project is
pre-release and does not yet follow semantic versioning.

## [Unreleased]

### Added

- **Observability.**
  - `GET /metrics` — Prometheus metrics, behind an `admin` key. Request counts
    and latency by route, queue depth by operation and state, worker counts by
    state, lease outcomes, task outcomes and durations, CPU seconds and bytes
    processed. Labels never carry a tenant, asset or job id: one time series per
    asset defeats the metrics system at the moment it is most needed.
  - `transflux_unschedulable_tasks` counts queued tasks no online worker is
    capable of running. That situation looks identical to a busy queue from
    outside, so it gets its own number rather than waiting to be noticed.
  - Per-attempt resource accounting now records real CPU time and peak memory
    from the media process, not just wall clock, which is what makes
    cost-per-asset answerable later.
- **Output validation.** Every transcode now ends in a validation task, and the
  job only succeeds if it passes. An encoder exiting zero is not evidence that
  the file plays.
  - Each output is fetched back from storage and checked: it exists, its size
    and checksum match what was registered, it probes cleanly, and its codec,
    resolution, duration, audio and HDR format are what the plan asked for. The
    set is also checked for missing outputs, which no per-artifact check can
    see.
  - A failed job reports which check failed, in `GET /v1/jobs/{id}` under
    `validation`, rather than only that something did.
  - An artifact set whose validation fails is marked `failed`, so nothing is
    delivered from it.
- **Artifacts.** A job's outputs are recorded as immutable, versioned artifacts
  and can be fetched with a short-lived signed URL.
  - `GET /v1/jobs/{id}/artifacts` — the job's artifact sets and what is in them:
    label, kind, size, checksum, and a media summary (codec, resolution, frame
    rate, colour) so a caller can choose between renditions without fetching
    any of them.
  - `GET /v1/artifacts/{id}/download` — a signed URL valid for 15 minutes.
    Media never passes through the control plane, and a permanent public URL is
    never the only way to reach content.
  - Nothing is overwritten: a second output claiming a label that is taken is a
    conflict, not an update. A re-run produces a new set version alongside the
    old, so a URL that resolved to some bytes yesterday still resolves to those
    bytes today.
  - An artifact set is `building` until its job finishes, so nothing is
    delivered from a set that is still missing outputs.
- **Transcoding.** `POST /v1/jobs` with `pipeline: "transcode-h264"` produces a
  720p H.264 rendition with AAC audio, uploaded straight from the worker to
  storage.
  - **Encoding is configured, not scripted.** Codec, profile, level, resolution,
    frame rate, rate control, GOP, keyframe interval, pixel format, preset,
    tune, B-frames, reference frames and colour are structured fields. Every
    value that reaches the command line comes from an allowlist or is a
    bounds-checked number, so nothing a caller writes is passed through to a
    subprocess.
  - **A rung never upscales.** The ladder is planned against the source's actual
    tracks: resolution is capped at the source's, aspect ratio is preserved, and
    the frame rate is never raised. Requires the version to have been probed
    (`409 probe_required`).
  - **Colour survives the transcode.** An HDR source produces an HDR rendition,
    with colour written into the encoder's own signalling rather than only
    tagged on the stream.
  - `GET /v1/pipelines` lists what you can name: `probe` and `transcode-h264`.
- **Media probing.** A probe job inspects a source and records what it
  contains: container, duration, bitrate, and every video, audio and subtitle
  track with codec, resolution, frame rate, pixel format, bit depth, language,
  title, disposition and accessibility role.
  - **Colour and HDR are preserved.** Primaries, transfer, matrix and range are
    recorded as found, and the HDR format is derived from them and from side
    data: HDR10, HLG, HDR10+, Dolby Vision, or SDR. An HDR source is never
    silently recorded as SDR.
  - `POST /v1/jobs` — run a pipeline against an asset version. Refuses a source
    that has not been verified (`409 source_unverified`), so expensive work
    never starts on a half-uploaded file.
  - `GET /v1/jobs/{id}` — job state with its tasks.
  - `POST /v1/jobs/{id}/cancel` — cancel a job and everything it has queued.
  - `GET /v1/pipelines` — the pipelines you can name. `probe` is the first.
  - `GET /v1/assets/{id}` now includes each version's `media`: what the probe
    found. Absent rather than empty when a version has not been probed.
- **`transflux-worker`, the worker binary.** Deployed independently of the
  control plane; it needs only an outbound route to it — no inbound
  connectivity, no orchestrator, and no database or storage credentials of its
  own. Configure with `TRANSFLUX_CONTROL_PLANE_URL` and
  `TRANSFLUX_WORKER_BOOTSTRAP_TOKEN`; everything else is detected.
  - Capabilities are discovered by asking FFmpeg what it supports, rather than
    declared in config that drifts the moment FFmpeg is upgraded.
  - A worker only advertises operations it can actually execute, so the
    scheduler never sends work that fails on arrival.
  - `TRANSFLUX_WORKER_SLOTS` overrides per-class capacity; the default is
    deliberately conservative and meant to be replaced by measurement.
  - Cancelling a job stops the media tool within one progress interval, killing
    its whole process group — an abandoned encode would otherwise keep
    consuming CPU and writing output.
  - The worker image pins FFmpeg by its base image tag, so behaviour is decided
    by the deployed build rather than by whatever a developer has installed.
    The worker refuses to start on an unsupported version.
- **Worker registry and worker protocol v1.** Workers dial out to the control
  plane, so a machine behind NAT or in another cloud joins by setting two
  environment variables — nothing needs a route back to a worker.
  - `POST /worker/v1/register` — authenticated with
    `TRANSFLUX_WORKER_BOOTSTRAP_TOKEN`, exchanged immediately for a per-worker
    credential. A worker declares its hardware, capabilities and per-workload
    slot capacity; the scheduler gates on those rather than on machine identity.
  - `POST /worker/v1/heartbeat` — renews liveness, reports utilisation, and
    returns the worker's state, which is how a drain reaches a worker we cannot
    dial.
  - `POST /worker/v1/lease` — poll for work. `204` when there is nothing to do,
    which is the normal answer for an idle fleet rather than an error. A
    draining, unhealthy or offline worker is given nothing.
  - `POST /worker/v1/attempts/{id}/started` — execution actually began.
  - `POST /worker/v1/attempts/{id}/progress` — renews the lease, reports
    progress, and returns `cancel` when the task has been cancelled. This is how
    a cancellation reaches a worker the control plane cannot dial; worst-case
    latency is one heartbeat interval.
  - `POST /worker/v1/attempts/{id}/complete` — success or a classified failure,
    with per-attempt resource accounting. Returns whether the task will retry.
  - `GET /v1/workers`, `GET /v1/workers/{id}` — fleet view (`admin` scope).
  - `POST /v1/workers/{id}/state` — drain a worker before maintenance, or return
    it to service.
- **Assets and resumable uploads.**
  - `POST /v1/assets` — create an asset and its first version. A repeated
    `external_id` returns `409`, so the caller's own identifier gives them
    idempotency.
  - `GET /v1/assets`, `GET /v1/assets/{id}` — list and fetch, with versions.
  - `POST /v1/assets/{id}/uploads` — start a resumable upload. Returns the part
    size, part count and presigned URLs to `PUT` parts directly to storage.
  - `GET /v1/uploads/{id}` — resume: reports which parts arrived, which are
    missing, and fresh presigned URLs for the missing ones.
  - `POST /v1/uploads/{id}/complete` — verifies every part is present and
    correctly sized, assembles the object, and checks the stored size against
    the declared size before marking the source usable. Idempotent.
  - `DELETE /v1/uploads/{id}` — abort, leaving nothing behind in storage.
- **Object storage abstraction** over any S3-compatible endpoint (AWS, MinIO,
  R2, B2, Spaces). Configured with `TRANSFLUX_S3_*` environment variables; set
  `TRANSFLUX_S3_PATH_STYLE=true` for MinIO and most self-hosted gateways.
  Supports presigned upload and download URLs, so media never proxies through
  the control plane, and resumable multipart uploads.
- **Tenancy and API-key authentication.** All `/v1` endpoints require
  `Authorization: Bearer <key>`. Keys are shown once at creation and stored only
  as a hash; there is no recovery path.
- `GET /v1/me` — returns the tenant, key id and scopes a key resolves to.
- `transflux bootstrap -name "<tenant>"` — provisions a tenant and prints one
  admin key. There is no self-serve signup.
- Scopes: `admin`, `assets:read`, `assets:write`, `jobs:read`, `jobs:write`.
  `admin` implies all others.
- **Audit trail** for tenant creation and key issuance, with sensitive values
  redacted before they are written.
- `GET /healthz` (liveness, never touches the database) and `GET /readyz`
  (readiness, fails when the database is unreachable).
- Schema migrations are embedded and applied automatically on boot; no
  migration tool is needed to deploy.
- `TRANSFLUX_S3_PUBLIC_ENDPOINT` — the storage endpoint **clients** can reach,
  when it differs from the one the control plane uses. Presigned URLs for
  customers are signed against it. Required when the control plane reaches
  storage over a private network, as it does inside Docker.
- `TRANSFLUX_S3_WORKER_ENDPOINT` — the storage endpoint **workers** can reach.
  Workers usually sit inside the network with storage while customers are
  outside, so the URL signed for a worker is not the one signed for a customer.
  Defaults to the public endpoint.
- Docker Compose stack: control plane, PostgreSQL 17 and MinIO. Published host
  ports default to 7080 (API), 7432 (Postgres) and 7900/7901 (MinIO), chosen to
  avoid colliding with a local Postgres on 5432 or another service on 8080; all
  are overridable via `TRANSFLUX_HTTP_PORT`, `TRANSFLUX_PG_PORT`,
  `TRANSFLUX_S3_PORT` and `TRANSFLUX_S3_CONSOLE_PORT`.

### Security

- Secrets are redacted by the logging handler itself rather than at call sites:
  API keys, worker credentials, content keys and presigned URLs cannot reach a
  log file even from a debug line added later. A presigned URL is treated as a
  secret because it is a bearer credential for one object.

### Notes

- Every authentication failure returns an identical `401 unauthorized` —
  unknown, malformed, revoked, expired and suspended-tenant keys alike — so a
  caller cannot determine which. A database outage returns `500` instead, so an
  outage is never mistaken for a bad credential.
- A source is only usable once `complete` has verified it. Until then no
  downstream work may run against it — this is the gate that stops expensive
  processing starting on a half-uploaded file.
- The declared `checksum` is recorded but not yet verified; that happens during
  probing, which streams the whole file anyway. Size and part completeness are
  verified now.
- A missing or unreachable bucket now fails at startup instead of surfacing as
  a `500` on the first upload. In `dev` the bucket is created automatically.
- A worker's input URL is signed when the work is handed out, not when the job
  is created. A task can sit queued for hours behind a busy fleet, and a URL
  minted at creation would already have expired.
- Workers are fleet infrastructure, not tenant resources: one worker serves
  every tenant, so worker endpoints are `admin`-scoped and the worker protocol
  sits outside the `/v1` tenant surface entirely. A worker credential carries no
  tenant and grants no access to media.
- Re-registering under the same name keeps the worker's id and rotates its
  credential, so a restart does not add a fleet entry and a leaked credential
  stops working.
- Registration checks storage before recording anything: a worker reporting an
  output it did not upload, or one that arrived truncated, is refused rather
  than leaving a record that points at nothing.
- A worker whose lease has been taken away gets `409 stale_attempt` on any
  report, including artifact registration, telling it to stop and discard its output. This is what stops a worker
  returning from a network partition overwriting a result another worker already
  produced.
- **Work survives losing a worker.** A lease that stops being renewed is
  reclaimed and the task is offered to another worker as a new attempt. A crash,
  a kill, a network partition and a vanished spot instance all look the same and
  all recover the same way. The previous attempt is kept on record as `expired`
  with the reason, so the history of a task is not rewritten by its recovery.
- Cancelling a job reaches a running worker on its next progress call, and a
  worker that finishes just as the cancel lands has its report accepted quietly
  and its result discarded — failing that call would only make it retry.
- `TRANSFLUX_LEASE_TTL_SECONDS` sets how long a lease survives without a
  progress report (default 60).
- Retry budget is consumed when a task is leased, not when it fails. A worker
  that dies silently still counts, or a task that kills every worker it touches
  would be retried forever.
- A worker is marked `offline` after 30 seconds without a heartbeat. Reclaiming
  the work it held is a separate mechanism, so a slow network does not abandon
  work that is still running.
- No media pipeline yet: probing, encoding and packaging are not implemented.
