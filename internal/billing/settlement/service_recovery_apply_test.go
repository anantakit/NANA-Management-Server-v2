package settlement

import (
	"context"
	"testing"

	"nana/internal/billing"
	"nana/internal/contract"
	"nana/internal/meterreading"

	"github.com/google/uuid"
)

// Q1 Recovery Decision — UpdateSettlementDraft resolves pending recoveries into
// the settlement DRAFT via AppliedCorrections, mirroring UpdateMonthlyDraft.
// These are the service-wiring tests: the risky reason/amount routing itself is
// covered by billing.TestBuildRecoveryAdjustmentLine.
func newDraftSettlementWithRecovery(billID, recID uuid.UUID) (*mockBillStore, *Service) {
	bill := &billing.Bill{
		ID:       billID,
		Status:   billing.BillStatusDraft,
		BillType: billing.BillTypeSettlement,
		LineItems: []billing.BillLineItem{
			{LineType: billing.LineItemProrateRent, Source: billing.LineItemSourceAuto, Amount: 300000, SortOrder: 1},
		},
	}
	bills := &mockBillStore{
		findByIDFn: func(_ context.Context, _ uuid.UUID) (*billing.Bill, error) { return bill, nil },
		findByIDWithRelationsFn: func(_ context.Context, _ uuid.UUID) (*billing.BillWithRelations, error) {
			return &billing.BillWithRelations{Bill: *bill}, nil
		},
	}
	ar := meterreading.AnchorReasonReadingRecovery
	elecRecorded := 1100 // over-record: recorded 1100 > physical 1000
	meters := &mockMeterReadingSource{
		findByIDFn: func(_ context.Context, id uuid.UUID) (*meterreading.MeterReading, error) {
			return &meterreading.MeterReading{
				ID: id, AnchorReason: &ar,
				ElectricityCurrent: 1000, ElectricityRecorded: &elecRecorded,
			}, nil
		},
	}
	contracts := &mockContractSource{
		findByIDFn: func(_ context.Context, _ uuid.UUID) (*contract.Contract, error) {
			// rate 150 satang/unit → refund = (1100-1000) × 150 = -15000 satang.
			return &contract.Contract{ElectricityRatePerUnit: 150, WaterRatePerUnit: 1800}, nil
		},
	}
	svc := newSvcWithMocks(
		bills,
		contracts,
		meters,
		&mockBillingConfigSource{},
		&mockMoveOutSource{},
	)
	return bills, svc
}

func adjustmentLines(items []billing.BillLineItem) []billing.BillLineItem {
	out := make([]billing.BillLineItem, 0, len(items))
	for _, li := range items {
		if li.LineType == billing.LineItemAdjustment {
			out = append(out, li)
		}
	}
	return out
}

func TestUpdateSettlementDraft_AppliesRecoveryRefund(t *testing.T) {
	billID, recID := uuid.New(), uuid.New()
	bills, svc := newDraftSettlementWithRecovery(billID, recID)

	// ACCEPT the deterministic refund: (1100-1000) × 150 satang = -15000.
	_, err := svc.UpdateSettlementDraft(context.Background(), billID, UpdateSettlementDraftRequest{
		AppliedCorrections: []billing.AppliedCorrectionInput{
			{RecoveryReadingID: recID.String(), Utility: "ELECTRICITY", Decision: "ACCEPT", AdjustmentNote: "คืนยอดที่เก็บเกินจากการจดมิเตอร์ผิด"},
		},
	}, nil)
	if err != nil {
		t.Fatalf("UpdateSettlementDraft: %v", err)
	}

	adj := adjustmentLines(bills.createdLineItems)
	if len(adj) != 1 {
		t.Fatalf("adjustment lines = %d, want 1", len(adj))
	}
	if adj[0].Amount != -15000 {
		t.Errorf("amount = %d satang, want -15000 (refund)", adj[0].Amount)
	}
	if adj[0].AdjustmentReasonCode == nil || *adj[0].AdjustmentReasonCode != billing.AdjustmentReasonMeterRecovery {
		t.Errorf("reason = %v, want METER_RECOVERY", adj[0].AdjustmentReasonCode)
	}
	if adj[0].AdjustmentRecoveryReadingID == nil || *adj[0].AdjustmentRecoveryReadingID != recID {
		t.Errorf("recovery FK = %v, want %v", adj[0].AdjustmentRecoveryReadingID, recID)
	}
}

func TestUpdateSettlementDraft_AppliesRecoveryWaive(t *testing.T) {
	billID, recID := uuid.New(), uuid.New()
	bills, svc := newDraftSettlementWithRecovery(billID, recID)

	// WAIVE on an affected utility → zero-amount METER_RECOVERY_WAIVED line.
	_, err := svc.UpdateSettlementDraft(context.Background(), billID, UpdateSettlementDraftRequest{
		AppliedCorrections: []billing.AppliedCorrectionInput{
			{RecoveryReadingID: recID.String(), Utility: "ELECTRICITY", Decision: "WAIVE", AdjustmentNote: "ตรวจแล้วไม่คิดเงินเพิ่ม"},
		},
	}, nil)
	if err != nil {
		t.Fatalf("UpdateSettlementDraft: %v", err)
	}

	adj := adjustmentLines(bills.createdLineItems)
	if len(adj) != 1 {
		t.Fatalf("adjustment lines = %d, want 1", len(adj))
	}
	if adj[0].Amount != 0 {
		t.Errorf("waive amount = %d, want 0", adj[0].Amount)
	}
	if adj[0].AdjustmentReasonCode == nil || *adj[0].AdjustmentReasonCode != billing.AdjustmentReasonMeterRecoveryWaived {
		t.Errorf("reason = %v, want METER_RECOVERY_WAIVED", adj[0].AdjustmentReasonCode)
	}
}

func TestUpdateSettlementDraft_RejectsAlreadyAppliedRecovery(t *testing.T) {
	billID, recID := uuid.New(), uuid.New()
	bills, svc := newDraftSettlementWithRecovery(billID, recID)
	bills.hasNonVoidAdjustmentFn = func(_ context.Context, _ uuid.UUID) (bool, error) { return true, nil }

	_, err := svc.UpdateSettlementDraft(context.Background(), billID, UpdateSettlementDraftRequest{
		AppliedCorrections: []billing.AppliedCorrectionInput{
			{RecoveryReadingID: recID.String(), Utility: "ELECTRICITY", Decision: "ACCEPT", AdjustmentNote: "คืนยอดที่เก็บเกินจากการจดมิเตอร์ผิด"},
		},
	}, nil)
	if err == nil {
		t.Fatal("expected conflict error for already-applied recovery, got nil")
	}
	if len(adjustmentLines(bills.createdLineItems)) != 0 {
		t.Error("no adjustment line should be created when the recovery is already applied")
	}
}

// Q1 finalization gate: FinalizeSettlement (and thus move-out closure, which
// drives it via port) must block while the contract has an unresolved recovery.
func TestFinalizeSettlement_BlockedByPendingRecovery(t *testing.T) {
	billID := uuid.New()
	bills, svc := newDraftSettlementWithRecovery(billID, uuid.New())
	bills.hasPendingRecoveryFn = func(_ context.Context, _ uuid.UUID) (bool, error) { return true, nil }

	err := svc.FinalizeSettlement(context.Background(), billID)
	if err == nil {
		t.Fatal("expected FinalizeSettlement to be blocked by pending recovery")
	}
}
