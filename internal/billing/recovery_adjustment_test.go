package billing

import (
	"testing"

	"nana/internal/meterreading"

	"github.com/google/uuid"
)

func utilPtr(u AdjustmentUtility) *AdjustmentUtility { return &u }

func TestBuildRecoveryAdjustmentLine(t *testing.T) {
	billID := uuid.New()
	recID := uuid.New()

	// Evidence: recorded 1500 → physical 1200, rate 50 satang/unit → overUnits
	// 300, refund −15000. quantity=300, unit_price=50 carry the evidence.
	cases := []struct {
		name          string
		res           RecoveryResolution
		wantAmount    int64
		wantQuantity  int
		wantUnitPrice int64
		wantDesc      string
		wantUtility   *AdjustmentUtility
		wantErr       error
	}{
		{
			name:          "refund electricity — evidence on the line",
			res:           RecoveryResolution{RecoveryReadingID: recID, Utility: AdjustmentUtilityElectricity, Amount: -15000, Recorded: 1500, Physical: 1200, RatePerUnit: 50},
			wantAmount:    -15000,
			wantQuantity:  300,
			wantUnitPrice: 50,
			wantDesc:      "จดไว้ 1500 → จริง 1200",
			wantUtility:   utilPtr(AdjustmentUtilityElectricity),
		},
		{
			name:          "refund water",
			res:           RecoveryResolution{RecoveryReadingID: recID, Utility: AdjustmentUtilityWater, Amount: -15000, Recorded: 1500, Physical: 1200, RatePerUnit: 50},
			wantAmount:    -15000,
			wantQuantity:  300,
			wantUnitPrice: 50,
			wantDesc:      "จดไว้ 1500 → จริง 1200",
			wantUtility:   utilPtr(AdjustmentUtilityWater),
		},
		{
			name:    "positive (charge) rejected — refund-only",
			res:     RecoveryResolution{RecoveryReadingID: recID, Utility: AdjustmentUtilityElectricity, Amount: 15000, Recorded: 1500, Physical: 1200, RatePerUnit: 50},
			wantErr: ErrAdjustmentRefundMustBeNegative,
		},
		{
			// A zero-rate over-record resolves with Amount 0; the caller must SKIP
			// it (emit no line). If it ever reaches the builder, ValidateAdjustment
			// rejects a zero METER_RECOVERY amount — a loud guard, not a silent 0-line.
			name:    "zero amount rejected (zero-rate over-record is skipped by caller)",
			res:     RecoveryResolution{RecoveryReadingID: recID, Utility: AdjustmentUtilityElectricity, Amount: 0, Recorded: 1500, Physical: 1200, RatePerUnit: 0},
			wantErr: ErrAdjustmentRefundMustBeNegative,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			line, err := BuildRecoveryAdjustmentLine(billID, tc.res, 7)
			if err != tc.wantErr {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr != nil {
				return
			}
			if line.AdjustmentReasonCode == nil || *line.AdjustmentReasonCode != AdjustmentReasonMeterRecovery {
				t.Errorf("reason = %v, want METER_RECOVERY", line.AdjustmentReasonCode)
			}
			if line.Amount != tc.wantAmount {
				t.Errorf("amount = %d, want %d", line.Amount, tc.wantAmount)
			}
			if line.Quantity != tc.wantQuantity || line.UnitPrice != tc.wantUnitPrice {
				t.Errorf("quantity/unitPrice = %d/%d, want %d/%d", line.Quantity, line.UnitPrice, tc.wantQuantity, tc.wantUnitPrice)
			}
			if line.Description != tc.wantDesc {
				t.Errorf("description = %q, want %q", line.Description, tc.wantDesc)
			}
			if line.BillID != billID || line.SortOrder != 7 || line.Source != LineItemSourceManual {
				t.Errorf("billID/sortOrder/source mismatch: %v/%d/%v", line.BillID, line.SortOrder, line.Source)
			}
			if line.AdjustmentRecoveryReadingID == nil || *line.AdjustmentRecoveryReadingID != recID {
				t.Errorf("recovery FK = %v, want %v", line.AdjustmentRecoveryReadingID, recID)
			}
			if tc.wantUtility != nil {
				if line.AdjustmentUtility == nil || *line.AdjustmentUtility != *tc.wantUtility {
					t.Errorf("utility = %v, want %v", line.AdjustmentUtility, *tc.wantUtility)
				}
			}
		})
	}
}

// RecommendRefund — deterministic over-record refund. Precondition recorded >
// physical; otherwise not an over-record (ok=false, no engagement).
func TestRecommendRefund(t *testing.T) {
	cases := []struct {
		name       string
		recorded   int64
		physical   int64
		rate       int64
		wantRefund int64
		wantOK     bool
	}{
		{"over-record refunds negative", 1500, 1200, 800, -(300 * 800), true},
		{"equal → not over-record", 1200, 1200, 800, 0, false},
		{"under-record → not over-record", 1200, 1500, 800, 0, false},
		{"minimal over-record", 1201, 1200, 800, -800, true},
		{"zero-rate over-record → 0 refund, still ok", 1500, 1200, 0, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := RecommendRefund(tc.recorded, tc.physical, tc.rate)
			if got != tc.wantRefund || ok != tc.wantOK {
				t.Errorf("RecommendRefund(%d,%d,%d) = (%d,%v), want (%d,%v)",
					tc.recorded, tc.physical, tc.rate, got, ok, tc.wantRefund, tc.wantOK)
			}
		})
	}
}

func TestIsAffected(t *testing.T) {
	if !IsAffected(1500, 1200) {
		t.Error("recorded > physical must be affected")
	}
	if IsAffected(1200, 1200) {
		t.Error("recorded == physical must be unaffected")
	}
	if IsAffected(1200, 1500) {
		t.Error("recorded < physical must be unaffected")
	}
}

// ResolveCorrection — Q1.6 pure refund builder: NO decision, NO note. Derives
// the deterministic refund from recorded/physical/rate. Rate-0 → Amount 0
// (caller skips the line). A utility that is not an over-record → error.
func TestResolveCorrection(t *testing.T) {
	recorded := 1500
	rec := &meterreading.MeterReading{
		ID:                  uuid.New(),
		ElectricityCurrent:  1200, // physical; recorded 1500 > 1200 → over-record of 300
		ElectricityRecorded: &recorded,
	}

	// Normal over-record at ฿8/unit (800 satang).
	res, lt, err := ResolveCorrection(rec, AdjustmentUtilityElectricity, 800)
	if err != nil {
		t.Fatalf("ResolveCorrection: %v", err)
	}
	if res.Amount != -(300*800) || res.Recorded != 1500 || res.Physical != 1200 || res.RatePerUnit != 800 {
		t.Errorf("resolution = %+v, want amount %d / recorded 1500 / physical 1200 / rate 800", res, -(300 * 800))
	}
	if res.RecoveryReadingID != rec.ID {
		t.Errorf("recovery FK = %v, want %v", res.RecoveryReadingID, rec.ID)
	}
	if lt != LineItemElectricity {
		t.Errorf("re-baseline line type = %v, want ELECTRICITY", lt)
	}

	// Zero-rate over-record → Amount 0 (nothing was charged; caller emits no line).
	res0, _, err := ResolveCorrection(rec, AdjustmentUtilityElectricity, 0)
	if err != nil {
		t.Fatalf("ResolveCorrection rate-0: %v", err)
	}
	if res0.Amount != 0 {
		t.Errorf("rate-0 amount = %d, want 0", res0.Amount)
	}

	// Water was not recorded on this row → not an over-record → error.
	if _, _, err := ResolveCorrection(rec, AdjustmentUtilityWater, 1800); err != ErrCorrectionUtilityNotAffected {
		t.Errorf("water err = %v, want ErrCorrectionUtilityNotAffected", err)
	}
}
