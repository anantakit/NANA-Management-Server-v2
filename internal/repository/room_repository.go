package repository

import (
	"context"
	"fmt"

	"nana/internal/domain"
	"nana/internal/dto"
	"nana/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type RoomRepository interface {
	FindByApartmentID(ctx context.Context, apartmentID uuid.UUID, params dto.PaginationParams) ([]domain.Room, int64, error)
	FindByID(ctx context.Context, id uuid.UUID) (*domain.Room, error)
	Create(ctx context.Context, room *domain.Room) error
	Update(ctx context.Context, room *domain.Room) error
	Delete(ctx context.Context, id uuid.UUID) error
	ExistsByNumber(ctx context.Context, apartmentID uuid.UUID, number string) (bool, error)
	ExistsByNumberExcluding(ctx context.Context, apartmentID uuid.UUID, number string, excludeID uuid.UUID) (bool, error)
}

type roomRepository struct {
	db *gorm.DB
}

func NewRoomRepository(db *gorm.DB) RoomRepository {
	return &roomRepository{db: db}
}

func (r *roomRepository) FindByApartmentID(ctx context.Context, apartmentID uuid.UUID, params dto.PaginationParams) ([]domain.Room, int64, error) {
	var total int64
	query := r.db.WithContext(ctx).Model(&model.Room{}).Where("apartment_id = ?", apartmentID)

	if params.Search != "" {
		query = query.Where("number ILIKE ?", "%"+params.Search+"%")
	}

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	col, order := dto.SafeSort(params.Sort, params.Order, []string{"number", "floor", "type", "status", "base_rent", "created_at"}, "number")
	if params.Sort == "" {
		order = "asc"
	}
	orderClause := fmt.Sprintf("%s %s", col, order)

	var models []model.Room
	if err := query.Order(orderClause).Offset(params.Offset()).Limit(params.Limit).Find(&models).Error; err != nil {
		return nil, 0, err
	}

	result := make([]domain.Room, len(models))
	for i, m := range models {
		result[i] = m.ToDomain()
	}
	return result, total, nil
}

func (r *roomRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.Room, error) {
	var m model.Room
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&m).Error; err != nil {
		return nil, err
	}
	d := m.ToDomain()
	return &d, nil
}

func (r *roomRepository) Create(ctx context.Context, room *domain.Room) error {
	m := model.RoomFromDomain(*room)
	if err := r.db.WithContext(ctx).Create(&m).Error; err != nil {
		return err
	}
	*room = m.ToDomain()
	return nil
}

func (r *roomRepository) Update(ctx context.Context, room *domain.Room) error {
	m := model.RoomFromDomain(*room)
	if err := r.db.WithContext(ctx).Model(&m).Select("*").Omit("deleted_at").Updates(&m).Error; err != nil {
		return err
	}
	*room = m.ToDomain()
	return nil
}

func (r *roomRepository) Delete(ctx context.Context, id uuid.UUID) error {
	return r.db.WithContext(ctx).Delete(&model.Room{}, "id = ?", id).Error
}

func (r *roomRepository) ExistsByNumber(ctx context.Context, apartmentID uuid.UUID, number string) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&model.Room{}).Where("apartment_id = ? AND number = ?", apartmentID, number).Count(&count).Error
	return count > 0, err
}

func (r *roomRepository) ExistsByNumberExcluding(ctx context.Context, apartmentID uuid.UUID, number string, excludeID uuid.UUID) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&model.Room{}).Where("apartment_id = ? AND number = ? AND id != ?", apartmentID, number, excludeID).Count(&count).Error
	return count > 0, err
}
