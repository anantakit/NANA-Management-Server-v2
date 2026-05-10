package seed

import (
	"errors"
	"fmt"
	"log/slog"
	"time"

	"nana/internal/apartment"
	"nana/internal/billing"
	"nana/internal/contract"
	"nana/internal/domain"
	"nana/internal/meterreading"
	"nana/internal/moveout"
	"nana/internal/room"
	"nana/internal/tenant"

	"gorm.io/gorm"
)

// seedDevMoveOuts populates the move-out queue with a wide set of scenarios
// so every branch of the frontend kanban view is demonstrably exercised.
//
// All scenarios that should logically have a settlement bill (PENDING_PAYMENT
// onward) get one attached so the seed mirrors what real workflow would
// produce — never a COMPLETED notice without `settlement_bill_id`, never a
// READY_TO_CLOSE without a finalized bill, etc. This keeps the dev queue
// free of states that can't actually occur in production.
//
// | # | Room | Status                  | Scenario                                          |
// |---|------|-------------------------|---------------------------------------------------|
// | 1 | A110 | PENDING_METER           | Scheduled 14 days out, no meter                   |
// | 2 | A111 | PENDING_METER           | Scheduled 5 days out, no meter                    |
// | 3 | A201 | PENDING_METER           | Scheduled today, no meter                         |
// | 4 | A202 | PENDING_METER           | Scheduled -3 days, overdue no meter               |
// | 5 | A203 | PENDING_SETTLEMENT      | Meter captured, no draft bill yet                 |
// | 6 | A204 | PENDING_SETTLEMENT      | Overdue, meter captured, no draft yet             |
// | 7 | A205 | COMPLETED + settled     | Min months met, full refund (REFUNDED)            |
// | 8 | A206 | COMPLETED + settled     | Broke min months, deposit forfeited (PAID_EXTRA)  |
// | 9 | A207 | COMPLETED + settled     | M-1 paid, deposit > utilities → refund (REFUNDED) |
// |10 | B101 | CANCELLED               | Notice cancelled, tenant still occupying          |
// |11 | B102 | PENDING_SETTLEMENT      | Scheduled +13, meter captured early               |
// |12 | A209 | PENDING_PAYMENT         | Settlement bill FINALIZED, awaiting collect       |
// |13 | B104 | READY_TO_CLOSE          | Payment recorded (ZERO_BALANCE), pre-close        |
// |14 | A208 | COMPLETED + UNSETTLED   | Closed-with-unsettled (Phase 2 back-fill demo)    |
// |15 | A210 | PENDING_PAYMENT         | Settlement bill FINALIZED, awaiting refund        |
//
// NOTE: scenario rooms must be VACANT in base seed (no contracts yet).
// Reserved rooms in dev seeds — DO NOT pick these for new scenarios:
//   - A101-A107          → seedDevContracts (base tenants)
//   - A108, A109         → seedDevTenantHistory (multi-tenant timeline)
//   - B103               → seedB103History (VACANT-with-history demo)
//   - smokeRoomNumbers   → seedDevSmoke (B105, B2xx, Cxx, Dxx, E201)
// PENDING_PAYMENT used to live on B103 (skipped silently due to history),
// then A108 (skipped silently due to tenant-history) — both bugs traced
// to overlapping reservations. A209 is fan-floor-2 and currently the
// first truly-free A-block room. The per-scenario skip below now logs a
// warning when the room is already taken so future overlaps surface
// immediately instead of mysteriously dropping a scenario.
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
		{"กิตติ มงคลชัย", "1100200300014", "0871234599", "8/16 ซ.อ่อนนุช 17 แขวงสวนหลวง เขตสวนหลวง กรุงเทพฯ 10250", "กิตติยา มงคลชัย (น้องสาว) 0981234560"},
		{"วราภรณ์ สิริมงคล", "1100200300015", "0998765432", "112/5 ซ.รามคำแหง 53 แขวงพลับพลา เขตวังทองหลาง กรุงเทพฯ 10310", "วารุณี สิริมงคล (พี่สาว) 0867812345"},
	}

	// rooms aligned 1:1 with tenants[] — must be vacant in seed base set.
	// See header comment for the reserved-rooms list. PENDING_PAYMENT lives
	// on A209 because B103, A108, A109 are all already taken.
	roomNumbers := []string{
		"A110", "A111", "A201", "A202",
		"A203", "A204", "A205", "A206",
		"A207", "B101", "B102", "A209", "B104",
		"A208", "A210",
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
		roomNumber          string
		tenantIDCard        string
		noticeOffset        int // days from today for notice_date
		scheduledOffset     int // days from today for scheduled_move_out_date
		minMonths           int
		contractStartMonths int // how many months ago the contract started
		note                string
		// Notice status
		status moveout.MoveOutStatus
		// Contract status after scenario
		contractStatus contract.ContractStatus
		depositStatus  contract.DepositStatus
		// Room status after scenario
		roomStatus room.RoomStatus
		// EXIT meter?
		withExitMeter bool
		// Relative date of end (for history cases) — also drives notice.ClosedAt
		// for COMPLETED scenarios.
		endOffset *int
		// Settlement bill spec — non-nil when the notice should have a bill
		// attached. Real workflow generates this bill via PENDING_SETTLEMENT
		// → finalize-settlement chain; the seed inserts an equivalent row
		// directly. Use nil for PENDING_METER, PENDING_SETTLEMENT (no draft
		// yet), and CANCELLED.
		settlement *devSettlementSpec
	}

	endOffsetPtr := func(d int) *int { return &d }
	outcomePtr := func(o moveout.PaymentOutcome) *moveout.PaymentOutcome { return &o }
	methodPtr := func(m domain.PaymentMethod) *domain.PaymentMethod { return &m }

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
		// 5. PENDING_SETTLEMENT — meter captured, no draft yet
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
		// 7. COMPLETED + settled — full refund (deposit > charges)
		{
			roomNumber: "A205", tenantIDCard: "1100200300007",
			noticeOffset: -20, scheduledOffset: -10,
			minMonths: 6, contractStartMonths: 14,
			note:           "ย้ายออกตามปกติ คืนเงินประกันส่วนเกิน",
			status:         moveout.MoveOutStatusCompleted,
			contractStatus: contract.ContractStatusEnded,
			depositStatus:  contract.DepositStatusRefunded,
			roomStatus:     room.RoomStatusVacant,
			withExitMeter:  true,
			endOffset:      endOffsetPtr(-10),
			settlement: &devSettlementSpec{
				rentMode:       billing.RentModeProrated,
				rentPaid:       true, // M-1 monthly bill paid → settlement skips rent line
				cleaningFee:    30000, // ฿300
				paymentOutcome: outcomePtr(moveout.PaymentOutcomeRefunded),
				paymentMethod:  methodPtr(domain.PaymentMethodTransfer),
				paymentNote:    "โอนคืนผ่าน SCB",
			},
		},
		// 8. COMPLETED + settled — broke min months, deposit forfeited
		{
			roomNumber: "A206", tenantIDCard: "1100200300008",
			noticeOffset: -18, scheduledOffset: -8,
			minMonths: 12, contractStartMonths: 4,
			note:           "ย้ายก่อนครบขั้นต่ำ ริบเงินประกัน เก็บค่าใช้จ่ายเต็ม",
			status:         moveout.MoveOutStatusCompleted,
			contractStatus: contract.ContractStatusEnded,
			depositStatus:  contract.DepositStatusForfeited,
			roomStatus:     room.RoomStatusVacant,
			withExitMeter:  true,
			endOffset:      endOffsetPtr(-8),
			settlement: &devSettlementSpec{
				rentMode:       billing.RentModeProrated,
				rentPaid:       false, // M move-out: pro-rate rent included
				cleaningFee:    30000, // ฿300
				depositForfeit: true,  // bill marked DepositForfeited; net = full charges
				paymentOutcome: outcomePtr(moveout.PaymentOutcomePaidExtra),
				paymentMethod:  methodPtr(domain.PaymentMethodCash),
				paymentNote:    "เก็บเป็นเงินสด",
			},
		},
		// 9. COMPLETED + settled — refund (M-1 paid, deposit > utilities)
		// Mid-month move-out where the previous month's rent was already
		// paid (rentPaid=true → no rent line in settlement). Charges =
		// utilities only, which sit below the deposit → net is negative
		// (refund). REFUNDED outcome is semantically valid: net_amount < 0
		// means the landlord owes the tenant. Setting rentPaid=false here
		// would mix prorated rent into the total and could push net
		// positive depending on day-of-month, breaking the REFUNDED
		// semantics — see review note #1.
		{
			roomNumber: "A207", tenantIDCard: "1100200300009",
			noticeOffset: -25, scheduledOffset: -15,
			minMonths: 6, contractStartMonths: 13,
			note:           "M-1 ชำระแล้ว คืนเงินส่วนเกินจากเงินประกัน",
			status:         moveout.MoveOutStatusCompleted,
			contractStatus: contract.ContractStatusEnded,
			depositStatus:  contract.DepositStatusRefunded,
			roomStatus:     room.RoomStatusVacant,
			withExitMeter:  true,
			endOffset:      endOffsetPtr(-15),
			settlement: &devSettlementSpec{
				rentMode:       billing.RentModeProrated,
				rentPaid:       true, // M-1 paid → settlement has no rent line, deterministic refund
				cleaningFee:    0,    // no cleaning → utilities only
				paymentOutcome: outcomePtr(moveout.PaymentOutcomeRefunded),
				paymentMethod:  methodPtr(domain.PaymentMethodTransfer),
				paymentNote:    "คืนเงินส่วนเกินหลังหักค่าใช้จ่าย",
			},
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
		// 12. PENDING_PAYMENT — bill FINALIZED, awaiting payment record
		// Uses A209 — see header comment for the reserved-rooms list.
		{
			roomNumber: "A209", tenantIDCard: "1100200300012",
			noticeOffset: -8, scheduledOffset: -1,
			minMonths: 6, contractStartMonths: 10,
			note:           "สร้างบิลสรุปแล้ว รอชำระ",
			status:         moveout.MoveOutStatusPendingPayment,
			contractStatus: contract.ContractStatusActive,
			depositStatus:  contract.DepositStatusCollected,
			roomStatus:     room.RoomStatusOccupied,
			withExitMeter:  true,
			settlement: &devSettlementSpec{
				rentMode:    billing.RentModeProrated,
				rentPaid:    false,
				cleaningFee: 30000,
				// No paymentOutcome — operator hasn't recorded yet.
			},
		},
		// 13. READY_TO_CLOSE — payment recorded (ZERO_BALANCE), pre-close
		{
			roomNumber: "B104", tenantIDCard: "1100200300013",
			noticeOffset: -12, scheduledOffset: -4,
			minMonths: 6, contractStartMonths: 8,
			note:           "ชำระเรียบร้อย ค่าใช้จ่ายเท่าเงินประกันพอดี",
			status:         moveout.MoveOutStatusReadyToClose,
			contractStatus: contract.ContractStatusActive,
			depositStatus:  contract.DepositStatusCollected,
			roomStatus:     room.RoomStatusOccupied,
			withExitMeter:  true,
			settlement: &devSettlementSpec{
				rentMode:       billing.RentModeProrated,
				rentPaid:       true, // forces total to depend only on utilities + cleaning
				cleaningFee:    30000,
				balanceToZero:  true, // tweak amounts so total == deposit (ZERO_BALANCE)
				paymentOutcome: outcomePtr(moveout.PaymentOutcomeZeroBalance),
				// No paymentMethod — ZERO_BALANCE invariant
				paymentNote: "ค่าใช้จ่ายเท่าเงินประกัน",
			},
		},
		// 14. COMPLETED + UNSETTLED — Phase 2 close-with-unsettled demo
		// Card surfaces in Payment tab Section B "ปิดแล้ว • ค้างชำระ".
		// Workflow path that produces this state:
		//   PENDING_PAYMENT → /skip-payment → READY_TO_CLOSE+nil
		//   READY_TO_CLOSE+nil → /close-with-unsettled → COMPLETED+nil
		// Contract is ENDED and room VACANT (close happened), but
		// payment_outcome stays nil so the operator can back-fill later
		// via PaymentStep re-entry from the queue card.
		{
			roomNumber: "A208", tenantIDCard: "1100200300014",
			noticeOffset: -14, scheduledOffset: -7,
			minMonths: 6, contractStartMonths: 9,
			note:           "ปิดงานก่อน รอเวลามาบันทึกการเงิน",
			status:         moveout.MoveOutStatusCompleted,
			contractStatus: contract.ContractStatusEnded,
			// Deposit status stays Collected — the operator hasn't decided
			// yet whether to refund / forfeit. Will flip to Refunded /
			// Forfeited only when payment is back-filled through the FE.
			depositStatus: contract.DepositStatusCollected,
			roomStatus:    room.RoomStatusVacant,
			withExitMeter: true,
			endOffset:     endOffsetPtr(-7),
			settlement: &devSettlementSpec{
				rentMode:    billing.RentModeProrated,
				rentPaid:    false,
				cleaningFee: 30000,
				// No paymentOutcome — this IS the unsettled invariant.
			},
		},
		// 15. PENDING_PAYMENT — refund awaiting record (counterpart to #12)
		// M-1 monthly bill paid → settlement has only utilities, well below
		// deposit → DepositBalance positive → bill list classifies as
		// "คืนเงิน" (refund) and surfaces a "คืนเงิน" CTA. Pairs with A209
		// (#12, collect-more) so the FE settlement section demonstrates
		// both directions side-by-side.
		{
			roomNumber: "A210", tenantIDCard: "1100200300015",
			noticeOffset: -7, scheduledOffset: -2,
			minMonths: 6, contractStartMonths: 11,
			note:           "M-1 ชำระแล้ว รอคืนเงินส่วนเกิน",
			status:         moveout.MoveOutStatusPendingPayment,
			contractStatus: contract.ContractStatusActive,
			depositStatus:  contract.DepositStatusCollected,
			roomStatus:     room.RoomStatusOccupied,
			withExitMeter:  true,
			settlement: &devSettlementSpec{
				rentMode:    billing.RentModeProrated,
				rentPaid:    true, // M-1 paid → no rent line; charges = utilities only
				cleaningFee: 0,
				// No paymentOutcome — operator hasn't recorded the refund yet.
			},
		},
	}

	created := 0
	for _, sc := range scenarios {
		rm := roomByNumber[sc.roomNumber]
		tn := tenantByIDCard[sc.tenantIDCard]

		// Skip if room already has any contract (avoid clobbering seed base).
		// Logged at WARN so a misconfigured scenario surfaces immediately
		// instead of silently producing fewer notices than the table claims —
		// this is exactly how the original B103/PENDING_PAYMENT bug went
		// unnoticed (B103 is reserved by seedB103History).
		var existingContracts int64
		if err := db.Model(&contract.Contract{}).
			Where("room_id = ?", rm.ID).Count(&existingContracts).Error; err != nil {
			return fmt.Errorf("check contracts %s: %w", rm.Number, err)
		}
		if existingContracts > 0 {
			slog.Warn(
				"move-out seed: scenario skipped — room already has contract(s)",
				"room", rm.Number,
				"scenario_status", sc.status,
				"existing_contracts", existingContracts,
			)
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
		// Set closed_at for COMPLETED notices so the audit timestamp
		// reflects when the close action would have happened.
		if sc.status == moveout.MoveOutStatusCompleted && sc.endOffset != nil {
			closedAt := today.AddDate(0, 0, *sc.endOffset)
			notice.ClosedAt = &closedAt
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

		// Settlement bill — required for any notice past PENDING_SETTLEMENT
		// in real workflow. Skipping this for COMPLETED/READY_TO_CLOSE/
		// PENDING_PAYMENT would surface as Step3DraftMissingFallback in
		// the FE drawer because draftConfirmed depends on settlement_bill_id.
		if sc.settlement != nil {
			actualMoveOut := today.AddDate(0, 0, sc.scheduledOffset)
			if err := attachDevSettlement(db, &notice, &c, actualMoveOut, sc.settlement); err != nil {
				return fmt.Errorf("attach settlement for %s: %w", rm.Number, err)
			}
		}

		created++
	}

	if created > 0 {
		slog.Info("seeded dev move-outs", "count", created)
	}
	return nil
}

// devSettlementSpec describes the settlement bill that should be attached
// to a dev-seed move-out notice. Mirrors what `generate-settlement +
// finalize-settlement + record-payment` would produce in real workflow,
// but inserts directly so the seed doesn't depend on service layer.
type devSettlementSpec struct {
	rentMode    billing.SettlementRentMode
	rentPaid    bool  // skip rent line if previous monthly bill is paid
	cleaningFee int64 // satang; 0 = no cleaning line
	// depositForfeit = true → bill marked DepositForfeited, deposit NOT
	// applied to net. For "broke min months" scenarios.
	depositForfeit bool
	// balanceToZero = true → cleaningFee is auto-adjusted so total exactly
	// equals deposit. Used by the ZERO_BALANCE READY_TO_CLOSE scenario.
	// Has no effect when rentPaid=false (rent line dominates the math).
	balanceToZero bool
	// paymentOutcome / paymentMethod / paymentNote — set when payment has
	// been recorded. Leave nil to seed a "no outcome" state (PENDING_PAYMENT
	// or COMPLETED+UNSETTLED).
	paymentOutcome *moveout.PaymentOutcome
	paymentMethod  *domain.PaymentMethod
	paymentNote    string
}

// attachDevSettlement creates a FINALIZED settlement bill aligned with the
// notice's actual move-out date and the standard EXIT meter inserted by
// the seed (15 water units, 120 elec units), then links it to the notice
// and writes payment fields per the spec.
//
// Bill stays FINALIZED through close in production (CloseMoveOut doesn't
// promote it to PAID), so seeded COMPLETED scenarios keep FINALIZED too —
// the FE doesn't query bill status for queue display, only for the
// settlement detail page.
func attachDevSettlement(
	db *gorm.DB,
	notice *moveout.MoveOutNotice,
	c *contract.Contract,
	actualMoveOut time.Time,
	spec *devSettlementSpec,
) error {
	// Fail fast on inconsistent specs — without rentPaid=true the rent
	// line dominates the math and the cleaning-fee adjustment can't drive
	// total exactly to deposit. Silently producing a non-zero net under a
	// ZERO_BALANCE outcome would violate the FE invariant pinned in smoke
	// (TestRecordPaymentOutcome_NormalizesZeroBalanceOnCompletedBackfill).
	if spec.balanceToZero && !spec.rentPaid {
		return fmt.Errorf("balanceToZero requires rentPaid=true")
	}

	billingMonth := actualMoveOut.Format("2006-01")

	var rentLine *billing.BillLineItem
	var rentAmount int64
	if !spec.rentPaid {
		if spec.rentMode == billing.RentModeFullMonthKeepDeposit {
			rentAmount = c.MonthlyRent
			rentLine = &billing.BillLineItem{
				LineType:    billing.LineItemRoomRent,
				Source:      billing.LineItemSourceAuto,
				Description: "ค่าเช่า (เต็มเดือน)",
				Amount:      rentAmount,
				SortOrder:   1,
			}
		} else {
			day := actualMoveOut.Day()
			totalDays := endOfMonth(actualMoveOut).Day()
			rentAmount = (c.MonthlyRent * int64(day)) / int64(totalDays)
			rentLine = &billing.BillLineItem{
				LineType:    billing.LineItemProrateRent,
				Source:      billing.LineItemSourceAuto,
				Description: fmt.Sprintf("วันที่ 1 - %d (%d วัน)", day, day),
				Amount:      rentAmount,
				SortOrder:   1,
			}
		}
	}

	const waterUnits int64 = 15 // matches the EXIT meter inserted by the seed
	const elecUnits int64 = 120
	waterAmount := waterUnits * c.WaterRatePerUnit
	elecAmount := elecUnits * c.ElectricityRatePerUnit
	cleaningFee := spec.cleaningFee

	// Special-case: ZERO_BALANCE READY_TO_CLOSE wants total == deposit so
	// net_amount lands on exactly 0. We pin rentPaid=true (no rent line)
	// and adjust cleaningFee to absorb the gap. Negative gaps fall back
	// to 0 cleaning — close-enough for a dev demo.
	if spec.balanceToZero && spec.rentPaid {
		cleaningFee = c.DepositAmount - waterAmount - elecAmount
		if cleaningFee < 0 {
			cleaningFee = 0
		}
	}

	total := rentAmount + waterAmount + elecAmount + cleaningFee

	// DepositBalance: positive = refund to tenant, negative = tenant owes.
	// Mirrors `Bill.applyDepositCalculation()` — kept inline so the seed
	// stays decoupled from the unexported domain helper. The FE bill list
	// reads `deposit_balance` to classify settlement rows as collect /
	// refund / zero, so seeding it correctly is required for the
	// settlement section to render anything other than "จบแล้ว".
	var depositBalance int64
	if spec.depositForfeit {
		depositBalance = -total
	} else {
		depositBalance = c.DepositAmount - total
	}

	bill := billing.Bill{
		ContractID:         c.ID,
		BillingMonth:       billingMonth,
		BillType:           billing.BillTypeSettlement,
		Status:             billing.BillStatusFinalized,
		DepositAmount:      c.DepositAmount,
		DepositBalance:     depositBalance,
		DepositForfeited:   spec.depositForfeit,
		TotalAmount:        total,
		SettlementRentMode: spec.rentMode,
		RentPaid:           spec.rentPaid,
	}
	if spec.depositForfeit {
		bill.DepositApp = billing.DepositAppNone
	}

	items := []billing.BillLineItem{}
	if rentLine != nil {
		items = append(items, *rentLine)
	}
	items = append(items,
		billing.BillLineItem{
			LineType:    billing.LineItemWater,
			Source:      billing.LineItemSourceAuto,
			Description: fmt.Sprintf("ค่าน้ำ %d หน่วย", waterUnits),
			Amount:      waterAmount,
			Quantity:    int(waterUnits),
			UnitPrice:   c.WaterRatePerUnit,
			SortOrder:   2,
		},
		billing.BillLineItem{
			LineType:    billing.LineItemElectricity,
			Source:      billing.LineItemSourceAuto,
			Description: fmt.Sprintf("ค่าไฟฟ้า %d หน่วย", elecUnits),
			Amount:      elecAmount,
			Quantity:    int(elecUnits),
			UnitPrice:   c.ElectricityRatePerUnit,
			SortOrder:   3,
		},
	)
	if cleaningFee > 0 {
		items = append(items, billing.BillLineItem{
			LineType:    billing.LineItemCleaningFee,
			Source:      billing.LineItemSourceAuto,
			Description: "ค่าทำความสะอาด",
			Amount:      cleaningFee,
			SortOrder:   4,
		})
	}

	if err := createBillWithLines(db, &bill, items); err != nil {
		return fmt.Errorf("create settlement bill: %w", err)
	}

	// Notice net_amount uses the inverted convention vs DepositBalance:
	// notice net is "what tenant owes" (positive = owes more, negative =
	// gets refund). Equivalent to -depositBalance.
	netAmount := -depositBalance

	updates := map[string]any{
		"settlement_bill_id": bill.ID,
		"net_amount":         netAmount,
	}
	if spec.paymentOutcome != nil {
		updates["payment_outcome"] = *spec.paymentOutcome
	}
	if spec.paymentMethod != nil {
		updates["payment_method"] = *spec.paymentMethod
	}
	if spec.paymentNote != "" {
		updates["payment_note"] = spec.paymentNote
	}

	return db.Model(&moveout.MoveOutNotice{}).Where("id = ?", notice.ID).Updates(updates).Error
}

func truncateToDate(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
