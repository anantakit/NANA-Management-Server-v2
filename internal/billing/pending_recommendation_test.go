package billing

import (
	"testing"

	"nana/internal/meterreading"
)

// buildPendingCorrectionForBill derives the per-utility deterministic refund
// from the bill's contract rate. Refund is signed baht (negative = money back);
// 0 when the utility is not an over-record. Electricity and water are independent.
func TestBuildPendingCorrectionForBill(t *testing.T) {
	const elecRate = 800 // satang/unit
	const waterRate = 1800

	cases := []struct {
		name            string
		row             meterreading.PendingBaselineCorrection
		wantElecRefund  float64
		wantWaterRefund float64
	}{
		// NOTE: billing derives the refund from recorded/physical + rate only; the
		// *Affected booleans are FE-facing meter facts billing does not consume, so
		// they are omitted here.
		{
			name: "electricity over-record only, water unaffected",
			row: meterreading.PendingBaselineCorrection{
				RecoveryElectricity: 1200, ElectricityRecorded: 1500,
				RecoveryWater: 220, WaterRecorded: 0,
			},
			wantElecRefund:  -2400, // -(1500-1200)*800 satang = -240000 → -2400 baht
			wantWaterRefund: 0,
		},
		{
			name: "water over-record only",
			row: meterreading.PendingBaselineCorrection{
				RecoveryElectricity: 1200, ElectricityRecorded: 0,
				RecoveryWater: 220, WaterRecorded: 300,
			},
			wantElecRefund:  0,
			wantWaterRefund: -1440, // -(300-220)*1800 = -144000 satang → -1440 baht
		},
		{
			name: "both over-record",
			row: meterreading.PendingBaselineCorrection{
				RecoveryElectricity: 1000, ElectricityRecorded: 1100,
				RecoveryWater: 100, WaterRecorded: 110,
			},
			wantElecRefund:  -800, // -(100)*800 = -80000 → -800 baht
			wantWaterRefund: -180, // -(10)*1800 = -18000 → -180 baht
		},
		{
			name: "recorded == physical → unaffected, no refund",
			row: meterreading.PendingBaselineCorrection{
				RecoveryElectricity: 1200, ElectricityRecorded: 1200,
				RecoveryWater: 220, WaterRecorded: 220,
			},
			wantElecRefund:  0,
			wantWaterRefund: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildPendingCorrectionForBill(tc.row, elecRate, waterRate)
			if got.ElectricityRecommendedRefundBaht != tc.wantElecRefund {
				t.Errorf("elec refund = %v, want %v", got.ElectricityRecommendedRefundBaht, tc.wantElecRefund)
			}
			if got.WaterRecommendedRefundBaht != tc.wantWaterRefund {
				t.Errorf("water refund = %v, want %v", got.WaterRecommendedRefundBaht, tc.wantWaterRefund)
			}
		})
	}
}

// A zero contract rate (contract default) yields a zero refund even for a genuine
// over-record — the discrepancy is real but there is nothing to refund at rate 0.
func TestBuildPendingCorrectionForBill_ZeroRate(t *testing.T) {
	row := meterreading.PendingBaselineCorrection{
		RecoveryElectricity: 1200, ElectricityRecorded: 1500, // over-record
		RecoveryWater: 220, WaterRecorded: 300, // over-record
	}
	got := buildPendingCorrectionForBill(row, 0, 0)
	if got.ElectricityRecommendedRefundBaht != 0 || got.WaterRecommendedRefundBaht != 0 {
		t.Errorf("rate=0 refunds = %v/%v, want 0/0", got.ElectricityRecommendedRefundBaht, got.WaterRecommendedRefundBaht)
	}
}
