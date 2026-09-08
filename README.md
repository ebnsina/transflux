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
make up                       # postgres + minio + control plane
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
cmd/transflux/       control plane binary and the bootstrap subcommand
internal/api/        HTTP surface, routing, error envelope
internal/auth/       API keys, authentication, scopes
internal/tenant/     tenants and quotas
internal/audit/      append-only audit trail, redaction
internal/config/     environment configuration
internal/db/         pool + embedded forward-only migrations
docs/adr/            architecture decision records
docs/schema.sql      full schema proposal (design; migrations grow per slice)
ARCHITECTURE.md      planes, domain model, state machine, protocol, threat model
PLAN.md              phased implementation plan
```
