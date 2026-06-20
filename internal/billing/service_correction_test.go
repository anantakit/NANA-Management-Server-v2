package billing

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"nana/internal/meterreading"
	"nana/internal/shared/respond"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// finalizedMonthlyForCorrection builds a FINALIZED monthly bill suitable as
// the "old" bill in a correction flow. TotalAmount is intentionally wrong
// (999999) — proves regeneration replaces it with the snapshot-derived
// amount rather than copying old line items.
func finalizedMonthlyForCorrection(contractID uuid.UUID, oldBillID uuid.UUID, roomID uuid.UUID) *Bill {
	return &Bill{
		ID:           oldBillID,
		ContractID:   contractID,
		BillingMonth: "2026-03",
		BillType:     BillTypeMonthly,
		Status:       BillStatusFinalized,
		TotalAmount:  999999,
		LineItems: []BillLineItem{
			{LineType: LineItemRoomRent, Source: LineItemSourceAuto, Amount: 999999, SortOrder: 1},
		},
	}
}

// TestCorrectMonthlyBill_HappyPath asserts the full void+recreate state
// machine in one TX: old becomes VOID+CORRECTION+linked, new is a fresh
// DRAFT regenerated from contract+meter source-of-truth (not cloned), and
// both audit events fire with the correct payloads.
func TestCorrectMonthlyBill_HappyPath(t *testing.T) {
	c := testContract()
	oldID := uuid.New()
	old := finalizedMonthlyForCorrection(c.ID, oldID, c.RoomID)
	reading := testMonthlyReading(c.RoomID, "2026-03")

	repo := &mockBillingRepo{}
	// Bill lookup must resolve by id — old via fixture, new via createdBills.
	// FindByIDWithRelations falls back to FindByID for IDs not in updatedBills.
	repo.findByIDFn = func(_ context.Context, id uuid.UUID) (*Bill, error) {
		if id == oldID {
			return old, nil
		}
		for _, b := range repo.createdBills {
			if b.ID == id {
				return b, nil
			}
		}
		return nil, gorm.ErrRecordNotFound
	}

	audit := &mockBillAuditRepo{}
	actor := uuid.New()
	meters := &mockMeterQuerier{
		findMonthlyByRoomsAndMonthFn: func(_ context.Context, _ []uuid.UUID, _ string) (map[uuid.UUID]*meterreading.MeterReading, error) {
			return map[uuid.UUID]*meterreading.MeterReading{c.RoomID: reading}, nil
		},
	}
	svc := NewBillingService(repo, audit, &mockContractQuerier{contract: c}, meters,
		&mockConfigQuerier{}, nil, &mockTxManager{})

	reason := "ออกบิลผิดอัตราค่าไฟ ขอออกใหม่"
	newBill, err := svc.CorrectBill(context.Background(), oldID, CorrectBillRequest{CorrectionReason: reason}, &actor)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// New DRAFT regenerated from source (rent 500000 + 150*800 + 10*1800 = 638000).
	if newBill.Status != BillStatusDraft {
		t.Errorf("new bill status = %s, want DRAFT", newBill.Status)
	}
	if newBill.BillType != BillTypeMonthly {
		t.Errorf("new bill type = %s, want MONTHLY", newBill.BillType)
	}
	if newBill.ID == oldID {
		t.Errorf("new bill ID equals old — must be distinct")
	}
	if newBill.BatchID != nil {
		t.Errorf("new BatchID = %v, want nil (corrected bill is manual artifact)", newBill.BatchID)
	}
	if newBill.TotalAmount != 638000 {
		t.Errorf("new total = %d, want 638000 (regenerated, not cloned 999999)", newBill.TotalAmount)
	}
	if len(newBill.LineItems) != 3 {
		t.Errorf("new line items = %d, want 3 (rent + elec + water)", len(newBill.LineItems))
	}

	// Old bill mutated: VOID + CORRECTION + superseded_by → new.
	if !old.IsVoid() {
		t.Errorf("old status = %s, want VOID", old.Status)
	}
	if old.VoidReason == nil || *old.VoidReason != "CORRECTION" {
		t.Errorf("old void_reason = %v, want CORRECTION", old.VoidReason)
	}
	if old.SupersededByBillID == nil || *old.SupersededByBillID != newBill.ID {
		t.Errorf("old superseded_by = %v, want %v", old.SupersededByBillID, newBill.ID)
	}

	// Exactly 2 audit events: SUPERSEDE on old + CREATE_FROM_CORRECTION on new.
	if len(audit.logs) != 2 {
		t.Fatalf("audit.logs = %d, want 2", len(audit.logs))
	}
	var supersede, createFrom *BillAuditLog
	for i := range audit.logs {
		switch audit.logs[i].Action {
		case AuditSupersede:
			supersede = &audit.logs[i]
		case AuditCreateFromCorrection:
			createFrom = &audit.logs[i]
		}
	}
	if supersede == nil {
		t.Fatalf("missing SUPERSEDE event")
	}
	if createFrom == nil {
		t.Fatalf("missing CREATE_FROM_CORRECTION event")
	}
	if supersede.BillID != oldID {
		t.Errorf("SUPERSEDE bill_id = %v, want %v", supersede.BillID, oldID)
	}
	if createFrom.BillID != newBill.ID {
		t.Errorf("CREATE_FROM_CORRECTION bill_id = %v, want %v", createFrom.BillID, newBill.ID)
	}
	if supersede.ActorID == nil || *supersede.ActorID != actor {
		t.Errorf("SUPERSEDE actor = %v, want %v", supersede.ActorID, actor)
	}

	var sp AuditSupersedePayload
	if err := json.Unmarshal([]byte(supersede.Payload), &sp); err != nil {
		t.Fatalf("unmarshal SUPERSEDE: %v", err)
	}
	if sp.PreviousStatus != string(BillStatusFinalized) {
		t.Errorf("SUPERSEDE.previous_status = %s, want FINALIZED", sp.PreviousStatus)
	}
	if sp.NewBillID != newBill.ID {
		t.Errorf("SUPERSEDE.new_bill_id = %v, want %v", sp.NewBillID, newBill.ID)
	}
	if sp.CorrectionReason != reason {
		t.Errorf("SUPERSEDE.correction_reason = %q, want %q", sp.CorrectionReason, reason)
	}

	var cp AuditCreateFromCorrectionPayload
	if err := json.Unmarshal([]byte(createFrom.Payload), &cp); err != nil {
		t.Fatalf("unmarshal CREATE_FROM_CORRECTION: %v", err)
	}
	if cp.SupersededBillID != oldID {
		t.Errorf("CREATE_FROM_CORRECTION.superseded_bill_id = %v, want %v", cp.SupersededBillID, oldID)
	}
	if cp.CorrectionReason != reason {
		t.Errorf("CREATE_FROM_CORRECTION.correction_reason = %q, want %q", cp.CorrectionReason, reason)
	}
}

// TestCorrectBill_GuardsPropagate asserts every CanCorrect domain guard
// surfaces as a 400 AppError with the Thai sentinel — and that no audit
// event or new bill leaks out when a guard rejects.
func TestCorrectBill_GuardsPropagate(t *testing.T) {
	c := testContract()
	cases := []struct {
		name    string
		mutate  func(*Bill)
		wantMsg string
	}{
		{"PAID rejected", func(b *Bill) { b.Status = BillStatusPaid }, ErrAlreadyPaid.Error()},
		{"VOID rejected", func(b *Bill) { b.Status = BillStatusVoid }, ErrAlreadyVoided.Error()},
		{"DRAFT rejected", func(b *Bill) { b.Status = BillStatusDraft }, ErrNotFinalized.Error()},
		{"SETTLEMENT rejected", func(b *Bill) { b.BillType = BillTypeSettlement }, ErrSettlementCorrectionNotSupported.Error()},
		{"already superseded rejected", func(b *Bill) {
			next := uuid.New()
			b.SupersededByBillID = &next
		}, ErrAlreadySuperseded.Error()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			oldID := uuid.New()
			old := finalizedMonthlyForCorrection(c.ID, oldID, c.RoomID)
			tc.mutate(old)

			repo := &mockBillingRepo{}
			repo.findByIDFn = func(_ context.Context, _ uuid.UUID) (*Bill, error) { return old, nil }
			audit := &mockBillAuditRepo{}
			svc := NewBillingService(repo, audit, &mockContractQuerier{contract: c}, &mockMeterQuerier{},
				&mockConfigQuerier{}, nil, &mockTxManager{})

			_, err := svc.CorrectBill(context.Background(), oldID, CorrectBillRequest{CorrectionReason: "เหตุผลทดสอบ"}, nil)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			appErr, ok := respond.Is(err)
			if !ok {
				t.Fatalf("expected AppError, got %T: %v", err, err)
			}
			if appErr.HTTPStatus != 400 {
				t.Errorf("HTTP status = %d, want 400", appErr.HTTPStatus)
			}
			if !strings.Contains(appErr.Message, tc.wantMsg) {
				t.Errorf("message = %q, want to contain %q", appErr.Message, tc.wantMsg)
			}
			if len(audit.logs) != 0 {
				t.Errorf("audit.logs = %d, want 0 (no audit on guard reject)", len(audit.logs))
			}
			if len(repo.createdBills) != 0 {
				t.Errorf("createdBills = %d, want 0 (no new bill on guard reject)", len(repo.createdBills))
			}
		})
	}
}

// TestCorrectMonthlyBill_MeterMissing asserts the regeneration path bails
// cleanly when no monthly meter exists for the (room, billing_month).
// State must not be half-applied: old stays FINALIZED, no new bill, no audit.
func TestCorrectMonthlyBill_MeterMissing(t *testing.T) {
	c := testContract()
	oldID := uuid.New()
	old := finalizedMonthlyForCorrection(c.ID, oldID, c.RoomID)

	repo := &mockBillingRepo{}
	repo.findByIDFn = func(_ context.Context, _ uuid.UUID) (*Bill, error) { return old, nil }
	audit := &mockBillAuditRepo{}
	meters := &mockMeterQuerier{
		findMonthlyByRoomsAndMonthFn: func(_ context.Context, _ []uuid.UUID, _ string) (map[uuid.UUID]*meterreading.MeterReading, error) {
			return map[uuid.UUID]*meterreading.MeterReading{}, nil
		},
	}
	svc := NewBillingService(repo, audit, &mockContractQuerier{contract: c}, meters,
		&mockConfigQuerier{}, nil, &mockTxManager{})

	_, err := svc.CorrectBill(context.Background(), oldID, CorrectBillRequest{CorrectionReason: "เหตุผลทดสอบ"}, nil)
	if err != ErrMeterNotFound {
		t.Fatalf("expected ErrMeterNotFound, got %v", err)
	}
	if len(audit.logs) != 0 {
		t.Errorf("audit.logs = %d, want 0", len(audit.logs))
	}
	if len(repo.createdBills) != 0 {
		t.Errorf("createdBills = %d, want 0", len(repo.createdBills))
	}
	if !old.IsFinalized() {
		t.Errorf("old status = %s, want FINALIZED (no mutation on meter-missing path)", old.Status)
	}
}

func TestCorrectBill_NotFound(t *testing.T) {
	repo := &mockBillingRepo{} // empty — FindByID returns ErrRecordNotFound
	audit := &mockBillAuditRepo{}
	svc := NewBillingService(repo, audit, &mockContractQuerier{}, &mockMeterQuerier{},
		&mockConfigQuerier{}, nil, &mockTxManager{})

	_, err := svc.CorrectBill(context.Background(), uuid.New(), CorrectBillRequest{CorrectionReason: "เหตุผลทดสอบ"}, nil)
	if err != ErrBillNotFound {
		t.Fatalf("expected ErrBillNotFound, got %v", err)
	}
}
