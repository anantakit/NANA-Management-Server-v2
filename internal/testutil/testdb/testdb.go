// Package testdb provides a PostgreSQL test DB helper for integration tests.
//
// Usage (integration tests):
//
//	//go:build integration
//
//	func TestX(t *testing.T) {
//	    db := testdb.Open(t)
//	    testdb.Truncate(t, db, "bills", "bill_line_items")
//	    // ...test...
//	}
//
// Env vars:
//   - NANA_TEST_DATABASE_URL — full DSN (either URL or key=value form).
//     Defaults to the docker-compose.dev.yml test DB.
//
// If the DB is unreachable, tests SKIP (not fail) — makes it safe to run
// the default `go test ./...` without Postgres available.
package testdb

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"nana/internal/shared/database"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// Default DSN — matches docker-compose.dev.yml credentials, points to a
// separate `nana_test` database (must be created before running tests;
// see Makefile target `test-integration-setup`).
const defaultDSN = "host=localhost port=5432 user=postgres password=postgres dbname=nana_test sslmode=disable TimeZone=Asia/Bangkok"

var (
	once     sync.Once
	sharedDB *gorm.DB
	openErr  error
)

// Open returns a shared *gorm.DB pointing to the test database.
// On first call, connects and runs migrations. Subsequent calls return the
// cached connection. If the DB is unreachable, the test is skipped.
func Open(t *testing.T) *gorm.DB {
	t.Helper()
	once.Do(func() { sharedDB, openErr = connect() })
	if openErr != nil {
		t.Skipf("integration test skipped (test DB unreachable): %v", openErr)
	}
	return sharedDB
}

func connect() (*gorm.DB, error) {
	dsn := os.Getenv("NANA_TEST_DATABASE_URL")
	if dsn == "" {
		dsn = defaultDSN
	}
	logLevel := gormlogger.Silent
	if os.Getenv("NANA_TEST_LOG_SQL") == "1" {
		logLevel = gormlogger.Info
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger:                 gormlogger.Default.LogMode(logLevel),
		SkipDefaultTransaction: true,
	})
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("unwrap sql.DB: %w", err)
	}
	if err := sqlDB.Ping(); err != nil {
		return nil, fmt.Errorf("ping: %w", err)
	}
	if err := database.RunMigrations(db); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return db, nil
}

// Truncate wipes the given tables. Use at the start of every test that writes
// data, so tests don't pollute each other.
//
// RESTART IDENTITY resets sequences; CASCADE handles dependent tables so
// caller doesn't need to list FK children explicitly.
func Truncate(t *testing.T, db *gorm.DB, tables ...string) {
	t.Helper()
	if len(tables) == 0 {
		return
	}
	// SAFE: `tables` is always composed of Go string literals from test code
	// (see TruncateAll and direct Truncate call sites). Never user input.
	// Go's database/sql does not bind identifiers, so string-formatted SQL
	// is the standard approach for DDL/identifier positions.
	stmt := fmt.Sprintf("TRUNCATE TABLE %s RESTART IDENTITY CASCADE", strings.Join(tables, ", "))
	if err := db.Exec(stmt).Error; err != nil {
		t.Fatalf("truncate %v: %v", tables, err)
	}
}

// TruncateAll wipes every data table (in FK-safe CASCADE order).
// Convenience for tests that want a fully-clean slate.
func TruncateAll(t *testing.T, db *gorm.DB) {
	t.Helper()
	Truncate(t, db,
		"bill_audit_log",
		"bill_line_items",
		"bills",
		"move_out_notices",
		"meter_readings",
		"billing_configs",
		"contracts",
		"tenants",
		"rooms",
		"apartment_bank_accounts",
		"apartments",
		"refresh_tokens",
		"users",
	)
}
