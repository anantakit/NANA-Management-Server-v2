package billing

import (
	"testing"

	"github.com/google/uuid"
)

func TestBuildRecoveryAdjustmentLine(t *testing.T) {
	billID := uuid.New()
	recID := uuid.New()
	note := "แก้ค่ามิเตอร์ที่จดผิด"

	cases := []struct {
		name        string
		res         RecoveryResolution
		sourceMonth string
		wantReason  AdjustmentReasonCode
		wantAmount  int64
		wantDesc    string
		wantUtility *AdjustmentUtility
		wantErr     error
	}{
		{
			name:        "refund with source + utility",
			res:         RecoveryResolution{RecoveryReadingID: recID, Utility: AdjustmentUtilityElectricity, Amount: -15000, Note: note},
			sourceMonth: "2026-04",
			wantReason:  AdjustmentReasonMeterRecovery,
			wantAmount:  -15000,
			wantDesc:    "คืนยอดที่เก็บเกินจากเดือน 2026-04 (จดมิเตอร์ผิด)",
			wantUtility: utilPtr(AdjustmentUtilityElectricity),
		},
		{
			name:        "refund source-less",
			res:         RecoveryResolution{RecoveryReadingID: recID, Utility: AdjustmentUtilityWater, Amount: -15000, Note: note},
			wantReason:  AdjustmentReasonMeterRecovery,
			wantAmount:  -15000,
			wantDesc:    "คืนยอดที่เก็บเกิน (จดมิเตอร์ผิด)",
			wantUtility: utilPtr(AdjustmentUtilityWater),
		},
		{
			name:        "waive forces zero + waived reason regardless of Amount",
			res:         RecoveryResolution{RecoveryReadingID: recID, Utility: AdjustmentUtilityElectricity, Amount: -999, Note: note, Waive: true},
			wantReason:  AdjustmentReasonMeterRecoveryWaived,
			wantAmount:  0,
			wantDesc:    "ไม่คิดเงินเพิ่ม (จดมิเตอร์ผิด)",
			wantUtility: utilPtr(AdjustmentUtilityElectricity),
		},
		{
			name:    "positive (charge) rejected — refund-only",
			res:     RecoveryResolution{RecoveryReadingID: recID, Utility: AdjustmentUtilityElectricity, Amount: 15000, Note: note},
			wantErr: ErrAdjustmentRefundMustBeNegative,
		},
		{
			name:    "zero non-waive rejected (forgot amount)",
			res:     RecoveryResolution{RecoveryReadingID: recID, Utility: AdjustmentUtilityElectricity, Amount: 0, Note: note},
			wantErr: ErrAdjustmentRefundMustBeNegative,
		},
		{
			name:    "short note rejected via ValidateAdjustment passthrough",
			res:     RecoveryResolution{RecoveryReadingID: recID, Utility: AdjustmentUtilityElectricity, Amount: -100, Note: "short"},
			wantErr: ErrAdjustmentNoteTooShort,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			line, err := BuildRecoveryAdjustmentLine(billID, tc.res, tc.sourceMonth, 7)
			if err != tc.wantErr {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr != nil {
				return
			}
			if line.AdjustmentReasonCode == nil || *line.AdjustmentReasonCode != tc.wantReason {
				t.Errorf("reason = %v, want %v", line.AdjustmentReasonCode, tc.wantReason)
			}
			if line.Amount != tc.wantAmount || line.UnitPrice != tc.wantAmount {
				t.Errorf("amount/unitPrice = %d/%d, want %d", line.Amount, line.UnitPrice, tc.wantAmount)
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

// ResolveRefundAmount — override ∈ [recommended, 0): smaller refund only, no
// over-refund, no charge, no escape hatch (Q1.5 §0a #2).
func TestResolveRefundAmount(t *testing.T) {
	const recommended = -3000
	cases := []struct {
		name     string
		decision RecoveryDecision
		override int64
		want     int64
		wantErr  error
	}{
		{"accept → full recommendation", RecoveryDecisionAccept, 0, recommended, nil},
		{"waive → zero", RecoveryDecisionWaive, 0, 0, nil},
		{"override smaller refund ok", RecoveryDecisionOverride, -2500, -2500, nil},
		{"override equal to recommended ok", RecoveryDecisionOverride, -3000, -3000, nil},
		{"override larger refund rejected", RecoveryDecisionOverride, -3500, 0, ErrRecoveryOverrideOutOfBounds},
		{"override positive (charge) rejected", RecoveryDecisionOverride, 100, 0, ErrRecoveryOverrideOutOfBounds},
		{"override zero (not a refund) rejected", RecoveryDecisionOverride, 0, 0, ErrRecoveryOverrideOutOfBounds},
		{"unknown decision rejected", RecoveryDecision("BOGUS"), -100, 0, ErrRecoveryDecisionInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveRefundAmount(tc.decision, recommended, tc.override)
			if err != tc.wantErr {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if err == nil && got != tc.want {
				t.Errorf("amount = %d, want %d", got, tc.want)
			}
		})
	}
}

func utilPtr(u AdjustmentUtility) *AdjustmentUtility { return &u }
