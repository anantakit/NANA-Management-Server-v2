package seed

import (
	"fmt"
	"log/slog"

	"nana/internal/apartment"
	"nana/internal/auth"
	"nana/internal/room"
	"nana/internal/shared/role"
	"nana/internal/tenant"

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
	Type        room.RoomType
	Floor       int
	BaseRent    int64
	BaseDeposit int64
}

func Run(db *gorm.DB, env string) error {
	if err := seedApartments(db); err != nil {
		return fmt.Errorf("seed apartments: %w", err)
	}
	if err := seedRooms(db); err != nil {
		return fmt.Errorf("seed rooms: %w", err)
	}
	if err := seedAdminUser(db); err != nil {
		return fmt.Errorf("seed admin user: %w", err)
	}

	// Dev-only seed data
	if env == "development" {
		if err := seedDevTenants(db); err != nil {
			return fmt.Errorf("seed dev tenants: %w", err)
		}
		if err := seedDevContracts(db); err != nil {
			return fmt.Errorf("seed dev contracts: %w", err)
		}
		if err := seedDevMeterReadings(db); err != nil {
			return fmt.Errorf("seed dev meter readings: %w", err)
		}
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
		if err := db.Model(&apartment.Apartment{}).Where("name = ?", a.Name).Count(&count).Error; err != nil {
			return fmt.Errorf("check apartment %s: %w", a.Name, err)
		}
		if count > 0 {
			continue
		}

		apt := apartment.Apartment{
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
		var apt apartment.Apartment
		if err := db.Where("name = ?", aptName).First(&apt).Error; err != nil {
			slog.Warn("apartment not found for room seed", "apartment", aptName)
			continue
		}

		created := 0
		for _, rs := range rooms {
			var count int64
			if err := db.Model(&room.Room{}).Where("apartment_id = ? AND number = ?", apt.ID, rs.Number).Count(&count).Error; err != nil {
				return fmt.Errorf("check room %s: %w", rs.Number, err)
			}
			if count > 0 {
				continue
			}

			room := room.Room{
				ApartmentID: apt.ID,
				Number:      rs.Number,
				Type:        rs.Type,
				Floor:       rs.Floor,
				BaseRent:    rs.BaseRent,
				BaseDeposit: rs.BaseDeposit,
				Status:      room.RoomStatusVacant,
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
	rooms = append(rooms, rangeRooms("A", 101, 111, room.RoomTypeAir, 1, 300000, 300000)...)
	// A201-A211: fan, rent=2500, deposit=2000 (floor 2)
	rooms = append(rooms, rangeRooms("A", 201, 211, room.RoomTypeFan, 2, 250000, 200000)...)
	// B101-B105: air, rent=3000, deposit=3000 (floor 1)
	rooms = append(rooms, rangeRooms("B", 101, 105, room.RoomTypeAir, 1, 300000, 300000)...)
	// B201-B205: fan, rent=2500, deposit=2000 (floor 2)
	rooms = append(rooms, rangeRooms("B", 201, 205, room.RoomTypeFan, 2, 250000, 200000)...)
	// C101-C102: air, rent=3000, deposit=3000 (floor 1)
	rooms = append(rooms, rangeRooms("C", 101, 102, room.RoomTypeAir, 1, 300000, 300000)...)
	// C201-C205: fan, rent=2500, deposit=2000 (floor 2)
	rooms = append(rooms, rangeRooms("C", 201, 205, room.RoomTypeFan, 2, 250000, 200000)...)
	// D101-D111: fan, rent=2500, deposit=2000 (floor 1)
	rooms = append(rooms, rangeRooms("D", 101, 111, room.RoomTypeFan, 1, 250000, 200000)...)
	// D201-D211: fan, rent=2500, deposit=2000 (floor 2)
	rooms = append(rooms, rangeRooms("D", 201, 211, room.RoomTypeFan, 2, 250000, 200000)...)
	// E101-E104: fan, rent=2500, deposit=2000 (floor 1)
	rooms = append(rooms, rangeRooms("E", 101, 104, room.RoomTypeFan, 1, 250000, 200000)...)
	// E201-E204: fan, rent=2500, deposit=2000 (floor 2)
	rooms = append(rooms, rangeRooms("E", 201, 204, room.RoomTypeFan, 2, 250000, 200000)...)
	// OFFICE: air, rent=3500, deposit=3500
	rooms = append(rooms, roomSeed{Number: "OFFICE", Type: room.RoomTypeAir, Floor: 1, BaseRent: 350000, BaseDeposit: 350000})
	// MART: air, rent=0, deposit=0
	rooms = append(rooms, roomSeed{Number: "MART", Type: room.RoomTypeAir, Floor: 1, BaseRent: 0, BaseDeposit: 0})

	return rooms
}

// nanaPlace — นานาเพลส (46 rooms, all air, rent=3500, deposit=3500)
func nanaPlace() []roomSeed {
	var rooms []roomSeed
	// 0000
	rooms = append(rooms, roomSeed{Number: "0000", Type: room.RoomTypeAir, Floor: 1, BaseRent: 350000, BaseDeposit: 350000})
	// 1001-1020 (floor 1)
	rooms = append(rooms, rangeRooms("", 1001, 1020, room.RoomTypeAir, 1, 350000, 350000)...)
	// 2001-2025 (floor 2)
	rooms = append(rooms, rangeRooms("", 2001, 2025, room.RoomTypeAir, 2, 350000, 350000)...)
	return rooms
}

// nanaMansion — นานาแมนชั่น (57 rooms, all air, rent=3500, deposit=3500)
func nanaMansion() []roomSeed {
	var rooms []roomSeed
	// MART
	rooms = append(rooms, roomSeed{Number: "MART", Type: room.RoomTypeAir, Floor: 1, BaseRent: 350000, BaseDeposit: 350000})
	// 101-114 (floor 1)
	rooms = append(rooms, rangeRooms("", 101, 114, room.RoomTypeAir, 1, 350000, 350000)...)
	// 201-221 (floor 2)
	rooms = append(rooms, rangeRooms("", 201, 221, room.RoomTypeAir, 2, 350000, 350000)...)
	// 301-321 (floor 3)
	rooms = append(rooms, rangeRooms("", 301, 321, room.RoomTypeAir, 3, 350000, 350000)...)
	return rooms
}

func rangeRooms(prefix string, start, end int, roomType room.RoomType, floor int, baseRent, baseDeposit int64) []roomSeed {
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
	if err := db.Model(&auth.User{}).Where("username = ?", "admin").Count(&count).Error; err != nil {
		return fmt.Errorf("check admin user: %w", err)
	}
	if count > 0 {
		return nil
	}

	hash, err := bcrypt.GenerateFromPassword([]byte("admin123"), 12)
	if err != nil {
		return fmt.Errorf("hash admin password: %w", err)
	}

	user := auth.User{
		Username:           "admin",
		PasswordHash:       string(hash),
		FullName:           "ผู้ดูแลระบบ",
		Role:               role.Admin,
		MustChangePassword: true,
	}

	if err := db.Create(&user).Error; err != nil {
		return fmt.Errorf("create admin user: %w", err)
	}

	slog.Info("seeded admin user", "username", "admin")
	return nil
}

// seedDevTenants seeds sample tenants for development only.
func seedDevTenants(db *gorm.DB) error {
	tenants := []struct {
		FullName         string
		IDCard           string
		Phone            string
		Address          string
		EmergencyContact string
	}{
		{FullName: "สมชาย ใจดี", IDCard: "1100100100001", Phone: "0812345678", Address: "123 ถ.สุขุมวิท กรุงเทพฯ", EmergencyContact: "สมหญิง ใจดี 0898765432"},
		{FullName: "สมหญิง รักสุข", IDCard: "1100100100002", Phone: "0823456789", Address: "456 ถ.พหลโยธิน กรุงเทพฯ", EmergencyContact: "สมศรี รักสุข 0867654321"},
		{FullName: "วิชัย มั่งมี", IDCard: "1100100100003", Phone: "0834567890", Address: "789 ถ.รัชดา กรุงเทพฯ", EmergencyContact: "วิภา มั่งมี 0856543210"},
		{FullName: "นภา สุขใจ", IDCard: "1100100100004", Phone: "0845678901", Address: "321 ถ.ลาดพร้าว กรุงเทพฯ", EmergencyContact: "นที สุขใจ 0845432109"},
		{FullName: "ประเสริฐ ดีงาม", IDCard: "1100100100005", Phone: "0856789012", Address: "654 ถ.งามวงศ์วาน นนทบุรี", EmergencyContact: "ประภา ดีงาม 0834321098"},
		{FullName: "อรุณ แสงจันทร์", IDCard: "1100100100006", Phone: "0867890123", Address: "111 ถ.พระราม 9 กรุงเทพฯ", EmergencyContact: "อรุณี แสงจันทร์ 0823210987"},
		{FullName: "พิมพ์ใจ สุวรรณ", IDCard: "1100100100007", Phone: "0878901234", Address: "222 ถ.วิภาวดี กรุงเทพฯ", EmergencyContact: "พิมาน สุวรรณ 0812109876"},
	}

	for _, t := range tenants {
		var count int64
		if err := db.Model(&tenant.Tenant{}).Where("id_card = ?", t.IDCard).Count(&count).Error; err != nil {
			return fmt.Errorf("check tenant %s: %w", t.FullName, err)
		}
		if count > 0 {
			continue
		}

		tn := tenant.Tenant{
			FullName:         t.FullName,
			IDCard:           t.IDCard,
			Phone:            t.Phone,
			Address:          t.Address,
			EmergencyContact: t.EmergencyContact,
		}
		if err := db.Create(&tn).Error; err != nil {
			return fmt.Errorf("create tenant %s: %w", t.FullName, err)
		}
	}

	slog.Info("seeded dev tenants", "count", len(tenants))
	return nil
}
