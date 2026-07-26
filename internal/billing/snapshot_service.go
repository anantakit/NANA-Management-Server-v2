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
	replacements []*meterreading.MeterReading,
	recon *RecoveryReconciliation,
) ComputedSnapshot {
	nextMonth := advanceMonth(billingMonth)
	// ONE canonical result drives BOTH the charge (Total) and the frozen breakdown
	// (Segments) — no re-query, no separate reconstruction (owner invariant
	// 2026-07-21). With no replacements, Total == reading.Used() and a single
	// READING segment, so ordinary + recovery bills are byte-identical.
	elecUsage, waterUsage := meterreading.CanonicalPeriodUsage(reading, replacements)

	lines := []ComputedLineItem{
		{
			Type:        LineItemRoomRent,
			Description: fmt.Sprintf("ค่าห้อง %s", nextMonth),
			Amount:      monthlyRent,
			SortOrder:   1,
		},
		meteredComputedLine(LineItemElectricity, "ค่าไฟฟ้า", elecUsage, elecRate, 2),
		meteredComputedLine(LineItemWater, "ค่าน้ำ", waterUsage, waterRate, 3),
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

// meteredComputedLine builds one metered line (ELECTRICITY / WATER) from a single
// canonical usage result: the charge is Total × rate (ONE line = ONE financial /
// override authority), and the frozen usage_breakdown carries the same Segments.
// A single-segment (ordinary/recovery) line keeps meter_previous/meter_current
// for backward compatibility and leaves the breakdown nil; a replacement line
// (>1 segment) carries the breakdown instead, since one meter pair cannot span it.
func meteredComputedLine(t LineItemType, label string, usage meterreading.UtilityCanonicalUsage, rate int64, sortOrder int) ComputedLineItem {
	li := ComputedLineItem{
		Type:        t,
		Description: fmt.Sprintf("%s %d หน่วย", label, usage.Total),
		Amount:      int64(usage.Total) * rate,
		Quantity:    usage.Total,
		UnitPrice:   rate,
		SortOrder:   sortOrder,
	}
	if len(usage.Segments) > 1 {
		li.UsageBreakdown = usageBreakdownFrom(usage.Segments)
	} else if len(usage.Segments) == 1 {
		p, c := usage.Segments[0].Previous, usage.Segments[0].Current
		li.MeterPrevious, li.MeterCurrent = &p, &c
	}
	return li
}

// SetMeteredLineUsage applies a canonical usage result to an already-built metered
// BillLineItem: >1 segment (a replacement period) → freeze the breakdown and clear
// the single meter pair (which cannot span >1 segment); else keep the single pair.
// Shared so settlement (direct BillLineItem) renders the breakdown identically to
// monthly (via meteredComputedLine) — same evidence, no drift.
func SetMeteredLineUsage(li *BillLineItem, usage meterreading.UtilityCanonicalUsage) {
	if len(usage.Segments) > 1 {
		li.UsageBreakdown = usageBreakdownFrom(usage.Segments)
		li.MeterPrevious, li.MeterCurrent = nil, nil
		return
	}
	if len(usage.Segments) == 1 {
		p, c := usage.Segments[0].Previous, usage.Segments[0].Current
		li.MeterPrevious, li.MeterCurrent = &p, &c
	}
}

// usageBreakdownFrom maps the domain segments into the STABLE billing evidence
// schema (no domain internals / IDs).
func usageBreakdownFrom(segs []meterreading.UsageSegment) UsageBreakdown {
	out := make(UsageBreakdown, len(segs))
	for i, s := range segs {
		out[i] = UsageBreakdownSegment{Kind: string(s.Kind), Previous: s.Previous, Current: s.Current, Usage: s.Usage}
	}
	return out
}

// DetectRecoveryReplacementCollision is the R-b guard (owner OD-R 2026-07-21):
// a period with BOTH a READING_RECOVERY and a PHYSICAL_REPLACEMENT on the SAME
// utility cannot be composed by the current resolver → surface an actionable
// error rather than silently under-bill. `winner` is the (possibly recovery)
// billing input; `replacements` are the period's replacement events. TEMPORARY
// architecture limitation, NOT a business rule (the event is always recordable);
// does NOT touch Recovery composition semantics.
func DetectRecoveryReplacementCollision(winner *meterreading.MeterReading, replacements []*meterreading.MeterReading) error {
	if winner == nil || len(replacements) == 0 {
		return nil
	}
	recovered := map[string]bool{}
	for _, u := range winner.AffectedRecoveryUtilities() {
		recovered[u] = true
	}
	if len(recovered) == 0 {
		return nil
	}
	for _, r := range replacements {
		for _, u := range r.AffectedReplacementUtilities() {
			if recovered[u] {
				return ErrRecoveryReplacementCollision
			}
		}
	}
	return nil
}
