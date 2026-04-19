.PHONY: dev build run migrate seed test test-integration test-integration-setup lint clean \
       smoke-settlement smoke-settlement-legacy smoke-settlement-all smoke-queue smoke-all smoke-install

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

# Test — unit tests (mocks, no DB)
test:
	go test ./... -v

test-coverage:
	go test ./... -coverprofile=coverage.out && go tool cover -html=coverage.out

# Integration tests — real PostgreSQL required.
# Creates a separate `nana_test` database alongside the dev DB so tests don't
# pollute dev data. Requires `make dev` to have started the postgres container.
test-integration-setup:
	docker compose -f docker-compose.dev.yml exec -T postgres \
		psql -U postgres -c "CREATE DATABASE nana_test" 2>/dev/null || true

test-integration: test-integration-setup
	# -p 1: integration test packages share one nana_test DB and TRUNCATE between
	# tests; running packages in parallel would let them wipe each other's data
	# mid-run. Serialize at the package level.
	NANA_TEST_DATABASE_URL="host=localhost port=5432 user=postgres password=postgres dbname=nana_test sslmode=disable TimeZone=Asia/Bangkok" \
	go test -tags=integration -p 1 ./... -count=1 -v

# Lint
lint:
	go vet ./...

# Smoke tests — dev-only Playwright suites (NOT CI).
# Requires: make dev (backend + frontend + postgres running)
# First time: make smoke-install
smoke-install:
	cd devtools/smoke && npm install

smoke-settlement:
	cd devtools/smoke && node playwright-test-settlement-scenario-smoke.js

smoke-settlement-legacy:
	cd devtools/smoke && node playwright-test-settlement-preview-legacy.js

smoke-settlement-all:
	cd devtools/smoke && node playwright-test-settlement-preview-legacy.js && node playwright-test-settlement-scenario-smoke.js

smoke-queue:
	cd devtools/smoke && node playwright-test-queue-settlement-smoke.js

smoke-all:
	cd devtools/smoke && node playwright-test-settlement-preview-legacy.js && node playwright-test-settlement-scenario-smoke.js && node playwright-test-queue-settlement-smoke.js

# Clean
clean:
	rm -rf ./tmp
