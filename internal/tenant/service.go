package tenant

import (
	"context"
	"fmt"

	"nana/internal/shared/pagination"
	"nana/internal/shared/respond"

	"github.com/google/uuid"
)

type TenantService interface {
	List(ctx context.Context, params pagination.PaginationParams) ([]Tenant, int64, error)
	GetByID(ctx context.Context, id uuid.UUID) (*Tenant, error)
	Create(ctx context.Context, req CreateTenantRequest) (*Tenant, error)
	Update(ctx context.Context, id uuid.UUID, req UpdateTenantRequest) (*Tenant, error)
	Delete(ctx context.Context, id uuid.UUID) error
}

type tenantService struct {
	repo            TenantRepository
	contractChecker ContractChecker
}

func NewTenantService(repo TenantRepository, contractChecker ContractChecker) TenantService {
	return &tenantService{repo: repo, contractChecker: contractChecker}
}

func (s *tenantService) List(ctx context.Context, params pagination.PaginationParams) ([]Tenant, int64, error) {
	return s.repo.FindAll(ctx, params)
}

func (s *tenantService) GetByID(ctx context.Context, id uuid.UUID) (*Tenant, error) {
	tenant, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, respond.ErrNotFound.WithMessage("ไม่พบผู้เช่า")
	}
	return tenant, nil
}

func (s *tenantService) Create(ctx context.Context, req CreateTenantRequest) (*Tenant, error) {
	exists, err := s.repo.ExistsByIDCard(ctx, req.IDCard)
	if err != nil {
		return nil, fmt.Errorf("check id card: %w", err)
	}
	if exists {
		return nil, respond.ErrConflict.WithMessage("เลขบัตรประชาชนซ้ำ")
	}

	tenant := Tenant{
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

func (s *tenantService) Update(ctx context.Context, id uuid.UUID, req UpdateTenantRequest) (*Tenant, error) {
	tenant, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, respond.ErrNotFound.WithMessage("ไม่พบผู้เช่า")
	}

	if req.IDCard != nil && *req.IDCard != tenant.IDCard {
		exists, err := s.repo.ExistsByIDCardExcluding(ctx, *req.IDCard, id)
		if err != nil {
			return nil, fmt.Errorf("check id card: %w", err)
		}
		if exists {
			return nil, respond.ErrConflict.WithMessage("เลขบัตรประชาชนซ้ำ")
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
		return respond.ErrNotFound.WithMessage("ไม่พบผู้เช่า")
	}

	hasActive, err := s.contractChecker.HasActiveByTenantID(ctx, id)
	if err != nil {
		return fmt.Errorf("check active contract: %w", err)
	}
	if hasActive {
		return respond.ErrBadRequest.WithMessage("ไม่สามารถลบผู้เช่าที่มีสัญญาใช้งานอยู่")
	}

	return s.repo.Delete(ctx, id)
}
