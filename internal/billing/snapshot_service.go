package billing

import (
	"fmt"
	"time"

	"nana/internal/meterreading"
)

// ComputeMonthlyBillSnapshot builds the line items + total for a monthly bill
// without touching the bills table. Persistence happens later in the commit
// step (or in single-bill CreateMonthlyBill).
//
// Shared between billing root (CreateMonthlyBill — W1 single-bill ad-hoc
// path) and the monthly package (W2 batch commit + replan). Lives at billing
// root because the ComputedSnapshot type it returns lives here too, and
// because the W1 caller cannot import monthly without inverting the
// established consumer/provider direction. Exported so the monthly package
// can call it across the package boundary.
//
// Pure function — no DB, no audit, no side effects. Stable across batch and
// single-bill paths so a replanned snapshot is byte-equivalent to a freshly
// generated one for the same inputs.
func ComputeMonthlyBillSnapshot(
	billingMonth string,
	monthlyRent, elecRate, waterRate int64,
	reading *meterreading.MeterReading,
	recon *RecoveryReconciliation,
) ComputedSnapshot {
	nextMonth := advanceMonth(billingMonth)
	elecUnits := reading.ElectricityUsed()
	waterUnits := reading.WaterUsed()
	elecPrev, elecCur := reading.ElectricityPrevious, reading.ElectricityCurrent
	waterPrev, waterCur := reading.WaterPrevious, reading.WaterCurrent

	lines := []ComputedLineItem{
		{
			Type:        LineItemRoomRent,
			Description: fmt.Sprintf("ค่าห้อง %s", nextMonth),
			Amount:      monthlyRent,
			SortOrder:   1,
		},
		{
			Type:          LineItemElectricity,
			Description:   fmt.Sprintf("ค่าไฟฟ้า %d หน่วย", elecUnits),
			Amount:        int64(elecUnits) * elecRate,
			Quantity:      elecUnits,
			UnitPrice:     elecRate,
			SortOrder:     2,
			MeterPrevious: &elecPrev,
			MeterCurrent:  &elecCur,
		},
		{
			Type:          LineItemWater,
			Description:   fmt.Sprintf("ค่าน้ำ %d หน่วย", waterUnits),
			Amount:        int64(waterUnits) * waterRate,
			Quantity:      waterUnits,
			UnitPrice:     waterRate,
			SortOrder:     3,
			MeterPrevious: &waterPrev,
			MeterCurrent:  &waterCur,
		},
	}

	// Q1.6 — auto-emit a refund ADJUSTMENT line per over-recorded utility (ontology
	// lock 2026-07-08). F fires ONLY when `recon` supplies a rate for the utility:
	// the caller resolved that the source month produced a real bill (S0 gate) and
	// carries the SOURCE bill's line unit_price (the historical rate the tenant was
	// actually charged) — never the current contract rate. A nil rate → no refund
	// (source-less / not over-recorded / source never billed → P-only re-anchor).
	// The AUTO lines above are already re-baselined to 0 (a recovery reading has
	// prev==current → usage 0), so the refund is a separate line, not an offset.
	sortOrder := 3
	for _, u := range []AdjustmentUtility{AdjustmentUtilityElectricity, AdjustmentUtilityWater} {
		rate := recon.rateFor(u)
		if rate == nil {
			continue // F does not apply for this utility
		}
		res, _, err := ResolveCorrection(reading, u, *rate)
		if err != nil || res.Amount == 0 {
			continue // not an over-record, or zero-rate (nothing to refund)
		}
		sortOrder++
		lines = append(lines, ComputedLineItem{
			Type:                        LineItemAdjustment,
			Description:                 buildAdjustmentDescription(res),
			Amount:                      res.Amount,
			Quantity:                    int(res.Recorded - res.Physical),
			UnitPrice:                   res.RatePerUnit,
			SortOrder:                   sortOrder,
			AdjustmentRecoveryReadingID: &res.RecoveryReadingID,
			AdjustmentUtility:           &res.Utility,
		})
	}

	var total int64
	for _, li := range lines {
		total += li.Amount
	}

	return ComputedSnapshot{
		Version:     ComputedSnapshotVersion,
		LineItems:   lines,
		TotalAmount: total,
		ComputedAt:  time.Now().UTC(),
	}
}
