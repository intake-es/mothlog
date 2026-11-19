DATABASE_URL ?= postgres://intake_errors:intake_errors@127.0.0.1:5433/intake_errors?sslmode=disable

.PHONY: run test lint

run:
	docker compose up -d --wait db
	DATABASE_URL='$(DATABASE_URL)' go run ./cmd/intake-errors

test:
	go test ./...

lint:
	go vet ./...
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "sin formatear:"; echo "$$out"; exit 1; fi
	golangci-lint run
