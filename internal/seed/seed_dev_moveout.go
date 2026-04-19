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

// seedDevMoveOuts populates the move-out queue with a wide set of scenarios
// so every branch of the frontend kanban view is demonstrably exercised.
//
// | # | Room | Status               | Scenario                                   |
// |---|------|----------------------|--------------------------------------------|
// | 1 | A110 | PENDING_METER        | Scheduled 14 days out, no meter             |
// | 2 | A111 | PENDING_METER        | Scheduled 5 days out, no meter              |
// | 3 | A201 | PENDING_METER        | Scheduled today, no meter                   |
// | 4 | A202 | PENDING_METER        | Scheduled -3 days, overdue no meter         |
// | 5 | A203 | PENDING_SETTLEMENT   | Meter captured, waiting for settlement bill |
// | 6 | A204 | PENDING_SETTLEMENT   | Overdue, meter captured, waiting settlement |
// | 7 | A205 | COMPLETED            | Min months met, full refund                 |
// | 8 | A206 | COMPLETED            | Broke min months, deposit forfeited         |
// | 9 | A207 | COMPLETED            | Mid-month move-out, pro-rate refund         |
// |10 | B101 | CANCELLED            | Notice cancelled, tenant still occupying    |
// |11 | B102 | PENDING_SETTLEMENT   | Scheduled +13, meter captured early         |
// |12 | B103 | PENDING_PAYMENT      | Settlement bill created, waiting payment    |
// |13 | B104 | READY_TO_CLOSE       | Payment recorded, ready to close            |
//
// Idempotent — skipped entirely if any move-out notice already exists.
func seedDevMoveOuts(db *gorm.DB) error {
	var existing int64
	if err := db.Model(&moveout.MoveOutNotice{}).Count(&existing).Error; err != nil {
		return fmt.Errorf("check existing move-outs: %w", err)
	}
	if existing > 0 {
		return nil
	}

	var apt apartment.Apartment
	if err := db.Where("name = ?", "นานาคอร์ท").First(&apt).Error; err != nil {
		return fmt.Errorf("find apartment: %w", err)
	}

	// today is anchored to DB server time (UTC) to keep urgency buckets deterministic.
	today := truncateToDate(time.Now().UTC())

	tenants := []struct {
		FullName, IDCard, Phone, Address, Emergency string
	}{
		{"มานะ ศรีสุข", "1100200300001", "0891234501", "45/12 ซ.ลาดพร้าว 71 แขวงลาดพร้าว เขตลาดพร้าว กรุงเทพฯ 10230", "สุภาพร ศรีสุข (แม่) 0812345678"},
		{"สุดา เจริญผล", "1100200300002", "0956789012", "88 ถ.นวมินทร์ แขวงคลองกุ่ม เขตบึงกุ่ม กรุงเทพฯ 10240", "วิชัย เจริญผล (พ่อ) 0834567890"},
		{"ชาติชาย วงศ์ประเสริฐ", "1100200300003", "0623456789", "19/3 ม.5 ถ.รังสิต-นครนายก ต.ประชาธิปัตย์ อ.ธัญบุรี ปทุมธานี 12130", "นิตยา วงศ์ประเสริฐ (ภรรยา) 0897654321"},
		{"ไชยวัฒน์ ทองดี", "1100200300004", "0814567890", "127 ถ.แจ้งวัฒนะ แขวงทุ่งสองห้อง เขตหลักสี่ กรุงเทพฯ 10210", "สมชาย ทองดี (พี่ชาย) 0923456781"},
		{"พรเทพ สุวรรณชาติ", "1100200300005", "0935678901", "33/7 ซ.รามอินทรา 34 แขวงท่าแร้ง เขตบางเขน กรุงเทพฯ 10230", "พรทิพย์ สุวรรณชาติ (แม่) 0856789012"},
		{"ชัยวัฒน์ จันทร์แก้ว", "1100200300006", "0867890123", "56 ถ.พหลโยธิน แขวงอนุสาวรีย์ เขตบางเขน กรุงเทพฯ 10220", "วรรณา จันทร์แก้ว (ภรรยา) 0912345670"},
		{"วีระชัย พงษ์พิพัฒน์", "1100200300007", "0641234567", "201 ม.3 ถ.ติวานนท์ ต.ท่าทราย อ.เมือง นนทบุรี 11000", "วีรวรรณ พงษ์พิพัฒน์ (น้องสาว) 0845678901"},
		{"สมศักดิ์ แสงอรุณ", "1100200300008", "0892345678", "78/4 ซ.สุขุมวิท 77 แขวงพระโขนงเหนือ เขตวัฒนา กรุงเทพฯ 10110", "สมศรี แสงอรุณ (ภรรยา) 0876543210"},
		{"อนุชา รัตนโชติ", "1100200300009", "0953456789", "42 ถ.วิภาวดีรังสิต แขวงสามเสนใน เขตพญาไท กรุงเทพฯ 10400", "อนุชิต รัตนโชติ (พี่ชาย) 0834567891"},
		{"จารึก มั่นคง", "1100200300010", "0824567890", "15/9 ม.7 ถ.บางนา-ตราด ต.บางแก้ว อ.บางพลี สมุทรปราการ 10540", "จารุณี มั่นคง (น้องสาว) 0891234560"},
		{"บรรเจิด อินทรสุข", "1100200300011", "0615678901", "234 ถ.ศรีนครินทร์ แขวงหนองบอน เขตประเวศ กรุงเทพฯ 10250", "บังอร อินทรสุข (แม่) 0945678902"},
		{"ธนากร วิไลพร", "1100200300012", "0946789012", "67/2 ซ.รัชดาภิเษก 36 แขวงจันทรเกษม เขตจตุจักร กรุงเทพฯ 10900", "ธนาภรณ์ วิไลพร (ภรรยา) 0867890120"},
		{"ประสิทธิ์ เกษมสุข", "1100200300013", "0837890123", "91 ม.2 ถ.เทพารักษ์ ต.เทพารักษ์ อ.เมือง สมุทรปราการ 10270", "ประภา เกษมสุข (ภรรยา) 0923456780"},
	}

	// rooms aligned 1:1 with tenants[] — must be vacant in seed base set.
	roomNumbers := []string{
		"A110", "A111", "A201", "A202",
		"A203", "A204", "A205", "A206",
		"A207", "B101", "B102", "B103", "B104",
	}

	// Create tenants (idempotent per id_card)
	tenantByIDCard := make(map[string]tenant.Tenant, len(tenants))
	for _, t := range tenants {
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

	// Load target rooms
	var rooms []room.Room
	if err := db.Where("apartment_id = ? AND number IN ?", apt.ID, roomNumbers).Find(&rooms).Error; err != nil {
		return fmt.Errorf("find rooms: %w", err)
	}
	roomByNumber := make(map[string]room.Room, len(rooms))
	for _, r := range rooms {
		roomByNumber[r.Number] = r
	}
	if len(roomByNumber) != len(roomNumbers) {
		slog.Warn("move-out seed: missing rooms", "found", len(roomByNumber), "want", len(roomNumbers))
		return nil
	}

	type scenario struct {
		roomNumber         string
		tenantIDCard       string
		noticeOffset       int // days from today for notice_date
		scheduledOffset    int // days from today for scheduled_move_out_date
		minMonths          int
		contractStartMonths int           // how many months ago the contract started
		note               string
		// Notice status
		status moveout.MoveOutStatus
		// Contract status after scenario
		contractStatus contract.ContractStatus
		depositStatus  contract.DepositStatus
		// Room status after scenario
		roomStatus room.RoomStatus
		// EXIT meter?
		withExitMeter bool
		// Relative date of end (for history cases)
		endOffset *int
	}

	endOffsetPtr := func(d int) *int { return &d }

	scenarios := []scenario{
		// 1. NORMAL (Stage 1)
		{
			roomNumber: "A110", tenantIDCard: "1100200300001",
			noticeOffset: -2, scheduledOffset: 14,
			minMonths: 6, contractStartMonths: 8,
			note:           "แจ้งล่วงหน้า ย้ายปลายเดือน",
			status:         moveout.MoveOutStatusPendingMeter,
			contractStatus: contract.ContractStatusActive,
			depositStatus:  contract.DepositStatusCollected,
			roomStatus:     room.RoomStatusOccupied,
		},
		// 2. SOON (Stage 1)
		{
			roomNumber: "A111", tenantIDCard: "1100200300002",
			noticeOffset: -5, scheduledOffset: 5,
			minMonths: 6, contractStartMonths: 7,
			note:           "ใกล้กำหนดภายใน 5 วัน",
			status:         moveout.MoveOutStatusPendingMeter,
			contractStatus: contract.ContractStatusActive,
			depositStatus:  contract.DepositStatusCollected,
			roomStatus:     room.RoomStatusOccupied,
		},
		// 3. TODAY (Stage 1)
		{
			roomNumber: "A201", tenantIDCard: "1100200300003",
			noticeOffset: -7, scheduledOffset: 0,
			minMonths: 6, contractStartMonths: 9,
			note:           "วันนี้ต้องจดมิเตอร์",
			status:         moveout.MoveOutStatusPendingMeter,
			contractStatus: contract.ContractStatusActive,
			depositStatus:  contract.DepositStatusCollected,
			roomStatus:     room.RoomStatusOccupied,
		},
		// 4. OVERDUE (Stage 1)
		{
			roomNumber: "A202", tenantIDCard: "1100200300004",
			noticeOffset: -10, scheduledOffset: -3,
			minMonths: 6, contractStartMonths: 10,
			note:           "เลยกำหนดยังไม่ได้จดมิเตอร์",
			status:         moveout.MoveOutStatusPendingMeter,
			contractStatus: contract.ContractStatusActive,
			depositStatus:  contract.DepositStatusCollected,
			roomStatus:     room.RoomStatusOccupied,
		},
		// 5. PENDING_SETTLEMENT — meter captured, waiting for settlement bill
		{
			roomNumber: "A203", tenantIDCard: "1100200300005",
			noticeOffset: -6, scheduledOffset: 4,
			minMonths: 6, contractStartMonths: 12,
			note:           "จดมิเตอร์แล้ว รอสร้างบิลสรุป",
			status:         moveout.MoveOutStatusPendingSettlement,
			contractStatus: contract.ContractStatusActive,
			depositStatus:  contract.DepositStatusCollected,
			roomStatus:     room.RoomStatusOccupied,
			withExitMeter:  true,
		},
		// 6. PENDING_SETTLEMENT — overdue, meter captured
		{
			roomNumber: "A204", tenantIDCard: "1100200300006",
			noticeOffset: -9, scheduledOffset: -2,
			minMonths: 6, contractStartMonths: 11,
			note:           "จดมิเตอร์แล้วแต่ยังไม่ได้สร้างบิลสรุป",
			status:         moveout.MoveOutStatusPendingSettlement,
			contractStatus: contract.ContractStatusActive,
			depositStatus:  contract.DepositStatusCollected,
			roomStatus:     room.RoomStatusOccupied,
			withExitMeter:  true,
		},
		// 7. COMPLETED — full refund
		{
			roomNumber: "A205", tenantIDCard: "1100200300007",
			noticeOffset: -20, scheduledOffset: -10,
			minMonths: 6, contractStartMonths: 14,
			note:           "ย้ายออกตามปกติ คืนเงินประกันเต็มจำนวน",
			status:         moveout.MoveOutStatusCompleted,
			contractStatus: contract.ContractStatusEnded,
			depositStatus:  contract.DepositStatusRefunded,
			roomStatus:     room.RoomStatusVacant,
			withExitMeter:  true,
			endOffset:      endOffsetPtr(-10),
		},
		// 8. COMPLETED — forfeit (broke min months)
		{
			roomNumber: "A206", tenantIDCard: "1100200300008",
			noticeOffset: -18, scheduledOffset: -8,
			minMonths: 12, contractStartMonths: 4,
			note:           "ย้ายก่อนครบขั้นต่ำ หักเงินประกัน",
			status:         moveout.MoveOutStatusCompleted,
			contractStatus: contract.ContractStatusEnded,
			depositStatus:  contract.DepositStatusForfeited,
			roomStatus:     room.RoomStatusVacant,
			withExitMeter:  true,
			endOffset:      endOffsetPtr(-8),
		},
		// 9. COMPLETED — pro-rate mid-month
		{
			roomNumber: "A207", tenantIDCard: "1100200300009",
			noticeOffset: -25, scheduledOffset: -15,
			minMonths: 6, contractStartMonths: 13,
			note:           "ย้ายกลางเดือน pro-rate refund",
			status:         moveout.MoveOutStatusCompleted,
			contractStatus: contract.ContractStatusEnded,
			depositStatus:  contract.DepositStatusRefunded,
			roomStatus:     room.RoomStatusVacant,
			withExitMeter:  true,
			endOffset:      endOffsetPtr(-15),
		},
		// 10. CANCELLED — tenant reverted
		{
			roomNumber: "B101", tenantIDCard: "1100200300010",
			noticeOffset: -12, scheduledOffset: 6,
			minMonths: 6, contractStartMonths: 9,
			note:           "ยกเลิกแจ้งย้ายออก อยู่ต่อ",
			status:         moveout.MoveOutStatusCancelled,
			contractStatus: contract.ContractStatusActive,
			depositStatus:  contract.DepositStatusCollected,
			roomStatus:     room.RoomStatusOccupied,
		},
		// 11. PENDING_SETTLEMENT — meter captured early, scheduled > 1 week out
		{
			roomNumber: "B102", tenantIDCard: "1100200300011",
			noticeOffset: -2, scheduledOffset: 13,
			minMonths: 6, contractStartMonths: 12,
			note:           "จดมิเตอร์ล่วงหน้า รอสร้างบิลสรุป",
			status:         moveout.MoveOutStatusPendingSettlement,
			contractStatus: contract.ContractStatusActive,
			depositStatus:  contract.DepositStatusCollected,
			roomStatus:     room.RoomStatusOccupied,
			withExitMeter:  true,
		},
		// 12. PENDING_PAYMENT — settlement bill created, waiting for payment
		{
			roomNumber: "B103", tenantIDCard: "1100200300012",
			noticeOffset: -8, scheduledOffset: -1,
			minMonths: 6, contractStartMonths: 10,
			note:           "สร้างบิลสรุปแล้ว รอชำระ",
			status:         moveout.MoveOutStatusPendingPayment,
			contractStatus: contract.ContractStatusActive,
			depositStatus:  contract.DepositStatusCollected,
			roomStatus:     room.RoomStatusOccupied,
			withExitMeter:  true,
		},
		// 13. READY_TO_CLOSE — payment recorded, ready to close
		{
			roomNumber: "B104", tenantIDCard: "1100200300013",
			noticeOffset: -12, scheduledOffset: -4,
			minMonths: 6, contractStartMonths: 8,
			note:           "ชำระเรียบร้อย พร้อมปิดสัญญา",
			status:         moveout.MoveOutStatusReadyToClose,
			contractStatus: contract.ContractStatusActive,
			depositStatus:  contract.DepositStatusCollected,
			roomStatus:     room.RoomStatusOccupied,
			withExitMeter:  true,
		},
	}

	created := 0
	for _, sc := range scenarios {
		rm := roomByNumber[sc.roomNumber]
		tn := tenantByIDCard[sc.tenantIDCard]

		// Skip if room already has any contract (avoid clobbering seed base)
		var existingContracts int64
		if err := db.Model(&contract.Contract{}).
			Where("room_id = ?", rm.ID).Count(&existingContracts).Error; err != nil {
			return fmt.Errorf("check contracts %s: %w", rm.Number, err)
		}
		if existingContracts > 0 {
			continue
		}

		start := today.AddDate(0, -sc.contractStartMonths, 0)
		c := contract.Contract{
			TenantID:               tn.ID,
			RoomID:                 rm.ID,
			StartDate:              start,
			MinMonths:              sc.minMonths,
			MonthlyRent:            rm.BaseRent,
			DepositAmount:          rm.BaseDeposit,
			DepositStatus:          sc.depositStatus,
			ElectricityRatePerUnit: apt.ElectricityRatePerUnit,
			WaterRatePerUnit:       apt.WaterRatePerUnit,
			Status:                 sc.contractStatus,
		}
		if sc.endOffset != nil {
			end := today.AddDate(0, 0, *sc.endOffset)
			c.EndDate = &end
		}
		if err := db.Create(&c).Error; err != nil {
			return fmt.Errorf("create contract for %s: %w", rm.Number, err)
		}

		// Update room status
		if err := db.Model(&room.Room{}).Where("id = ?", rm.ID).
			Update("status", sc.roomStatus).Error; err != nil {
			return fmt.Errorf("update room status %s: %w", rm.Number, err)
		}

		// Create move-out notice
		notice := moveout.MoveOutNotice{
			ContractID:           c.ID,
			NoticeDate:           today.AddDate(0, 0, sc.noticeOffset),
			ScheduledMoveOutDate: today.AddDate(0, 0, sc.scheduledOffset),
			Status:               sc.status,
			Note:                 sc.note,
		}
		// Set actual_move_out_date when exit meter exists — required for settlement preview
		if sc.withExitMeter {
			actual := today.AddDate(0, 0, sc.scheduledOffset)
			notice.ActualMoveOutDate = &actual
		}
		if err := db.Create(&notice).Error; err != nil {
			return fmt.Errorf("create notice %s: %w", rm.Number, err)
		}

		// Optional EXIT meter reading (seed bypasses validation — direct insert)
		if sc.withExitMeter {
			readingDate := today.AddDate(0, 0, sc.scheduledOffset)
			exit := meterreading.MeterReading{
				RoomID:              rm.ID,
				ReadingType:         meterreading.ReadingTypeExit,
				ReadingDateActual:   &readingDate,
				ElectricityPrevious: 1000,
				ElectricityCurrent:  1120,
				WaterPrevious:       100,
				WaterCurrent:        115,
			}
			if err := db.Create(&exit).Error; err != nil {
				return fmt.Errorf("create exit reading %s: %w", rm.Number, err)
			}
		}

		created++
	}

	if created > 0 {
		slog.Info("seeded dev move-outs", "count", created)
	}
	return nil
}

func truncateToDate(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
