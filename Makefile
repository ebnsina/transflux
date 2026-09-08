.PHONY: up down test lint

up:    ; docker compose up --build -d
down:  ; docker compose down
test:  ; go test ./...
lint:  ; go vet ./... && gofmt -l .
