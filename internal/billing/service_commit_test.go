package billing

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"nana/internal/shared/respond"

	"github.com/google/uuid"
)

// --- helpers ---

func newTestBatch(itemCount int) (*BillGenerationBatch, []BillGenerationBatchItem) {
	batchID := uuid.New()
	batch := &BillGenerationBatch{
		ID:             batchID,
		ApartmentID:    uuid.New(),
		BillingMonth:   "2026-04",
		Status:         BatchStatusCompleted,
		TotalContracts: itemCount,
		CreatedCount:   itemCount,
		CreatedAt:      time.Now(),
	}

	items := make([]BillGenerationBatchItem, itemCount)
	for i := range items {
		items[i] = BillGenerationBatchItem{
			ID:         uuid.New(),
			BatchID:    batchID,
			ContractID: uuid.New(),
			RoomID:     uuid.New(),
			RoomNumber: fmt.Sprintf("10%d", i+1),
			RoomFloor:  1,
			ResultType: ResultCreated,
			ComputedSnapshot: ComputedSnapshot{
				Version: ComputedSnapshotVersion,
				LineItems: []ComputedLineItem{
					{Type: LineItemRoomRent, Description: "ค่าห้อง 2026-05", Amount: 500000, SortOrder: 1},
					{Type: LineItemElectricity, Description: "ค่าไฟ 50 หน่วย", Amount: 30000, Quantity: 50, UnitPrice: 600, SortOrder: 2},
					{Type: LineItemWater, Description: "ค่าน้ำ 5 หน่วย", Amount: 9000, Quantity: 5, UnitPrice: 1800, SortOrder: 3},
				},
				TotalAmount: 539000,
				ComputedAt:  time.Now(),
			},
		}
	}
	return batch, items
}

func newCommitService(repo *mockBillingRepo) BillingService {
	return NewBillingService(
		repo,
		&mockBillAuditRepo{},
		&mockContractQuerier{},
		&mockMeterQuerier{},
		&mockConfigQuerier{},
		&mockMoveOutQuerier{},
		&mockTxManager{},
	)
}

// --- tests ---

func TestCommitBatch_HappyPath(t *testing.T) {
	batch, items := newTestBatch(3)
	repo := &mockBillingRepo{createdBatch: batch, createdBatchItems: items}
	svc := newCommitService(repo)

	result, err := svc.CommitBatch(context.Background(), batch.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.SuccessCount != 3 {
		t.Errorf("SuccessCount = %d, want 3", result.SuccessCount)
	}
	if result.FailCount != 0 {
		t.Errorf("FailCount = %d, want 0", result.FailCount)
	}
	if result.PendingCount != 0 {
		t.Errorf("PendingCount = %d, want 0", result.PendingCount)
	}
	if result.Batch.CommitStatus == nil || *result.Batch.CommitStatus != CommitStatusCommitted {
		t.Errorf("CommitStatus = %v, want COMMITTED", result.Batch.CommitStatus)
	}
	if result.Batch.CommittedAt == nil {
		t.Error("CommittedAt should be set")
	}

	// Verify all items got bill_id
	for i, it := range repo.createdBatchItems {
		if it.BillID == nil {
			t.Errorf("item %d: BillID should be set", i)
		}
	}

	// Verify bills were created with correct fields
	if repo.createdBill == nil {
		t.Fatal("expected at least one bill created")
	}
	if repo.createdBill.Status != BillStatusFinalized {
		t.Errorf("bill status = %s, want FINALIZED", repo.createdBill.Status)
	}
	if repo.createdBill.BillType != BillTypeMonthly {
		t.Errorf("bill type = %s, want MONTHLY", repo.createdBill.BillType)
	}
}

func TestCommitBatch_PartialBusinessFailure(t *testing.T) {
	batch, items := newTestBatch(3)

	// Corrupt one snapshot so validation fails
	items[1].ComputedSnapshot.Version = 999

	repo := &mockBillingRepo{createdBatch: batch, createdBatchItems: items}
	svc := newCommitService(repo)

	result, err := svc.CommitBatch(context.Background(), batch.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.SuccessCount != 2 {
		t.Errorf("SuccessCount = %d, want 2", result.SuccessCount)
	}
	if result.FailCount != 1 {
		t.Errorf("FailCount = %d, want 1", result.FailCount)
	}
	if result.Batch.CommitStatus == nil || *result.Batch.CommitStatus != CommitStatusPartiallyCommitted {
		t.Errorf("CommitStatus = %v, want PARTIALLY_COMMITTED", result.Batch.CommitStatus)
	}

	// Failed item should have COMMIT_ERROR reason
	if repo.createdBatchItems[1].ReasonCode != ReasonCodeCommitError {
		t.Errorf("failed item reason_code = %q, want COMMIT_ERROR", repo.createdBatchItems[1].ReasonCode)
	}
}

func TestCommitBatch_IdempotentRetry(t *testing.T) {
	batch, items := newTestBatch(3)

	// Simulate: item 0 already committed (has bill_id), items 1-2 still pending
	committedBillID := uuid.New()
	items[0].BillID = &committedBillID

	repo := &mockBillingRepo{createdBatch: batch, createdBatchItems: items}
	svc := newCommitService(repo)

	result, err := svc.CommitBatch(context.Background(), batch.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Only 2 pending items should be processed
	if result.SuccessCount != 2 {
		t.Errorf("SuccessCount = %d, want 2", result.SuccessCount)
	}

	// Item 0's bill_id should remain unchanged
	if *repo.createdBatchItems[0].BillID != committedBillID {
		t.Error("already-committed item's BillID was changed")
	}
}

func TestCommitBatch_AlreadyCommitted(t *testing.T) {
	batch, items := newTestBatch(1)
	committed := CommitStatusCommitted
	batch.CommitStatus = &committed

	repo := &mockBillingRepo{createdBatch: batch, createdBatchItems: items}
	svc := newCommitService(repo)

	_, err := svc.CommitBatch(context.Background(), batch.ID)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var appErr *respond.AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("expected AppError, got %T: %v", err, err)
	}
	if appErr.HTTPStatus != 409 {
		t.Errorf("HTTP status = %d, want 409", appErr.HTTPStatus)
	}
}

func TestCommitBatch_MixedSkippedAndCreated(t *testing.T) {
	batch, items := newTestBatch(2)

	// Add a SKIPPED item — commit should not touch it
	skippedItem := BillGenerationBatchItem{
		ID:         uuid.New(),
		BatchID:    batch.ID,
		ContractID: uuid.New(),
		RoomID:     uuid.New(),
		RoomNumber: "200",
		RoomFloor:  2,
		ResultType: ResultSkipped,
		ReasonCode: ReasonMoveOutPending,
		ReasonText: "มีใบแจ้งย้ายออก",
	}
	items = append(items, skippedItem)
	batch.TotalContracts = 3

	repo := &mockBillingRepo{createdBatch: batch, createdBatchItems: items}
	svc := newCommitService(repo)

	result, err := svc.CommitBatch(context.Background(), batch.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Only CREATED items processed
	if result.SuccessCount != 2 {
		t.Errorf("SuccessCount = %d, want 2", result.SuccessCount)
	}

	// SKIPPED item should be untouched
	lastItem := repo.createdBatchItems[2]
	if lastItem.ResultType != ResultSkipped {
		t.Errorf("skipped item ResultType = %s, want SKIPPED", lastItem.ResultType)
	}
	if lastItem.BillID != nil {
		t.Error("skipped item should not have BillID")
	}
}

func TestCommitBatch_NotFound(t *testing.T) {
	repo := &mockBillingRepo{} // no batch
	svc := newCommitService(repo)

	_, err := svc.CommitBatch(context.Background(), uuid.New())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var appErr *respond.AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("expected AppError, got %T: %v", err, err)
	}
	if appErr.HTTPStatus != 404 {
		t.Errorf("HTTP status = %d, want 404", appErr.HTTPStatus)
	}
}

// --- isInfraError ---

func TestIsInfraError(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		isInfra bool
	}{
		{"snapshot version", ErrSnapshotUnsupportedVersion, false},
		{"snapshot no line items", ErrSnapshotNoLineItems, false},
		{"snapshot negative total", ErrSnapshotNegativeTotal, false},
		{"bill already exists", ErrBillAlreadyExists, false},
		{"batch already committed", ErrBatchAlreadyCommitted, false},
		{"4xx AppError", respond.ErrBadRequest.WithMessage("bad"), false},
		{"generic error", errors.New("connection refused"), true},
		{"context canceled", context.Canceled, true},
		{"deadline exceeded", context.DeadlineExceeded, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isInfraError(tt.err); got != tt.isInfra {
				t.Errorf("isInfraError(%v) = %v, want %v", tt.err, got, tt.isInfra)
			}
		})
	}
}
