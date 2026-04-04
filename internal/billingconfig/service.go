package billingconfig

import (
	"context"
	"fmt"

	"nana/internal/shared/money"
	"nana/internal/shared/respond"

	"github.com/google/uuid"
)

type BillingConfigService interface {
	ListByApartment(ctx context.Context, apartmentID uuid.UUID) ([]BillingConfig, error)
	Create(ctx context.Context, apartmentID uuid.UUID, req CreateBillingConfigRequest) (*BillingConfig, error)
	Update(ctx context.Context, id uuid.UUID, req UpdateBillingConfigRequest) (*BillingConfig, error)
	Delete(ctx context.Context, id uuid.UUID) error
}

type billingConfigService struct {
	repo      BillingConfigRepository
	apartments ApartmentChecker
}

var _ BillingConfigService = (*billingConfigService)(nil)

func NewBillingConfigService(repo BillingConfigRepository, apartments ApartmentChecker) BillingConfigService {
	return &billingConfigService{repo: repo, apartments: apartments}
}

func (s *billingConfigService) ListByApartment(ctx context.Context, apartmentID uuid.UUID) ([]BillingConfig, error) {
	if err := s.validateApartment(ctx, apartmentID); err != nil {
		return nil, err
	}
	return s.repo.FindByApartmentID(ctx, apartmentID)
}

func (s *billingConfigService) Create(ctx context.Context, apartmentID uuid.UUID, req CreateBillingConfigRequest) (*BillingConfig, error) {
	if err := s.validateApartment(ctx, apartmentID); err != nil {
		return nil, err
	}

	if !IsValidFeeType(req.FeeType) {
		return nil, respond.ErrBadRequest.WithMessage("ประเภทค่าธรรมเนียมไม่ถูกต้อง")
	}

	// Check duplicate
	existing, _ := s.repo.FindByApartmentAndFeeType(ctx, apartmentID, FeeType(req.FeeType))
	if existing != nil {
		return nil, respond.ErrConflict.WithMessage("มีการตั้งค่าประเภทนี้แล้ว")
	}

	isActive := true
	if req.IsActive != nil {
		isActive = *req.IsActive
	}

	config := BillingConfig{
		ApartmentID:   apartmentID,
		FeeType:       FeeType(req.FeeType),
		DefaultAmount: money.ToSatang(req.DefaultAmount),
		IsActive:      isActive,
	}

	if err := s.repo.Create(ctx, &config); err != nil {
		return nil, fmt.Errorf("create billing config: %w", err)
	}
	return &config, nil
}

func (s *billingConfigService) Update(ctx context.Context, id uuid.UUID, req UpdateBillingConfigRequest) (*BillingConfig, error) {
	config, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, respond.ErrNotFound.WithMessage("ไม่พบการตั้งค่า")
	}

	if req.DefaultAmount != nil {
		config.DefaultAmount = money.ToSatang(*req.DefaultAmount)
	}
	if req.IsActive != nil {
		config.IsActive = *req.IsActive
	}

	if err := s.repo.Update(ctx, config); err != nil {
		return nil, fmt.Errorf("update billing config: %w", err)
	}
	return config, nil
}

func (s *billingConfigService) Delete(ctx context.Context, id uuid.UUID) error {
	if _, err := s.repo.FindByID(ctx, id); err != nil {
		return respond.ErrNotFound.WithMessage("ไม่พบการตั้งค่า")
	}
	return s.repo.Delete(ctx, id)
}

func (s *billingConfigService) validateApartment(ctx context.Context, apartmentID uuid.UUID) error {
	exists, err := s.apartments.ExistsByID(ctx, apartmentID)
	if err != nil {
		return fmt.Errorf("check apartment: %w", err)
	}
	if !exists {
		return respond.ErrNotFound.WithMessage("ไม่พบอพาร์ทเมนท์")
	}
	return nil
}
