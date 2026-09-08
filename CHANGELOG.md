# Changelog

All notable user-facing changes to Transflux. Format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); this project is
pre-release and does not yet follow semantic versioning.

## [Unreleased]

### Added

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
- No media pipeline yet: probing, encoding and packaging are not implemented.
