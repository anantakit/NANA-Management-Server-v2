package apartment

import (
	"context"
	"fmt"
	"strings"

	"nana/internal/shared/respond"

	"github.com/google/uuid"
)

type PaymentRoutingService interface {
	ListByApartment(ctx context.Context, apartmentID uuid.UUID) ([]PaymentDestinationRule, error)
	Create(ctx context.Context, apartmentID uuid.UUID, req CreatePaymentDestinationRuleRequest) (*PaymentDestinationRule, error)
	Update(ctx context.Context, id uuid.UUID, req UpdatePaymentDestinationRuleRequest) (*PaymentDestinationRule, error)
	Delete(ctx context.Context, id uuid.UUID) error
	// ResolveDestination resolves the payment destination for a room in an apartment.
	// Returns nil, nil when no rule matches — caller treats nil as "not configured".
	ResolveDestination(ctx context.Context, apartmentID uuid.UUID, roomNumber string) (*PaymentDestinationInfo, error)
}

type paymentRoutingService struct {
	ruleRepo PaymentDestinationRuleRepository
	bankRepo BankAccountRepository
	aptRepo  ApartmentRepository
}

func NewPaymentRoutingService(
	ruleRepo PaymentDestinationRuleRepository,
	bankRepo BankAccountRepository,
	aptRepo ApartmentRepository,
) PaymentRoutingService {
	return &paymentRoutingService{ruleRepo: ruleRepo, bankRepo: bankRepo, aptRepo: aptRepo}
}

var _ PaymentRoutingService = (*paymentRoutingService)(nil)

func (s *paymentRoutingService) ListByApartment(ctx context.Context, apartmentID uuid.UUID) ([]PaymentDestinationRule, error) {
	if _, err := s.aptRepo.FindByID(ctx, apartmentID); err != nil {
		return nil, respond.ErrNotFound.WithMessage("ไม่พบอาคาร")
	}
	return s.ruleRepo.FindByApartmentID(ctx, apartmentID)
}

func (s *paymentRoutingService) Create(ctx context.Context, apartmentID uuid.UUID, req CreatePaymentDestinationRuleRequest) (*PaymentDestinationRule, error) {
	if _, err := s.aptRepo.FindByID(ctx, apartmentID); err != nil {
		return nil, respond.ErrNotFound.WithMessage("ไม่พบอาคาร")
	}

	bankAccountID, err := uuid.Parse(req.BankAccountID)
	if err != nil {
		return nil, respond.ErrBadRequest.WithMessage("bank_account_id ไม่ถูกต้อง")
	}
	account, err := s.bankRepo.FindByID(ctx, bankAccountID)
	if err != nil {
		return nil, respond.ErrNotFound.WithMessage("ไม่พบบัญชีธนาคาร")
	}
	if account.ApartmentID != apartmentID {
		return nil, respond.ErrBadRequest.WithMessage("บัญชีธนาคารไม่ได้อยู่ในอาคารนี้")
	}

	rule := PaymentDestinationRule{
		ApartmentID:   apartmentID,
		BankAccountID: bankAccountID,
		RuleType:      PaymentDestinationRuleType(req.RuleType),
		RoomNumber:    req.RoomNumber,
		RangeStart:    req.RangeStart,
		RangeEnd:      req.RangeEnd,
	}

	if err := rule.ValidateRuleFields(); err != nil {
		return nil, respond.ErrBadRequest.WithMessage(err.Error())
	}

	if rule.RuleType == RuleTypeRoomRange {
		existing, err := s.ruleRepo.FindByApartmentID(ctx, apartmentID)
		if err != nil {
			return nil, fmt.Errorf("check range overlaps: %w", err)
		}
		for _, ex := range existing {
			if ex.RuleType != RuleTypeRoomRange || ex.RangeStart == nil || ex.RangeEnd == nil {
				continue
			}
			if RangesOverlap(*rule.RangeStart, *rule.RangeEnd, *ex.RangeStart, *ex.RangeEnd) {
				return nil, respond.ErrConflict.WithMessage(
					fmt.Sprintf("ช่วงห้องซ้อนทับกับกฎที่มีอยู่แล้ว (%s–%s)", *ex.RangeStart, *ex.RangeEnd),
				)
			}
		}
	}

	if err := s.ruleRepo.Create(ctx, &rule); err != nil {
		if isDuplicateKeyError(err) {
			return nil, respond.ErrConflict.WithMessage("กฎประเภทนี้มีอยู่แล้วสำหรับห้องนี้")
		}
		return nil, fmt.Errorf("create payment destination rule: %w", err)
	}
	return &rule, nil
}

func (s *paymentRoutingService) Update(ctx context.Context, id uuid.UUID, req UpdatePaymentDestinationRuleRequest) (*PaymentDestinationRule, error) {
	rule, err := s.ruleRepo.FindByID(ctx, id)
	if err != nil {
		return nil, respond.ErrNotFound.WithMessage("ไม่พบกฎการชำระเงิน")
	}

	if req.BankAccountID != nil {
		bankAccountID, err := uuid.Parse(*req.BankAccountID)
		if err != nil {
			return nil, respond.ErrBadRequest.WithMessage("bank_account_id ไม่ถูกต้อง")
		}
		account, err := s.bankRepo.FindByID(ctx, bankAccountID)
		if err != nil {
			return nil, respond.ErrNotFound.WithMessage("ไม่พบบัญชีธนาคาร")
		}
		if account.ApartmentID != rule.ApartmentID {
			return nil, respond.ErrBadRequest.WithMessage("บัญชีธนาคารไม่ได้อยู่ในอาคารนี้")
		}
		rule.BankAccountID = bankAccountID
	}

	if err := rule.ValidateRuleFields(); err != nil {
		return nil, respond.ErrBadRequest.WithMessage(err.Error())
	}

	if err := s.ruleRepo.Update(ctx, rule); err != nil {
		if isDuplicateKeyError(err) {
			return nil, respond.ErrConflict.WithMessage("กฎประเภทนี้มีอยู่แล้วสำหรับห้องนี้")
		}
		return nil, fmt.Errorf("update payment destination rule: %w", err)
	}
	return rule, nil
}

func (s *paymentRoutingService) Delete(ctx context.Context, id uuid.UUID) error {
	if _, err := s.ruleRepo.FindByID(ctx, id); err != nil {
		return respond.ErrNotFound.WithMessage("ไม่พบกฎการชำระเงิน")
	}
	return s.ruleRepo.Delete(ctx, id)
}

func (s *paymentRoutingService) ResolveDestination(ctx context.Context, apartmentID uuid.UUID, roomNumber string) (*PaymentDestinationInfo, error) {
	rules, err := s.ruleRepo.FindByApartmentID(ctx, apartmentID)
	if err != nil {
		return nil, fmt.Errorf("load routing rules: %w", err)
	}
	if len(rules) == 0 {
		return nil, nil
	}

	accounts, err := s.bankRepo.FindByApartmentID(ctx, apartmentID)
	if err != nil {
		return nil, fmt.Errorf("load bank accounts: %w", err)
	}
	accountMap := make(map[uuid.UUID]ApartmentBankAccount, len(accounts))
	for _, a := range accounts {
		accountMap[a.ID] = a
	}

	return ResolvePaymentDestination(rules, accountMap, roomNumber), nil
}

// isDuplicateKeyError detects Postgres unique constraint violations.
func isDuplicateKeyError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "duplicate key") || strings.Contains(msg, "unique constraint")
}
