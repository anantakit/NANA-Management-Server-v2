package seed

import (
	"errors"
	"fmt"
	"time"

	"nana/internal/apartment"
	"nana/internal/contract"
	"nana/internal/meterreading"
	"nana/internal/moveout"
	"nana/internal/room"

	"gorm.io/gorm"
)

// Dedicated นานาคอร์ท rooms for the F1 exit-meter rollover/replacement HTTP smoke.
// Distinct from SR-SETTLE (settlement recovery) and the A2xx move-out rooms so the
// mutating smokes never collide.
const (
	exitRolloverRoomNumber    = "XM-ROLL"
	exitReplacementRoomNumber = "XM-REPL"
)

// ExitMeterFlagsCase is one PENDING_METER move-out the smoke drives end-to-end:
// record-exit-meter (with the hardware flag) → generate-settlement → inspect the
// real bill. Expected* values are independent literals (NOT computed via the
// model's ElectricityUsed) so the assertion is a genuine cross-check of the
// settlement generation, not a tautology.
type ExitMeterFlagsCase struct {
	NoticeID   string `json:"notice_id"`
	RoomID     string `json:"room_id"`
	ContractID string `json:"contract_id"`

	// Inputs the smoke POSTs to record-exit-meter.
	MoveOutDate      string `json:"move_out_date"` // YYYY-MM-DD
	ExitElecCurrent  int    `json:"exit_electricity_current"`
	ExitWaterCurrent int    `json:"exit_water_current"`

	// Expected values on the persisted EXIT reading + settlement bill.
	ExpectedElecPrevious int `json:"expected_electricity_previous"`
	ExpectedElecUsage    int `json:"expected_electricity_usage"`
	ExpectedWaterUsage   int `json:"expected_water_usage"`
}

// ExitMeterFlagsSmokeFixture is the JSON the dev endpoint returns to the F1 smoke.
type ExitMeterFlagsSmokeFixture struct {
	ApartmentID string `json:"apartment_id"`
	ElecRate    int64  `json:"electricity_rate"` // satang/unit — assert unit_price against this
	WaterRate   int64  `json:"water_rate"`

	Rollover    ExitMeterFlagsCase `json:"rollover"`
	Replacement ExitMeterFlagsCase `json:"replacement"`
}

// SetupExitMeterFlagsSmoke (re)creates two clean PENDING_METER move-out fixtures for
// the F1 exit-meter rollover/replacement HTTP smoke and returns the IDs + expected
// numbers the smoke asserts against.
//
// Nothing about the EXIT reading is seeded — the smoke records it over HTTP so the
// chain (handler → DTO binding → MeterReading semantics → settlement generation →
// persisted bill lines) is exercised for real. Only the prior MONTHLY reading is
// seeded, so the exit's "previous" is populated:
//
//   - Rollover:    prior MONTHLY elec current = 99000 → exit previous 99000; the
//     smoke records current 500 with the rollover flag → usage wraps to 1499.
//   - Replacement: the smoke records current 45 with the replaced flag → previous
//     is zeroed → usage counts from 0 = 45.
//
// Re-runnable: wipes each room's notices/bills/readings first.
func SetupExitMeterFlagsSmoke(db *gorm.DB) (*ExitMeterFlagsSmokeFixture, error) {
	var apt apartment.Apartment
	if err := db.Where("name = ?", "นานาคอร์ท").First(&apt).Error; err != nil {
		return nil, fmt.Errorf("find apartment: %w", err)
	}

	moveOutDate := truncateToDate(time.Now().UTC()).AddDate(0, 0, -3)

	// Rollover case: prior MONTHLY elec current 99000 becomes the exit previous.
	// Smoke records 500 + rollover → usage = (digitMax(99000)-99000)+500
	//                                       = (99999-99000)+500 = 1499.
	// Water is a clean control: prior 8500 → record 8600 → usage 100.
	rollover, err := setupExitFlagCase(db, apt, exitFlagCaseSeed{
		roomNumber:       exitRolloverRoomNumber,
		idCard:           "1100100100291",
		tenantName:       "สมหญิง ทดสอบครบรอบ",
		phone:            "0890000291",
		priorElecPrev:    98000,
		priorElecCurr:    99000,
		priorWaterPrev:   8000,
		priorWaterCurr:   8500,
		moveOutDate:      moveOutDate,
		exitElecCurrent:  500,
		exitWaterCurrent: 8600,
		expElecPrevious:  99000,
		expElecUsage:     1499,
		expWaterUsage:    100,
	})
	if err != nil {
		return nil, err
	}

	// Replacement case: smoke records 45 + replaced → previous zeroed → usage 45.
	// Water clean control: prior 300 → record 320 → usage 20.
	replacement, err := setupExitFlagCase(db, apt, exitFlagCaseSeed{
		roomNumber:       exitReplacementRoomNumber,
		idCard:           "1100100100292",
		tenantName:       "สมศักดิ์ ทดสอบเปลี่ยนมิเตอร์",
		phone:            "0890000292",
		priorElecPrev:    7800,
		priorElecCurr:    8000,
		priorWaterPrev:   280,
		priorWaterCurr:   300,
		moveOutDate:      moveOutDate,
		exitElecCurrent:  45,
		exitWaterCurrent: 320,
		expElecPrevious:  0,
		expElecUsage:     45,
		expWaterUsage:    20,
	})
	if err != nil {
		return nil, err
	}

	return &ExitMeterFlagsSmokeFixture{
		ApartmentID: apt.ID.String(),
		ElecRate:    apt.ElectricityRatePerUnit,
		WaterRate:   apt.WaterRatePerUnit,
		Rollover:    *rollover,
		Replacement: *replacement,
	}, nil
}

type exitFlagCaseSeed struct {
	roomNumber string
	idCard     string
	tenantName string
	phone      string

	priorElecPrev  int
	priorElecCurr  int
	priorWaterPrev int
	priorWaterCurr int

	moveOutDate      time.Time
	exitElecCurrent  int
	exitWaterCurrent int

	expElecPrevious int
	expElecUsage    int
	expWaterUsage   int
}

func setupExitFlagCase(db *gorm.DB, apt apartment.Apartment, s exitFlagCaseSeed) (*ExitMeterFlagsCase, error) {
	// Find-or-create the dedicated room.
	var rm room.Room
	err := db.Where("apartment_id = ? AND number = ?", apt.ID, s.roomNumber).First(&rm).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		rm = room.Room{
			ApartmentID: apt.ID, Number: s.roomNumber, Type: room.RoomTypeAir,
			Floor: 9, BaseRent: 500000, BaseDeposit: 500000, Status: room.RoomStatusVacant,
		}
		if err := db.Create(&rm).Error; err != nil {
			return nil, fmt.Errorf("create smoke room %s: %w", s.roomNumber, err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("find smoke room %s: %w", s.roomNumber, err)
	}

	// Find-or-create the active contract (one per room — never duplicate).
	var c contract.Contract
	err = db.Where("room_id = ? AND status = ?", rm.ID, contract.ContractStatusActive).First(&c).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		tn, tErr := upsertReconciliationTenant(db, s.idCard, s.tenantName, s.phone)
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
			return nil, fmt.Errorf("create smoke contract %s: %w", s.roomNumber, err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("find smoke contract %s: %w", s.roomNumber, err)
	}
	if err := db.Model(&room.Room{}).Where("id = ?", rm.ID).Update("status", room.RoomStatusOccupied).Error; err != nil {
		return nil, fmt.Errorf("occupy smoke room %s: %w", s.roomNumber, err)
	}

	// Reset the room's mutable artifacts (FK-safe order — mirrors the settlement
	// recovery fixture): notices reference bills via settlement_bill_id, so drop
	// notices first; audit + deliveries RESTRICT before bills; readings last.
	billIDs := `SELECT id FROM bills WHERE contract_id = ?`
	if err := db.Exec(`DELETE FROM move_out_notices WHERE contract_id = ?`, c.ID).Error; err != nil {
		return nil, fmt.Errorf("reset notices %s: %w", s.roomNumber, err)
	}
	for _, stmt := range []string{
		`DELETE FROM bill_audit_log WHERE bill_id IN (` + billIDs + `)`,
		`DELETE FROM bill_deliveries WHERE bill_id IN (` + billIDs + `)`,
		`DELETE FROM bills WHERE contract_id = ?`,
	} {
		if err := db.Exec(stmt, c.ID).Error; err != nil {
			return nil, fmt.Errorf("reset bills %s: %w", s.roomNumber, err)
		}
	}
	if err := db.Exec(`DELETE FROM meter_readings WHERE room_id = ?`, rm.ID).Error; err != nil {
		return nil, fmt.Errorf("reset readings %s: %w", s.roomNumber, err)
	}

	// Prior MONTHLY reading — its CURRENT becomes the exit's "previous" via
	// populatePrevious. This is the ONLY reading seeded; the smoke records the
	// EXIT itself over HTTP.
	now := time.Now().UTC()
	priorMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0).Format("2006-01")
	prior := meterreading.MeterReading{
		RoomID: rm.ID, ReadingType: meterreading.ReadingTypeMonthly, BillingMonth: &priorMonth,
		ElectricityPrevious: s.priorElecPrev, ElectricityCurrent: s.priorElecCurr,
		WaterPrevious: s.priorWaterPrev, WaterCurrent: s.priorWaterCurr,
	}
	if err := db.Create(&prior).Error; err != nil {
		return nil, fmt.Errorf("create prior monthly reading %s: %w", s.roomNumber, err)
	}

	// PENDING_METER notice — no actual date, no exit reading, no settlement bill.
	notice := moveout.MoveOutNotice{
		ContractID: c.ID, NoticeDate: s.moveOutDate.AddDate(0, 0, -7),
		ScheduledMoveOutDate: s.moveOutDate,
		Status:               moveout.MoveOutStatusPendingMeter,
	}
	if err := db.Create(&notice).Error; err != nil {
		return nil, fmt.Errorf("create notice %s: %w", s.roomNumber, err)
	}

	return &ExitMeterFlagsCase{
		NoticeID:   notice.ID.String(),
		RoomID:     rm.ID.String(),
		ContractID: c.ID.String(),

		MoveOutDate:      s.moveOutDate.Format("2006-01-02"),
		ExitElecCurrent:  s.exitElecCurrent,
		ExitWaterCurrent: s.exitWaterCurrent,

		ExpectedElecPrevious: s.expElecPrevious,
		ExpectedElecUsage:    s.expElecUsage,
		ExpectedWaterUsage:   s.expWaterUsage,
	}, nil
}
