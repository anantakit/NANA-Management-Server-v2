package seed

import (
	"errors"
	"fmt"
	"log/slog"
	"time"

	"nana/internal/apartment"
	"nana/internal/billing"
	"nana/internal/contract"
	"nana/internal/meterreading"
	"nana/internal/moveout"
	"nana/internal/room"
	"nana/internal/tenant"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Smoke test fixtures for the billing / move-out smoke suites.
//
// All fixtures are tagged with ID-card prefix "9999" so CleanupSmokeData
// can find and remove them cleanly after testing.
//
// Sorted by TC number. Move-out fixtures dominate (TC1–TC25); the bill-edit
// fixtures (TC26–TC27) are ACTIVE contracts with a DRAFT monthly bill in
// the current month — no move-out notice involved.
//
// | TC | Room | Status             | Scenario                                          |
// |----|------|--------------------|---------------------------------------------------|
// |  1 | B105 | PENDING_SETTLEMENT | Happy path — past minMonths, no draft             |
// |  3 | B201 | PENDING_SETTLEMENT | MinMonths threshold flip (PRORATED fails)         |
// |  4 | B202 | PENDING_SETTLEMENT | Has existing DRAFT settlement bill                |
// |  6 | B203 | PENDING_SETTLEMENT | Advance rent already paid (M-1 PAID)              |
// |  7 | B204 | PENDING_SETTLEMENT | Has absorbed bills (FINALIZED earlier)            |
// | 10 | B205 | PENDING_METER      | No actual_move_out_date yet                       |
// | 13 | C201 | PENDING_SETTLEMENT | Paid monthly bill carry-forward                   |
// | 14 | C202 | PENDING_SETTLEMENT | ONE unpaid monthly bill absorbed                  |
// | 15 | C203 | PENDING_SETTLEMENT | THREE unpaid monthly bills                        |
// | 16 | C204 | PENDING_SETTLEMENT | Scheduled ≠ actual move-out date                  |
// | 17 | C205 | PENDING_SETTLEMENT | Actual move-out date backdated                    |
// | 18 | C101 | PENDING_SETTLEMENT | Deposit refund (covers charges)                   |
// | 19 | C102 | PENDING_SETTLEMENT | Deposit shortfall (owe money)                     |
// | 20 | D101 | PENDING_METER      | Invalid exit < prior MONTHLY                      |
// | 21 | E201 | PENDING_SETTLEMENT | Missing baseline / incomplete data                |
// | 22 | D201 | PENDING_SETTLEMENT | Queue: draft + 2 MANUAL items (confirm modal)     |
// | 23 | D202 | PENDING_SETTLEMENT | Queue: draft + FULL_MONTH rent mode (continuity)  |
// | 24 | D103 | PENDING_SETTLEMENT | Zero balance (charges = deposit)                  |
// | 25 | D104 | PENDING_SETTLEMENT | Deposit forfeited (short stay)                    |
// | 26 | E101 | ACTIVE contract    | Bill-edit: DRAFT MONTHLY clean, no overrides      |
// | 27 | E102 | ACTIVE contract    | Bill-edit: DRAFT MONTHLY w/ pre-existing override |
//
// Date-sensitive: TC3 requires mid-month "today" to flip across minMonths.
// Works reliably when today.Day() <= daysInMonth(today) - 3.
//
// Idempotent — skipped if any smoke tenant already exists.

// SmokeIDCardPrefix is the shared marker that identifies smoke-test fixtures.
// Exported so dev endpoints can filter on it without duplicating the string.
const SmokeIDCardPrefix = "9999"

// smokeIDCardPrefix is the internal alias used by the seed/cleanup routines.
// Keeping both names for clarity; they are always equal.
const smokeIDCardPrefix = SmokeIDCardPrefix

// SmokeFixture is a lightweight projection returned by ListSmokeFixtures —
// used by the dev-only HTTP endpoint to expose fixture IDs to Playwright.
type SmokeFixture struct {
	IDCard             string  `json:"id_card"`
	TenantName         string  `json:"tenant_name"`
	RoomNumber         string  `json:"room_number"`
	NoticeID           string  `json:"notice_id"`
	Status             string  `json:"status"`
	HasDraft           bool    `json:"has_draft"`
	ActualMoveOutDate  *string `json:"actual_move_out_date"`
	SettlementRentMode string  `json:"settlement_rent_mode"`
	ManualItemCount    int     `json:"manual_item_count"`
}

// ListSmokeFixtures returns all active smoke-test fixtures in a compact form
// suitable for the dev HTTP endpoint. Filters by SmokeIDCardPrefix so the
// marker lives in ONE place.
func ListSmokeFixtures(db *gorm.DB) ([]SmokeFixture, error) {
	rows, err := db.Raw(`
		SELECT t.id_card, t.full_name, r.number, n.id::text, n.status,
		       n.settlement_bill_id IS NOT NULL AS has_draft,
		       to_char(n.actual_move_out_date, 'YYYY-MM-DD') AS actual_date,
		       COALESCE(b.settlement_rent_mode, '') AS settlement_rent_mode,
		       COALESCE((SELECT COUNT(*) FROM bill_line_items WHERE bill_line_items.bill_id = b.id AND bill_line_items.source = 'MANUAL'), 0) AS manual_item_count
		FROM tenants t
		JOIN contracts c ON c.tenant_id = t.id
		JOIN rooms r ON r.id = c.room_id
		JOIN move_out_notices n ON n.contract_id = c.id
		LEFT JOIN bills b ON b.id = n.settlement_bill_id AND b.deleted_at IS NULL
		WHERE t.id_card LIKE ? AND t.deleted_at IS NULL
		ORDER BY t.id_card
	`, SmokeIDCardPrefix+"%").Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SmokeFixture
	for rows.Next() {
		var f SmokeFixture
		var actualDate *string
		if err := rows.Scan(&f.IDCard, &f.TenantName, &f.RoomNumber, &f.NoticeID, &f.Status, &f.HasDraft, &actualDate, &f.SettlementRentMode, &f.ManualItemCount); err != nil {
			return nil, err
		}
		f.ActualMoveOutDate = actualDate
		out = append(out, f)
	}
	return out, nil
}

// smokeRoomNumbers is the exclusive set of rooms used by smoke fixtures.
// Must be VACANT in base seed (none overlap with seed_dev_moveout.go).
var smokeRoomNumbers = []string{
	"B105", "B201", "B202", "B203", "B204", "B205",
	// Scenario smoke v1 (TC13–TC21)
	"C201", "C202", "C203", "C204", "C205", "C101", "C102", "D101",
	"E201",
	// Zero-balance + forfeiture smoke (TC24–TC25)
	"D103", "D104",
	// Queue settlement smoke (TC22–TC23)
	"D201", "D202",
	// Bill-edit smoke (TC26–TC27): ACTIVE contract + DRAFT MONTHLY bill
	"E101", "E102",
	// Batch-replan smoke (TC28–TC29): ACTIVE contract, no meter, no move-out
	// → produces SKIPPED(MISSING_METER_READING) in a monthly batch run.
	// Rooms must be VACANT in every other dev seed (A101–A111 / A108–A109 /
	// A110–A111 / B101–B104 / B201–B205 etc. are all taken by dev fixtures
	// or smoke move-out seeds) — D105/D106 are clean across every other
	// seeder so cleanup → seed re-occupies them with no leakage.
	"D105", "D106",
}

func seedDevSmoke(db *gorm.DB) error {
	var existing int64
	if err := db.Model(&tenant.Tenant{}).
		Where("id_card LIKE ?", smokeIDCardPrefix+"%").
		Count(&existing).Error; err != nil {
		return fmt.Errorf("check existing smoke tenants: %w", err)
	}
	if existing > 0 {
		return nil
	}

	var apt apartment.Apartment
	if err := db.Where("name = ?", "นานาคอร์ท").First(&apt).Error; err != nil {
		return fmt.Errorf("find apartment: %w", err)
	}

	today := truncateToDate(time.Now().UTC())

	var rooms []room.Room
	if err := db.Where("apartment_id = ? AND number IN ?", apt.ID, smokeRoomNumbers).Find(&rooms).Error; err != nil {
		return fmt.Errorf("find smoke rooms: %w", err)
	}
	roomByNumber := make(map[string]room.Room, len(rooms))
	for _, r := range rooms {
		roomByNumber[r.Number] = r
	}
	if len(roomByNumber) != len(smokeRoomNumbers) {
		slog.Warn("smoke seed: missing rooms", "found", len(roomByNumber), "want", len(smokeRoomNumbers))
		return nil
	}

	// Calibrate threshold-flip dates for TC3:
	// start_date = today + gap - minMonths months, where gap is mid-point
	// of days remaining in current month. Guarantees:
	//   today (PRORATED)   < start + minMonths → fails
	//   EOM(today) (FULL)  >= start + minMonths → passes
	lastDay := endOfMonth(today).Day()
	daysRemaining := lastDay - today.Day()
	flipGap := daysRemaining / 2
	if flipGap < 1 {
		flipGap = 1 // guard edge of month; TC3 may not demonstrate flip reliably
	}

	// --- TC1: Happy path, no draft ---
	if _, _, _, err := createSmokeScenarioReturn(db, apt, roomByNumber["B105"], smokeTenant{
		idCard: "9999999999001",
		name:   "TC1_SMOKE ปกติไม่มีร่าง",
		phone:  "0999000001",
	}, smokeScenario{
		contractStartMonths: 12,
		minMonths:           6,
		actualOffset:        -3, // 3 days ago
		note:                "SMOKE TC1 — ผ่าน minMonths, ไม่มี draft",
		status:              moveout.MoveOutStatusPendingSettlement,
		withExitMeter:       true,
		today:               today,
	}); err != nil {
		return err
	}

	// --- TC3: MinMonths threshold flip ---
	tc3Start := today.AddDate(0, -6, flipGap) // ~5 months ~X days ago
	if err := createCustomStartScenario(db, apt, roomByNumber["B201"], smokeTenant{
		idCard: "9999999999003",
		name:   "TC3_SMOKE เกือบครบขั้นต่ำ",
		phone:  "0999000003",
	}, customStartScenario{
		startDate:     tc3Start,
		minMonths:     6,
		noticeOffset:  -5,
		actualOffset:  0, // today
		note:          "SMOKE TC3 — PRORATED fails / FULL_MONTH passes minMonths",
		status:        moveout.MoveOutStatusPendingSettlement,
		withExitMeter: true,
		today:         today,
	}); err != nil {
		return err
	}

	// --- TC4: Has existing DRAFT settlement bill ---
	tc4Notice, tc4Contract, tc4Room, err := createSmokeScenarioReturn(db, apt, roomByNumber["B202"], smokeTenant{
		idCard: "9999999999004",
		name:   "TC4_SMOKE มีร่างอยู่แล้ว",
		phone:  "0999000004",
	}, smokeScenario{
		contractStartMonths: 12,
		minMonths:           6,
		actualOffset:        -4,
		note:                "SMOKE TC4 — has DRAFT settlement bill",
		status:              moveout.MoveOutStatusPendingSettlement,
		withExitMeter:       true,
		today:               today,
	})
	if err != nil {
		return err
	}
	if err := attachDraftSettlementBill(db, tc4Notice, tc4Contract, tc4Room, today.AddDate(0, 0, -4)); err != nil {
		return fmt.Errorf("attach draft bill for TC4: %w", err)
	}

	// --- TC6: Advance rent already paid ---
	tc6Notice, tc6Contract, _, err := createSmokeScenarioReturn(db, apt, roomByNumber["B203"], smokeTenant{
		idCard: "9999999999006",
		name:   "TC6_SMOKE ค่าเช่าจ่ายแล้ว",
		phone:  "0999000006",
	}, smokeScenario{
		contractStartMonths: 12,
		minMonths:           6,
		actualOffset:        -2,
		note:                "SMOKE TC6 — advance rent for move-out month paid (M-1 PAID bill)",
		status:              moveout.MoveOutStatusPendingSettlement,
		withExitMeter:       true,
		today:               today,
	})
	if err != nil {
		return err
	}
	moveOutMonth := tc6Notice.ActualMoveOutDate.Format("2006-01")
	prevMonth := previousMonthStr(moveOutMonth)
	if err := createPaidMonthlyBill(db, tc6Contract, prevMonth); err != nil {
		return fmt.Errorf("create paid M-1 bill for TC6: %w", err)
	}

	// --- TC7: Has absorbed bills (earlier unpaid FINALIZED) ---
	tc7Notice, tc7Contract, _, err := createSmokeScenarioReturn(db, apt, roomByNumber["B204"], smokeTenant{
		idCard: "9999999999007",
		name:   "TC7_SMOKE บิลค้างโดนรวม",
		phone:  "0999000007",
	}, smokeScenario{
		contractStartMonths: 6,
		minMonths:           6,
		actualOffset:        -1,
		note:                "SMOKE TC7 — 2 unpaid FINALIZED monthly bills to be absorbed",
		status:              moveout.MoveOutStatusPendingSettlement,
		withExitMeter:       true,
		today:               today,
	})
	if err != nil {
		return err
	}
	moveOutMonth7 := tc7Notice.ActualMoveOutDate.Format("2006-01")
	// Create 2 FINALIZED bills for M-3 and M-2 (BEFORE M-1, so they absorb fully)
	for i := 3; i >= 2; i-- {
		bm := monthOffset(moveOutMonth7, -i)
		if err := createFinalizedMonthlyBill(db, tc7Contract, bm); err != nil {
			return fmt.Errorf("create finalized bill %s for TC7: %w", bm, err)
		}
	}

	// --- TC10: No actual_move_out_date (PENDING_METER) ---
	if _, _, _, err := createSmokeScenarioReturn(db, apt, roomByNumber["B205"], smokeTenant{
		idCard: "9999999999010",
		name:   "TC10_SMOKE ยังไม่ได้จดมิเตอร์",
		phone:  "0999000010",
	}, smokeScenario{
		contractStartMonths: 10,
		minMonths:           6,
		scheduledOffset:     3,
		actualOffset:        0, // ignored — PENDING_METER has no actual date
		skipActualDate:      true,
		note:                "SMOKE TC10 — no actual_move_out_date, preview button must be disabled",
		status:              moveout.MoveOutStatusPendingMeter,
		withExitMeter:       false,
		today:               today,
	}); err != nil {
		return err
	}

	// ─── Scenario smoke v1 (TC13–TC20) ────────────────────────────────
	// Backs playwright-test-settlement-scenario-smoke.js. Each fixture
	// targets one business-risk scenario from senario.md.

	// --- TC13: Paid monthly bill carry-forward (no absorbed section) ---
	// Rent for move-out month already covered by M-1 PAID bill (advance-billing).
	// Distinct from TC6 so the scenario suite has its own fixture.
	tc13Notice, tc13Contract, _, err := createSmokeScenarioReturn(db, apt, roomByNumber["C201"], smokeTenant{
		idCard: "9999999999013",
		name:   "TC13_SMOKE ค่าเช่าเดือนนี้จ่ายแล้ว",
		phone:  "0999000013",
	}, smokeScenario{
		contractStartMonths: 12,
		minMonths:           6,
		actualOffset:        -2,
		note:                "SMOKE TC13 — carry-forward, rent_paid=true, no absorbed",
		status:              moveout.MoveOutStatusPendingSettlement,
		withExitMeter:       true,
		today:               today,
	})
	if err != nil {
		return err
	}
	tc13Month := previousMonthStr(tc13Notice.ActualMoveOutDate.Format("2006-01"))
	if err := createPaidMonthlyBill(db, tc13Contract, tc13Month); err != nil {
		return fmt.Errorf("create paid M-1 bill for TC13: %w", err)
	}

	// --- TC14: ONE unpaid monthly bill absorbed ---
	tc14Notice, tc14Contract, _, err := createSmokeScenarioReturn(db, apt, roomByNumber["C202"], smokeTenant{
		idCard: "9999999999014",
		name:   "TC14_SMOKE บิลค้างโดนรวม 1 ใบ",
		phone:  "0999000014",
	}, smokeScenario{
		contractStartMonths: 6,
		minMonths:           6,
		actualOffset:        -1,
		note:                "SMOKE TC14 — single unpaid FINALIZED bill absorbed",
		status:              moveout.MoveOutStatusPendingSettlement,
		withExitMeter:       true,
		today:               today,
	})
	if err != nil {
		return err
	}
	tc14MoveOutMonth := tc14Notice.ActualMoveOutDate.Format("2006-01")
	if err := createFinalizedMonthlyBill(db, tc14Contract, monthOffset(tc14MoveOutMonth, -2)); err != nil {
		return fmt.Errorf("create finalized bill for TC14: %w", err)
	}

	// --- TC15: THREE unpaid monthly bills ---
	tc15Notice, tc15Contract, _, err := createSmokeScenarioReturn(db, apt, roomByNumber["C203"], smokeTenant{
		idCard: "9999999999015",
		name:   "TC15_SMOKE บิลค้างหลายใบ",
		phone:  "0999000015",
	}, smokeScenario{
		contractStartMonths: 8,
		minMonths:           6,
		actualOffset:        -1,
		note:                "SMOKE TC15 — 3 unpaid FINALIZED monthly bills",
		status:              moveout.MoveOutStatusPendingSettlement,
		withExitMeter:       true,
		today:               today,
	})
	if err != nil {
		return err
	}
	tc15MoveOutMonth := tc15Notice.ActualMoveOutDate.Format("2006-01")
	for i := 4; i >= 2; i-- {
		bm := monthOffset(tc15MoveOutMonth, -i)
		if err := createFinalizedMonthlyBill(db, tc15Contract, bm); err != nil {
			return fmt.Errorf("create finalized bill %s for TC15: %w", bm, err)
		}
	}

	// --- TC16: scheduled_move_out_date ≠ actual_move_out_date ---
	// Scheduled was 2 days ago; admin recorded actual 6 days ago (moved out earlier).
	if _, _, _, err := createSmokeScenarioReturn(db, apt, roomByNumber["C204"], smokeTenant{
		idCard: "9999999999016",
		name:   "TC16_SMOKE นัดกับจริงต่างกัน",
		phone:  "0999000016",
	}, smokeScenario{
		contractStartMonths: 12,
		minMonths:           6,
		noticeOffset:        -9,
		scheduledOffset:     -2,
		actualOffset:        -6,
		note:                "SMOKE TC16 — scheduled=-2d, actual=-6d (differ)",
		status:              moveout.MoveOutStatusPendingSettlement,
		withExitMeter:       true,
		today:               today,
	}); err != nil {
		return err
	}

	// --- TC17: Backdated actual move-out date (far in past) ---
	// Simulates admin recording move-out ~10 days after the fact.
	if _, _, _, err := createSmokeScenarioReturn(db, apt, roomByNumber["C205"], smokeTenant{
		idCard: "9999999999017",
		name:   "TC17_SMOKE บันทึกย้อนหลัง",
		phone:  "0999000017",
	}, smokeScenario{
		contractStartMonths: 12,
		minMonths:           6,
		noticeOffset:        -13,
		scheduledOffset:     -10,
		actualOffset:        -10,
		note:                "SMOKE TC17 — actual backdated 10 days",
		status:              moveout.MoveOutStatusPendingSettlement,
		withExitMeter:       true,
		today:               today,
	}); err != nil {
		return err
	}

	// --- TC18: Deposit refund (deposit > charges) ---
	// Air room (deposit=฿3,000), rent_paid=true via PAID M-1 bill.
	// Settlement charges = utilities + cleaning only → well below deposit → REFUND.
	tc18Notice, tc18Contract, _, err := createSmokeScenarioReturn(db, apt, roomByNumber["C101"], smokeTenant{
		idCard: "9999999999018",
		name:   "TC18_SMOKE คืนเงินประกัน",
		phone:  "0999000018",
	}, smokeScenario{
		contractStartMonths: 12,
		minMonths:           6,
		actualOffset:        -2,
		note:                "SMOKE TC18 — deposit refund (rent paid, small charges)",
		status:              moveout.MoveOutStatusPendingSettlement,
		withExitMeter:       true,
		today:               today,
	})
	if err != nil {
		return err
	}
	tc18Month := previousMonthStr(tc18Notice.ActualMoveOutDate.Format("2006-01"))
	if err := createPaidMonthlyBill(db, tc18Contract, tc18Month); err != nil {
		return fmt.Errorf("create paid M-1 bill for TC18: %w", err)
	}

	// --- TC19: Deposit shortfall (charges > deposit) ---
	// Air room (deposit=฿3,000), 3 unpaid FINALIZED bills absorbed (~฿4,480 each)
	// + exit-period charges → far exceeds deposit → PAY_MORE.
	tc19Notice, tc19Contract, _, err := createSmokeScenarioReturn(db, apt, roomByNumber["C102"], smokeTenant{
		idCard: "9999999999019",
		name:   "TC19_SMOKE ประกันไม่พอ",
		phone:  "0999000019",
	}, smokeScenario{
		contractStartMonths: 8,
		minMonths:           6,
		actualOffset:        -1,
		note:                "SMOKE TC19 — deposit shortfall (3 absorbed bills > deposit)",
		status:              moveout.MoveOutStatusPendingSettlement,
		withExitMeter:       true,
		today:               today,
	})
	if err != nil {
		return err
	}
	tc19MoveOutMonth := tc19Notice.ActualMoveOutDate.Format("2006-01")
	for i := 4; i >= 2; i-- {
		bm := monthOffset(tc19MoveOutMonth, -i)
		if err := createFinalizedMonthlyBill(db, tc19Contract, bm); err != nil {
			return fmt.Errorf("create finalized bill %s for TC19: %w", bm, err)
		}
	}

	// --- TC20: Invalid exit reading (must reject current < previous) ---
	// PENDING_METER fixture with a committed MONTHLY reading so the exit-meter
	// drawer shows non-zero "previous" values. Playwright enters a lower value
	// and expects the UI to reject.
	tc20Room := roomByNumber["D101"]
	if _, _, _, err := createSmokeScenarioReturn(db, apt, tc20Room, smokeTenant{
		idCard: "9999999999020",
		name:   "TC20_SMOKE เลขมิเตอร์ต่ำกว่าครั้งก่อน",
		phone:  "0999000020",
	}, smokeScenario{
		contractStartMonths: 10,
		minMonths:           6,
		scheduledOffset:     3,
		actualOffset:        0, // ignored — PENDING_METER has no actual date
		skipActualDate:      true,
		note:                "SMOKE TC20 — PENDING_METER w/ prior MONTHLY reading",
		status:              moveout.MoveOutStatusPendingMeter,
		withExitMeter:       false,
		today:               today,
	}); err != nil {
		return err
	}
	// Prior MONTHLY reading supplies the "ครั้งก่อน" reference in the drawer.
	// Current values (elec=2500, water=300) are well above the threshold
	// Playwright will type (e.g. elec=100, water=50) so the negative-usage
	// guard fires immediately.
	tc20PrevMonth := previousMonthStr(today.Format("2006-01"))
	if err := createMonthlyMeterReading(db, tc20Room.ID, tc20PrevMonth, 2200, 2500, 250, 300); err != nil {
		return fmt.Errorf("create prior MONTHLY reading for TC20: %w", err)
	}

	// --- TC21: Missing baseline / incomplete data (C3) ---
	// Short contract (1 month), no prior monthly bills, no historical meter
	// readings → baseline API returns has_enough_data=false for this room.
	// Preview drawer must degrade gracefully: no NaN, no undefined, helpers hidden.
	if _, _, _, err := createSmokeScenarioReturn(db, apt, roomByNumber["E201"], smokeTenant{
		idCard: "9999999999021",
		name:   "TC21_SMOKE ข้อมูลไม่ครบ",
		phone:  "0999000021",
	}, smokeScenario{
		contractStartMonths: 1,
		minMonths:           6,
		actualOffset:        -1,
		note:                "SMOKE TC21 — no baseline, no bills, fresh contract",
		status:              moveout.MoveOutStatusPendingSettlement,
		withExitMeter:       true,
		today:               today,
	}); err != nil {
		return err
	}

	// --- TC24: Zero balance (charges = deposit exactly) ---
	// FAN room (deposit=฿2,000), rent_paid=true via PAID M-1 bill.
	// Exit meter tuned so utilities + cleaning + key_service = deposit exactly:
	//   electricity 150 units × ฿8 = ฿1,200
	//   water 25 units × ฿18 = ฿450
	//   cleaning fee = ฿300
	//   key service = ฿50  (config fee, added 2026-05-11)
	//   total = ฿2,000 = deposit → ZERO_BALANCE
	// Whenever config fees change (CLEANING_FEE / KEY_SERVICE / future fees),
	// retune the meter to keep charges == deposit. See smoke_formula_contract.md.
	tc24Notice, tc24Contract, _, err := createSmokeScenarioReturn(db, apt, roomByNumber["D103"], smokeTenant{
		idCard: "9999999999024",
		name:   "TC24_SMOKE ประกันพอดี",
		phone:  "0999000024",
	}, smokeScenario{
		contractStartMonths: 12,
		minMonths:           6,
		actualOffset:        -2,
		note:                "SMOKE TC24 — zero balance (charges = deposit exactly)",
		status:              moveout.MoveOutStatusPendingSettlement,
		withExitMeter:       false, // custom meter below
		today:               today,
	})
	if err != nil {
		return err
	}
	// Custom exit meter: 150 units electricity, 25 units water
	tc24ReadingDate := today.AddDate(0, 0, -2)
	tc24Exit := meterreading.MeterReading{
		RoomID:              roomByNumber["D103"].ID,
		ReadingType:         meterreading.ReadingTypeExit,
		ReadingDateActual:   &tc24ReadingDate,
		ElectricityPrevious: 1000,
		ElectricityCurrent:  1150, // 150 units
		WaterPrevious:       100,
		WaterCurrent:        125, // 25 units
	}
	if err := db.Create(&tc24Exit).Error; err != nil {
		return fmt.Errorf("create exit reading for TC24: %w", err)
	}
	tc24Month := previousMonthStr(tc24Notice.ActualMoveOutDate.Format("2006-01"))
	if err := createPaidMonthlyBill(db, tc24Contract, tc24Month); err != nil {
		return fmt.Errorf("create paid M-1 bill for TC24: %w", err)
	}

	// --- TC25: Deposit forfeited (short stay, not meeting minMonths) ---
	// FAN room (deposit=฿2,000), contractStartMonths=3 < minMonths=6.
	// No M-1 PAID bill → pro-rate rent included in charges.
	// Standard exit meter → total charges > deposit → PAY_MORE,
	// but deposit_returnable=false → any excess would be forfeited.
	if _, _, _, err := createSmokeScenarioReturn(db, apt, roomByNumber["D104"], smokeTenant{
		idCard: "9999999999025",
		name:   "TC25_SMOKE ริบเงินประกัน",
		phone:  "0999000025",
	}, smokeScenario{
		contractStartMonths: 3,
		minMonths:           6,
		actualOffset:        -2,
		note:                "SMOKE TC25 — deposit forfeited (3 months < 6 min)",
		status:              moveout.MoveOutStatusPendingSettlement,
		withExitMeter:       true,
		today:               today,
	}); err != nil {
		return err
	}

	// ─── Queue settlement smoke (TC22–TC23) ──────────────────────────

	// --- TC22: Has draft + MANUAL items (for queue drawer confirm modal) ---
	tc22Notice, tc22Contract, tc22Room, err := createSmokeScenarioReturn(db, apt, roomByNumber["D201"], smokeTenant{
		idCard: "9999999999022",
		name:   "TC22_SMOKE มีร่าง+รายการปรับเอง",
		phone:  "0999000022",
	}, smokeScenario{
		contractStartMonths: 12,
		minMonths:           6,
		actualOffset:        -3,
		note:                "SMOKE TC22 — draft with MANUAL line items",
		status:              moveout.MoveOutStatusPendingSettlement,
		withExitMeter:       true,
		today:               today,
	})
	if err != nil {
		return err
	}
	if err := attachDraftSettlementBillWithManualItems(db, tc22Notice, tc22Contract, tc22Room, today.AddDate(0, 0, -3)); err != nil {
		return fmt.Errorf("attach draft+manual for TC22: %w", err)
	}

	// --- TC23: Has draft with FULL_MONTH rent mode (for rent mode continuity) ---
	tc23Notice, tc23Contract, tc23Room, err := createSmokeScenarioReturn(db, apt, roomByNumber["D202"], smokeTenant{
		idCard: "9999999999023",
		name:   "TC23_SMOKE ร่างเต็มเดือน",
		phone:  "0999000023",
	}, smokeScenario{
		contractStartMonths: 12,
		minMonths:           6,
		actualOffset:        -4,
		note:                "SMOKE TC23 — draft with FULL_MONTH rent mode",
		status:              moveout.MoveOutStatusPendingSettlement,
		withExitMeter:       true,
		today:               today,
	})
	if err != nil {
		return err
	}
	if err := attachDraftSettlementBillFullMonth(db, tc23Notice, tc23Contract, tc23Room, today.AddDate(0, 0, -4)); err != nil {
		return fmt.Errorf("attach full-month draft for TC23: %w", err)
	}

	// ─── Bill-edit smoke (TC26–TC27) ─────────────────────────────────
	// ACTIVE contract + DRAFT MONTHLY bill in the current billing month.
	// Anchors the playwright bill-edit smoke (no move-out involved).

	// --- TC26: clean DRAFT MONTHLY — no overrides, no manual items ---
	if err := createBillEditScenario(db, apt, roomByNumber["E101"], smokeTenant{
		idCard: "9999999999026",
		name:   "TC26_SMOKE บิลร่างยังไม่แก้",
		phone:  "0999000026",
	}, today, nil); err != nil {
		return fmt.Errorf("seed TC26: %w", err)
	}

	// --- TC27: DRAFT MONTHLY with pre-existing ROOM_RENT override ---
	// Override drops 500 baht from rent (250000 → 200000 satang). Used by
	// the "reset by empty input" scenario in the bill-edit smoke — admin
	// clears the input, save → BE removes the override → no more
	// "เดิม ฿X" hint on next fetch.
	tc27Overrides := billing.OverrideMap{
		string(billing.LineItemRoomRent): 200000,
	}
	if err := createBillEditScenario(db, apt, roomByNumber["E102"], smokeTenant{
		idCard: "9999999999027",
		name:   "TC27_SMOKE บิลร่างมี override เดิม",
		phone:  "0999000027",
	}, today, tc27Overrides); err != nil {
		return fmt.Errorf("seed TC27: %w", err)
	}

	// ─── Batch-replan smoke (TC28–TC29) ──────────────────────────────
	// ACTIVE contract + NO meter for current month + NO move-out notice.
	// When the monthly batch runs for นานาคอร์ท, both rows land as
	// SKIPPED(MISSING_METER_READING) — the exact pre-state the
	// playwright-test-bill-batch-replan-smoke needs:
	//   TC28 (A103) → happy path (record meter → replan → CREATED)
	//   TC29 (A104) → fail path (mocked replan 500 → warning toast)

	if err := createMissingMeterScenario(db, apt, roomByNumber["D105"], smokeTenant{
		idCard: "9999999999028",
		name:   "TC28_SMOKE รอจดมิเตอร์ (replan happy)",
		phone:  "0999000028",
	}); err != nil {
		return fmt.Errorf("seed TC28: %w", err)
	}

	if err := createMissingMeterScenario(db, apt, roomByNumber["D106"], smokeTenant{
		idCard: "9999999999029",
		name:   "TC29_SMOKE รอจดมิเตอร์ (replan fail)",
		phone:  "0999000029",
	}); err != nil {
		return fmt.Errorf("seed TC29: %w", err)
	}

	slog.Info("seeded smoke test fixtures", "count", 23)
	return nil
}

// CleanupSmokeData removes all smoke fixtures (tenants, contracts, notices,
// bills, meter readings) and resets affected rooms to VACANT.
// Idempotent — safe to call multiple times.
func CleanupSmokeData(db *gorm.DB) error {
	var tenants []tenant.Tenant
	if err := db.Where("id_card LIKE ?", smokeIDCardPrefix+"%").Find(&tenants).Error; err != nil {
		return fmt.Errorf("find smoke tenants: %w", err)
	}
	if len(tenants) == 0 {
		return nil
	}

	tenantIDs := make([]uuid.UUID, len(tenants))
	for i, t := range tenants {
		tenantIDs[i] = t.ID
	}

	var contracts []contract.Contract
	if err := db.Unscoped().Where("tenant_id IN ?", tenantIDs).Find(&contracts).Error; err != nil {
		return fmt.Errorf("find smoke contracts: %w", err)
	}
	contractIDs := make([]uuid.UUID, len(contracts))
	roomIDs := make([]uuid.UUID, len(contracts))
	for i, c := range contracts {
		contractIDs[i] = c.ID
		roomIDs[i] = c.RoomID
	}

	// Delete order (respects FKs):
	//  null out notice.settlement_bill_id → delete line items → bills
	//    → notices → meter readings → contracts → tenants
	if len(contractIDs) > 0 {
		// Null out notice → bill FK so bills can be deleted
		if err := db.Unscoped().Model(&moveout.MoveOutNotice{}).
			Where("contract_id IN ?", contractIDs).
			Update("settlement_bill_id", nil).Error; err != nil {
			return fmt.Errorf("clear smoke notice settlement_bill_id: %w", err)
		}
		// Bill line items
		if err := db.Unscoped().
			Where("bill_id IN (SELECT id FROM bills WHERE contract_id IN ?)", contractIDs).
			Delete(&billing.BillLineItem{}).Error; err != nil {
			return fmt.Errorf("delete smoke line items: %w", err)
		}
		// Bills
		if err := db.Unscoped().Where("contract_id IN ?", contractIDs).Delete(&billing.Bill{}).Error; err != nil {
			return fmt.Errorf("delete smoke bills: %w", err)
		}
		// Notices
		if err := db.Unscoped().Where("contract_id IN ?", contractIDs).Delete(&moveout.MoveOutNotice{}).Error; err != nil {
			return fmt.Errorf("delete smoke notices: %w", err)
		}
	}
	if len(roomIDs) > 0 {
		// Meter readings
		if err := db.Unscoped().Where("room_id IN ?", roomIDs).Delete(&meterreading.MeterReading{}).Error; err != nil {
			return fmt.Errorf("delete smoke meter readings: %w", err)
		}
		// Reset rooms to VACANT
		if err := db.Model(&room.Room{}).Where("id IN ?", roomIDs).Update("status", room.RoomStatusVacant).Error; err != nil {
			return fmt.Errorf("reset smoke rooms: %w", err)
		}
	}
	if len(contractIDs) > 0 {
		if err := db.Unscoped().Where("id IN ?", contractIDs).Delete(&contract.Contract{}).Error; err != nil {
			return fmt.Errorf("delete smoke contracts: %w", err)
		}
	}
	if err := db.Unscoped().Where("id IN ?", tenantIDs).Delete(&tenant.Tenant{}).Error; err != nil {
		return fmt.Errorf("delete smoke tenants: %w", err)
	}

	slog.Info("cleaned up smoke test fixtures", "tenants", len(tenants), "contracts", len(contracts))
	return nil
}

// --- Internal helpers ---

type smokeTenant struct {
	idCard string
	name   string
	phone  string
}

type smokeScenario struct {
	contractStartMonths int
	minMonths           int
	noticeOffset        int // days from today; default -3 if zero
	scheduledOffset     int // days from today; default actualOffset if zero
	actualOffset        int // days from today for actual_move_out_date
	skipActualDate      bool
	note                string
	status              moveout.MoveOutStatus
	withExitMeter       bool
	today               time.Time
}

type customStartScenario struct {
	startDate     time.Time
	minMonths     int
	noticeOffset  int
	actualOffset  int
	note          string
	status        moveout.MoveOutStatus
	withExitMeter bool
	today         time.Time
}


// createSmokeScenarioReturn creates tenant + contract + notice + exit meter
// (optional) and returns them for further customization.
func createSmokeScenarioReturn(db *gorm.DB, apt apartment.Apartment, rm room.Room, t smokeTenant, sc smokeScenario) (*moveout.MoveOutNotice, *contract.Contract, *room.Room, error) {
	tn, err := ensureSmokeTenant(db, t)
	if err != nil {
		return nil, nil, nil, err
	}

	start := sc.today.AddDate(0, -sc.contractStartMonths, 0)
	c := contract.Contract{
		TenantID:               tn.ID,
		RoomID:                 rm.ID,
		StartDate:              start,
		MinMonths:              sc.minMonths,
		MonthlyRent:            rm.BaseRent,
		DepositAmount:          rm.BaseDeposit,
		DepositStatus:          contract.DepositStatusCollected,
		ElectricityRatePerUnit: apt.ElectricityRatePerUnit,
		WaterRatePerUnit:       apt.WaterRatePerUnit,
		Status:                 contract.ContractStatusActive,
	}
	if err := db.Create(&c).Error; err != nil {
		return nil, nil, nil, fmt.Errorf("create smoke contract %s: %w", rm.Number, err)
	}

	if err := db.Model(&room.Room{}).Where("id = ?", rm.ID).
		Update("status", room.RoomStatusOccupied).Error; err != nil {
		return nil, nil, nil, fmt.Errorf("update room status %s: %w", rm.Number, err)
	}

	scheduledOffset := sc.scheduledOffset
	if scheduledOffset == 0 {
		scheduledOffset = sc.actualOffset
	}
	noticeOffset := sc.noticeOffset
	if noticeOffset == 0 {
		// Default: notice 3 days BEFORE scheduled — satisfies
		// CHECK constraint scheduled_move_out_date >= notice_date.
		noticeOffset = scheduledOffset - 3
	}

	notice := moveout.MoveOutNotice{
		ContractID:           c.ID,
		NoticeDate:           sc.today.AddDate(0, 0, noticeOffset),
		ScheduledMoveOutDate: sc.today.AddDate(0, 0, scheduledOffset),
		Status:               sc.status,
		Note:                 sc.note,
	}
	if !sc.skipActualDate {
		actual := sc.today.AddDate(0, 0, sc.actualOffset)
		notice.ActualMoveOutDate = &actual
	}
	if err := db.Create(&notice).Error; err != nil {
		return nil, nil, nil, fmt.Errorf("create smoke notice %s: %w", rm.Number, err)
	}

	if sc.withExitMeter {
		readingDate := sc.today.AddDate(0, 0, sc.actualOffset)
		exit := meterreading.MeterReading{
			RoomID:              rm.ID,
			ReadingType:         meterreading.ReadingTypeExit,
			ReadingDateActual:   &readingDate,
			ElectricityPrevious: 1000,
			ElectricityCurrent:  1135, // 135 units
			WaterPrevious:       100,
			WaterCurrent:        118, // 18 units
		}
		if err := db.Create(&exit).Error; err != nil {
			return nil, nil, nil, fmt.Errorf("create smoke exit reading %s: %w", rm.Number, err)
		}
	}

	return &notice, &c, &rm, nil
}

// createCustomStartScenario is for cases where start_date needs precise control (TC3).
func createCustomStartScenario(db *gorm.DB, apt apartment.Apartment, rm room.Room, t smokeTenant, sc customStartScenario) error {
	tn, err := ensureSmokeTenant(db, t)
	if err != nil {
		return err
	}

	c := contract.Contract{
		TenantID:               tn.ID,
		RoomID:                 rm.ID,
		StartDate:              sc.startDate,
		MinMonths:              sc.minMonths,
		MonthlyRent:            rm.BaseRent,
		DepositAmount:          rm.BaseDeposit,
		DepositStatus:          contract.DepositStatusCollected,
		ElectricityRatePerUnit: apt.ElectricityRatePerUnit,
		WaterRatePerUnit:       apt.WaterRatePerUnit,
		Status:                 contract.ContractStatusActive,
	}
	if err := db.Create(&c).Error; err != nil {
		return fmt.Errorf("create smoke contract %s: %w", rm.Number, err)
	}

	if err := db.Model(&room.Room{}).Where("id = ?", rm.ID).
		Update("status", room.RoomStatusOccupied).Error; err != nil {
		return fmt.Errorf("update room status %s: %w", rm.Number, err)
	}

	actual := sc.today.AddDate(0, 0, sc.actualOffset)
	notice := moveout.MoveOutNotice{
		ContractID:           c.ID,
		NoticeDate:           sc.today.AddDate(0, 0, sc.noticeOffset),
		ScheduledMoveOutDate: actual,
		ActualMoveOutDate:    &actual,
		Status:               sc.status,
		Note:                 sc.note,
	}
	if err := db.Create(&notice).Error; err != nil {
		return fmt.Errorf("create smoke notice %s: %w", rm.Number, err)
	}

	if sc.withExitMeter {
		exit := meterreading.MeterReading{
			RoomID:              rm.ID,
			ReadingType:         meterreading.ReadingTypeExit,
			ReadingDateActual:   &actual,
			ElectricityPrevious: 1000,
			ElectricityCurrent:  1135,
			WaterPrevious:       100,
			WaterCurrent:        118,
		}
		if err := db.Create(&exit).Error; err != nil {
			return fmt.Errorf("create smoke exit reading %s: %w", rm.Number, err)
		}
	}

	return nil
}

func ensureSmokeTenant(db *gorm.DB, t smokeTenant) (*tenant.Tenant, error) {
	var found tenant.Tenant
	err := db.Where("id_card = ?", t.idCard).First(&found).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		found = tenant.Tenant{
			FullName:         t.name,
			IDCard:           t.idCard,
			Phone:            t.phone,
			Address:          "SMOKE_TEST_ADDRESS",
			EmergencyContact: "SMOKE_EMERGENCY_CONTACT",
		}
		if err := db.Create(&found).Error; err != nil {
			return nil, fmt.Errorf("create smoke tenant %s: %w", t.name, err)
		}
		return &found, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find smoke tenant %s: %w", t.name, err)
	}
	return &found, nil
}

// createBillWithLines creates a Bill and its line items in two steps to work
// around GORM cascade behavior that drops nested creates in some cases.
// createBillWithLines inserts a bill plus its line items atomically.
// Wrapping in db.Transaction prevents an orphan bill row when line item
// insert fails midway (worst-case scenario: re-seed cycles leak rows
// into bills that later cause duplicate-bill conflicts on retry).
func createBillWithLines(db *gorm.DB, bill *billing.Bill, items []billing.BillLineItem) error {
	bill.LineItems = nil // prevent cascade attempt
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(bill).Error; err != nil {
			return err
		}
		for i := range items {
			items[i].BillID = bill.ID
		}
		if len(items) == 0 {
			return nil
		}
		return tx.Create(&items).Error
	})
}

// attachDraftSettlementBill creates a DRAFT settlement bill and links it to the notice.
// Used by TC4 to simulate a previously-generated draft.
//
// Canonicalization: mirrors `prepareSettlementPlan` + `addConfigFees` output —
// PRORATE_RENT, WATER, ELECTRICITY, CLEANING_FEE, KEY_SERVICE. See
// `smoke_formula_contract.md`.
func attachDraftSettlementBill(db *gorm.DB, notice *moveout.MoveOutNotice, c *contract.Contract, _ *room.Room, actualMoveOut time.Time) error {
	billingMonth := actualMoveOut.Format("2006-01")

	day := actualMoveOut.Day()
	const dailyRate int64 = 10000 // ฿100/day — matches PRORATE_DAILY_RATE default
	proRateRent := dailyRate * int64(day)

	waterAmount := int64(18) * c.WaterRatePerUnit
	elecAmount := int64(135) * c.ElectricityRatePerUnit
	cleaningFee := int64(30000)  // ฿300 — CLEANING_FEE config default
	keyServiceFee := int64(5000) // ฿50 — KEY_SERVICE config default

	total := proRateRent + waterAmount + elecAmount + cleaningFee + keyServiceFee

	bill := billing.Bill{
		ContractID:         c.ID,
		BillingMonth:       billingMonth,
		BillType:           billing.BillTypeSettlement,
		Status:             billing.BillStatusDraft,
		DepositAmount:      c.DepositAmount,
		TotalAmount:        total,
		SettlementRentMode: billing.RentModeProrated,
		RentPaid:           false,
	}
	items := []billing.BillLineItem{
		{LineType: billing.LineItemProrateRent, Source: billing.LineItemSourceAuto, Description: fmt.Sprintf("%d วัน × ฿100/วัน", day), Amount: proRateRent, Quantity: day, UnitPrice: dailyRate, SortOrder: 1},
		{LineType: billing.LineItemWater, Source: billing.LineItemSourceAuto, Description: "ค่าน้ำ 18 หน่วย", Amount: waterAmount, Quantity: 18, UnitPrice: c.WaterRatePerUnit, SortOrder: 2},
		{LineType: billing.LineItemElectricity, Source: billing.LineItemSourceAuto, Description: "ค่าไฟฟ้า 135 หน่วย", Amount: elecAmount, Quantity: 135, UnitPrice: c.ElectricityRatePerUnit, SortOrder: 3},
		{LineType: billing.LineItemCleaningFee, Source: billing.LineItemSourceAuto, Description: "ค่าทำความสะอาด", Amount: cleaningFee, SortOrder: 4},
		{LineType: billing.LineItemKeyService, Source: billing.LineItemSourceAuto, Description: "ค่าบริการกุญแจ", Amount: keyServiceFee, SortOrder: 5},
	}
	if err := createBillWithLines(db, &bill, items); err != nil {
		return fmt.Errorf("create draft bill: %w", err)
	}

	netAmount := total - c.DepositAmount
	if err := db.Model(&moveout.MoveOutNotice{}).Where("id = ?", notice.ID).Updates(map[string]any{
		"settlement_bill_id": bill.ID,
		"net_amount":         netAmount,
	}).Error; err != nil {
		return fmt.Errorf("attach bill to notice: %w", err)
	}

	return nil
}

// attachDraftSettlementBillWithManualItems creates a DRAFT settlement bill with
// MANUAL line items and links it to the notice. Used by TC22 for the queue
// drawer confirm-modal scenario.
//
// Canonicalization: the AUTO line items here MUST mirror what
// `prepareSettlementPlan` → `addConfigFees` would emit for the same contract +
// move-out date (PRORATE_RENT, WATER, ELECTRICITY, CLEANING_FEE, KEY_SERVICE).
// Otherwise smoke regen will surface drift that looks like a regen bug but is
// just a stale baseline. See `smoke_formula_contract.md` + memory entry
// `feedback_seed_canonicalization`.
func attachDraftSettlementBillWithManualItems(db *gorm.DB, notice *moveout.MoveOutNotice, c *contract.Contract, _ *room.Room, actualMoveOut time.Time) error {
	billingMonth := actualMoveOut.Format("2006-01")

	day := actualMoveOut.Day()
	const dailyRate int64 = 10000 // ฿100/day — matches PRORATE_DAILY_RATE default
	proRateRent := dailyRate * int64(day)

	waterAmount := int64(18) * c.WaterRatePerUnit
	elecAmount := int64(135) * c.ElectricityRatePerUnit
	cleaningFee := int64(30000)   // ฿300 — CLEANING_FEE config default
	keyServiceFee := int64(5000)  // ฿50 — KEY_SERVICE config default
	manualCharge1 := int64(50000) // ฿500 — ค่าซ่อมแซม
	manualCharge2 := int64(30000) // ฿300 — ค่าอื่น ๆ

	total := proRateRent + waterAmount + elecAmount + cleaningFee + keyServiceFee + manualCharge1 + manualCharge2

	bill := billing.Bill{
		ContractID:         c.ID,
		BillingMonth:       billingMonth,
		BillType:           billing.BillTypeSettlement,
		Status:             billing.BillStatusDraft,
		DepositAmount:      c.DepositAmount,
		TotalAmount:        total,
		SettlementRentMode: billing.RentModeProrated,
		RentPaid:           false,
	}
	items := []billing.BillLineItem{
		{LineType: billing.LineItemProrateRent, Source: billing.LineItemSourceAuto, Description: fmt.Sprintf("%d วัน × ฿100/วัน", day), Amount: proRateRent, Quantity: day, UnitPrice: dailyRate, SortOrder: 1},
		{LineType: billing.LineItemWater, Source: billing.LineItemSourceAuto, Description: "ค่าน้ำ 18 หน่วย", Amount: waterAmount, Quantity: 18, UnitPrice: c.WaterRatePerUnit, SortOrder: 2},
		{LineType: billing.LineItemElectricity, Source: billing.LineItemSourceAuto, Description: "ค่าไฟฟ้า 135 หน่วย", Amount: elecAmount, Quantity: 135, UnitPrice: c.ElectricityRatePerUnit, SortOrder: 3},
		{LineType: billing.LineItemCleaningFee, Source: billing.LineItemSourceAuto, Description: "ค่าทำความสะอาด", Amount: cleaningFee, SortOrder: 4},
		{LineType: billing.LineItemKeyService, Source: billing.LineItemSourceAuto, Description: "ค่าบริการกุญแจ", Amount: keyServiceFee, SortOrder: 5},
		{LineType: billing.LineItemOther, Source: billing.LineItemSourceManual, Description: "ค่าซ่อมแซมห้อง", Amount: manualCharge1, SortOrder: 6},
		{LineType: billing.LineItemOther, Source: billing.LineItemSourceManual, Description: "ค่าเคลื่อนย้ายของ", Amount: manualCharge2, SortOrder: 7},
	}
	if err := createBillWithLines(db, &bill, items); err != nil {
		return fmt.Errorf("create draft bill with manual: %w", err)
	}

	netAmount := total - c.DepositAmount
	if err := db.Model(&moveout.MoveOutNotice{}).Where("id = ?", notice.ID).Updates(map[string]any{
		"settlement_bill_id": bill.ID,
		"net_amount":         netAmount,
	}).Error; err != nil {
		return fmt.Errorf("attach bill to notice: %w", err)
	}

	return nil
}

// attachDraftSettlementBillFullMonth creates a DRAFT settlement bill with
// FULL_MONTH rent mode and links it to the notice. Used by TC23 for rent
// mode continuity testing.
//
// Canonicalization: mirrors `prepareSettlementPlan` FULL_MONTH output —
// description `"ค่าห้องเดือน YYYY-MM"` (not legacy `"ค่าเช่า (เต็มเดือน)"`) +
// KEY_SERVICE injected. Stay in sync with `addRentAdjustment` +
// `addConfigFees`. See `smoke_formula_contract.md`.
func attachDraftSettlementBillFullMonth(db *gorm.DB, notice *moveout.MoveOutNotice, c *contract.Contract, _ *room.Room, actualMoveOut time.Time) error {
	billingMonth := actualMoveOut.Format("2006-01")

	waterAmount := int64(18) * c.WaterRatePerUnit
	elecAmount := int64(135) * c.ElectricityRatePerUnit
	cleaningFee := int64(30000)  // ฿300 — CLEANING_FEE config default
	keyServiceFee := int64(5000) // ฿50 — KEY_SERVICE config default

	total := c.MonthlyRent + waterAmount + elecAmount + cleaningFee + keyServiceFee

	bill := billing.Bill{
		ContractID:         c.ID,
		BillingMonth:       billingMonth,
		BillType:           billing.BillTypeSettlement,
		Status:             billing.BillStatusDraft,
		DepositAmount:      c.DepositAmount,
		TotalAmount:        total,
		SettlementRentMode: billing.RentModeFullMonthKeepDeposit,
		RentPaid:           false,
	}
	items := []billing.BillLineItem{
		{LineType: billing.LineItemRoomRent, Source: billing.LineItemSourceAuto, Description: fmt.Sprintf("ค่าห้องเดือน %s", billingMonth), Amount: c.MonthlyRent, SortOrder: 1},
		{LineType: billing.LineItemWater, Source: billing.LineItemSourceAuto, Description: "ค่าน้ำ 18 หน่วย", Amount: waterAmount, Quantity: 18, UnitPrice: c.WaterRatePerUnit, SortOrder: 2},
		{LineType: billing.LineItemElectricity, Source: billing.LineItemSourceAuto, Description: "ค่าไฟฟ้า 135 หน่วย", Amount: elecAmount, Quantity: 135, UnitPrice: c.ElectricityRatePerUnit, SortOrder: 3},
		{LineType: billing.LineItemCleaningFee, Source: billing.LineItemSourceAuto, Description: "ค่าทำความสะอาด", Amount: cleaningFee, SortOrder: 4},
		{LineType: billing.LineItemKeyService, Source: billing.LineItemSourceAuto, Description: "ค่าบริการกุญแจ", Amount: keyServiceFee, SortOrder: 5},
	}
	if err := createBillWithLines(db, &bill, items); err != nil {
		return fmt.Errorf("create full-month draft bill: %w", err)
	}

	netAmount := total - c.DepositAmount
	if err := db.Model(&moveout.MoveOutNotice{}).Where("id = ?", notice.ID).Updates(map[string]any{
		"settlement_bill_id": bill.ID,
		"net_amount":         netAmount,
	}).Error; err != nil {
		return fmt.Errorf("attach bill to notice: %w", err)
	}

	return nil
}

// createPaidMonthlyBill creates a PAID monthly bill (for TC6 — rent already paid).
func createPaidMonthlyBill(db *gorm.DB, c *contract.Contract, billingMonth string) error {
	waterAmount := int64(20) * c.WaterRatePerUnit
	elecAmount := int64(140) * c.ElectricityRatePerUnit
	total := c.MonthlyRent + waterAmount + elecAmount

	bill := billing.Bill{
		ContractID:   c.ID,
		BillingMonth: billingMonth,
		BillType:     billing.BillTypeMonthly,
		Status:       billing.BillStatusPaid,
		TotalAmount:  total,
	}
	items := []billing.BillLineItem{
		{LineType: billing.LineItemRoomRent, Source: billing.LineItemSourceAuto, Description: "ค่าเช่า", Amount: c.MonthlyRent, SortOrder: 1},
		{LineType: billing.LineItemWater, Source: billing.LineItemSourceAuto, Description: "ค่าน้ำ 20 หน่วย", Amount: waterAmount, Quantity: 20, UnitPrice: c.WaterRatePerUnit, SortOrder: 2},
		{LineType: billing.LineItemElectricity, Source: billing.LineItemSourceAuto, Description: "ค่าไฟฟ้า 140 หน่วย", Amount: elecAmount, Quantity: 140, UnitPrice: c.ElectricityRatePerUnit, SortOrder: 3},
	}
	return createBillWithLines(db, &bill, items)
}

// createMonthlyMeterReading inserts a committed MONTHLY reading. Used by
// TC20 to seed a non-zero "previous" reference for the exit-meter drawer.
func createMonthlyMeterReading(db *gorm.DB, roomID uuid.UUID, billingMonth string, elecPrev, elecCurr, waterPrev, waterCurr int) error {
	bm := billingMonth
	reading := meterreading.MeterReading{
		RoomID:              roomID,
		ReadingType:         meterreading.ReadingTypeMonthly,
		BillingMonth:        &bm,
		ElectricityPrevious: elecPrev,
		ElectricityCurrent:  elecCurr,
		WaterPrevious:       waterPrev,
		WaterCurrent:        waterCurr,
	}
	return db.Create(&reading).Error
}

// createBillEditScenario creates tenant + ACTIVE contract + DRAFT MONTHLY
// bill for the current billing month. Used by TC26/TC27 to anchor the
// bill-edit playwright smoke (no move-out involved).
//
// `preOverrides` seeds an existing Overrides map at insert time so TC27
// can simulate "admin previously edited ROOM_RENT". When set, the bill's
// TotalAmount reflects the post-override sum so the BillList row total
// matches what the BE would render on fetch — line.Amount stays at the
// original computed value (BE's source of truth for "เดิม ฿X" provenance).
func createBillEditScenario(
	db *gorm.DB,
	apt apartment.Apartment,
	rm room.Room,
	t smokeTenant,
	today time.Time,
	preOverrides billing.OverrideMap,
) error {
	tn, err := ensureSmokeTenant(db, t)
	if err != nil {
		return err
	}

	start := today.AddDate(0, -6, 0)
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
		return fmt.Errorf("create bill-edit contract %s: %w", rm.Number, err)
	}
	if err := db.Model(&room.Room{}).Where("id = ?", rm.ID).
		Update("status", room.RoomStatusOccupied).Error; err != nil {
		return fmt.Errorf("update room status %s: %w", rm.Number, err)
	}

	// Standard line item amounts (mirrors createFinalizedMonthlyBill so the
	// breakdown is predictable across smoke runs — 20 water + 140 elec).
	const waterUnits = 20
	const elecUnits = 140
	waterAmount := int64(waterUnits) * c.WaterRatePerUnit
	elecAmount := int64(elecUnits) * c.ElectricityRatePerUnit

	// Compute TotalAmount with override applied if present so list rows
	// and detail view match.
	roomRent := c.MonthlyRent
	if preOverrides != nil {
		if v, ok := preOverrides[string(billing.LineItemRoomRent)]; ok {
			roomRent = v
		}
	}
	total := roomRent + waterAmount + elecAmount

	billingMonth := today.Format("2006-01")
	bill := billing.Bill{
		ContractID:   c.ID,
		BillingMonth: billingMonth,
		BillType:     billing.BillTypeMonthly,
		Status:       billing.BillStatusDraft,
		TotalAmount:  total,
	}
	if preOverrides != nil {
		bill.Overrides = preOverrides
	}
	items := []billing.BillLineItem{
		{LineType: billing.LineItemRoomRent, Source: billing.LineItemSourceAuto, Description: "ค่าเช่า", Amount: c.MonthlyRent, SortOrder: 1},
		{LineType: billing.LineItemWater, Source: billing.LineItemSourceAuto, Description: fmt.Sprintf("ค่าน้ำ %d หน่วย", waterUnits), Amount: waterAmount, Quantity: waterUnits, UnitPrice: c.WaterRatePerUnit, SortOrder: 2},
		{LineType: billing.LineItemElectricity, Source: billing.LineItemSourceAuto, Description: fmt.Sprintf("ค่าไฟฟ้า %d หน่วย", elecUnits), Amount: elecAmount, Quantity: elecUnits, UnitPrice: c.ElectricityRatePerUnit, SortOrder: 3},
	}
	return createBillWithLines(db, &bill, items)
}

// createMissingMeterScenario plants the minimal pre-state for a single
// MISSING_METER_READING row in a monthly batch: ACTIVE contract, no
// MONTHLY meter reading for the current month, no move-out notice.
// Used by TC28/TC29 to anchor the batch-replan smoke.
//
// MonthlyRent + apartment rates come from the room/apartment defaults so
// the snapshot post-replan is deterministic and matches what a fresh
// generate would produce.
func createMissingMeterScenario(
	db *gorm.DB,
	apt apartment.Apartment,
	rm room.Room,
	t smokeTenant,
) error {
	tn, err := ensureSmokeTenant(db, t)
	if err != nil {
		return err
	}
	today := truncateToDate(time.Now().UTC())
	start := today.AddDate(0, -6, 0)
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
		return fmt.Errorf("create missing-meter contract %s: %w", rm.Number, err)
	}
	if err := db.Model(&room.Room{}).Where("id = ?", rm.ID).
		Update("status", room.RoomStatusOccupied).Error; err != nil {
		return fmt.Errorf("update room status %s: %w", rm.Number, err)
	}
	return nil
}

// createFinalizedMonthlyBill creates a FINALIZED (unpaid) monthly bill
// that should be absorbed by settlement (for TC7).
func createFinalizedMonthlyBill(db *gorm.DB, c *contract.Contract, billingMonth string) error {
	waterAmount := int64(20) * c.WaterRatePerUnit
	elecAmount := int64(140) * c.ElectricityRatePerUnit
	total := c.MonthlyRent + waterAmount + elecAmount

	// Stamp FinalizedAt at the billing-month start (matches production
	// invariant — FINALIZED bills always have a finalize timestamp).
	billMonthStart, err := time.Parse("2006-01", billingMonth)
	if err != nil {
		return fmt.Errorf("parse billing month %q: %w", billingMonth, err)
	}
	finalizedAt := time.Date(
		billMonthStart.Year(), billMonthStart.Month(), 1,
		9, 0, 0, 0, time.UTC,
	)

	bill := billing.Bill{
		ContractID:   c.ID,
		BillingMonth: billingMonth,
		BillType:     billing.BillTypeMonthly,
		Status:       billing.BillStatusFinalized,
		TotalAmount:  total,
		FinalizedAt:  &finalizedAt,
	}
	items := []billing.BillLineItem{
		{LineType: billing.LineItemRoomRent, Source: billing.LineItemSourceAuto, Description: "ค่าเช่า", Amount: c.MonthlyRent, SortOrder: 1},
		{LineType: billing.LineItemWater, Source: billing.LineItemSourceAuto, Description: "ค่าน้ำ 20 หน่วย", Amount: waterAmount, Quantity: 20, UnitPrice: c.WaterRatePerUnit, SortOrder: 2},
		{LineType: billing.LineItemElectricity, Source: billing.LineItemSourceAuto, Description: "ค่าไฟฟ้า 140 หน่วย", Amount: elecAmount, Quantity: 140, UnitPrice: c.ElectricityRatePerUnit, SortOrder: 3},
	}
	return createBillWithLines(db, &bill, items)
}

// endOfMonth returns the last day of the month for the given date (00:00 UTC).
func endOfMonth(t time.Time) time.Time {
	y, m, _ := t.Date()
	firstOfNext := time.Date(y, m+1, 1, 0, 0, 0, 0, time.UTC)
	return firstOfNext.AddDate(0, 0, -1)
}

// previousMonthStr returns YYYY-MM of the month before the given YYYY-MM.
func previousMonthStr(yyyymm string) string {
	t, _ := time.Parse("2006-01", yyyymm)
	return t.AddDate(0, -1, 0).Format("2006-01")
}

// monthOffset returns YYYY-MM shifted by the given month offset (can be negative).
func monthOffset(yyyymm string, months int) string {
	t, _ := time.Parse("2006-01", yyyymm)
	return t.AddDate(0, months, 0).Format("2006-01")
}
