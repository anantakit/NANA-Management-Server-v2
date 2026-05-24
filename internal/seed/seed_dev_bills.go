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
	"nana/internal/room"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// seedDevMonthlyBills creates current-month MONTHLY bills for the seven
// base นานาคอร์ท contracts (A101–A107) so the bill list page shows a
// realistic mix of monthly + settlement rows out of the box.
//
// Status mix (mirrors what an admin would see mid-month):
//
//	| Room | Status     | Why                                         |
//	|------|------------|---------------------------------------------|
//	| A101 | FINALIZED  | "รอชำระ" — high-elec scan target             |
//	| A102 | FINALIZED  | "รอชำระ" — typical apartment                 |
//	| A103 | FINALIZED  | "รอชำระ" — varied usage                      |
//	| A104 | FINALIZED  | "รอชำระ" — high water bias                   |
//	| A105 | PAID       | "ชำระแล้ว" — terminal-row variant (muted)    |
//	| A106 | PAID       | "ชำระแล้ว" — heavy AC user, paid early       |
//	| A107 | FINALIZED  | "รอชำระ" — water-only anomaly history        |
//
// Usage figures track the meter-reading baselines from seedDevMeterReadings
// so the bill totals look plausible alongside the meter history. Idempotent
// per (contract_id, billing_month).
func seedDevMonthlyBills(db *gorm.DB) error {
	var apt apartment.Apartment
	if err := db.Where("name = ?", "นานาคอร์ท").First(&apt).Error; err != nil {
		return fmt.Errorf("find apartment: %w", err)
	}

	roomNumbers := []string{"A101", "A102", "A103", "A104", "A105", "A106", "A107"}
	var rooms []room.Room
	if err := db.Where("apartment_id = ? AND number IN ?", apt.ID, roomNumbers).
		Find(&rooms).Error; err != nil {
		return fmt.Errorf("find dev rooms: %w", err)
	}
	roomByID := make(map[string]room.Room, len(rooms))
	for _, r := range rooms {
		roomByID[r.ID.String()] = r
	}

	roomIDs := make([]string, 0, len(rooms))
	for _, r := range rooms {
		roomIDs = append(roomIDs, r.ID.String())
	}
	var contracts []contract.Contract
	if err := db.Where("room_id IN ? AND status = ?", roomIDs, contract.ContractStatusActive).
		Find(&contracts).Error; err != nil {
		return fmt.Errorf("find dev contracts: %w", err)
	}
	if len(contracts) == 0 {
		// seedDevContracts hasn't run yet (or skipped) — nothing to bill.
		return nil
	}

	// Anchor on DB server time (UTC) so the billing month aligns with the
	// rest of the dev seed (move-out, smoke) which all use UTC `today`.
	billingMonth := time.Now().UTC().Format("2006-01")

	// Per-room usage matches the meter-reading baselines so monthly bill
	// totals look consistent with the meter history admin sees on the
	// room detail page.
	type usage struct {
		elecUnits  int64
		waterUnits int64
		status     billing.BillStatus
	}
	plan := map[string]usage{
		"A101": {elecUnits: 120, waterUnits: 12, status: billing.BillStatusFinalized},
		"A102": {elecUnits: 81, waterUnits: 15, status: billing.BillStatusFinalized},
		"A103": {elecUnits: 133, waterUnits: 20, status: billing.BillStatusFinalized},
		"A104": {elecUnits: 196, waterUnits: 24, status: billing.BillStatusFinalized},
		"A105": {elecUnits: 40, waterUnits: 8, status: billing.BillStatusPaid},
		"A106": {elecUnits: 250, waterUnits: 30, status: billing.BillStatusPaid},
		"A107": {elecUnits: 100, waterUnits: 50, status: billing.BillStatusFinalized},
	}

	// All bills in a monthly batch share the same FinalizedAt — production
	// reality (admin runs the generation flow once at start of month). The
	// per-row variance in the list view comes from PAID/DRAFT/VOID state
	// transitions, NOT a synthetic finalized-date stagger.
	// See feedback_operational_column_reality.
	billMonthStart, monthParseErr := time.Parse("2006-01", billingMonth)
	if monthParseErr != nil {
		return fmt.Errorf("parse billing month %q: %w", billingMonth, monthParseErr)
	}
	batchFinalizedAt := time.Date(
		billMonthStart.Year(), billMonthStart.Month(), 1,
		9, 0, 0, 0, time.UTC,
	)

	created := 0
	for _, c := range contracts {
		rm, ok := roomByID[c.RoomID.String()]
		if !ok {
			continue
		}
		u, ok := plan[rm.Number]
		if !ok {
			continue
		}

		// Idempotent — skip if this contract already has a bill for this month.
		var existing int64
		if err := db.Model(&billing.Bill{}).
			Where("contract_id = ? AND billing_month = ?", c.ID, billingMonth).
			Count(&existing).Error; err != nil {
			return fmt.Errorf("check existing bill %s: %w", rm.Number, err)
		}
		if existing > 0 {
			continue
		}

		// Ensure a matching MONTHLY meter exists for (room, billingMonth)
		// BEFORE creating the bill. Real CreateMonthlyBill validates this
		// invariant, so the seed must satisfy it too — otherwise the
		// correction flow (which regenerates from contract+meter) can't
		// find a source-of-truth meter and fails with ErrMeterNotFound.
		// See feedback_seed_data ("Seed data must be realistic").
		if err := ensureDevMonthlyMeter(db, rm.ID, billingMonth,
			u.elecUnits, u.waterUnits); err != nil {
			return fmt.Errorf("ensure meter %s: %w", rm.Number, err)
		}

		waterAmount := u.waterUnits * c.WaterRatePerUnit
		elecAmount := u.elecUnits * c.ElectricityRatePerUnit
		total := c.MonthlyRent + waterAmount + elecAmount

		bill := billing.Bill{
			ContractID:   c.ID,
			BillingMonth: billingMonth,
			BillType:     billing.BillTypeMonthly,
			Status:       u.status,
			TotalAmount:  total,
			FinalizedAt:  &batchFinalizedAt,
		}
		items := []billing.BillLineItem{
			{
				LineType: billing.LineItemRoomRent, Source: billing.LineItemSourceAuto,
				Description: "ค่าเช่า", Amount: c.MonthlyRent, SortOrder: 1,
			},
			{
				LineType: billing.LineItemWater, Source: billing.LineItemSourceAuto,
				Description: fmt.Sprintf("ค่าน้ำ %d หน่วย", u.waterUnits),
				Amount:      waterAmount,
				Quantity:    int(u.waterUnits),
				UnitPrice:   c.WaterRatePerUnit,
				SortOrder:   2,
			},
			{
				LineType: billing.LineItemElectricity, Source: billing.LineItemSourceAuto,
				Description: fmt.Sprintf("ค่าไฟฟ้า %d หน่วย", u.elecUnits),
				Amount:      elecAmount,
				Quantity:    int(u.elecUnits),
				UnitPrice:   c.ElectricityRatePerUnit,
				SortOrder:   3,
			},
		}

		if err := createBillWithLines(db, &bill, items); err != nil {
			if errors.Is(err, gorm.ErrInvalidData) {
				slog.Warn("skip dev monthly bill", "room", rm.Number, "err", err)
				continue
			}
			return fmt.Errorf("create monthly bill %s: %w", rm.Number, err)
		}
		created++
	}

	if created > 0 {
		slog.Info("seeded dev monthly bills",
			"count", created, "billing_month", billingMonth)
	}
	return nil
}

// ensureDevMonthlyMeter creates a MONTHLY meter reading for (roomID, month)
// if one doesn't already exist. Chains off the room's most recent meter so
// the new reading's previous values match real-world meter continuity.
//
// Idempotent: returns nil without writing if a meter for the slot already
// exists. Defaults to (1000 elec / 100 water) baseline when the room has
// no prior history — mirrors seedDevMeterReadings defaults so chained
// rooms stay self-consistent.
//
// Exists to keep the dev seed self-consistent for the void+recreate
// correction flow, which regenerates the new DRAFT from the meter for
// the bill's billing_month. Without this, dev bills created at
// "current UTC month" would have no matching meter (seedDevMeterReadings
// only goes Oct 2025 → Mar 2026), so correction would always 404.
func ensureDevMonthlyMeter(
	db *gorm.DB,
	roomID uuid.UUID,
	month string,
	elecUnits, waterUnits int64,
) error {
	var existing int64
	if err := db.Model(&meterreading.MeterReading{}).
		Where("room_id = ? AND reading_type = ? AND billing_month = ?",
			roomID, meterreading.ReadingTypeMonthly, month).
		Count(&existing).Error; err != nil {
		return fmt.Errorf("check existing meter: %w", err)
	}
	if existing > 0 {
		return nil
	}

	// Chain off the room's most recent non-deleted reading. Order by
	// billing_month so we pick the freshest monthly reading; created_at
	// is a stable tiebreak for same-month re-records (shouldn't happen
	// in seed but defensive).
	var latest meterreading.MeterReading
	err := db.
		Where("room_id = ?", roomID).
		Order("billing_month DESC NULLS LAST, created_at DESC").
		First(&latest).Error
	var elecPrev, waterPrev int
	switch {
	case err == nil:
		elecPrev = latest.ElectricityCurrent
		waterPrev = latest.WaterCurrent
	case errors.Is(err, gorm.ErrRecordNotFound):
		// No prior meter for this room — start from the same baseline
		// seedDevMeterReadings uses for rooms without explicit overrides.
		elecPrev = 1000
		waterPrev = 100
	default:
		return fmt.Errorf("find latest meter: %w", err)
	}

	bm := month
	reading := meterreading.MeterReading{
		RoomID:              roomID,
		ReadingType:         meterreading.ReadingTypeMonthly,
		BillingMonth:        &bm,
		ElectricityPrevious: elecPrev,
		ElectricityCurrent:  elecPrev + int(elecUnits),
		WaterPrevious:       waterPrev,
		WaterCurrent:        waterPrev + int(waterUnits),
	}
	if err := db.Create(&reading).Error; err != nil {
		return fmt.Errorf("create meter: %w", err)
	}
	return nil
}
