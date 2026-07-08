package billing

import (
	"context"

	"github.com/google/uuid"

	"nana/internal/meterreading"
)

// --- Q1.6 forward-credit reconciliation (ontology lock 2026-07-08) ---
//
// Doctrine (project_reading_recovery_ontology_lock): Recovery = P (physical
// append-only re-anchor, always) + optional F (a forward credit on the current
// bill). F exists ONLY when the OVER-RECORDED utility's SOURCE month produced a
// real financial obligation — i.e. a FINALIZED or PAID monthly bill — and is
// priced at that bill's line unit_price (the historical rate the tenant was
// actually charged), NEVER the current contract rate. Append-only makes the
// source month's bill the single source of both facts:
//   - its EXISTENCE is the S0 gate (no bill → money never moved → no refund),
//   - its line unit_price is the refund RATE.
// The resolution needs DB access, so it happens in the caller (which has ctx +
// repos); ComputeMonthlyBillSnapshot stays a pure function that consumes the
// pre-resolved result.

// SourceRefundRates carries the per-utility unit_price (satang/unit) charged in
// the source month's FINALIZED/PAID bill. A nil *SourceRefundRates from the
// finder means the source month was never billed → nothing to refund (S0).
type SourceRefundRates struct {
	Electricity int64
	Water       int64
}

// SourceRefundRateFinder resolves the S0 gate + refund rate in one lookup: given
// a recovery reading's source_reading_id and the current contract that owns it,
// it joins to the source month's FINALIZED/PAID MONTHLY bill and returns that
// bill's electricity/water line unit_price, or (nil, nil) when no such bill
// exists. Satisfied by BillingRepository and, for the monthly workflow, by
// monthly.BillStore (via MonthlyAdapter) — both structurally implement the
// single method, so the shared resolver below serves every call site.
type SourceRefundRateFinder interface {
	FindRecoverySourceRefundRates(ctx context.Context, sourceReadingID, contractID uuid.UUID) (*SourceRefundRates, error)
}

// RecoveryReconciliation is the pre-resolved, per-utility forward-credit input
// for ComputeMonthlyBillSnapshot. A nil utility pointer means F does NOT apply
// for that utility — source-less, not an over-record, or the S0 gate fired
// (source month never billed). A non-nil pointer is the source-bill unit_price
// to refund at. nil (whole struct) means no reconciliation at all.
type RecoveryReconciliation struct {
	Electricity *int64
	Water       *int64
}

// rateFor returns the resolved refund rate for a utility, or nil when F does not
// apply. Nil-receiver safe so the snapshot can call it unconditionally.
func (r *RecoveryReconciliation) rateFor(u AdjustmentUtility) *int64 {
	if r == nil {
		return nil
	}
	if u == AdjustmentUtilityWater {
		return r.Water
	}
	return r.Electricity
}

// ResolveRecoveryReconciliation derives the forward-credit gate + rate for a
// reading about to be billed. It short-circuits to nil (no refund, P-only)
// whenever F must not fire — the reading is not a recovery, has no source, is
// not over-recorded on either utility, or the source month was never billed
// (S0). Only a genuine, source-billed over-record hits the DB. contractID is the
// current bill's contract; a valid recovery's source lies within it (Lock D), so
// it scopes the source bill correctly.
func ResolveRecoveryReconciliation(
	ctx context.Context,
	reading *meterreading.MeterReading,
	contractID uuid.UUID,
	finder SourceRefundRateFinder,
) (*RecoveryReconciliation, error) {
	if reading == nil || reading.AnchorReason == nil ||
		*reading.AnchorReason != meterreading.AnchorReasonReadingRecovery {
		return nil, nil
	}
	if reading.RecoverySourceReadingID == nil {
		return nil, nil // source-less recovery = re-anchor only, no refund
	}
	// "Over-recorded" is the same predicate the finalize gate + ResolveCorrection
	// use — reuse IsAffected so the definition never diverges across the money path.
	affectedElec := reading.ElectricityRecorded != nil && IsAffected(int64(*reading.ElectricityRecorded), int64(reading.ElectricityCurrent))
	affectedWater := reading.WaterRecorded != nil && IsAffected(int64(*reading.WaterRecorded), int64(reading.WaterCurrent))
	if !affectedElec && !affectedWater {
		return nil, nil // not an over-record on either utility
	}

	rates, err := finder.FindRecoverySourceRefundRates(ctx, *reading.RecoverySourceReadingID, contractID)
	if err != nil {
		return nil, err
	}
	if rates == nil {
		return nil, nil // S0: source month never billed → nothing to refund
	}

	recon := &RecoveryReconciliation{}
	if affectedElec {
		e := rates.Electricity
		recon.Electricity = &e
	}
	if affectedWater {
		w := rates.Water
		recon.Water = &w
	}
	return recon, nil
}
