# Transflux

A media processing and protection platform: source media in, validated,
packaged, encrypted, DRM-ready delivery artifacts out.

Not a CDN, not a player, not an analytics platform. See `ARCHITECTURE.md` for
the boundary and `docs/adr/` for why each major choice was made.

## Status

P0, slice 1 of 16 (`PLAN.md`): control plane skeleton, migrations, tenancy,
API-key authentication and audit. No media pipeline yet.

## Running

Requires Docker, or a local PostgreSQL 17.

```sh
cp .env.example .env
make up                       # postgres + minio + control plane + worker + dashboard
open http://localhost:7000    # the dashboard
curl localhost:7080/healthz   # liveness  (never touches the database)
curl localhost:7080/readyz    # readiness (fails when the database is down)
```

Published host ports default to 7080 (API), 7432 (Postgres), 7900/7901 (MinIO
and its console) so the stack does not collide with a local Postgres on 5432 or
another project on 8080. Override any of them in `.env`.

Tenants are provisioned deliberately — there is no self-serve signup:

```sh
docker compose exec control-plane /transflux bootstrap -name "Acme Media"
# tenant_id:  01a07fc6-...
# api_key:    tf_live_...        shown once; only its hash is stored

curl -H "Authorization: Bearer $KEY" localhost:7080/v1/me
```

### Uploading

Bytes go straight from the client to object storage over presigned URLs; they
never pass through the control plane.

```sh
# 1. create an asset (and its first version)
POST /v1/assets                      {"name": "film.mp4"}

# 2. start an upload; the response carries part_size and presigned part URLs
POST /v1/assets/{id}/uploads         {"size_bytes": 9437184}

# 3. PUT each part directly to its URL

# 4. resume at any point: returns what arrived and URLs for what did not
GET  /v1/uploads/{id}

# 5. verify and promote to the asset version's source
POST /v1/uploads/{id}/complete
```

`complete` checks every part is present and correctly sized, then compares the
assembled object's size against the declared size. Only then does a source
record exist, and only a verified source may be processed.

API keys are 256 bits of CSPRNG output, stored as SHA-256. A password KDF would
be the wrong tool: argon2 exists to slow brute force of low-entropy secrets, and
would only add latency to every authenticated request. Every authentication
failure — unknown, malformed, revoked, expired, suspended tenant — returns the
same 401, so a caller cannot probe which.

Against a local Postgres instead:

```sh
createdb transflux_dev
TRANSFLUX_DATABASE_URL=postgres://$USER@localhost:5432/transflux_dev?sslmode=disable \
  go run ./cmd/transflux
```

Migrations are embedded and applied on boot, forward-only, each in its own
transaction, serialised across instances by an advisory lock.

## Testing

```sh
make test                     # unit tests, no dependencies
make up && make test-all      # adds integration tests against the stack
```

Integration tests skip with a message when `TRANSFLUX_TEST_DATABASE_URL` is
unset. The migration test drops the `public` schema, so point it at a
throwaway database.

## Layout

```
web/                 SvelteKit dashboard (static build, served by nginx)
cmd/transflux/       control plane binary and the bootstrap subcommand
cmd/transflux-worker/ worker binary (data plane)
internal/agent/      worker internals: capability detection, task loop,
                     FFmpeg supervision, probing
internal/api/        HTTP surface, routing, error envelope
internal/auth/       API keys, authentication, scopes
internal/tenant/     tenants and quotas
internal/audit/      append-only audit trail, redaction
internal/job/        jobs, tasks, attempts, the task state machine
internal/artifact/   immutable artifact sets and signed delivery
internal/encode/     structured encoding config and argv construction
internal/pipeline/   built-in pipeline presets and ladder planning
internal/probe/      ffprobe parsing, tracks, colour and HDR
internal/validate/   output validation checks and their results
internal/worker/     worker registry, capabilities, heartbeats, lifecycle
internal/obs/        log redaction, metrics, queue gauges
internal/config/     environment configuration
internal/db/         pool + embedded forward-only migrations
docs/adr/            architecture decision records
docs/TESTING.md      how to run the tests, and the failure suite
docs/schema.sql      full schema proposal (design; migrations grow per slice)
ARCHITECTURE.md      planes, domain model, state machine, protocol, threat model
PLAN.md              phased implementation plan
```
