.PHONY: up down test lint schema-check

up:    ; docker compose up --build -d
down:  ; docker compose down
test:  ; go test ./...
lint:  ; go vet ./... && gofmt -l .

# docs/schema.sql is a design document, so prove it still applies.
# Needs a throwaway database: make schema-check DB=transflux_schemacheck
schema-check:
	psql -q -d $(DB) -c 'drop schema public cascade; create schema public'
	psql -v ON_ERROR_STOP=1 -q -d $(DB) -f docs/schema.sql
	@echo 'schema.sql applies cleanly'
