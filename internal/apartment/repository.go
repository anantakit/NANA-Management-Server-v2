package apartment

import (
	"context"

	"nana/internal/shared/database"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type RoomStats struct {
	ApartmentID uuid.UUID
	Total       int
	Vacant      int
	Occupied    int
}

type ApartmentRepository interface {
	FindAll(ctx context.Context) ([]Apartment, error)
	FindByID(ctx context.Context, id uuid.UUID) (*Apartment, error)
	Create(ctx context.Context, apartment *Apartment) error
	Update(ctx context.Context, apartment *Apartment) error
	ExistsByName(ctx context.Context, name string) (bool, error)
	ExistsByNameExcluding(ctx context.Context, name string, excludeID uuid.UUID) (bool, error)
	GetRoomStatsByApartmentIDs(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]RoomStats, error)
}

type apartmentRepository struct {
	db *gorm.DB
}

func NewApartmentRepository(db *gorm.DB) ApartmentRepository {
	return &apartmentRepository{db: db}
}

func (r *apartmentRepository) FindAll(ctx context.Context) ([]Apartment, error) {
	var result []Apartment
	if err := database.DB(ctx, r.db).Order("display_order ASC, name ASC").Find(&result).Error; err != nil {
		return nil, err
	}
	return result, nil
}

func (r *apartmentRepository) FindByID(ctx context.Context, id uuid.UUID) (*Apartment, error) {
	var m Apartment
	if err := database.DB(ctx, r.db).Where("id = ?", id).First(&m).Error; err != nil {
		return nil, err
	}
	return &m, nil
}

func (r *apartmentRepository) Create(ctx context.Context, apartment *Apartment) error {
	return database.DB(ctx, r.db).Create(apartment).Error
}

func (r *apartmentRepository) Update(ctx context.Context, apartment *Apartment) error {
	return database.DB(ctx, r.db).Model(apartment).Select("*").Omit("deleted_at").Updates(apartment).Error
}

func (r *apartmentRepository) ExistsByName(ctx context.Context, name string) (bool, error) {
	var count int64
	err := database.DB(ctx, r.db).Model(&Apartment{}).Where("name = ?", name).Count(&count).Error
	return count > 0, err
}

func (r *apartmentRepository) ExistsByNameExcluding(ctx context.Context, name string, excludeID uuid.UUID) (bool, error) {
	var count int64
	err := database.DB(ctx, r.db).Model(&Apartment{}).Where("name = ? AND id != ?", name, excludeID).Count(&count).Error
	return count > 0, err
}

func (r *apartmentRepository) GetRoomStatsByApartmentIDs(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]RoomStats, error) {
	if len(ids) == 0 {
		return map[uuid.UUID]RoomStats{}, nil
	}

	type row struct {
		ApartmentID uuid.UUID `gorm:"column:apartment_id"`
		Status      string    `gorm:"column:status"`
		Count       int       `gorm:"column:count"`
	}

	var rows []row
	err := database.DB(ctx, r.db).
		Table("rooms").
		Select("apartment_id, status, COUNT(*) as count").
		Where("apartment_id IN ? AND deleted_at IS NULL", ids).
		Group("apartment_id, status").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	stats := make(map[uuid.UUID]RoomStats, len(ids))
	for _, r := range rows {
		s := stats[r.ApartmentID]
		s.ApartmentID = r.ApartmentID
		s.Total += r.Count
		switch r.Status {
		case "VACANT":
			s.Vacant = r.Count
		case "OCCUPIED":
			s.Occupied = r.Count
		}
		stats[r.ApartmentID] = s
	}

	return stats, nil
}
