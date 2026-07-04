package billing

import (
	"nana/internal/meterreading"
	"nana/internal/shared/money"
)

// PendingCorrectionForBillResponse is the bill-scoped pending-correction shape:
// meterreading's meter facts (recorded/physical/affected) embedded, plus the
// per-utility deterministic refund recommendation billing derives from the
// bill's own contract rate.
//
// Money authority stays in billing (Q1.5 §0a): meterreading emits rate-free
// meter facts; billing owns RecommendRefund and the satang→baht conversion.
// The embedded response is reused deliberately — this is the established
// "bill-side convenience route" that already surfaces meterreading's shape.
type PendingCorrectionForBillResponse struct {
	meterreading.PendingBaselineCorrectionResponse
	// Signed refund in baht (negative = money back to the tenant); 0 when the
	// utility is not an over-record. FE reads recommendation-first from here.
	ElectricityRecommendedRefundBaht float64 `json:"electricity_recommended_refund_baht"`
	WaterRecommendedRefundBaht       float64 `json:"water_recommended_refund_baht"`
}

// buildPendingCorrectionForBill enriches one meter-facts row with the
// per-utility deterministic refund, computed from the bill's contract rate
// (satang per unit) via RecommendRefund. Pure: no I/O.
//
// RecommendRefund's precondition (recorded > physical) is the single gate — a
// non-over-record utility (recorded == physical, or 0 when uncorrected) yields
// ok=false and a 0 recommendation, so no separate "affected" branch is needed.
func buildPendingCorrectionForBill(row meterreading.PendingBaselineCorrection, elecRateSatang, waterRateSatang int64) PendingCorrectionForBillResponse {
	resp := PendingCorrectionForBillResponse{
		PendingBaselineCorrectionResponse: meterreading.ToPendingBaselineCorrectionResponse(row),
	}
	if refund, ok := RecommendRefund(int64(row.ElectricityRecorded), int64(row.RecoveryElectricity), elecRateSatang); ok {
		resp.ElectricityRecommendedRefundBaht = money.ToBaht(refund)
	}
	if refund, ok := RecommendRefund(int64(row.WaterRecorded), int64(row.RecoveryWater), waterRateSatang); ok {
		resp.WaterRecommendedRefundBaht = money.ToBaht(refund)
	}
	return resp
}

// buildPendingCorrectionsForBill maps a row set with one contract's rates.
func buildPendingCorrectionsForBill(rows []meterreading.PendingBaselineCorrection, elecRateSatang, waterRateSatang int64) []PendingCorrectionForBillResponse {
	out := make([]PendingCorrectionForBillResponse, len(rows))
	for i, r := range rows {
		out[i] = buildPendingCorrectionForBill(r, elecRateSatang, waterRateSatang)
	}
	return out
}
