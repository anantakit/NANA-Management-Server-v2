//go:build integration

package billing

import (
	"context"
	"errors"
	"testing"
	"time"

	"nana/internal/testutil/fixtures"
	"nana/internal/testutil/testdb"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// FindApartmentIDByRoomID is a tiny repo method, but a real regression-prone
// seam: it's called by settlement's addConfigFees to resolve room → apartment.
//
// History: the original implementation used gorm.Scan(&uuid.UUID) which
// silently failed in pgx text-encoding and caused config-driven fees
// (CLEANING_FEE, KEY_SERVICE) to vanish from settlement bills in some
// connection states.
//
// These tests pin the three contracts that matter, so any future regression
// localizes HERE (single repo method) instead of surfacing as missing line
// items several layers up the stack.

func TestFindApartmentIDByRoomID_ReturnsApartmentID(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)

	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "T-101")

	repo := NewBillingRepository(db)
	got, err := repo.FindApartmentIDByRoomID(context.Background(), rm.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != apt.ID {
		t.Fatalf("apartment_id = %s, want %s", got, apt.ID)
	}
}

func TestFindApartmentIDByRoomID_UnknownRoomReturnsNotFound(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)

	repo := NewBillingRepository(db)
	_, err := repo.FindApartmentIDByRoomID(context.Background(), uuid.New())
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("expected gorm.ErrRecordNotFound, got %v", err)
	}
}

func TestFindApartmentIDByRoomID_SoftDeletedRoomReturnsNotFound(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)

	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "T-102")

	// Soft-delete the room the way GORM would — set deleted_at.
	now := time.Now().UTC()
	if err := db.Table("rooms").Where("id = ?", rm.ID).Update("deleted_at", now).Error; err != nil {
		t.Fatalf("soft-delete room: %v", err)
	}

	repo := NewBillingRepository(db)
	_, err := repo.FindApartmentIDByRoomID(context.Background(), rm.ID)
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("expected gorm.ErrRecordNotFound for soft-deleted room, got %v", err)
	}
}

// TestFindApartmentIDByRoomID_UUIDDecodingIsDriverAgnostic is the specific
// regression guard for the Scan(&uuid.UUID)-into-[16]byte bug. The earlier
// implementation worked in some pgx connection states and failed in others;
// this test ensures we always return a parseable UUID by round-tripping
// value → string → uuid and asserting equality.
func TestFindApartmentIDByRoomID_UUIDDecodingIsDriverAgnostic(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)

	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "T-103")

	repo := NewBillingRepository(db)
	got, err := repo.FindApartmentIDByRoomID(context.Background(), rm.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Round-trip: the returned uuid.UUID must stringify into the canonical
	// form that matches what was seeded. Catches cases where 16 bytes get
	// populated with text-encoded ASCII.
	if got.String() != apt.ID.String() {
		t.Fatalf("UUID round-trip mismatch: got=%s want=%s", got.String(), apt.ID.String())
	}
	// Also check bytes aren't ASCII garbage (paranoid guard for the specific
	// regression: if the string "xxxxxxxx-..." got written byte-by-byte into
	// [16]byte, the first byte would be 'x' (0x78) — never the case for a
	// real UUID v4 which has 0x40|rand in byte 6.
	raw := got[6]
	if raw&0xF0 != 0x40 {
		t.Errorf("UUID byte 6 version nibble = 0x%X, expected 0x4_ (v4) — looks like ASCII-corrupted decode", raw)
	}
}
