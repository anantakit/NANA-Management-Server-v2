//go:build integration

package room

import (
	"context"
	"testing"
	"time"

	"nana/internal/apartment"
	"nana/internal/testutil/testdb"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// FindRoomIDsByApartment had the same UUID-scan-as-bytes bug as
// billing.FindApartmentIDByRoomID — Pluck into []uuid.UUID silently
// corrupts when pgx returns UUIDs in text encoding. These tests pin the
// fix so it can't regress.
//
// Note: the room package CAN'T import internal/testutil/fixtures because
// fixtures imports room (cycle). Tests below seed inline.

func TestFindRoomIDsByApartment_ReturnsAllRoomIDs(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)

	apt := seedTestApartment(t, db)
	rm1 := seedTestRoom(t, db, apt.ID, "T-101")
	rm2 := seedTestRoom(t, db, apt.ID, "T-102")
	rm3 := seedTestRoom(t, db, apt.ID, "T-103")

	repo := NewRoomRepository(db)
	got, err := repo.FindRoomIDsByApartment(context.Background(), apt.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d ids, want 3", len(got))
	}

	// Round-trip: every returned ID must equal one of the seeded IDs.
	want := map[uuid.UUID]bool{rm1.ID: true, rm2.ID: true, rm3.ID: true}
	for _, id := range got {
		if !want[id] {
			t.Errorf("unexpected room ID returned: %s", id)
		}
		// Defense against ASCII-corrupted decode (same paranoid guard as
		// billing.FindApartmentIDByRoomID test). v4 UUIDs have 0x4_ in byte 6.
		if id[6]&0xF0 != 0x40 {
			t.Errorf("UUID byte 6 version nibble = 0x%X, expected 0x4_ — looks ASCII-corrupted", id[6])
		}
	}
}

func TestFindRoomIDsByApartment_EmptyApartmentReturnsEmpty(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)

	apt := seedTestApartment(t, db)
	// No rooms seeded.

	repo := NewRoomRepository(db)
	got, err := repo.FindRoomIDsByApartment(context.Background(), apt.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d ids, want 0", len(got))
	}
}

func TestFindRoomIDsByApartment_SoftDeletedRoomsExcluded(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)

	apt := seedTestApartment(t, db)
	keep := seedTestRoom(t, db, apt.ID, "T-201")
	deleted := seedTestRoom(t, db, apt.ID, "T-202")

	now := time.Now().UTC()
	if err := db.Table("rooms").Where("id = ?", deleted.ID).Update("deleted_at", now).Error; err != nil {
		t.Fatalf("soft-delete room: %v", err)
	}

	repo := NewRoomRepository(db)
	got, err := repo.FindRoomIDsByApartment(context.Background(), apt.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d ids, want 1 (soft-deleted should be excluded)", len(got))
	}
	if got[0] != keep.ID {
		t.Errorf("returned id %s, want %s", got[0], keep.ID)
	}
}

// --- inline fixtures (cannot use testutil/fixtures due to import cycle) ---

func seedTestApartment(t *testing.T, db *gorm.DB) *apartment.Apartment {
	t.Helper()
	apt := &apartment.Apartment{
		Name:                   "TestApt",
		DisplayOrder:           1,
		ElectricityRatePerUnit: 800,
		WaterRatePerUnit:       1800,
	}
	if err := db.Create(apt).Error; err != nil {
		t.Fatalf("seed apartment: %v", err)
	}
	return apt
}

func seedTestRoom(t *testing.T, db *gorm.DB, aptID uuid.UUID, number string) *Room {
	t.Helper()
	rm := &Room{
		ApartmentID: aptID,
		Number:      number,
		Type:        RoomTypeAir,
		Floor:       1,
		BaseRent:    300000,
		BaseDeposit: 300000,
		Status:      RoomStatusVacant,
	}
	if err := db.Create(rm).Error; err != nil {
		t.Fatalf("seed room: %v", err)
	}
	return rm
}
