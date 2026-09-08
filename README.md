# Transflux

A media processing and protection platform: source media in, validated,
packaged, encrypted, DRM-ready delivery artifacts out.

Not a CDN, not a player, not an analytics platform. See `ARCHITECTURE.md` for
the boundary and `docs/adr/` for why each major choice was made.

## Status

P0, slice 0 of 16 (`PLAN.md`): control plane skeleton, config, migrations,
health endpoints. No media pipeline yet.

## Running

Requires Docker, or a local PostgreSQL 17.

```sh
cp .env.example .env
make up                       # postgres + control plane
curl localhost:8080/healthz   # liveness  (never touches the database)
curl localhost:8080/readyz    # readiness (fails when the database is down)
```

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
createdb transflux_test
TRANSFLUX_TEST_DATABASE_URL=postgres://$USER@localhost:5432/transflux_test?sslmode=disable \
  go test ./...               # adds integration tests
```

Integration tests skip with a message when `TRANSFLUX_TEST_DATABASE_URL` is
unset. The migration test drops the `public` schema, so point it at a
throwaway database.

## Layout

```
cmd/transflux/       control plane binary
internal/config/     environment configuration
internal/db/         pool + embedded forward-only migrations
docs/adr/            architecture decision records
docs/schema.sql      full schema proposal (design; migrations grow per slice)
ARCHITECTURE.md      planes, domain model, state machine, protocol, threat model
PLAN.md              phased implementation plan
```
