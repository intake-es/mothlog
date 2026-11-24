DATABASE_URL ?= postgres://mothlog:mothlog@127.0.0.1:5433/mothlog?sslmode=disable

.PHONY: run test lint carga

# Prueba de carga de la ingesta con --cpus=0.25 --memory=256m; ver README.
# Opciones: make carga CARGA='-ref main -concurrencia 1,8,32 -salida carga-main'
CARGA ?=

run:
	docker compose up -d --wait db
	DATABASE_URL='$(DATABASE_URL)' go run ./cmd/mothlog

test:
	go test ./...

lint:
	go vet ./...
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "sin formatear:"; echo "$$out"; exit 1; fi
	golangci-lint run

carga:
	go run ./tools/carga $(CARGA)
