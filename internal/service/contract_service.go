package service

import (
	"context"
	"fmt"
	"time"

	"nana/internal/domain"
	"nana/internal/dto"
	"nana/internal/repository"
	roomPkg "nana/internal/room"
	"nana/internal/shared/database"
	"nana/internal/shared/money"
	"nana/internal/shared/respond"
	"nana/internal/tenant"

	"github.com/google/uuid"
)

type ContractService interface {
	List(ctx context.Context, params dto.ContractListParams) ([]dto.ContractWithRelations, int64, error)
	GetByID(ctx context.Context, id uuid.UUID) (*dto.ContractWithRelations, error)
	Create(ctx context.Context, req dto.CreateContractRequest) (*dto.ContractWithRelations, error)
	Update(ctx context.Context, id uuid.UUID, req dto.UpdateContractRequest) (*dto.ContractWithRelations, error)
	Delete(ctx context.Context, id uuid.UUID) error
}

type contractService struct {
	contractRepo repository.ContractRepository
	roomRepo     roomPkg.RoomRepository
	tenantRepo   tenant.TenantRepository
	tx           database.TxManager
}

func NewContractService(
	contractRepo repository.ContractRepository,
	roomRepo roomPkg.RoomRepository,
	tenantRepo tenant.TenantRepository,
	tx database.TxManager,
) ContractService {
	return &contractService{
		contractRepo: contractRepo,
		roomRepo:     roomRepo,
		tenantRepo:   tenantRepo,
		tx:           tx,
	}
}

func (s *contractService) List(ctx context.Context, params dto.ContractListParams) ([]dto.ContractWithRelations, int64, error) {
	return s.contractRepo.FindAll(ctx, params)
}

func (s *contractService) GetByID(ctx context.Context, id uuid.UUID) (*dto.ContractWithRelations, error) {
	contract, err := s.contractRepo.FindByID(ctx, id)
	if err != nil {
		return nil, respond.ErrNotFound.WithMessage("ไม่พบสัญญา")
	}
	return contract, nil
}

func (s *contractService) Create(ctx context.Context, req dto.CreateContractRequest) (*dto.ContractWithRelations, error) {
	tenantID, err := uuid.Parse(req.TenantID)
	if err != nil {
		return nil, respond.ErrBadRequest.WithMessage("รหัสผู้เช่าไม่ถูกต้อง")
	}
	roomID, err := uuid.Parse(req.RoomID)
	if err != nil {
		return nil, respond.ErrBadRequest.WithMessage("รหัสห้องไม่ถูกต้อง")
	}

	// Verify tenant exists
	if _, err := s.tenantRepo.FindByID(ctx, tenantID); err != nil {
		return nil, respond.ErrNotFound.WithMessage("ไม่พบผู้เช่า")
	}

	// Verify room exists and is VACANT
	room, err := s.roomRepo.FindByID(ctx, roomID)
	if err != nil {
		return nil, respond.ErrNotFound.WithMessage("ไม่พบห้อง")
	}
	if room.Status != roomPkg.RoomStatusVacant {
		return nil, respond.ErrBadRequest.WithMessage("ห้องนี้ไม่ว่าง ไม่สามารถสร้างสัญญาได้")
	}

	// Check no active contract on this room
	hasActive, err := s.contractRepo.HasActiveByRoomID(ctx, roomID)
	if err != nil {
		return nil, fmt.Errorf("check active contract: %w", err)
	}
	if hasActive {
		return nil, respond.ErrConflict.WithMessage("ห้องนี้มีสัญญาที่ใช้งานอยู่แล้ว")
	}

	startDate, err := time.Parse("2006-01-02", req.StartDate)
	if err != nil {
		return nil, respond.ErrBadRequest.WithMessage("รูปแบบวันที่ไม่ถูกต้อง")
	}

	contract := domain.Contract{
		TenantID:               tenantID,
		RoomID:                 roomID,
		StartDate:              startDate,
		MinMonths:              req.MinMonths,
		MonthlyRent:            money.ToSatang(req.MonthlyRent),
		DepositAmount:          money.ToSatang(req.DepositAmount),
		DepositStatus:          domain.DepositStatusCollected,
		ElectricityRatePerUnit: money.ToSatang(req.ElectricityRatePerUnit),
		WaterRatePerUnit:       money.ToSatang(req.WaterRatePerUnit),
		Status:                 domain.ContractStatusActive,
	}

	// Transaction: create contract + update room status
	if err := s.tx.RunInTx(ctx, func(txCtx context.Context) error {
		if err := s.contractRepo.Create(txCtx, &contract); err != nil {
			return err
		}
		return s.roomRepo.UpdateStatus(txCtx, roomID, roomPkg.RoomStatusOccupied)
	}); err != nil {
		return nil, fmt.Errorf("create contract: %w", err)
	}

	result, err := s.contractRepo.FindByID(ctx, contract.ID)
	if err != nil {
		return nil, fmt.Errorf("fetch created contract: %w", err)
	}
	return result, nil
}

func (s *contractService) Update(ctx context.Context, id uuid.UUID, req dto.UpdateContractRequest) (*dto.ContractWithRelations, error) {
	contract, err := s.contractRepo.FindByIDSimple(ctx, id)
	if err != nil {
		return nil, respond.ErrNotFound.WithMessage("ไม่พบสัญญา")
	}
	if contract.Status != domain.ContractStatusActive {
		return nil, respond.ErrBadRequest.WithMessage("ไม่สามารถแก้ไขสัญญาที่ไม่ใช่สถานะใช้งาน")
	}

	if req.MinMonths != nil {
		contract.MinMonths = *req.MinMonths
	}
	if req.MonthlyRent != nil {
		contract.MonthlyRent = money.ToSatang(*req.MonthlyRent)
	}
	if req.DepositAmount != nil {
		contract.DepositAmount = money.ToSatang(*req.DepositAmount)
	}
	if req.ElectricityRatePerUnit != nil {
		contract.ElectricityRatePerUnit = money.ToSatang(*req.ElectricityRatePerUnit)
	}
	if req.WaterRatePerUnit != nil {
		contract.WaterRatePerUnit = money.ToSatang(*req.WaterRatePerUnit)
	}
	if req.MoveOutDate != nil {
		if *req.MoveOutDate == "" {
			contract.MoveOutDate = nil
		} else {
			t, err := time.Parse("2006-01-02", *req.MoveOutDate)
			if err != nil {
				return nil, respond.ErrBadRequest.WithMessage("รูปแบบวันที่แจ้งย้ายออกไม่ถูกต้อง")
			}
			contract.MoveOutDate = &t
		}
	}

	if err := s.contractRepo.Update(ctx, contract); err != nil {
		return nil, fmt.Errorf("update contract: %w", err)
	}

	result, err := s.contractRepo.FindByID(ctx, contract.ID)
	if err != nil {
		return nil, fmt.Errorf("fetch updated contract: %w", err)
	}
	return result, nil
}

func (s *contractService) Delete(ctx context.Context, id uuid.UUID) error {
	contract, err := s.contractRepo.FindByIDSimple(ctx, id)
	if err != nil {
		return respond.ErrNotFound.WithMessage("ไม่พบสัญญา")
	}
	if contract.Status == domain.ContractStatusActive {
		return respond.ErrBadRequest.WithMessage("ไม่สามารถลบสัญญาที่ยังใช้งานอยู่")
	}

	return s.contractRepo.Delete(ctx, id)
}
