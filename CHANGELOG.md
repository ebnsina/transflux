# Changelog

All notable user-facing changes to Transflux. Format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); this project is
pre-release and does not yet follow semantic versioning.

## [Unreleased]

### Added

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
- `TRANSFLUX_S3_PUBLIC_ENDPOINT` — the storage endpoint clients and workers can
  reach, when it differs from the one the control plane uses. Presigned URLs are
  signed against it. Required when the control plane reaches storage over a
  private network, as it does inside Docker.
- Docker Compose stack: control plane, PostgreSQL 17 and MinIO. Published host
  ports default to 7080 (API), 7432 (Postgres) and 7900/7901 (MinIO), chosen to
  avoid colliding with a local Postgres on 5432 or another service on 8080; all
  are overridable via `TRANSFLUX_HTTP_PORT`, `TRANSFLUX_PG_PORT`,
  `TRANSFLUX_S3_PORT` and `TRANSFLUX_S3_CONSOLE_PORT`.

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
- Workers are fleet infrastructure, not tenant resources: one worker serves
  every tenant, so worker endpoints are `admin`-scoped and the worker protocol
  sits outside the `/v1` tenant surface entirely. A worker credential carries no
  tenant and grants no access to media.
- Re-registering under the same name keeps the worker's id and rotates its
  credential, so a restart does not add a fleet entry and a leaked credential
  stops working.
- A worker whose lease has been taken away gets `409 stale_attempt` on any
  report, telling it to stop and discard its output. This is what stops a worker
  returning from a network partition overwriting a result another worker already
  produced.
- Retry budget is consumed when a task is leased, not when it fails. A worker
  that dies silently still counts, or a task that kills every worker it touches
  would be retried forever.
- A worker is marked `offline` after 30 seconds without a heartbeat. Reclaiming
  the work it held is a separate mechanism, so a slow network does not abandon
  work that is still running.
- No media pipeline yet: probing, encoding and packaging are not implemented.
