// Package fixtures provides typed test seeders for integration tests.
//
// Kept SEPARATE from testutil/testdb so the core test-DB helpers (Open,
// Truncate) have no feature imports — that lets feature packages (like
// `room`) import testdb without creating an import cycle.
//
// Usage:
//
//	//go:build integration
//
//	import (
//	    "nana/internal/testutil/testdb"
//	    "nana/internal/testutil/fixtures"
//	)
//
//	func TestX(t *testing.T) {
//	    db := testdb.Open(t)
//	    testdb.TruncateAll(t, db)
//	    apt := fixtures.SeedApartment(t, db)
//	    rm  := fixtures.SeedRoom(t, db, apt.ID.String(), "T-101")
//	    // ...
//	}
package fixtures

import (
	"fmt"
	"testing"
	"time"

	"nana/internal/apartment"
	"nana/internal/billingconfig"
	"nana/internal/contract"
	"nana/internal/meterreading"
	"nana/internal/moveout"
	"nana/internal/room"
	"nana/internal/tenant"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// SeedApartment creates a minimal apartment with default rates.
func SeedApartment(t *testing.T, db *gorm.DB) *apartment.Apartment {
	t.Helper()
	apt := &apartment.Apartment{
		Name:                   "TestApt",
		DisplayOrder:           1,
		ElectricityRatePerUnit: 800,  // ฿8/unit
		WaterRatePerUnit:       1800, // ฿18/unit
		Address:                "Test address",
	}
	if err := db.Create(apt).Error; err != nil {
		t.Fatalf("seed apartment: %v", err)
	}
	return apt
}

// SeedRoom creates a vacant room under the given apartment.
func SeedRoom(t *testing.T, db *gorm.DB, aptID, number string) *room.Room {
	t.Helper()
	rm := &room.Room{
		ApartmentID: mustParseUUID(t, aptID),
		Number:      number,
		Type:        room.RoomTypeAir,
		Floor:       1,
		BaseRent:    300000, // ฿3,000
		BaseDeposit: 300000,
		Status:      room.RoomStatusVacant,
	}
	if err := db.Create(rm).Error; err != nil {
		t.Fatalf("seed room: %v", err)
	}
	return rm
}

// SeedTenant creates a tenant with a unique ID card (time-based).
func SeedTenant(t *testing.T, db *gorm.DB) *tenant.Tenant {
	t.Helper()
	idCard := fmt.Sprintf("1%012d", time.Now().UnixNano()%1000000000000)
	tn := &tenant.Tenant{
		FullName:         "Test Tenant",
		IDCard:           idCard,
		Phone:            "0812345678",
		Address:          "Test address",
		EmergencyContact: "Test emergency 0898765432",
	}
	if err := db.Create(tn).Error; err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	return tn
}

// SeedContract creates an active contract for the given tenant/room with
// start_date set `monthsAgo` months in the past.
func SeedContract(t *testing.T, db *gorm.DB, tenantID, roomID string, monthsAgo int) *contract.Contract {
	t.Helper()
	apt := &apartment.Apartment{}
	var rm room.Room
	if err := db.Where("id = ?", roomID).First(&rm).Error; err != nil {
		t.Fatalf("load room for contract: %v", err)
	}
	if err := db.Where("id = ?", rm.ApartmentID).First(apt).Error; err != nil {
		t.Fatalf("load apartment for contract: %v", err)
	}
	start := truncateToDate(time.Now().UTC()).AddDate(0, -monthsAgo, 0)
	c := &contract.Contract{
		TenantID:               mustParseUUID(t, tenantID),
		RoomID:                 mustParseUUID(t, roomID),
		StartDate:              start,
		MinMonths:              6,
		MonthlyRent:            rm.BaseRent,
		DepositAmount:          rm.BaseDeposit,
		DepositStatus:          contract.DepositStatusCollected,
		ElectricityRatePerUnit: apt.ElectricityRatePerUnit,
		WaterRatePerUnit:       apt.WaterRatePerUnit,
		Status:                 contract.ContractStatusActive,
	}
	if err := db.Create(c).Error; err != nil {
		t.Fatalf("seed contract: %v", err)
	}
	if err := db.Model(&room.Room{}).Where("id = ?", roomID).
		Update("status", room.RoomStatusOccupied).Error; err != nil {
		t.Fatalf("occupy room: %v", err)
	}
	return c
}

// SeedExitMeter creates an EXIT meter reading for the given room.
func SeedExitMeter(t *testing.T, db *gorm.DB, roomID string, readingDate time.Time, elecUsed, waterUsed int) *meterreading.MeterReading {
	t.Helper()
	mr := &meterreading.MeterReading{
		RoomID:              mustParseUUID(t, roomID),
		ReadingType:         meterreading.ReadingTypeExit,
		ReadingDateActual:   &readingDate,
		ElectricityPrevious: 1000,
		ElectricityCurrent:  1000 + elecUsed,
		WaterPrevious:       100,
		WaterCurrent:        100 + waterUsed,
	}
	if err := db.Create(mr).Error; err != nil {
		t.Fatalf("seed exit meter: %v", err)
	}
	return mr
}

// SeedMoveOutNotice creates a PENDING_SETTLEMENT notice with the given
// actual_move_out_date.
func SeedMoveOutNotice(t *testing.T, db *gorm.DB, contractID string, actualMoveOut time.Time) *moveout.MoveOutNotice {
	t.Helper()
	notice := &moveout.MoveOutNotice{
		ContractID:           mustParseUUID(t, contractID),
		NoticeDate:           actualMoveOut.AddDate(0, 0, -7),
		ScheduledMoveOutDate: actualMoveOut,
		ActualMoveOutDate:    &actualMoveOut,
		Status:               moveout.MoveOutStatusPendingSettlement,
	}
	if err := db.Create(notice).Error; err != nil {
		t.Fatalf("seed move-out notice: %v", err)
	}
	return notice
}

// SeedCleaningFeeConfig creates a CLEANING_FEE billing config for the apartment.
func SeedCleaningFeeConfig(t *testing.T, db *gorm.DB, aptID string, amount int64) *billingconfig.BillingConfig {
	t.Helper()
	cfg := &billingconfig.BillingConfig{
		ApartmentID:   mustParseUUID(t, aptID),
		FeeType:       billingconfig.FeeTypeCleaningFee,
		DefaultAmount: amount,
		IsActive:      true,
	}
	if err := db.Create(cfg).Error; err != nil {
		t.Fatalf("seed cleaning fee config: %v", err)
	}
	return cfg
}

// SeedProrateConfig creates a PRORATE_DAILY_RATE billing config for the apartment.
// Settlement generation requires this row whenever a partial-month rent adjustment
// is computed (move-out before end-of-month with no PAID advance rent).
// Default seed/prod rate is ฿100/day (10000 satang).
func SeedProrateConfig(t *testing.T, db *gorm.DB, aptID string, dailyRate int64) *billingconfig.BillingConfig {
	t.Helper()
	cfg := &billingconfig.BillingConfig{
		ApartmentID:   mustParseUUID(t, aptID),
		FeeType:       billingconfig.FeeTypeProrateDailyRate,
		DefaultAmount: dailyRate,
		IsActive:      true,
	}
	if err := db.Create(cfg).Error; err != nil {
		t.Fatalf("seed prorate config: %v", err)
	}
	return cfg
}

// --- internal helpers ---

func mustParseUUID(t *testing.T, s string) uuid.UUID {
	t.Helper()
	id, err := uuid.Parse(s)
	if err != nil {
		t.Fatalf("parse uuid %q: %v", s, err)
	}
	return id
}

func truncateToDate(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
