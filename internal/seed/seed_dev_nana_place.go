package seed

import (
	"errors"
	"fmt"
	"log/slog"
	"time"

	"nana/internal/apartment"
	"nana/internal/contract"
	"nana/internal/meterreading"
	"nana/internal/moveout"
	"nana/internal/room"
	"nana/internal/tenant"

	"gorm.io/gorm"
)

// seedDevNanaPlaceBilling seeds Nana Place with a monthly-batch-billing test
// scenario:
//
//   - Room 1001 → notice scheduled NEXT month   → should bill normally
//     (BE rule fix 858619c: future-month move-outs do not skip)
//   - Room 1002 → notice scheduled THIS month   → should be SKIPPED
//     (settlement bill covers the move-out month, no monthly bill)
//   - Room 1003 → notice scheduled THIS month   → should be SKIPPED
//
// Room 1001 also gets a monthly meter reading for the current billing month
// so the readiness preflight + batch can classify it as READY (otherwise it
// would fall through to MISSING_METER_READING). Rooms 1002 and 1003 are
// PENDING_METER (no exit meter yet) so the move-out rule wins regardless.
//
// Idempotent: skipped if room 1001 already has any contract.
func seedDevNanaPlaceBilling(db *gorm.DB) error {
	var apt apartment.Apartment
	if err := db.Where("name = ?", "นานาเพลส").First(&apt).Error; err != nil {
		return fmt.Errorf("find นานาเพลส: %w", err)
	}

	roomNumbers := []string{"1001", "1002", "1003"}
	var rooms []room.Room
	if err := db.Where("apartment_id = ? AND number IN ?", apt.ID, roomNumbers).
		Find(&rooms).Error; err != nil {
		return fmt.Errorf("find Nana Place rooms: %w", err)
	}
	if len(rooms) != len(roomNumbers) {
		slog.Warn("Nana Place billing seed: missing rooms", "found", len(rooms), "want", len(roomNumbers))
		return nil
	}
	roomByNumber := make(map[string]room.Room, len(rooms))
	for _, r := range rooms {
		roomByNumber[r.Number] = r
	}

	// Idempotency anchor: if room 1001 already has a contract, assume scenario seeded
	var existing int64
	if err := db.Model(&contract.Contract{}).Where("room_id = ?", roomByNumber["1001"].ID).Count(&existing).Error; err != nil {
		return fmt.Errorf("check existing contracts: %w", err)
	}
	if existing > 0 {
		return nil
	}

	// today is anchored to UTC for deterministic offsets (same convention as
	// seedDevMoveOuts).
	today := truncateToDate(time.Now().UTC())
	year, month, _ := today.Date()
	startOfMonth := time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)
	endOfMonth := startOfMonth.AddDate(0, 1, -1)
	startOfNextMonth := startOfMonth.AddDate(0, 1, 0)
	nextMonthMid := startOfNextMonth.AddDate(0, 0, 14)
	currentBillingMonth := today.Format("2006-01")

	// 3 fresh tenants — distinct ID card prefix (1100200400xxx) so they don't
	// clash with nanaCourt move-out seeds (1100200300xxx).
	tenantSeeds := []struct {
		IDCard, FullName, Phone, Address, Emergency string
	}{
		{"1100200400001", "ปริญญา สุขสมบูรณ์", "0801112201", "12 ถ.สุรินทร์-ปราสาท ต.นอกเมือง อ.เมืองสุรินทร์ จ.สุรินทร์ 32000", "สมหญิง สุขสมบูรณ์ (แม่) 0834567121"},
		{"1100200400002", "อรณิชา เพชรงาม", "0812223302", "84/2 ถ.หลักเมือง ต.ในเมือง อ.เมืองสุรินทร์ จ.สุรินทร์ 32000", "วิรัตน์ เพชรงาม (พ่อ) 0856781234"},
		{"1100200400003", "ภาคภูมิ พงษ์ทอง", "0823334403", "33 ม.5 ต.สลักได อ.เมืองสุรินทร์ จ.สุรินทร์ 32000", "ภาวินี พงษ์ทอง (พี่สาว) 0891234562"},
	}

	tenantByIDCard := make(map[string]tenant.Tenant, len(tenantSeeds))
	for _, t := range tenantSeeds {
		var found tenant.Tenant
		err := db.Where("id_card = ?", t.IDCard).First(&found).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			found = tenant.Tenant{
				FullName:         t.FullName,
				IDCard:           t.IDCard,
				Phone:            t.Phone,
				Address:          t.Address,
				EmergencyContact: t.Emergency,
			}
			if err := db.Create(&found).Error; err != nil {
				return fmt.Errorf("create tenant %s: %w", t.FullName, err)
			}
		} else if err != nil {
			return fmt.Errorf("find tenant %s: %w", t.FullName, err)
		}
		tenantByIDCard[t.IDCard] = found
	}

	type scenario struct {
		roomNumber   string
		tenantIDCard string
		// scheduled = absolute date (we anchor on this-month vs next-month
		// rather than offset-from-today, so scenario meaning is preserved
		// regardless of when seed runs).
		scheduledMoveOut time.Time
		noticeDate       time.Time
		withMonthlyMeter bool
		note             string
	}

	scenarios := []scenario{
		{
			roomNumber:       "1001",
			tenantIDCard:     "1100200400001",
			scheduledMoveOut: nextMonthMid, // next month → should BILL normally
			noticeDate:       today.AddDate(0, 0, -3),
			withMonthlyMeter: true, // need meter so it lands as READY in preflight
			note:             "แจ้งล่วงหน้า ย้ายเดือนหน้า (ยังต้องออกบิลเดือนนี้)",
		},
		{
			roomNumber:       "1002",
			tenantIDCard:     "1100200400002",
			scheduledMoveOut: endOfMonth, // this month → SKIP
			noticeDate:       today.AddDate(0, 0, -5),
			withMonthlyMeter: false,
			note:             "ย้ายปลายเดือนนี้ — settlement จะคิดเอง",
		},
		{
			roomNumber:       "1003",
			tenantIDCard:     "1100200400003",
			scheduledMoveOut: endOfMonth.AddDate(0, 0, -5), // this month → SKIP
			noticeDate:       today.AddDate(0, 0, -2),
			withMonthlyMeter: false,
			note:             "ย้ายปลายเดือนนี้ก่อนสิ้นเดือน — settlement จะคิดเอง",
		},
	}

	created := 0
	for _, sc := range scenarios {
		rm := roomByNumber[sc.roomNumber]
		tn := tenantByIDCard[sc.tenantIDCard]

		// 8 months ago — past min_months for natural deposit-returnable case
		start := today.AddDate(0, -8, 0)
		c := contract.Contract{
			TenantID:               tn.ID,
			RoomID:                 rm.ID,
			StartDate:              start,
			MinMonths:              6,
			MonthlyRent:            rm.BaseRent,
			DepositAmount:          rm.BaseDeposit,
			DepositStatus:          contract.DepositStatusCollected,
			ElectricityRatePerUnit: apt.ElectricityRatePerUnit,
			WaterRatePerUnit:       apt.WaterRatePerUnit,
			Status:                 contract.ContractStatusActive,
		}
		if err := db.Create(&c).Error; err != nil {
			return fmt.Errorf("create contract %s: %w", rm.Number, err)
		}

		if err := db.Model(&room.Room{}).Where("id = ?", rm.ID).
			Update("status", room.RoomStatusOccupied).Error; err != nil {
			return fmt.Errorf("update room status %s: %w", rm.Number, err)
		}

		if sc.withMonthlyMeter {
			meter := meterreading.MeterReading{
				RoomID:              rm.ID,
				BillingMonth:        &currentBillingMonth,
				ElectricityPrevious: 1000,
				ElectricityCurrent:  1120,
				WaterPrevious:       100,
				WaterCurrent:        112,
			}
			if err := db.Create(&meter).Error; err != nil {
				return fmt.Errorf("create meter %s: %w", rm.Number, err)
			}
		}

		notice := moveout.MoveOutNotice{
			ContractID:           c.ID,
			NoticeDate:           sc.noticeDate,
			ScheduledMoveOutDate: sc.scheduledMoveOut,
			Status:               moveout.MoveOutStatusPendingMeter,
			Note:                 sc.note,
		}
		if err := db.Create(&notice).Error; err != nil {
			return fmt.Errorf("create notice %s: %w", rm.Number, err)
		}

		created++
	}

	if created > 0 {
		slog.Info("seeded Nana Place billing scenarios", "rooms", roomNumbers, "billing_month", currentBillingMonth)
	}
	return nil
}
