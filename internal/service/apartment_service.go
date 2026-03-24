package service

import (
	"context"
	"fmt"

	"nana/internal/apperror"
	"nana/internal/domain"
	"nana/internal/dto"
	"nana/internal/money"
	"nana/internal/repository"

	"github.com/google/uuid"
)

type ApartmentService interface {
	List(ctx context.Context) ([]dto.ApartmentResponse, error)
	GetByID(ctx context.Context, id uuid.UUID) (*dto.ApartmentResponse, error)
	Create(ctx context.Context, req dto.CreateApartmentRequest) (*domain.Apartment, error)
	Update(ctx context.Context, id uuid.UUID, req dto.UpdateApartmentRequest) (*domain.Apartment, error)
}

type apartmentService struct {
	repo repository.ApartmentRepository
}

func NewApartmentService(repo repository.ApartmentRepository) ApartmentService {
	return &apartmentService{repo: repo}
}

func (s *apartmentService) List(ctx context.Context) ([]dto.ApartmentResponse, error) {
	apartments, err := s.repo.FindAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("find apartments: %w", err)
	}

	ids := make([]uuid.UUID, len(apartments))
	for i, a := range apartments {
		ids[i] = a.ID
	}

	stats, err := s.repo.GetRoomStatsByApartmentIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("get room stats: %w", err)
	}

	result := make([]dto.ApartmentResponse, len(apartments))
	for i, a := range apartments {
		r := dto.ToApartmentResponse(a)
		if s, ok := stats[a.ID]; ok {
			r.TotalRooms = s.Total
			r.VacantRooms = s.Vacant
			r.OccupiedRooms = s.Occupied
		}
		result[i] = r
	}

	return result, nil
}

func (s *apartmentService) GetByID(ctx context.Context, id uuid.UUID) (*dto.ApartmentResponse, error) {
	apt, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, apperror.ErrNotFound.WithMessage("ไม่พบอาคาร")
	}

	stats, err := s.repo.GetRoomStatsByApartmentIDs(ctx, []uuid.UUID{id})
	if err != nil {
		return nil, fmt.Errorf("get room stats: %w", err)
	}

	r := dto.ToApartmentResponse(*apt)
	if st, ok := stats[id]; ok {
		r.TotalRooms = st.Total
		r.VacantRooms = st.Vacant
		r.OccupiedRooms = st.Occupied
	}
	return &r, nil
}

func (s *apartmentService) Create(ctx context.Context, req dto.CreateApartmentRequest) (*domain.Apartment, error) {
	exists, err := s.repo.ExistsByName(ctx, req.Name)
	if err != nil {
		return nil, fmt.Errorf("check name: %w", err)
	}
	if exists {
		return nil, apperror.ErrConflict.WithMessage("ชื่ออาคารซ้ำ")
	}

	apt := domain.Apartment{
		Name:                   req.Name,
		DisplayOrder:           req.DisplayOrder,
		ElectricityRatePerUnit: money.ToSatang(req.ElectricityRatePerUnit),
		WaterRatePerUnit:       money.ToSatang(req.WaterRatePerUnit),
		Address:                req.Address,
		TaxID:                  req.TaxID,
	}

	if err := s.repo.Create(ctx, &apt); err != nil {
		return nil, fmt.Errorf("create apartment: %w", err)
	}

	return &apt, nil
}

func (s *apartmentService) Update(ctx context.Context, id uuid.UUID, req dto.UpdateApartmentRequest) (*domain.Apartment, error) {
	apt, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, apperror.ErrNotFound.WithMessage("ไม่พบอาคาร")
	}

	if req.Name != nil && *req.Name != apt.Name {
		exists, err := s.repo.ExistsByNameExcluding(ctx, *req.Name, id)
		if err != nil {
			return nil, fmt.Errorf("check name: %w", err)
		}
		if exists {
			return nil, apperror.ErrConflict.WithMessage("ชื่ออาคารซ้ำ")
		}
		apt.Name = *req.Name
	}
	if req.DisplayOrder != nil {
		apt.DisplayOrder = *req.DisplayOrder
	}
	if req.ElectricityRatePerUnit != nil {
		apt.ElectricityRatePerUnit = money.ToSatang(*req.ElectricityRatePerUnit)
	}
	if req.WaterRatePerUnit != nil {
		apt.WaterRatePerUnit = money.ToSatang(*req.WaterRatePerUnit)
	}
	if req.Address != nil {
		apt.Address = *req.Address
	}
	if req.TaxID != nil {
		apt.TaxID = *req.TaxID
	}

	if err := s.repo.Update(ctx, apt); err != nil {
		return nil, fmt.Errorf("update apartment: %w", err)
	}

	return apt, nil
}
