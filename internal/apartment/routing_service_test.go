package apartment

import (
	"context"
	"errors"
	"testing"

	"nana/internal/shared/respond"

	"github.com/google/uuid"
)

// --- minimal mocks (routing service only) ---

type stubAptRepo struct {
	apts map[uuid.UUID]*Apartment
}

func (s *stubAptRepo) FindAll(_ context.Context) ([]Apartment, error) { return nil, nil }
func (s *stubAptRepo) FindByID(_ context.Context, id uuid.UUID) (*Apartment, error) {
	if a, ok := s.apts[id]; ok {
		return a, nil
	}
	return nil, errors.New("not found")
}
func (s *stubAptRepo) Create(_ context.Context, _ *Apartment) error                              { return nil }
func (s *stubAptRepo) Update(_ context.Context, _ *Apartment) error                              { return nil }
func (s *stubAptRepo) ExistsByID(_ context.Context, _ uuid.UUID) (bool, error)                   { return true, nil }
func (s *stubAptRepo) ExistsByName(_ context.Context, _ string) (bool, error)                    { return false, nil }
func (s *stubAptRepo) ExistsByNameExcluding(_ context.Context, _ string, _ uuid.UUID) (bool, error) {
	return false, nil
}
func (s *stubAptRepo) GetRoomStatsByApartmentIDs(_ context.Context, _ []uuid.UUID) (map[uuid.UUID]RoomStats, error) {
	return nil, nil
}

var _ ApartmentRepository = (*stubAptRepo)(nil)

type stubBankRepo struct {
	accounts map[uuid.UUID]*ApartmentBankAccount
}

func (s *stubBankRepo) FindByApartmentID(_ context.Context, _ uuid.UUID) ([]ApartmentBankAccount, error) {
	return nil, nil
}
func (s *stubBankRepo) FindByID(_ context.Context, id uuid.UUID) (*ApartmentBankAccount, error) {
	if a, ok := s.accounts[id]; ok {
		return a, nil
	}
	return nil, errors.New("not found")
}
func (s *stubBankRepo) Create(_ context.Context, _ *ApartmentBankAccount) error    { return nil }
func (s *stubBankRepo) Update(_ context.Context, _ *ApartmentBankAccount) error    { return nil }
func (s *stubBankRepo) Delete(_ context.Context, _ uuid.UUID) error                { return nil }
func (s *stubBankRepo) ClearPrimary(_ context.Context, _ uuid.UUID) error          { return nil }

var _ BankAccountRepository = (*stubBankRepo)(nil)

type stubRuleRepo struct {
	createCalled bool
	updateCalled bool
}

func (s *stubRuleRepo) FindByApartmentID(_ context.Context, _ uuid.UUID) ([]PaymentDestinationRule, error) {
	return nil, nil
}
func (s *stubRuleRepo) FindByID(_ context.Context, id uuid.UUID) (*PaymentDestinationRule, error) {
	return &PaymentDestinationRule{ID: id, ApartmentID: uuid.Nil}, nil
}
func (s *stubRuleRepo) Create(_ context.Context, _ *PaymentDestinationRule) error {
	s.createCalled = true
	return nil
}
func (s *stubRuleRepo) Update(_ context.Context, _ *PaymentDestinationRule) error {
	s.updateCalled = true
	return nil
}
func (s *stubRuleRepo) Delete(_ context.Context, _ uuid.UUID) error { return nil }

var _ PaymentDestinationRuleRepository = (*stubRuleRepo)(nil)

// newTestRoutingService wires a routing service with the given bank repo and a
// stub apt repo that recognises all provided apartmentIDs as valid.
func newTestRoutingService(aptIDs []uuid.UUID, bankRepo BankAccountRepository) PaymentRoutingService {
	apts := make(map[uuid.UUID]*Apartment, len(aptIDs))
	for _, id := range aptIDs {
		apts[id] = &Apartment{ID: id}
	}
	return NewPaymentRoutingService(&stubRuleRepo{}, bankRepo, &stubAptRepo{apts: apts})
}

// ============================================================
// Group 1 — Rule ownership: cross-apartment account rejection
// ============================================================

// TestRoutingService_Create_CrossApartmentAccount_Rejected is the primary
// integration-style regression test for the cross-apartment bank account bug.
//
// Scenario: Apartment A and Apartment B exist; Bank Account X belongs to B.
// Operator tries to create a routing rule for A that points at X.
// Must fail with 400 — no cross-apartment association allowed.
func TestRoutingService_Create_CrossApartmentAccount_Rejected(t *testing.T) {
	aptA := uuid.New()
	aptB := uuid.New()
	bankX := uuid.New()

	bankRepo := &stubBankRepo{
		accounts: map[uuid.UUID]*ApartmentBankAccount{
			bankX: {ID: bankX, ApartmentID: aptB}, // belongs to B, not A
		},
	}
	svc := newTestRoutingService([]uuid.UUID{aptA, aptB}, bankRepo)

	_, err := svc.Create(context.Background(), aptA, CreatePaymentDestinationRuleRequest{
		BankAccountID: bankX.String(),
		RuleType:      string(RuleTypeApartmentDefault),
	})
	if err == nil {
		t.Fatal("expected error when creating rule with cross-apartment bank account, got nil")
	}
	appErr, ok := respond.Is(err)
	if !ok {
		t.Fatalf("expected AppError, got %T: %v", err, err)
	}
	if appErr.HTTPStatus != 400 {
		t.Errorf("expected 400, got %d", appErr.HTTPStatus)
	}
}

// TestRoutingService_Create_SameApartmentAccount_OK verifies the happy path:
// bank account belongs to the same apartment as the rule.
func TestRoutingService_Create_SameApartmentAccount_OK(t *testing.T) {
	aptA := uuid.New()
	bankA := uuid.New()

	bankRepo := &stubBankRepo{
		accounts: map[uuid.UUID]*ApartmentBankAccount{
			bankA: {ID: bankA, ApartmentID: aptA}, // same apartment
		},
	}
	svc := newTestRoutingService([]uuid.UUID{aptA}, bankRepo)

	rule, err := svc.Create(context.Background(), aptA, CreatePaymentDestinationRuleRequest{
		BankAccountID: bankA.String(),
		RuleType:      string(RuleTypeApartmentDefault),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rule.BankAccountID != bankA {
		t.Errorf("rule.BankAccountID = %v, want %v", rule.BankAccountID, bankA)
	}
}

// TestRoutingService_Update_CrossApartmentAccount_Rejected verifies that
// updating a rule to point at a different apartment's bank account is rejected.
func TestRoutingService_Update_CrossApartmentAccount_Rejected(t *testing.T) {
	aptA := uuid.New()
	aptB := uuid.New()
	bankB := uuid.New()
	ruleID := uuid.New()

	bankRepo := &stubBankRepo{
		accounts: map[uuid.UUID]*ApartmentBankAccount{
			bankB: {ID: bankB, ApartmentID: aptB},
		},
	}

	// Rule repo stub returns a rule that already belongs to aptA.
	ruleRepo := &stubRuleRepoWithAptID{aptID: aptA, ruleID: ruleID}
	apts := map[uuid.UUID]*Apartment{aptA: {ID: aptA}, aptB: {ID: aptB}}
	svc := NewPaymentRoutingService(ruleRepo, bankRepo, &stubAptRepo{apts: apts})

	bankBStr := bankB.String()
	_, err := svc.Update(context.Background(), ruleID, UpdatePaymentDestinationRuleRequest{
		BankAccountID: &bankBStr,
	})
	if err == nil {
		t.Fatal("expected error when updating rule to cross-apartment bank account, got nil")
	}
	appErr, ok := respond.Is(err)
	if !ok {
		t.Fatalf("expected AppError, got %T: %v", err, err)
	}
	if appErr.HTTPStatus != 400 {
		t.Errorf("expected 400, got %d", appErr.HTTPStatus)
	}
}

// TestRoutingService_Update_SameApartmentAccount_OK verifies the happy-path update.
func TestRoutingService_Update_SameApartmentAccount_OK(t *testing.T) {
	aptA := uuid.New()
	bankA2 := uuid.New()
	ruleID := uuid.New()

	bankRepo := &stubBankRepo{
		accounts: map[uuid.UUID]*ApartmentBankAccount{
			bankA2: {ID: bankA2, ApartmentID: aptA}, // same apartment, different account
		},
	}
	ruleRepo := &stubRuleRepoWithAptID{aptID: aptA, ruleID: ruleID}
	apts := map[uuid.UUID]*Apartment{aptA: {ID: aptA}}
	svc := NewPaymentRoutingService(ruleRepo, bankRepo, &stubAptRepo{apts: apts})

	bankA2Str := bankA2.String()
	rule, err := svc.Update(context.Background(), ruleID, UpdatePaymentDestinationRuleRequest{
		BankAccountID: &bankA2Str,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rule.BankAccountID != bankA2 {
		t.Errorf("rule.BankAccountID = %v, want %v", rule.BankAccountID, bankA2)
	}
}

// stubRuleRepoWithAptID is like stubRuleRepo but FindByID returns a rule with
// the specified ApartmentID so the Update ownership check has a reference point.
type stubRuleRepoWithAptID struct {
	aptID  uuid.UUID
	ruleID uuid.UUID
	stubRuleRepo
}

func (s *stubRuleRepoWithAptID) FindByID(_ context.Context, id uuid.UUID) (*PaymentDestinationRule, error) {
	return &PaymentDestinationRule{ID: id, ApartmentID: s.aptID, RuleType: RuleTypeApartmentDefault}, nil
}
func (s *stubRuleRepoWithAptID) Update(_ context.Context, _ *PaymentDestinationRule) error {
	return nil
}
