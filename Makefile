.PHONY: up down logs test test-all lint schema-check

up:    ; docker compose up --build -d
down:  ; docker compose down
logs:  ; docker compose logs -f control-plane

# Unit tests only. Suites needing Postgres or S3 skip themselves.
test:  ; go test ./...

# Everything, against the Compose stack. Needs `make up` first.
#
# Deliberately a SEPARATE database: the migration test drops the public schema,
# so pointing this at the application database would wipe your dev data.
test-all:
	@docker compose exec -T postgres psql -U transflux -d postgres \
		-c 'create database transflux_test' 2>/dev/null || true
	TRANSFLUX_TEST_DATABASE_URL=postgres://transflux:$${POSTGRES_PASSWORD:-transflux}@localhost:$${TRANSFLUX_PG_PORT:-7432}/transflux_test?sslmode=disable \
	TRANSFLUX_TEST_S3_ENDPOINT=http://localhost:$${TRANSFLUX_S3_PORT:-7900} \
	TRANSFLUX_TEST_S3_ACCESS_KEY=$${S3_ACCESS_KEY:-transflux} \
	TRANSFLUX_TEST_S3_SECRET_KEY=$${S3_SECRET_KEY:-transflux123} \
	go test ./... -count=1

lint:  ; go vet ./... && gofmt -l .

# docs/schema.sql is a design document, so prove it still applies.
# Needs a throwaway database: make schema-check DB=transflux_schemacheck
schema-check:
	psql -q -d $(DB) -c 'drop schema public cascade; create schema public'
	psql -v ON_ERROR_STOP=1 -q -d $(DB) -f docs/schema.sql
	@echo 'schema.sql applies cleanly'
