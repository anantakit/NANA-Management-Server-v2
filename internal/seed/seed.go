package seed

import (
	"fmt"
	"log/slog"

	"nana/internal/domain"
	"nana/internal/model"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

type apartmentSeed struct {
	Name                   string
	DisplayOrder           int
	ElectricityRatePerUnit int64
	WaterRatePerUnit       int64
}

type roomSeed struct {
	ApartmentName string
	Prefix        string
	Start         int
	End           int
	RoomType      domain.RoomType
	Floor         int
	BaseRent      int64
}

func Run(db *gorm.DB) error {
	if err := seedApartments(db); err != nil {
		return fmt.Errorf("seed apartments: %w", err)
	}
	if err := seedRooms(db); err != nil {
		return fmt.Errorf("seed rooms: %w", err)
	}
	if err := seedAdminUser(db); err != nil {
		return fmt.Errorf("seed admin user: %w", err)
	}
	slog.Info("seed data completed")
	return nil
}

func seedApartments(db *gorm.DB) error {
	apartments := []apartmentSeed{
		{Name: "นานาคอร์ท", DisplayOrder: 1, ElectricityRatePerUnit: 800, WaterRatePerUnit: 1800},
		{Name: "นานาเพลส", DisplayOrder: 2, ElectricityRatePerUnit: 800, WaterRatePerUnit: 1800},
		{Name: "นานาแมนชั่น", DisplayOrder: 3, ElectricityRatePerUnit: 800, WaterRatePerUnit: 1800},
		{Name: "อีซี่เพลส", DisplayOrder: 4, ElectricityRatePerUnit: 800, WaterRatePerUnit: 1800},
	}

	for _, a := range apartments {
		var count int64
		if err := db.Model(&model.Apartment{}).Where("name = ?", a.Name).Count(&count).Error; err != nil {
			return fmt.Errorf("check apartment %s: %w", a.Name, err)
		}
		if count > 0 {
			continue
		}

		apt := model.Apartment{
			Name:                   a.Name,
			DisplayOrder:           a.DisplayOrder,
			ElectricityRatePerUnit: a.ElectricityRatePerUnit,
			WaterRatePerUnit:       a.WaterRatePerUnit,
		}
		if err := db.Create(&apt).Error; err != nil {
			return fmt.Errorf("create apartment %s: %w", a.Name, err)
		}
		slog.Info("seeded apartment", "name", a.Name)
	}

	return nil
}

func seedRooms(db *gorm.DB) error {
	rooms := []roomSeed{
		// นานาคอร์ท
		{ApartmentName: "นานาคอร์ท", Prefix: "A", Start: 101, End: 132, RoomType: domain.RoomTypeAir, Floor: 1, BaseRent: 300000},
		{ApartmentName: "นานาคอร์ท", Prefix: "B", Start: 201, End: 210, RoomType: domain.RoomTypeAir, Floor: 2, BaseRent: 300000},
		{ApartmentName: "นานาคอร์ท", Prefix: "C", Start: 301, End: 307, RoomType: domain.RoomTypeAir, Floor: 3, BaseRent: 300000},
		{ApartmentName: "นานาคอร์ท", Prefix: "D", Start: 401, End: 422, RoomType: domain.RoomTypeFan, Floor: 4, BaseRent: 250000},
		{ApartmentName: "นานาคอร์ท", Prefix: "E", Start: 501, End: 508, RoomType: domain.RoomTypeFan, Floor: 5, BaseRent: 250000},
		// นานาแมนชั่น
		{ApartmentName: "นานาแมนชั่น", Prefix: "", Start: 101, End: 156, RoomType: domain.RoomTypeAir, Floor: 0, BaseRent: 350000},
		// นานาเพลส
		{ApartmentName: "นานาเพลส", Prefix: "", Start: 1001, End: 1045, RoomType: domain.RoomTypeAir, Floor: 0, BaseRent: 350000},
	}

	for _, rs := range rooms {
		var apt model.Apartment
		if err := db.Where("name = ?", rs.ApartmentName).First(&apt).Error; err != nil {
			slog.Warn("apartment not found for room seed", "apartment", rs.ApartmentName)
			continue
		}

		for i := rs.Start; i <= rs.End; i++ {
			number := fmt.Sprintf("%s%d", rs.Prefix, i)

			var count int64
			if err := db.Model(&model.Room{}).Where("apartment_id = ? AND number = ?", apt.ID, number).Count(&count).Error; err != nil {
				return fmt.Errorf("check room %s: %w", number, err)
			}
			if count > 0 {
				continue
			}

			floor := rs.Floor
			if floor == 0 {
				floor = deriveFloor(i)
			}

			room := model.Room{
				ApartmentID: apt.ID,
				Number:      number,
				Type:        string(rs.RoomType),
				Floor:       floor,
				BaseRent:    rs.BaseRent,
				BaseDeposit: rs.BaseRent,
				Status:      string(domain.RoomStatusVacant),
			}
			if err := db.Create(&room).Error; err != nil {
				return fmt.Errorf("create room %s: %w", number, err)
			}
		}
		slog.Info("seeded rooms", "apartment", rs.ApartmentName, "prefix", rs.Prefix, "range", fmt.Sprintf("%d-%d", rs.Start, rs.End))
	}

	return nil
}

func deriveFloor(roomNumber int) int {
	return roomNumber / 100
}

func seedAdminUser(db *gorm.DB) error {
	var count int64
	if err := db.Model(&model.User{}).Where("username = ?", "admin").Count(&count).Error; err != nil {
		return fmt.Errorf("check admin user: %w", err)
	}
	if count > 0 {
		return nil
	}

	hash, err := bcrypt.GenerateFromPassword([]byte("admin123"), 12)
	if err != nil {
		return fmt.Errorf("hash admin password: %w", err)
	}

	user := model.User{
		Username:           "admin",
		PasswordHash:       string(hash),
		FullName:           "ผู้ดูแลระบบ",
		Role:               string(domain.UserRoleAdmin),
		MustChangePassword: true,
	}

	if err := db.Create(&user).Error; err != nil {
		return fmt.Errorf("create admin user: %w", err)
	}

	slog.Info("seeded admin user", "username", "admin")
	return nil
}
