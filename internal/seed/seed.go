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
	Address                string
	TaxID                  string
}

type roomSeed struct {
	Number      string
	Type        domain.RoomType
	Floor       int
	BaseRent    int64
	BaseDeposit int64
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
		{Name: "นานาคอร์ท", DisplayOrder: 1, ElectricityRatePerUnit: 800, WaterRatePerUnit: 1800, Address: "682 ม.1 Sripatana Rd", TaxID: "0105558123456"},
		{Name: "นานาเพลส", DisplayOrder: 2, ElectricityRatePerUnit: 800, WaterRatePerUnit: 1800, Address: "123 Sripatana Rd"},
		{Name: "นานาแมนชั่น", DisplayOrder: 3, ElectricityRatePerUnit: 800, WaterRatePerUnit: 1800, Address: "888", TaxID: "0105559987654"},
		{Name: "อีซี่เพลส", DisplayOrder: 4, ElectricityRatePerUnit: 800, WaterRatePerUnit: 1800, Address: "888", TaxID: "0105559987654"},
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
			Address:                a.Address,
			TaxID:                  a.TaxID,
		}
		if err := db.Create(&apt).Error; err != nil {
			return fmt.Errorf("create apartment %s: %w", a.Name, err)
		}
		slog.Info("seeded apartment", "name", a.Name)
	}

	return nil
}

func seedRooms(db *gorm.DB) error {
	roomsByApartment := map[string][]roomSeed{
		"นานาคอร์ท": nanaCourt(),
		"นานาเพลส":  nanaPlace(),
		"นานาแมนชั่น": nanaMansion(),
		// อีซี่เพลส — ยังไม่มีห้อง
	}

	for aptName, rooms := range roomsByApartment {
		var apt model.Apartment
		if err := db.Where("name = ?", aptName).First(&apt).Error; err != nil {
			slog.Warn("apartment not found for room seed", "apartment", aptName)
			continue
		}

		created := 0
		for _, rs := range rooms {
			var count int64
			if err := db.Model(&model.Room{}).Where("apartment_id = ? AND number = ?", apt.ID, rs.Number).Count(&count).Error; err != nil {
				return fmt.Errorf("check room %s: %w", rs.Number, err)
			}
			if count > 0 {
				continue
			}

			room := model.Room{
				ApartmentID: apt.ID,
				Number:      rs.Number,
				Type:        string(rs.Type),
				Floor:       rs.Floor,
				BaseRent:    rs.BaseRent,
				BaseDeposit: rs.BaseDeposit,
				Status:      string(domain.RoomStatusVacant),
			}
			if err := db.Create(&room).Error; err != nil {
				return fmt.Errorf("create room %s: %w", rs.Number, err)
			}
			created++
		}
		if created > 0 {
			slog.Info("seeded rooms", "apartment", aptName, "count", created)
		}
	}

	return nil
}

// nanaCourt — นานาคอร์ท (71 rooms)
func nanaCourt() []roomSeed {
	var rooms []roomSeed

	// A101-A111: air, rent=3000, deposit=3000 (floor 1)
	rooms = append(rooms, rangeRooms("A", 101, 111, domain.RoomTypeAir, 1, 300000, 300000)...)
	// A201-A211: fan, rent=2500, deposit=2000 (floor 2)
	rooms = append(rooms, rangeRooms("A", 201, 211, domain.RoomTypeFan, 2, 250000, 200000)...)
	// B101-B105: air, rent=3000, deposit=3000 (floor 1)
	rooms = append(rooms, rangeRooms("B", 101, 105, domain.RoomTypeAir, 1, 300000, 300000)...)
	// B201-B205: fan, rent=2500, deposit=2000 (floor 2)
	rooms = append(rooms, rangeRooms("B", 201, 205, domain.RoomTypeFan, 2, 250000, 200000)...)
	// C101-C102: air, rent=3000, deposit=3000 (floor 1)
	rooms = append(rooms, rangeRooms("C", 101, 102, domain.RoomTypeAir, 1, 300000, 300000)...)
	// C201-C205: fan, rent=2500, deposit=2000 (floor 2)
	rooms = append(rooms, rangeRooms("C", 201, 205, domain.RoomTypeFan, 2, 250000, 200000)...)
	// D101-D111: fan, rent=2500, deposit=2000 (floor 1)
	rooms = append(rooms, rangeRooms("D", 101, 111, domain.RoomTypeFan, 1, 250000, 200000)...)
	// D201-D211: fan, rent=2500, deposit=2000 (floor 2)
	rooms = append(rooms, rangeRooms("D", 201, 211, domain.RoomTypeFan, 2, 250000, 200000)...)
	// E101-E104: fan, rent=2500, deposit=2000 (floor 1)
	rooms = append(rooms, rangeRooms("E", 101, 104, domain.RoomTypeFan, 1, 250000, 200000)...)
	// E201-E204: fan, rent=2500, deposit=2000 (floor 2)
	rooms = append(rooms, rangeRooms("E", 201, 204, domain.RoomTypeFan, 2, 250000, 200000)...)
	// OFFICE: air, rent=3500, deposit=3500
	rooms = append(rooms, roomSeed{Number: "OFFICE", Type: domain.RoomTypeAir, Floor: 1, BaseRent: 350000, BaseDeposit: 350000})
	// MART: air, rent=0, deposit=0
	rooms = append(rooms, roomSeed{Number: "MART", Type: domain.RoomTypeAir, Floor: 1, BaseRent: 0, BaseDeposit: 0})

	return rooms
}

// nanaPlace — นานาเพลส (46 rooms, all air, rent=3500, deposit=3500)
func nanaPlace() []roomSeed {
	var rooms []roomSeed
	// 0000
	rooms = append(rooms, roomSeed{Number: "0000", Type: domain.RoomTypeAir, Floor: 1, BaseRent: 350000, BaseDeposit: 350000})
	// 1001-1020 (floor 1)
	rooms = append(rooms, rangeRooms("", 1001, 1020, domain.RoomTypeAir, 1, 350000, 350000)...)
	// 2001-2025 (floor 2)
	rooms = append(rooms, rangeRooms("", 2001, 2025, domain.RoomTypeAir, 2, 350000, 350000)...)
	return rooms
}

// nanaMansion — นานาแมนชั่น (57 rooms, all air, rent=3500, deposit=3500)
func nanaMansion() []roomSeed {
	var rooms []roomSeed
	// MART
	rooms = append(rooms, roomSeed{Number: "MART", Type: domain.RoomTypeAir, Floor: 1, BaseRent: 350000, BaseDeposit: 350000})
	// 101-114 (floor 1)
	rooms = append(rooms, rangeRooms("", 101, 114, domain.RoomTypeAir, 1, 350000, 350000)...)
	// 201-221 (floor 2)
	rooms = append(rooms, rangeRooms("", 201, 221, domain.RoomTypeAir, 2, 350000, 350000)...)
	// 301-321 (floor 3)
	rooms = append(rooms, rangeRooms("", 301, 321, domain.RoomTypeAir, 3, 350000, 350000)...)
	return rooms
}

func rangeRooms(prefix string, start, end int, roomType domain.RoomType, floor int, baseRent, baseDeposit int64) []roomSeed {
	rooms := make([]roomSeed, 0, end-start+1)
	for i := start; i <= end; i++ {
		rooms = append(rooms, roomSeed{
			Number:      fmt.Sprintf("%s%d", prefix, i),
			Type:        roomType,
			Floor:       floor,
			BaseRent:    baseRent,
			BaseDeposit: baseDeposit,
		})
	}
	return rooms
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
