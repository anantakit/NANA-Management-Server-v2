.PHONY: dev build run migrate seed test lint clean

# Development
dev:
	docker compose -f docker-compose.dev.yml up --build

dev-down:
	docker compose -f docker-compose.dev.yml down

dev-clean:
	docker compose -f docker-compose.dev.yml down -v

# Build
build:
	go build -o ./tmp/server ./cmd/main.go

run:
	go run ./cmd/main.go

# Database
migrate:
	go run ./cmd/main.go -migrate-only

# Test
test:
	go test ./... -v

test-coverage:
	go test ./... -coverprofile=coverage.out && go tool cover -html=coverage.out

# Lint
lint:
	go vet ./...

# Clean
clean:
	rm -rf ./tmp
