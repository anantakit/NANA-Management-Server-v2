package room

import (
	"context"
	"fmt"

	"nana/internal/shared/money"
	"nana/internal/shared/pagination"
	"nana/internal/shared/respond"

	"github.com/google/uuid"
)

type RoomService interface {
	ListByApartment(ctx context.Context, apartmentID uuid.UUID, params pagination.PaginationParams) ([]RoomWithContract, int64, error)
	GetByID(ctx context.Context, id uuid.UUID) (*RoomWithContract, error)
	Create(ctx context.Context, apartmentID uuid.UUID, req CreateRoomRequest) (*Room, error)
	Update(ctx context.Context, id uuid.UUID, req UpdateRoomRequest) (*Room, error)
	Delete(ctx context.Context, id uuid.UUID) error
}

type roomService struct {
	repo    RoomRepository
	aptRepo ApartmentQuerier
}

func NewRoomService(repo RoomRepository, aptRepo ApartmentQuerier) RoomService {
	return &roomService{repo: repo, aptRepo: aptRepo}
}

func (s *roomService) ListByApartment(ctx context.Context, apartmentID uuid.UUID, params pagination.PaginationParams) ([]RoomWithContract, int64, error) {
	if _, err := s.aptRepo.FindByID(ctx, apartmentID); err != nil {
		return nil, 0, respond.ErrNotFound.WithMessage("ไม่พบอาคาร")
	}
	return s.repo.FindByApartmentIDWithContracts(ctx, apartmentID, params)
}

func (s *roomService) GetByID(ctx context.Context, id uuid.UUID) (*RoomWithContract, error) {
	room, err := s.repo.FindByIDWithContract(ctx, id)
	if err != nil {
		return nil, respond.ErrNotFound.WithMessage("ไม่พบห้อง")
	}
	return room, nil
}

func (s *roomService) Create(ctx context.Context, apartmentID uuid.UUID, req CreateRoomRequest) (*Room, error) {
	if _, err := s.aptRepo.FindByID(ctx, apartmentID); err != nil {
		return nil, respond.ErrNotFound.WithMessage("ไม่พบอาคาร")
	}

	exists, err := s.repo.ExistsByNumber(ctx, apartmentID, req.Number)
	if err != nil {
		return nil, fmt.Errorf("check room number: %w", err)
	}
	if exists {
		return nil, respond.ErrConflict.WithMessage("เลขห้องซ้ำ")
	}

	room := Room{
		ApartmentID: apartmentID,
		Number:      req.Number,
		Type:        RoomType(req.Type),
		Floor:       req.Floor,
		BaseRent:    money.ToSatang(req.BaseRent),
		BaseDeposit: money.ToSatang(req.BaseDeposit),
		Status:      RoomStatusVacant,
	}

	if err := s.repo.Create(ctx, &room); err != nil {
		return nil, fmt.Errorf("create room: %w", err)
	}

	return &room, nil
}

func (s *roomService) Update(ctx context.Context, id uuid.UUID, req UpdateRoomRequest) (*Room, error) {
	room, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, respond.ErrNotFound.WithMessage("ไม่พบห้อง")
	}

	if req.Number != nil && *req.Number != room.Number {
		exists, err := s.repo.ExistsByNumberExcluding(ctx, room.ApartmentID, *req.Number, id)
		if err != nil {
			return nil, fmt.Errorf("check room number: %w", err)
		}
		if exists {
			return nil, respond.ErrConflict.WithMessage("เลขห้องซ้ำ")
		}
		room.Number = *req.Number
	}
	if req.Type != nil {
		room.Type = RoomType(*req.Type)
	}
	if req.Floor != nil {
		room.Floor = *req.Floor
	}
	if req.BaseRent != nil {
		room.BaseRent = money.ToSatang(*req.BaseRent)
	}
	if req.BaseDeposit != nil {
		room.BaseDeposit = money.ToSatang(*req.BaseDeposit)
	}
	if req.Status != nil {
		// Only allow VACANT and MAINTENANCE from manual update
		// OCCUPIED is set automatically by contract creation
		if err := room.ValidateStatusChange(); err != nil {
			return nil, respond.ErrBadRequest.WithMessage(err.Error())
		}
		room.Status = RoomStatus(*req.Status)
	}

	if err := s.repo.Update(ctx, room); err != nil {
		return nil, fmt.Errorf("update room: %w", err)
	}

	return room, nil
}

func (s *roomService) Delete(ctx context.Context, id uuid.UUID) error {
	room, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return respond.ErrNotFound.WithMessage("ไม่พบห้อง")
	}

	if err := room.CanBeDeleted(); err != nil {
		return respond.ErrBadRequest.WithMessage(err.Error())
	}

	return s.repo.Delete(ctx, id)
}
