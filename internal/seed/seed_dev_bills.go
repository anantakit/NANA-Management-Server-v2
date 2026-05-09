package seed

import (
	"errors"
	"fmt"
	"log/slog"
	"time"

	"nana/internal/apartment"
	"nana/internal/billing"
	"nana/internal/contract"
	"nana/internal/room"

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

		waterAmount := u.waterUnits * c.WaterRatePerUnit
		elecAmount := u.elecUnits * c.ElectricityRatePerUnit
		total := c.MonthlyRent + waterAmount + elecAmount

		bill := billing.Bill{
			ContractID:   c.ID,
			BillingMonth: billingMonth,
			BillType:     billing.BillTypeMonthly,
			Status:       u.status,
			TotalAmount:  total,
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
