package billing

import (
	"context"
	"testing"
	"time"

	"nana/internal/billingconfig"

	"github.com/google/uuid"
)

// Service-tier happy path for the overdue-awareness hint: GET /bills/:id
// populates BOTH `OverdueDays` and `LatePenaltyReferenceAmount` when the
// bill is FINALIZED, MONTHLY, overdue, and the apartment has an active
// LATE_PENALTY config.
//
// Per testing-strategy.md (happy path only at service level — domain
// tests cover branching), this test guards the wiring; the pure logic
// branches live in penalty_test.go.
func TestGetByID_PopulatesOverdueDaysAndPenaltyReference(t *testing.T) {
	billID := uuid.New()
	aptID := uuid.New()

	// Build an overdue FINALIZED bill. Use a billing month two months
	// back so the suggestion fires regardless of test runtime.
	billingMonth := time.Now().AddDate(0, -2, 0).Format("2006-01")
	bill := &Bill{
		ID:           billID,
		BillingMonth: billingMonth,
		BillType:     BillTypeMonthly,
		Status:       BillStatusFinalized,
		TotalAmount:  390000, // ฿3,900
	}

	repo := &mockBillingRepo{
		findByIDWithRelationsFn: func(_ context.Context, _ uuid.UUID) (*BillWithRelations, error) {
			return &BillWithRelations{
				Bill:          *bill,
				TenantName:    "TEST_TENANT",
				RoomNumber:    "B202",
				ApartmentName: "นานาคอร์ท",
				ApartmentID:   aptID,
			}, nil
		},
	}
	svc := newSvc(repo,
		&mockContractQuerier{},
		&mockMeterQuerier{},
		&mockConfigQuerier{configs: []billingconfig.BillingConfig{
			{FeeType: billingconfig.FeeTypeLatePenalty, DefaultAmount: 10000, IsActive: true}, // ฿100
		}},
		&mockMoveOutQuerier{},
	)

	got, err := svc.GetByID(context.Background(), billID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.OverdueDays <= 0 {
		t.Errorf("OverdueDays = %d, want > 0 (bill is ~2 months past due)", got.OverdueDays)
	}
	if got.LatePenaltyReferenceAmount != 10000 {
		t.Errorf("LatePenaltyReferenceAmount = %d satang, want 10000 (฿100)", got.LatePenaltyReferenceAmount)
	}
}

// Sanity: when the apartment has NO LATE_PENALTY config (or it's
// inactive), the policy-reference value is 0 — but OverdueDays is
// still emitted because overdue-awareness is independent of penalty
// policy configuration.
func TestGetByID_OverdueDaysIndependentOfPenaltyConfig(t *testing.T) {
	billID := uuid.New()
	aptID := uuid.New()

	billingMonth := time.Now().AddDate(0, -2, 0).Format("2006-01")
	bill := &Bill{
		ID:           billID,
		BillingMonth: billingMonth,
		BillType:     BillTypeMonthly,
		Status:       BillStatusFinalized,
		TotalAmount:  390000,
	}

	repo := &mockBillingRepo{
		findByIDWithRelationsFn: func(_ context.Context, _ uuid.UUID) (*BillWithRelations, error) {
			return &BillWithRelations{Bill: *bill, ApartmentID: aptID}, nil
		},
	}
	svc := newSvc(repo,
		&mockContractQuerier{},
		&mockMeterQuerier{},
		// Empty configs slice — no LATE_PENALTY at all. Contrast with
		// the happy-path test above.
		&mockConfigQuerier{configs: []billingconfig.BillingConfig{}},
		&mockMoveOutQuerier{},
	)

	got, err := svc.GetByID(context.Background(), billID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.OverdueDays <= 0 {
		t.Errorf("OverdueDays = %d, want > 0 (overdue is independent of penalty config)", got.OverdueDays)
	}
	if got.LatePenaltyReferenceAmount != 0 {
		t.Errorf("LatePenaltyReferenceAmount = %d, want 0 (missing config must suppress reference)", got.LatePenaltyReferenceAmount)
	}
}

// Settlement bill must NEVER produce overdue or penalty context, even
// if all "would-be" conditions look ripe (FINALIZED, past day-5 of
// billing month, apartment has active LATE_PENALTY config). The v1
// lock says late-payment context applies only to monthly bills —
// settlement is its own workflow. This pins the boundary at the
// service tier so future refactors can't drift.
func TestGetByID_SettlementBillSuppressesBothHints(t *testing.T) {
	billID := uuid.New()
	aptID := uuid.New()

	billingMonth := time.Now().AddDate(0, -2, 0).Format("2006-01")
	bill := &Bill{
		ID:           billID,
		BillingMonth: billingMonth,
		BillType:     BillTypeSettlement, // <-- the only differentiator
		Status:       BillStatusFinalized,
		TotalAmount:  390000,
	}

	repo := &mockBillingRepo{
		findByIDWithRelationsFn: func(_ context.Context, _ uuid.UUID) (*BillWithRelations, error) {
			return &BillWithRelations{Bill: *bill, ApartmentID: aptID}, nil
		},
	}
	svc := newSvc(repo,
		&mockContractQuerier{},
		&mockMeterQuerier{},
		&mockConfigQuerier{configs: []billingconfig.BillingConfig{
			{FeeType: billingconfig.FeeTypeLatePenalty, DefaultAmount: 10000, IsActive: true},
		}},
		&mockMoveOutQuerier{},
	)

	got, err := svc.GetByID(context.Background(), billID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.OverdueDays != 0 {
		t.Errorf("settlement bill OverdueDays = %d, want 0 (no late-payment context for settlement)", got.OverdueDays)
	}
	if got.LatePenaltyReferenceAmount != 0 {
		t.Errorf("settlement bill LatePenaltyReferenceAmount = %d, want 0", got.LatePenaltyReferenceAmount)
	}
}
