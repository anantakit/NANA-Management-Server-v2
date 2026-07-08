package seed

import (
	"fmt"
	"log/slog"
	"time"

	"nana/internal/apartment"
	"nana/internal/contract"
	"nana/internal/meterreading"
	"nana/internal/room"

	"gorm.io/gorm"
)

// recoveryRoomNumber is the dedicated นานาคอร์ท room for the Q1.6 Reading-Recovery
// E2E smoke. A211 is verified-free: it is seeded (A201-A211 fan floor 2) but
// referenced by NO other fixture (contracts stop at A107, tenant-history A108/109,
// move-out uses A201-A210, smoke uses B/C/D/E rooms). Keeping it isolated avoids
// the A207 double-booking that collided with move-out scenario #9.
const recoveryRoomNumber = "A211"

// seedDevRecoveryFixture sets up the Q1.6 Reading-Recovery starting state: A211
// gets an active contract and a meter history whose LAST reading electricity
// current is a wrong-high value (1500) while the room's true physical is ~1200.
// That is exactly the state the operator hits mid-entry — typing this cycle's
// real reading (lower than the last recorded value) is a forward breakage →
// "แก้ค่าที่จดผิด" → recovery from that source → backend derives the over-record
// (recorded 1500 − physical) → refund auto-emits at generation.
//
// NO pre-seeded recovery or bill — the smoke drives the whole chain through the
// UI. Water is deliberately clean (no over-record) so the smoke also exercises
// the per-utility derivation.
//
// Idempotent: skips when A211 already carries an active contract. Re-runnability
// for the mutating smoke is handled by ResetRecoverySmoke.
func seedDevRecoveryFixture(db *gorm.DB) error {
	var apt apartment.Apartment
	if err := db.Where("name = ?", "นานาคอร์ท").First(&apt).Error; err != nil {
		return fmt.Errorf("find apartment: %w", err)
	}

	var rm room.Room
	if err := db.Where("apartment_id = ? AND number = ?", apt.ID, recoveryRoomNumber).First(&rm).Error; err != nil {
		slog.Warn("recovery fixture: room not found", "room", recoveryRoomNumber, "err", err)
		return nil
	}

	var activeCount int64
	if err := db.Model(&contract.Contract{}).
		Where("room_id = ? AND status = ?", rm.ID, contract.ContractStatusActive).
		Count(&activeCount).Error; err != nil {
		return fmt.Errorf("check existing contract on %s: %w", recoveryRoomNumber, err)
	}
	if activeCount > 0 {
		return nil // already seeded
	}

	now := time.Now().UTC()
	cycleStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	twoMonthsAgo := cycleStart.AddDate(0, -2, 0).Format("2006-01")
	lastMonth := cycleStart.AddDate(0, -1, 0).Format("2006-01") // the mis-read cycle

	tn, err := upsertReconciliationTenant(db, "1100100100211", "ธนภร ทดสอบแก้ค่ามิเตอร์", "0890000211")
	if err != nil {
		return err
	}

	c := contract.Contract{
		TenantID:               tn.ID,
		RoomID:                 rm.ID,
		StartDate:              cycleStart.AddDate(0, -3, 0), // 3 months ago
		MinMonths:              6,
		MonthlyRent:            rm.BaseRent,
		DepositAmount:          rm.BaseDeposit,
		DepositStatus:          contract.DepositStatusCollected,
		ElectricityRatePerUnit: apt.ElectricityRatePerUnit,
		WaterRatePerUnit:       apt.WaterRatePerUnit,
		Status:                 contract.ContractStatusActive,
	}
	if err := db.Create(&c).Error; err != nil {
		return fmt.Errorf("create recovery contract: %w", err)
	}
	if err := db.Model(&rm).Update("status", room.RoomStatusOccupied).Error; err != nil {
		return fmt.Errorf("mark %s occupied: %w", recoveryRoomNumber, err)
	}

	// Meter history, no recovery and no bill. The last reading's electricity
	// CURRENT (1500) is the wrong-high value: physically the meter is ~1200, so
	// when the operator enters this cycle's real reading it reads lower than 1500
	// → a forward breakage → recovery-from-source. Water is clean.
	readings := []meterreading.MeterReading{
		{
			RoomID: rm.ID, ReadingType: meterreading.ReadingTypeMonthly, BillingMonth: &twoMonthsAgo,
			ElectricityPrevious: 1000, ElectricityCurrent: 1200,
			WaterPrevious: 180, WaterCurrent: 200,
		},
		{
			RoomID: rm.ID, ReadingType: meterreading.ReadingTypeMonthly, BillingMonth: &lastMonth,
			ElectricityPrevious: 1200, ElectricityCurrent: 1500, // mis-recorded high
			WaterPrevious: 200, WaterCurrent: 220,
		},
	}
	if err := db.Create(&readings).Error; err != nil {
		return fmt.Errorf("create recovery meter history: %w", err)
	}

	slog.Info("seeded recovery smoke fixture (Q1.6 clean start)", "room", recoveryRoomNumber, "source_month", lastMonth)
	return nil
}

// ResetRecoverySmoke restores the recovery room to the clean Q1.6 starting state
// so the mutating E2E smoke can re-run. It removes everything the smoke can
// create — bills and their dependents (audit + deliveries + line items +
// payments), plus recovery meter rows and any current-month reading — then
// re-asserts the wrong-high last reading. Contract + tenant + the two seeded
// history rows stay. Idempotent.
//
// Delete order respects FKs: bill_audit_log + bill_deliveries are RESTRICT so
// they go before bills; bill_line_items + bill_payments cascade with bills.
// bill_line_items carry an ON DELETE RESTRICT FK to meter_readings
// (adjustment_recovery_reading_id), so bills (→ line items) are removed before
// the recovery meter rows.
func ResetRecoverySmoke(db *gorm.DB) error {
	var apt apartment.Apartment
	if err := db.Where("name = ?", "นานาคอร์ท").First(&apt).Error; err != nil {
		return fmt.Errorf("find apartment: %w", err)
	}
	var rm room.Room
	if err := db.Where("apartment_id = ? AND number = ?", apt.ID, recoveryRoomNumber).First(&rm).Error; err != nil {
		return nil // nothing seeded yet
	}
	var c contract.Contract
	if err := db.Where("room_id = ? AND status = ?", rm.ID, contract.ContractStatusActive).First(&c).Error; err != nil {
		return nil // fixture not present
	}

	billIDs := `SELECT id FROM bills WHERE contract_id = ?`
	for _, stmt := range []string{
		`DELETE FROM bill_audit_log WHERE bill_id IN (` + billIDs + `)`,
		`DELETE FROM bill_deliveries WHERE bill_id IN (` + billIDs + `)`,
		`DELETE FROM bills WHERE contract_id = ?`, // cascades bill_line_items + bill_payments
	} {
		if err := db.Exec(stmt, c.ID).Error; err != nil {
			return fmt.Errorf("reset (%.40s): %w", stmt, err)
		}
	}
	if err := db.Exec(`DELETE FROM meter_readings WHERE room_id = ? AND anchor_reason = ?`,
		rm.ID, meterreading.AnchorReasonReadingRecovery).Error; err != nil {
		return fmt.Errorf("reset: delete recovery rows: %w", err)
	}
	if err := db.Exec(`DELETE FROM meter_readings WHERE room_id = ? AND reading_type = ? AND billing_month = ?`,
		rm.ID, meterreading.ReadingTypeMonthly, time.Now().UTC().Format("2006-01")).Error; err != nil {
		return fmt.Errorf("reset: delete current-month reading: %w", err)
	}

	// Re-assert the wrong-high last reading in case the smoke corrected it.
	lastMonth := time.Date(time.Now().UTC().Year(), time.Now().UTC().Month(), 1, 0, 0, 0, 0, time.UTC).
		AddDate(0, -1, 0).Format("2006-01")
	if err := db.Exec(`UPDATE meter_readings SET electricity_current = 1500, water_current = 220
		WHERE room_id = ? AND reading_type = ? AND billing_month = ? AND anchor_reason IS NULL`,
		rm.ID, meterreading.ReadingTypeMonthly, lastMonth).Error; err != nil {
		return fmt.Errorf("reset: restore wrong-high last reading: %w", err)
	}
	return nil
}
