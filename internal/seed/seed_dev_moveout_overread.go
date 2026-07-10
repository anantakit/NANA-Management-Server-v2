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

// moveOutOverReadRoomNumber is the dedicated นานาคอร์ท room for the operator
// end-to-end over-read smoke (distinct from SR-SETTLE / A211 / A2xx).
const moveOutOverReadRoomNumber = "OR-MOVEOUT"

// MoveOutOverReadSmokeFixture is the JSON the dev endpoint returns. The smoke
// then drives the WHOLE workflow over HTTP with no seeded terminal state:
// recovery (baseline-correction) → exit (record-exit-meter, same month) →
// generate-settlement → finalize. Nothing but the mis-read past + the notice is
// seeded; the recovery, exit, and settlement are all produced by real endpoints.
type MoveOutOverReadSmokeFixture struct {
	ApartmentID     string `json:"apartment_id"`
	RoomID          string `json:"room_id"`
	ContractID      string `json:"contract_id"`
	NoticeID        string `json:"notice_id"`
	SourceReadingID string `json:"source_reading_id"`
	PhysicalElec    int    `json:"physical_electricity"` // the true reading the operator enters
	PhysicalWater   int    `json:"physical_water"`
	SourceRate      int64  `json:"source_electricity_rate"`
	ExpectedRefund  int64  `json:"expected_refund"` // negative satang
}

// SetupMoveOutOverReadSmoke (re)creates a clean PENDING_METER move-out with a
// mis-read past cycle, and returns the IDs the smoke drives. Re-runnable.
func SetupMoveOutOverReadSmoke(db *gorm.DB) (*MoveOutOverReadSmokeFixture, error) {
	const (
		wrongElec    = 13500 // what the source month recorded (and billed)
		physicalElec = 12500 // the true reading the operator enters this cycle
		waterReading = 200   // water clean
		basePrev     = 12000 // source-month previous
		sourceRate   = int64(800)
	)

	var apt apartment.Apartment
	if err := db.Where("name = ?", "นานาคอร์ท").First(&apt).Error; err != nil {
		return nil, fmt.Errorf("find apartment: %w", err)
	}

	var rm room.Room
	err := db.Where("apartment_id = ? AND number = ?", apt.ID, moveOutOverReadRoomNumber).First(&rm).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		rm = room.Room{
			ApartmentID: apt.ID, Number: moveOutOverReadRoomNumber, Type: room.RoomTypeAir,
			Floor: 9, BaseRent: 500000, BaseDeposit: 500000, Status: room.RoomStatusVacant,
		}
		if err := db.Create(&rm).Error; err != nil {
			return nil, fmt.Errorf("create smoke room: %w", err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("find smoke room: %w", err)
	}

	var c contract.Contract
	err = db.Where("room_id = ? AND status = ?", rm.ID, contract.ContractStatusActive).First(&c).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		tn, tErr := upsertReconciliationTenant(db, "1100100100291", "อรุณ ทดสอบย้ายออกจดผิด", "0890000291")
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

	// Reset the room's mutable artifacts (FK-safe; notices reference bills).
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

	// Mis-read source month: elec current 13500 (physical was ~12500). This IS the
	// evidence the recovery derives its over-record from.
	src := meterreading.MeterReading{
		RoomID: rm.ID, ReadingType: meterreading.ReadingTypeMonthly, BillingMonth: &sourceMonth,
		ElectricityPrevious: basePrev, ElectricityCurrent: wrongElec,
		WaterPrevious: waterReading, WaterCurrent: waterReading,
	}
	if err := db.Create(&src).Error; err != nil {
		return nil, fmt.Errorf("create source reading: %w", err)
	}

	// PAID source bill (tenant paid the over-charge → refund owed; PAID so the
	// settlement does not absorb it and it keeps the S0 + rate basis).
	elecUnits := wrongElec - basePrev // 1500 billed units
	elecAmount := int64(elecUnits) * sourceRate
	finalizedAt := now
	sb := billing.Bill{
		ContractID: c.ID, BillingMonth: sourceMonth, BillType: billing.BillTypeMonthly,
		Status: billing.BillStatusPaid, TotalAmount: c.MonthlyRent + elecAmount, FinalizedAt: &finalizedAt,
	}
	if err := db.Create(&sb).Error; err != nil {
		return nil, fmt.Errorf("create source bill: %w", err)
	}
	if err := db.Create(&billing.BillLineItem{
		BillID: sb.ID, LineType: billing.LineItemElectricity, Source: billing.LineItemSourceAuto,
		Description: "ค่าไฟฟ้า", Amount: elecAmount, Quantity: elecUnits, UnitPrice: sourceRate, SortOrder: 2,
	}).Error; err != nil {
		return nil, fmt.Errorf("create source bill line: %w", err)
	}

	// PENDING_METER notice — NO exit reading yet (the smoke records it via HTTP).
	scheduled := now.AddDate(0, 0, -2)
	notice := moveout.MoveOutNotice{
		ContractID: c.ID, NoticeDate: scheduled.AddDate(0, 0, -7),
		ScheduledMoveOutDate: scheduled, Status: moveout.MoveOutStatusPendingMeter,
	}
	if err := db.Create(&notice).Error; err != nil {
		return nil, fmt.Errorf("create notice: %w", err)
	}

	return &MoveOutOverReadSmokeFixture{
		ApartmentID: apt.ID.String(), RoomID: rm.ID.String(), ContractID: c.ID.String(), NoticeID: notice.ID.String(),
		SourceReadingID: src.ID.String(), PhysicalElec: physicalElec, PhysicalWater: waterReading,
		SourceRate: sourceRate, ExpectedRefund: -int64(wrongElec-physicalElec) * sourceRate,
	}, nil
}
