.PHONY: dev server web build run test

# Development: run `make server` and `make web` in two terminals,
# then open http://localhost:5173 (hot reload).
server:
	cd server && go run ./cmd/nuevo -web ""

web:
	cd web && npm run dev

# Production-style: build everything, then one process serves it all on :8080.
build:
	cd web && npm ci && npm run build
	cd server && go build -o bin/nuevo ./cmd/nuevo

run: build
	cd server && ./bin/nuevo

test:
	cd server && go vet ./... && go test -race ./...
	cd web && npm run typecheck
