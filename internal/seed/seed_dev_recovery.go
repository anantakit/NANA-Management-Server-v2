package seed

import (
	"fmt"
	"log/slog"
	"time"

	"nana/internal/apartment"
	"nana/internal/billing"
	"nana/internal/contract"
	"nana/internal/meterreading"
	"nana/internal/room"

	"gorm.io/gorm"
)

// seedDevRecoveryFixture creates ONE deterministic Reading-Recovery scenario for
// the P5 owner smoke (playwright-test-reading-recovery-smoke.js): room A207 in
// นานาคอร์ท gets an active contract, a DRAFT monthly bill, and a PENDING
// source-less recovery. That is exactly enough state to drive the operator
// journey — see the "รอตัดสินใจ" badge on the bill list, open the bill, resolve
// the recovery (3-way + 2a), see finalize blocked while pending, then finalize
// after resolving.
//
// The invariants themselves are locked by the Go integration suite
// (service_recovery_canonical_integration_test.go RR01-RR09); this fixture is
// only for the UI smoke + owner manual walkthrough.
//
// Idempotent: skips when A207 already carries an active contract. Room choice
// (A207) is a confirmed-vacant นานาคอร์ท room with no prior tenant history,
// adjacent to the reconciliation fixtures (A205/A206). Delete this file + its
// caller in seed.go when a real regression environment replaces the smoke.
func seedDevRecoveryFixture(db *gorm.DB) error {
	var apt apartment.Apartment
	if err := db.Where("name = ?", "นานาคอร์ท").First(&apt).Error; err != nil {
		return fmt.Errorf("find apartment: %w", err)
	}

	var rm room.Room
	if err := db.Where("apartment_id = ? AND number = ?", apt.ID, "A207").First(&rm).Error; err != nil {
		slog.Warn("recovery fixture: room A207 not found", "err", err)
		return nil
	}

	var activeCount int64
	if err := db.Model(&contract.Contract{}).
		Where("room_id = ? AND status = ?", rm.ID, contract.ContractStatusActive).
		Count(&activeCount).Error; err != nil {
		return fmt.Errorf("check existing contract on A207: %w", err)
	}
	if activeCount > 0 {
		return nil // already seeded
	}

	now := time.Now().UTC()
	cycleStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	billingMonth := cycleStart.Format("2006-01")
	recoveryMonth := cycleStart.AddDate(0, -1, 0).Format("2006-01") // last month — the mis-read cycle

	tn, err := upsertReconciliationTenant(db, "1100100100207", "ธนภร ทดสอบแก้ค่ามิเตอร์", "0890000207")
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
		return fmt.Errorf("mark A207 occupied: %w", err)
	}

	// DRAFT monthly bill (with AUTO lines so it can finalize once the recovery
	// is resolved).
	bill := billing.Bill{
		ContractID:   c.ID,
		BillingMonth: billingMonth,
		BillType:     billing.BillTypeMonthly,
		Status:       billing.BillStatusDraft,
	}
	if err := db.Create(&bill).Error; err != nil {
		return fmt.Errorf("create recovery draft bill: %w", err)
	}
	lines := []billing.BillLineItem{
		{BillID: bill.ID, LineType: billing.LineItemRoomRent, Source: billing.LineItemSourceAuto, Description: "ค่าเช่า", Amount: rm.BaseRent, SortOrder: 1},
		{BillID: bill.ID, LineType: billing.LineItemElectricity, Source: billing.LineItemSourceAuto, Description: "ค่าไฟฟ้า", Amount: 80000, Quantity: 100, UnitPrice: 800, SortOrder: 2},
		{BillID: bill.ID, LineType: billing.LineItemWater, Source: billing.LineItemSourceAuto, Description: "ค่าน้ำ", Amount: 36000, Quantity: 20, UnitPrice: 1800, SortOrder: 3},
	}
	if err := db.Create(&lines).Error; err != nil {
		return fmt.Errorf("create recovery bill line items: %w", err)
	}

	// Source reading (the mis-read cycle) so the 2a calculator has a meter
	// delta to suggest from — the smoke exercises the [ใช้ยอดนี้] path.
	src := meterreading.MeterReading{
		RoomID:              rm.ID,
		ReadingType:         meterreading.ReadingTypeMonthly,
		BillingMonth:        &recoveryMonth,
		ElectricityPrevious: 1300,
		ElectricityCurrent:  1400,
		WaterPrevious:       190,
		WaterCurrent:        200,
	}
	if err := db.Create(&src).Error; err != nil {
		return fmt.Errorf("create recovery source reading: %w", err)
	}

	// PENDING recovery for the room (prev=curr per migration 00040), pointing at
	// the source so the resolve surface can show a suggested amount.
	reason := meterreading.AnchorReasonReadingRecovery
	note := fmt.Sprintf("จดมิเตอร์ผิดเดือน %s — บันทึกค่าที่ถูกต้อง", recoveryMonth)
	rec := meterreading.MeterReading{
		RoomID:                  rm.ID,
		ReadingType:             meterreading.ReadingTypeMonthly,
		BillingMonth:            &billingMonth,
		ElectricityPrevious:     1500,
		ElectricityCurrent:      1500,
		WaterPrevious:           220,
		WaterCurrent:            220,
		AnchorReason:            &reason,
		AnchorNote:              &note,
		RecoverySourceReadingID: &src.ID,
	}
	if err := db.Create(&rec).Error; err != nil {
		return fmt.Errorf("create pending recovery: %w", err)
	}

	slog.Info("seeded recovery smoke fixture", "room", "A207", "bill_month", billingMonth)
	return nil
}
