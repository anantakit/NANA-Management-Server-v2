package service

import (
	"context"
	"fmt"

	"nana/internal/apperror"
	"nana/internal/domain"
	"nana/internal/dto"
	"nana/internal/repository"

	"github.com/google/uuid"
)

type TenantService interface {
	List(ctx context.Context, params dto.PaginationParams) ([]domain.Tenant, int64, error)
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Tenant, error)
	Create(ctx context.Context, req dto.CreateTenantRequest) (*domain.Tenant, error)
	Update(ctx context.Context, id uuid.UUID, req dto.UpdateTenantRequest) (*domain.Tenant, error)
	Delete(ctx context.Context, id uuid.UUID) error
}

type tenantService struct {
	repo         repository.TenantRepository
	contractRepo repository.ContractRepository
}

func NewTenantService(repo repository.TenantRepository, contractRepo repository.ContractRepository) TenantService {
	return &tenantService{repo: repo, contractRepo: contractRepo}
}

func (s *tenantService) List(ctx context.Context, params dto.PaginationParams) ([]domain.Tenant, int64, error) {
	return s.repo.FindAll(ctx, params)
}

func (s *tenantService) GetByID(ctx context.Context, id uuid.UUID) (*domain.Tenant, error) {
	tenant, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, apperror.ErrNotFound.WithMessage("ไม่พบผู้เช่า")
	}
	return tenant, nil
}

func (s *tenantService) Create(ctx context.Context, req dto.CreateTenantRequest) (*domain.Tenant, error) {
	exists, err := s.repo.ExistsByIDCard(ctx, req.IDCard)
	if err != nil {
		return nil, fmt.Errorf("check id card: %w", err)
	}
	if exists {
		return nil, apperror.ErrConflict.WithMessage("เลขบัตรประชาชนซ้ำ")
	}

	tenant := domain.Tenant{
		FullName:         req.FullName,
		IDCard:           req.IDCard,
		Phone:            req.Phone,
		Address:          req.Address,
		EmergencyContact: req.EmergencyContact,
	}

	if err := s.repo.Create(ctx, &tenant); err != nil {
		return nil, fmt.Errorf("create tenant: %w", err)
	}

	return &tenant, nil
}

func (s *tenantService) Update(ctx context.Context, id uuid.UUID, req dto.UpdateTenantRequest) (*domain.Tenant, error) {
	tenant, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, apperror.ErrNotFound.WithMessage("ไม่พบผู้เช่า")
	}

	if req.IDCard != nil && *req.IDCard != tenant.IDCard {
		exists, err := s.repo.ExistsByIDCardExcluding(ctx, *req.IDCard, id)
		if err != nil {
			return nil, fmt.Errorf("check id card: %w", err)
		}
		if exists {
			return nil, apperror.ErrConflict.WithMessage("เลขบัตรประชาชนซ้ำ")
		}
		tenant.IDCard = *req.IDCard
	}
	if req.FullName != nil {
		tenant.FullName = *req.FullName
	}
	if req.Phone != nil {
		tenant.Phone = *req.Phone
	}
	if req.Address != nil {
		tenant.Address = *req.Address
	}
	if req.EmergencyContact != nil {
		tenant.EmergencyContact = *req.EmergencyContact
	}
	if err := s.repo.Update(ctx, tenant); err != nil {
		return nil, fmt.Errorf("update tenant: %w", err)
	}

	return tenant, nil
}

func (s *tenantService) Delete(ctx context.Context, id uuid.UUID) error {
	_, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return apperror.ErrNotFound.WithMessage("ไม่พบผู้เช่า")
	}

	hasActive, err := s.contractRepo.HasActiveByTenantID(ctx, id)
	if err != nil {
		return fmt.Errorf("check active contract: %w", err)
	}
	if hasActive {
		return apperror.ErrBadRequest.WithMessage("ไม่สามารถลบผู้เช่าที่มีสัญญาใช้งานอยู่")
	}

	return s.repo.Delete(ctx, id)
}
