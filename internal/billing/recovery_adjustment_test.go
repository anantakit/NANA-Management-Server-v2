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
		wantErr     error
	}{
		{
			name:        "charge",
			res:         RecoveryResolution{RecoveryReadingID: recID, Amount: 15000, Note: note},
			sourceMonth: "2026-04",
			wantReason:  AdjustmentReasonMeterRecovery,
			wantAmount:  15000,
			wantDesc:    "เก็บยอดเพิ่มเดือน 2026-04 (จดมิเตอร์ผิด)",
		},
		{
			name:       "refund source-less",
			res:        RecoveryResolution{RecoveryReadingID: recID, Amount: -15000, Note: note},
			wantReason: AdjustmentReasonMeterRecovery,
			wantAmount: -15000,
			wantDesc:   "คืนยอดที่เก็บเกิน (จดมิเตอร์ผิด)",
		},
		{
			name:       "waive forces zero + waived reason regardless of Amount",
			res:        RecoveryResolution{RecoveryReadingID: recID, Amount: 999, Note: note, Waive: true},
			wantReason: AdjustmentReasonMeterRecoveryWaived,
			wantAmount: 0,
			wantDesc:   "ไม่คิดเงินเพิ่ม (จดมิเตอร์ผิด)",
		},
		{
			name:    "zero non-waive rejected (forgot amount)",
			res:     RecoveryResolution{RecoveryReadingID: recID, Amount: 0, Note: note},
			wantErr: ErrAdjustmentAmountRequired,
		},
		{
			name:    "short note rejected via ValidateAdjustment passthrough",
			res:     RecoveryResolution{RecoveryReadingID: recID, Amount: 100, Note: "short"},
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
		})
	}
}
