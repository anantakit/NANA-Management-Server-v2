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
