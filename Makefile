.PHONY: dev build run migrate seed test test-integration test-integration-setup lint clean \
       smoke-settlement smoke-settlement-preview smoke-settlement-all smoke-draft smoke-moveout-step23 smoke-moveout-step4 smoke-moveout-detail smoke-bills-list smoke-bill-edit smoke-delivery-mode smoke-meter-focus-local-first smoke-settlement-recovery smoke-settlement-recovery-presentation smoke-recovery-survives-cancel smoke-operator-overread smoke-exit-meter-flags smoke-exit-meter-flags-ui smoke-settlement-exit-meter-edit smoke-moveout-exit-meter-e2e smoke-all smoke-install

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

smoke-settlement-preview:
	cd devtools/smoke && node playwright-test-settlement-preview-smoke.js

# Epic B settlement recovery — HTTP smoke (no browser; drives move-out settlement
# endpoints directly to prove the D1 fix through the real handler chain).
smoke-settlement-recovery:
	cd devtools/smoke && node smoke-settlement-recovery-http.js

# Epic B settlement recovery — PRESENTATION smoke (browser; self-seeding). Proves
# the FE render + provenance wiring through the canonical /bills/:id entry +
# move-out BreakdownPanel parity. Needs `make dev` (frontend :3001) + `make
# smoke-install` once. (SMOKE_HEADED=1 to watch.)
smoke-settlement-recovery-presentation:
	cd devtools/smoke && node playwright-test-settlement-recovery-presentation-smoke.js

# Epic B — "Recovery survives Move-out cancel" capability smoke (browser; self-seeding).
# over-record via UI -> settlement Model B -> cancel -> monthly refunds the
# over-charge exactly once -> no duplicate recovery. Needs `make dev` (frontend
# :3001) + `make smoke-install` once. (SMOKE_HEADED=1 to watch.)
smoke-recovery-survives-cancel:
	cd devtools/smoke && node playwright-test-recovery-survives-cancel-smoke.js

# Operator end-to-end (F1+F2) — HTTP smoke: recovery → exit (same month) →
# settlement, all via real endpoints, no seeded terminal state.
smoke-operator-overread:
	cd devtools/smoke && node smoke-operator-moveout-overread-http.js

# F1 exit-meter rollover/replacement — full operator-to-bill HTTP smoke: record
# EXIT with the hardware flag → generate settlement → assert the real bill's meter
# line (usage/unit_price/amount/meter_previous) + total + finalize.
smoke-exit-meter-flags:
	cd devtools/smoke && node smoke-exit-meter-flags-http.js

# F1 exit-meter — operator UI reachability (browser): Queue → RoomWorkflowDrawer →
# MeterStep, desktop + mobile. Proves the toggles are usable on the canonical
# surface (below-prev blocks until the flag unblocks it) — what the HTTP smoke
# bypasses. Needs `make dev` (frontend on :3001) + `make smoke-install` once.
smoke-exit-meter-flags-ui:
	cd devtools/smoke && node playwright-test-exit-meter-flags-smoke.js

# F2 Slice 2A — Settlement exit-meter EDIT path (browser): SettlementPage →
# "แก้ไข" pencil → ExitMeterDrawer → updateExitMeter → settlement re-init.
# Regression gate for the live edit surface (what the MeterStep UI smoke does
# NOT cover). Needs `make dev` (frontend on :3001) + `make smoke-install` once.
smoke-settlement-exit-meter-edit:
	cd devtools/smoke && node playwright-test-settlement-exit-meter-edit-smoke.js

# Full move-out lifecycle e2e (browser) WITH rollover + replacement flags:
# Queue → MeterStep record(+flag) → settlement → finalize → payment → close,
# cross-checking the flagged usage in the persisted settlement bill. Needs
# `make dev` (frontend :3001) + `make smoke-install` once.
smoke-moveout-exit-meter-e2e:
	cd devtools/smoke && node playwright-test-moveout-exit-meter-e2e-smoke.js

smoke-settlement-all:
	cd devtools/smoke && node playwright-test-settlement-preview-smoke.js && node playwright-test-settlement-scenario-smoke.js

smoke-draft:
	cd devtools/smoke && node playwright-test-draft-settlement-smoke.js

smoke-moveout-step23:
	cd devtools/smoke && node playwright-test-moveout-step23-smoke.js

smoke-moveout-step4:
	cd devtools/smoke && node playwright-test-moveout-step4-smoke.js

smoke-moveout-detail:
	cd devtools/smoke && node playwright-test-moveout-detail-smoke.js

smoke-bills-list:
	cd devtools/smoke && node playwright-test-bills-list-smoke.js

smoke-bill-edit:
	cd devtools/smoke && node playwright-test-bill-edit-smoke.js

smoke-delivery-mode:
	cd devtools/smoke && node playwright-test-delivery-mode-smoke.js

smoke-meter-focus-local-first:
	cd devtools/smoke && node playwright-test-meter-focus-local-first-smoke.js

smoke-all:
	cd devtools/smoke && node playwright-test-settlement-preview-smoke.js && node playwright-test-settlement-scenario-smoke.js && node playwright-test-draft-settlement-smoke.js && node playwright-test-draft-numeric-smoke.js && node playwright-test-moveout-step23-smoke.js && node playwright-test-moveout-step4-smoke.js && node playwright-test-moveout-detail-smoke.js && node playwright-test-bills-list-smoke.js && node playwright-test-bill-edit-smoke.js && node playwright-test-delivery-mode-smoke.js && node playwright-test-meter-focus-local-first-smoke.js && node playwright-test-settlement-exit-meter-edit-smoke.js

# Clean
clean:
	rm -rf ./tmp
