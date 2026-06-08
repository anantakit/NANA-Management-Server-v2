package billingreconciliation

import (
	"context"
	"errors"
	"testing"

	"nana/internal/shared/respond"

	"github.com/google/uuid"
)

// --- Mock BillsCommander ---

type mockBillsCommander struct {
	createFn func(ctx context.Context, req CreateMonthlyBillForReconciliationRequest, actor *uuid.UUID) (*CreatedBill, error)
}

var _ BillsCommander = (*mockBillsCommander)(nil)

func (m *mockBillsCommander) CreateMonthlyBillForReconciliation(
	ctx context.Context,
	req CreateMonthlyBillForReconciliationRequest,
	actor *uuid.UUID,
) (*CreatedBill, error) {
	if m.createFn != nil {
		return m.createFn(ctx, req, actor)
	}
	return &CreatedBill{ID: uuid.New()}, nil
}

// --- commitOneRow branch coverage ---
//
// Pure-ish helper: drives the per-row classify+commit decision without
// requiring a full Reconcile pass. Covers each branch documented in
// service_generate.go to lock the SUCCESS / SKIPPED / FAILED axis.

func TestCommitOneRow_AlreadyBilled_FromInlineEvidence(t *testing.T) {
	billID := uuid.New()
	row := readyRow()
	row.Bill = &BillSnapshot{BillID: billID, Status: "FINALIZED", TotalAmountSatang: 700000}

	svc := &service{commander: &mockBillsCommander{
		createFn: func(_ context.Context, _ CreateMonthlyBillForReconciliationRequest, _ *uuid.UUID) (*CreatedBill, error) {
			t.Fatal("commander should NOT be called when row already has bill evidence")
			return nil, nil
		},
	}}

	item := svc.commitOneRow(context.Background(), row, "2026-06", nil)
	if item.ResultType != GenerateItemSkipped {
		t.Fatalf("ResultType = %s, want SKIPPED", item.ResultType)
	}
	if item.SkipReason != GenerateSkipAlreadyBilled {
		t.Errorf("SkipReason = %s, want ALREADY_BILLED_BY_OTHER", item.SkipReason)
	}
}

func TestCommitOneRow_BucketNotReady_MapsToLostReady(t *testing.T) {
	row := readyRow()
	row.Bucket = BucketActionRequired
	row.Reason = ReasonMissingMeterReading

	svc := &service{commander: &mockBillsCommander{
		createFn: func(_ context.Context, _ CreateMonthlyBillForReconciliationRequest, _ *uuid.UUID) (*CreatedBill, error) {
			t.Fatal("commander should NOT be called when bucket != READY")
			return nil, nil
		},
	}}

	item := svc.commitOneRow(context.Background(), row, "2026-06", nil)
	if item.ResultType != GenerateItemSkipped {
		t.Fatalf("ResultType = %s, want SKIPPED", item.ResultType)
	}
	if item.SkipReason != GenerateSkipLostReady {
		t.Errorf("SkipReason = %s, want LOST_READY_BETWEEN_PREVIEW_AND_COMMIT", item.SkipReason)
	}
}

func TestCommitOneRow_HappyPath_DelegatesAndPopulatesBillID(t *testing.T) {
	row := readyRow()
	wantBillID := uuid.New()
	wantContractID := *row.Room.ContractID

	calls := 0
	svc := &service{commander: &mockBillsCommander{
		createFn: func(_ context.Context, req CreateMonthlyBillForReconciliationRequest, _ *uuid.UUID) (*CreatedBill, error) {
			calls++
			if req.ContractID != wantContractID {
				t.Errorf("commander.ContractID = %s, want %s", req.ContractID, wantContractID)
			}
			if req.BillingMonth != "2026-06" {
				t.Errorf("commander.BillingMonth = %s, want 2026-06", req.BillingMonth)
			}
			return &CreatedBill{ID: wantBillID}, nil
		},
	}}

	item := svc.commitOneRow(context.Background(), row, "2026-06", nil)
	if calls != 1 {
		t.Errorf("commander called %d times, want 1", calls)
	}
	if item.ResultType != GenerateItemSuccess {
		t.Fatalf("ResultType = %s, want SUCCESS", item.ResultType)
	}
	if item.BillID == nil || *item.BillID != wantBillID {
		t.Errorf("BillID = %v, want %s", item.BillID, wantBillID)
	}
}

func TestCommitOneRow_AlreadyBilledRace_FromCommander(t *testing.T) {
	row := readyRow()
	svc := &service{commander: &mockBillsCommander{
		createFn: func(_ context.Context, _ CreateMonthlyBillForReconciliationRequest, _ *uuid.UUID) (*CreatedBill, error) {
			return nil, ErrAlreadyBilled
		},
	}}

	item := svc.commitOneRow(context.Background(), row, "2026-06", nil)
	if item.ResultType != GenerateItemSkipped {
		t.Fatalf("ResultType = %s, want SKIPPED", item.ResultType)
	}
	if item.SkipReason != GenerateSkipAlreadyBilled {
		t.Errorf("SkipReason = %s, want ALREADY_BILLED_BY_OTHER (race with another caller)", item.SkipReason)
	}
}

func TestCommitOneRow_LostReadyRace_FromCommander(t *testing.T) {
	row := readyRow()
	svc := &service{commander: &mockBillsCommander{
		createFn: func(_ context.Context, _ CreateMonthlyBillForReconciliationRequest, _ *uuid.UUID) (*CreatedBill, error) {
			return nil, ErrLostReady
		},
	}}

	item := svc.commitOneRow(context.Background(), row, "2026-06", nil)
	if item.ResultType != GenerateItemSkipped {
		t.Fatalf("ResultType = %s, want SKIPPED", item.ResultType)
	}
	if item.SkipReason != GenerateSkipLostReady {
		t.Errorf("SkipReason = %s, want LOST_READY (state changed mid-commit)", item.SkipReason)
	}
}

func TestCommitOneRow_AppErrorFromCommander_SurfacesAsFailed(t *testing.T) {
	row := readyRow()
	customErr := respond.New("WEIRD_BILLING_STATE", 500, "บางอย่างผิดพลาด")
	svc := &service{commander: &mockBillsCommander{
		createFn: func(_ context.Context, _ CreateMonthlyBillForReconciliationRequest, _ *uuid.UUID) (*CreatedBill, error) {
			return nil, customErr
		},
	}}

	item := svc.commitOneRow(context.Background(), row, "2026-06", nil)
	if item.ResultType != GenerateItemFailed {
		t.Fatalf("ResultType = %s, want FAILED", item.ResultType)
	}
	if item.ErrorCode != "WEIRD_BILLING_STATE" {
		t.Errorf("ErrorCode = %s, want WEIRD_BILLING_STATE (passes through AppError.Code)", item.ErrorCode)
	}
	if item.ErrorMessage != "บางอย่างผิดพลาด" {
		t.Errorf("ErrorMessage = %s, want Thai message verbatim", item.ErrorMessage)
	}
}

func TestCommitOneRow_PlainErrorFromCommander_SurfacesAsInfraError(t *testing.T) {
	row := readyRow()
	svc := &service{commander: &mockBillsCommander{
		createFn: func(_ context.Context, _ CreateMonthlyBillForReconciliationRequest, _ *uuid.UUID) (*CreatedBill, error) {
			return nil, errors.New("postgres: connection refused")
		},
	}}

	item := svc.commitOneRow(context.Background(), row, "2026-06", nil)
	if item.ResultType != GenerateItemFailed {
		t.Fatalf("ResultType = %s, want FAILED", item.ResultType)
	}
	if item.ErrorCode != "INFRA_ERROR" {
		t.Errorf("ErrorCode = %s, want INFRA_ERROR for plain errors", item.ErrorCode)
	}
	if item.ErrorMessage == "" {
		t.Error("ErrorMessage should not be empty — FE needs a Thai user-facing string")
	}
}

// --- Helpers ---

func readyRow() RoomClassification {
	contractID := uuid.New()
	return RoomClassification{
		Room: RoomCandidate{
			RoomID:     uuid.New(),
			RoomNumber: "101",
			RoomFloor:  1,
			RoomStatus: "OCCUPIED",
			ContractID: &contractID,
		},
		Bucket: BucketReady,
	}
}
