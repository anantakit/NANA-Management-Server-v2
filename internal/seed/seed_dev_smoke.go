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

// Smoke test fixtures for the Move-out Settlement Preview flow.
//
// All fixtures are tagged with ID-card prefix "9999" so CleanupSmokeData
// can find and remove them cleanly after testing.
//
// | TC | Room | Status             | Scenario                                  |
// |----|------|--------------------|-------------------------------------------|
// | 1  | B105 | PENDING_SETTLEMENT | Happy path — past minMonths, no draft     |
// | 3  | B201 | PENDING_SETTLEMENT | MinMonths threshold flip (PRORATED fails) |
// | 4  | B202 | PENDING_SETTLEMENT | Has existing DRAFT settlement bill        |
// | 6  | B203 | PENDING_SETTLEMENT | Advance rent already paid (M-1 PAID)      |
// | 7  | B204 | PENDING_SETTLEMENT | Has absorbed bills (FINALIZED earlier)    |
// | 10 | B205 | PENDING_METER      | No actual_move_out_date yet               |
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
	IDCard            string  `json:"id_card"`
	TenantName        string  `json:"tenant_name"`
	RoomNumber        string  `json:"room_number"`
	NoticeID          string  `json:"notice_id"`
	Status            string  `json:"status"`
	HasDraft          bool    `json:"has_draft"`
	ActualMoveOutDate *string `json:"actual_move_out_date"`
}

// ListSmokeFixtures returns all active smoke-test fixtures in a compact form
// suitable for the dev HTTP endpoint. Filters by SmokeIDCardPrefix so the
// marker lives in ONE place.
func ListSmokeFixtures(db *gorm.DB) ([]SmokeFixture, error) {
	rows, err := db.Raw(`
		SELECT t.id_card, t.full_name, r.number, n.id::text, n.status,
		       n.settlement_bill_id IS NOT NULL AS has_draft,
		       to_char(n.actual_move_out_date, 'YYYY-MM-DD') AS actual_date
		FROM tenants t
		JOIN contracts c ON c.tenant_id = t.id
		JOIN rooms r ON r.id = c.room_id
		JOIN move_out_notices n ON n.contract_id = c.id
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
		if err := rows.Scan(&f.IDCard, &f.TenantName, &f.RoomNumber, &f.NoticeID, &f.Status, &f.HasDraft, &actualDate); err != nil {
			return nil, err
		}
		f.ActualMoveOutDate = actualDate
		out = append(out, f)
	}
	return out, nil
}

// smokeRoomNumbers is the exclusive set of rooms used by smoke fixtures.
// Must be VACANT in base seed (none overlap with seed_dev_moveout.go).
var smokeRoomNumbers = []string{"B105", "B201", "B202", "B203", "B204", "B205"}

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

	slog.Info("seeded smoke test fixtures", "count", 6)
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
func createBillWithLines(db *gorm.DB, bill *billing.Bill, items []billing.BillLineItem) error {
	items2 := items
	bill.LineItems = nil // prevent cascade attempt
	if err := db.Create(bill).Error; err != nil {
		return err
	}
	for i := range items2 {
		items2[i].BillID = bill.ID
	}
	return db.Create(&items2).Error
}

// attachDraftSettlementBill creates a DRAFT settlement bill and links it to the notice.
// Used by TC4 to simulate a previously-generated draft.
func attachDraftSettlementBill(db *gorm.DB, notice *moveout.MoveOutNotice, c *contract.Contract, _ *room.Room, actualMoveOut time.Time) error {
	billingMonth := actualMoveOut.Format("2006-01")

	day := actualMoveOut.Day()
	totalDays := endOfMonth(actualMoveOut).Day()
	proRateRent := (c.MonthlyRent * int64(day)) / int64(totalDays)

	waterAmount := int64(18) * c.WaterRatePerUnit
	elecAmount := int64(135) * c.ElectricityRatePerUnit
	cleaningFee := int64(30000) // ฿300

	total := proRateRent + waterAmount + elecAmount + cleaningFee

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
		{LineType: billing.LineItemProrateRent, Source: billing.LineItemSourceAuto, Description: fmt.Sprintf("ค่าเช่า (คิดตามสัดส่วน) %d/%d วัน", day, totalDays), Amount: proRateRent, SortOrder: 1},
		{LineType: billing.LineItemWater, Source: billing.LineItemSourceAuto, Description: "ค่าน้ำ 18 หน่วย", Amount: waterAmount, Quantity: 18, UnitPrice: c.WaterRatePerUnit, SortOrder: 2},
		{LineType: billing.LineItemElectricity, Source: billing.LineItemSourceAuto, Description: "ค่าไฟฟ้า 135 หน่วย", Amount: elecAmount, Quantity: 135, UnitPrice: c.ElectricityRatePerUnit, SortOrder: 3},
		{LineType: billing.LineItemCleaningFee, Source: billing.LineItemSourceAuto, Description: "ค่าทำความสะอาด", Amount: cleaningFee, SortOrder: 4},
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

// createFinalizedMonthlyBill creates a FINALIZED (unpaid) monthly bill
// that should be absorbed by settlement (for TC7).
func createFinalizedMonthlyBill(db *gorm.DB, c *contract.Contract, billingMonth string) error {
	waterAmount := int64(20) * c.WaterRatePerUnit
	elecAmount := int64(140) * c.ElectricityRatePerUnit
	total := c.MonthlyRent + waterAmount + elecAmount

	bill := billing.Bill{
		ContractID:   c.ID,
		BillingMonth: billingMonth,
		BillType:     billing.BillTypeMonthly,
		Status:       billing.BillStatusFinalized,
		TotalAmount:  total,
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
