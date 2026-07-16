package seed

import (
	"errors"
	"fmt"
	"time"

	"nana/internal/apartment"
	"nana/internal/billing"
	"nana/internal/contract"
	"nana/internal/meterreading"
	"nana/internal/moveout"
	"nana/internal/room"

	"gorm.io/gorm"
)

// settlementRecoveryRoomNumber is the dedicated นานาคอร์ท room for the Epic B
// settlement-recovery HTTP smoke. Distinct from A211 (monthly recovery smoke) and
// the A201-A210 move-out rooms, so the two mutating smokes never collide.
const settlementRecoveryRoomNumber = "SR-SETTLE"

// SettlementRecoverySmokeFixture is the JSON the dev endpoint returns to the HTTP
// smoke — everything it needs to drive the chain without touching the DB.
type SettlementRecoverySmokeFixture struct {
	ApartmentID     string `json:"apartment_id"`
	RoomID          string `json:"room_id"`
	ContractID      string `json:"contract_id"`
	NoticeID        string `json:"notice_id"` // PENDING_METER — awaiting the move-out reading
	SourceReadingID string `json:"source_reading_id"`
	// Epic B Model B: the operator observes the meter ONCE at move-out and records
	// it via record-exit-meter with the over-record flag ("เดือนก่อนจดเกิน"). The
	// source month recorded a wrong-high value; the refund = recorded − observed.
	SourceRecordedElec   int   `json:"source_recorded_electricity"`  // 1500 — the mis-record on the source bill
	ExitObservationElec  int   `json:"exit_observation_electricity"` // 1240 — true current observed at move-out
	ExitObservationWater int   `json:"exit_observation_water"`       // 228  — water (not over-recorded)
	SourceRate           int64 `json:"source_electricity_rate"`      // satang/unit the source bill charged
	ExpectedRefund       int64 `json:"expected_refund"`              // -(recorded − observed) × rate the resolver must emit
}

// SetupSettlementRecoverySmoke (re)creates a clean move-out fixture for the Epic B
// settlement-recovery HTTP smoke and returns the IDs the smoke drives.
//
// State: a dedicated room with an active contract; a mis-read source month whose
// electricity CURRENT is a wrong-high 1500 (true physical ~1200) and whose
// FINALIZED source bill charged those 300 phantom units at นานาคอร์ท's rate (the
// S0 + rate basis); an EXIT reading; and a PENDING_SETTLEMENT notice. The smoke
// then drives generate → baseline-correction (physical 1200) → finalize (blocked)
// → regenerate → finalize → correct entirely over HTTP. Nothing terminal is
// seeded — the refund is produced by the real resolver.
//
// Re-runnable: wipes the room's prior bills/notices/readings first, so the
// mutating smoke can run repeatedly.
func SetupSettlementRecoverySmoke(db *gorm.DB) (*SettlementRecoverySmokeFixture, error) {
	const (
		wrongElec    = 1500 // what the source month recorded (and billed)
		physicalElec = 1200 // the true reading the operator enters this cycle
		waterReading = 220  // water is clean (not over-recorded)
		sourceRate   = int64(800)
	)

	var apt apartment.Apartment
	if err := db.Where("name = ?", "นานาคอร์ท").First(&apt).Error; err != nil {
		return nil, fmt.Errorf("find apartment: %w", err)
	}

	// Find-or-create the dedicated room.
	var rm room.Room
	err := db.Where("apartment_id = ? AND number = ?", apt.ID, settlementRecoveryRoomNumber).First(&rm).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		rm = room.Room{
			ApartmentID: apt.ID, Number: settlementRecoveryRoomNumber, Type: room.RoomTypeAir,
			Floor: 9, BaseRent: 500000, BaseDeposit: 500000, Status: room.RoomStatusVacant,
		}
		if err := db.Create(&rm).Error; err != nil {
			return nil, fmt.Errorf("create smoke room: %w", err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("find smoke room: %w", err)
	}

	// Find-or-create the active contract (one per room — never duplicate).
	var c contract.Contract
	err = db.Where("room_id = ? AND status = ?", rm.ID, contract.ContractStatusActive).First(&c).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		tn, tErr := upsertReconciliationTenant(db, "1100100100290", "สมชาย ทดสอบปิดสัญญา", "0890000290")
		if tErr != nil {
			return nil, tErr
		}
		cycleStart := time.Date(time.Now().UTC().Year(), time.Now().UTC().Month(), 1, 0, 0, 0, 0, time.UTC)
		c = contract.Contract{
			TenantID: tn.ID, RoomID: rm.ID,
			StartDate: cycleStart.AddDate(0, -8, 0), MinMonths: 6,
			MonthlyRent: rm.BaseRent, DepositAmount: rm.BaseDeposit, DepositStatus: contract.DepositStatusCollected,
			ElectricityRatePerUnit: apt.ElectricityRatePerUnit, WaterRatePerUnit: apt.WaterRatePerUnit,
			Status: contract.ContractStatusActive,
		}
		if err := db.Create(&c).Error; err != nil {
			return nil, fmt.Errorf("create smoke contract: %w", err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("find smoke contract: %w", err)
	}
	if err := db.Model(&room.Room{}).Where("id = ?", rm.ID).Update("status", room.RoomStatusOccupied).Error; err != nil {
		return nil, fmt.Errorf("occupy smoke room: %w", err)
	}

	// Reset the room's mutable artifacts (FK-safe: notices reference bills via
	// settlement_bill_id, so they go first; audit + deliveries RESTRICT before
	// bills; bills cascade line items + payments; readings last).
	billIDs := `SELECT id FROM bills WHERE contract_id = ?`
	if err := db.Exec(`DELETE FROM move_out_notices WHERE contract_id = ?`, c.ID).Error; err != nil {
		return nil, fmt.Errorf("reset notices: %w", err)
	}
	for _, stmt := range []string{
		`DELETE FROM bill_audit_log WHERE bill_id IN (` + billIDs + `)`,
		`DELETE FROM bill_deliveries WHERE bill_id IN (` + billIDs + `)`,
		`DELETE FROM bills WHERE contract_id = ?`,
	} {
		if err := db.Exec(stmt, c.ID).Error; err != nil {
			return nil, fmt.Errorf("reset bills: %w", err)
		}
	}
	if err := db.Exec(`DELETE FROM meter_readings WHERE room_id = ?`, rm.ID).Error; err != nil {
		return nil, fmt.Errorf("reset readings: %w", err)
	}

	now := time.Now().UTC()
	sourceMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0).Format("2006-01")

	// Mis-read source month reading: elec current 1500 (wrong-high). This IS the
	// evidence — CreateBaselineCorrection derives recorded=1500 from it.
	src := meterreading.MeterReading{
		RoomID: rm.ID, ReadingType: meterreading.ReadingTypeMonthly, BillingMonth: &sourceMonth,
		ElectricityPrevious: physicalElec, ElectricityCurrent: wrongElec,
		WaterPrevious: 200, WaterCurrent: waterReading,
	}
	if err := db.Create(&src).Error; err != nil {
		return nil, fmt.Errorf("create source reading: %w", err)
	}

	// PAID source bill: the tenant already paid the mis-charged month — that is
	// why a refund is owed at settlement. It MUST be PAID (not just FINALIZED):
	// a FINALIZED-unpaid prior bill would be absorbed/voided by the settlement
	// (FindUnpaidMonthlyByContractID), stripping the recovery's S0 basis. It
	// charged the 300 phantom units (1500-1200) at sourceRate — its electricity
	// line unit_price is the S0 + rate basis the resolver refunds at.
	elecUnits := wrongElec - physicalElec
	elecAmount := int64(elecUnits) * sourceRate
	finalizedAt := now
	sb := billing.Bill{
		ContractID: c.ID, BillingMonth: sourceMonth, BillType: billing.BillTypeMonthly,
		Status: billing.BillStatusPaid, TotalAmount: c.MonthlyRent + elecAmount, FinalizedAt: &finalizedAt,
	}
	if err := db.Create(&sb).Error; err != nil {
		return nil, fmt.Errorf("create source bill: %w", err)
	}
	sbLines := []billing.BillLineItem{
		{BillID: sb.ID, LineType: billing.LineItemRoomRent, Source: billing.LineItemSourceAuto, Description: "ค่าเช่า", Amount: c.MonthlyRent, SortOrder: 1},
		{BillID: sb.ID, LineType: billing.LineItemElectricity, Source: billing.LineItemSourceAuto, Description: "ค่าไฟฟ้า", Amount: elecAmount, Quantity: elecUnits, UnitPrice: sourceRate, SortOrder: 2},
	}
	if err := db.Create(&sbLines).Error; err != nil {
		return nil, fmt.Errorf("create source bill lines: %w", err)
	}

	// Epic B Model B: NO pre-recorded exit reading. The notice sits in
	// PENDING_METER; the smoke drives record-exit-meter with the over-record flag,
	// which re-anchors from the single move-out observation and lets the settlement
	// refund (recorded − observed). exitObs = the true current at move-out (1240):
	// below the wrong source current (1500 → over-record), above the source
	// previous (1200 → 40 units of real usage from the true baseline).
	exitObsElec := physicalElec + 40 // 1240
	exitObsWater := waterReading + 8  // 228 (water not over-recorded)
	moveOutDate := now.AddDate(0, 0, -3)

	notice := moveout.MoveOutNotice{
		ContractID: c.ID, NoticeDate: moveOutDate.AddDate(0, 0, -7),
		ScheduledMoveOutDate: moveOutDate,
		Status:               moveout.MoveOutStatusPendingMeter,
	}
	if err := db.Create(&notice).Error; err != nil {
		return nil, fmt.Errorf("create notice: %w", err)
	}

	return &SettlementRecoverySmokeFixture{
		ApartmentID: apt.ID.String(), RoomID: rm.ID.String(), ContractID: c.ID.String(), NoticeID: notice.ID.String(),
		SourceReadingID:    src.ID.String(),
		SourceRecordedElec: wrongElec, ExitObservationElec: exitObsElec, ExitObservationWater: exitObsWater,
		SourceRate: sourceRate, ExpectedRefund: -int64(wrongElec-exitObsElec) * sourceRate,
	}, nil
}
