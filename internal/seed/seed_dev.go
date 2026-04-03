package seed

import (
	"fmt"
	"log/slog"
	"time"

	"nana/internal/apartment"
	"nana/internal/contract"
	"nana/internal/meterreading"
	"nana/internal/room"
	"nana/internal/tenant"

	"gorm.io/gorm"
)

// seedDevContracts assigns tenants to rooms in นานาคอร์ท (A101–A107).
// Must run after seedRooms + seedDevTenants.
func seedDevContracts(db *gorm.DB) error {
	var apt apartment.Apartment
	if err := db.Where("name = ?", "นานาคอร์ท").First(&apt).Error; err != nil {
		return fmt.Errorf("find apartment: %w", err)
	}

	// Load dev tenants (by id_card order)
	var tenants []tenant.Tenant
	if err := db.Where("id_card IN ?", []string{
		"1100100100001", "1100100100002", "1100100100003",
		"1100100100004", "1100100100005", "1100100100006",
		"1100100100007",
	}).Order("id_card").Find(&tenants).Error; err != nil {
		return fmt.Errorf("find tenants: %w", err)
	}
	if len(tenants) < 7 {
		slog.Warn("not enough dev tenants for contracts", "found", len(tenants))
		return nil
	}

	// Load rooms A101–A107
	roomNumbers := []string{"A101", "A102", "A103", "A104", "A105", "A106", "A107"}
	var rooms []room.Room
	if err := db.Where("apartment_id = ? AND number IN ?", apt.ID, roomNumbers).
		Order("number").Find(&rooms).Error; err != nil {
		return fmt.Errorf("find rooms: %w", err)
	}
	if len(rooms) < 7 {
		slog.Warn("not enough rooms for contracts", "found", len(rooms))
		return nil
	}

	startDate := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	created := 0

	for i := 0; i < 7; i++ {
		// Skip if contract already exists for this room
		var count int64
		if err := db.Model(&contract.Contract{}).
			Where("room_id = ? AND status = ?", rooms[i].ID, contract.ContractStatusActive).
			Count(&count).Error; err != nil {
			return fmt.Errorf("check contract: %w", err)
		}
		if count > 0 {
			continue
		}

		c := contract.Contract{
			TenantID:               tenants[i].ID,
			RoomID:                 rooms[i].ID,
			StartDate:              startDate,
			MinMonths:              6,
			MonthlyRent:            rooms[i].BaseRent,
			DepositAmount:          rooms[i].BaseDeposit,
			DepositStatus:          contract.DepositStatusCollected,
			ElectricityRatePerUnit: apt.ElectricityRatePerUnit,
			WaterRatePerUnit:       apt.WaterRatePerUnit,
			Status:                 contract.ContractStatusActive,
		}
		if err := db.Create(&c).Error; err != nil {
			return fmt.Errorf("create contract for %s: %w", rooms[i].Number, err)
		}

		// Mark room occupied
		if err := db.Model(&rooms[i]).Update("status", room.RoomStatusOccupied).Error; err != nil {
			return fmt.Errorf("mark room %s occupied: %w", rooms[i].Number, err)
		}
		created++
	}

	if created > 0 {
		slog.Info("seeded dev contracts", "count", created)
	}
	return nil
}

// seedDevMeterReadings creates meter reading history for A101–A107.
// Each room has a different usage pattern to test anomaly detection.
//
// | Room | History  | Elec baseline | Water baseline | Test scenario                          |
// |------|----------|---------------|----------------|----------------------------------------|
// | A101 | 6 months | ~120          | ~12            | Normal — usage 130 → no anomaly        |
// | A102 | 6 months | ~81           | ~15            | Elec 122 → OK, 123+ → anomaly         |
// | A103 | 6 months | ~133          | ~20            | Varied — 200 → OK, 201+ → anomaly     |
// | A104 | 2 months | (not enough)  | (not enough)   | No anomaly even with extreme values    |
// | A105 | 6 months | ~40           | ~8             | Low-usage fan room — anomaly at 61+     |
// | A106 | 6 months | ~250          | ~30            | Heavy AC user — anomaly at 376+        |
// | A107 | 6 months | ~100          | ~50            | Water-only anomaly (elec normal)       |
func seedDevMeterReadings(db *gorm.DB) error {
	var apt apartment.Apartment
	if err := db.Where("name = ?", "นานาคอร์ท").First(&apt).Error; err != nil {
		return fmt.Errorf("find apartment: %w", err)
	}

	roomNumbers := []string{"A101", "A102", "A103", "A104", "A105", "A106", "A107"}
	var rooms []room.Room
	if err := db.Where("apartment_id = ? AND number IN ?", apt.ID, roomNumbers).
		Order("number").Find(&rooms).Error; err != nil {
		return fmt.Errorf("find rooms: %w", err)
	}
	if len(rooms) < 7 {
		slog.Warn("not enough rooms for meter readings", "found", len(rooms))
		return nil
	}

	// Check idempotent — skip if any readings exist for these rooms
	var existingCount int64
	roomIDs := make([]interface{}, len(rooms))
	for i, r := range rooms {
		roomIDs[i] = r.ID
	}
	if err := db.Model(&meterreading.MeterReading{}).
		Where("room_id IN ?", roomIDs).Count(&existingCount).Error; err != nil {
		return fmt.Errorf("check existing readings: %w", err)
	}
	if existingCount > 0 {
		return nil // already seeded
	}

	// Usage profiles: [elec_per_month, water_per_month]
	// 6 months of history: Oct 2025 → Mar 2026
	type monthUsage struct {
		elec  int
		water int
	}

	profiles := map[string][]monthUsage{
		// A101: steady ~120 elec, ~12 water → baseline elec=119, water=12
		"A101": {
			{115, 11}, {125, 13}, {118, 12}, {122, 14}, {120, 11}, {119, 13},
		},
		// A102: steady ~81 elec, ~15 water → baseline elec=81, water=15
		"A102": {
			{78, 14}, {82, 16}, {85, 13}, {79, 15}, {81, 17}, {83, 14},
		},
		// A103: varied elec ~100-180, water ~20 → baseline elec=~130, water=~20
		"A103": {
			{100, 18}, {150, 22}, {120, 19}, {180, 21}, {110, 20}, {140, 23},
		},
		// A104: only 2 months → not enough data for baseline
		"A104": {
			{200, 25}, {190, 22},
		},
		// A105: low-usage fan room ~40 elec, ~8 water (คนอยู่ไม่ค่อยเปิดไฟ)
		"A105": {
			{38, 7}, {42, 9}, {40, 8}, {44, 7}, {37, 8}, {41, 9},
		},
		// A106: heavy AC user ~250 elec, ~30 water (เปิดแอร์ตลอด)
		"A106": {
			{240, 28}, {260, 32}, {245, 30}, {255, 29}, {248, 31}, {252, 30},
		},
		// A107: elec ~100, water ~50 → test water-only anomaly
		// Water baseline=~50, threshold=75 → usage 76+ = anomaly
		// Elec baseline=~100, threshold=150 → usage ≤150 = OK
		"A107": {
			{95, 48}, {105, 52}, {100, 50}, {98, 53}, {102, 47}, {100, 50},
		},
	}

	// Base meter values (starting point Oct 2025)
	// A107 starts electricity near 9999 to make rollover realistic
	baseElecByRoom := map[string]int{
		"A107": 9500,
	}
	defaultBaseElec := 1000
	baseWater := 100

	created := 0
	for _, rm := range rooms {
		profile, ok := profiles[rm.Number]
		if !ok {
			continue
		}

		elecPrev := baseElecByRoom[rm.Number]
		if elecPrev == 0 {
			elecPrev = defaultBaseElec
		}
		waterPrev := baseWater

		for monthIdx, usage := range profile {
			// Month: Oct=10, Nov=11, Dec=12, Jan=1, Feb=2, Mar=3
			year := 2025
			month := 10 + monthIdx
			if month > 12 {
				month -= 12
				year = 2026
			}
			readingDate := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)

			elecCurrent := elecPrev + usage.elec
			waterCurrent := waterPrev + usage.water

			reading := meterreading.MeterReading{
				RoomID:              rm.ID,
				ReadingDate:         readingDate,
				ElectricityPrevious: elecPrev,
				ElectricityCurrent:  elecCurrent,
				WaterPrevious:       waterPrev,
				WaterCurrent:        waterCurrent,
			}
			if err := db.Create(&reading).Error; err != nil {
				return fmt.Errorf("create reading %s %d-%02d: %w", rm.Number, year, month, err)
			}
			created++

			elecPrev = elecCurrent
			waterPrev = waterCurrent
		}
	}

	if created > 0 {
		slog.Info("seeded dev meter readings", "count", created)
	}

	// --- History-specific test data ---
	// Mark some readings as "edited" (bump updated_at so is_edited = true)
	// A101 Jan 2026 + A103 Dec 2025 → simulate admin corrections
	if err := db.Exec(`
		UPDATE meter_readings
		SET updated_at = created_at + INTERVAL '1 hour'
		WHERE room_id IN (
			SELECT id FROM rooms WHERE apartment_id = ? AND number IN ('A101', 'A103')
		)
		AND (
			(reading_date = '2026-01-01' AND room_id = (SELECT id FROM rooms WHERE apartment_id = ? AND number = 'A101'))
			OR
			(reading_date = '2025-12-01' AND room_id = (SELECT id FROM rooms WHERE apartment_id = ? AND number = 'A103'))
		)
		AND deleted_at IS NULL
	`, apt.ID, apt.ID, apt.ID).Error; err != nil {
		slog.Warn("failed to mark edited readings", "error", err)
	}

	// A107 Feb 2026: electricity rollover — meter passed 9999 and wrapped
	// Previous ~9,898 (from Jan), actual usage ~102 → meter wrapped to 0+1
	// Set: previous=9898, current=1, is_rollover=true
	// Also fix Mar 2026 to chain from the new current (1)
	a107RoomID := "(SELECT id FROM rooms WHERE apartment_id = ? AND number = 'A107')"
	if err := db.Exec(`
		UPDATE meter_readings
		SET electricity_previous = 9898, electricity_current = 1,
		    is_rollover_electricity = true
		WHERE room_id = `+a107RoomID+`
		AND reading_date = '2026-02-01'
		AND deleted_at IS NULL
	`, apt.ID).Error; err != nil {
		slog.Warn("failed to set rollover reading", "error", err)
	}
	// Fix A107 Mar 2026 to chain from rolled-over value (1)
	if err := db.Exec(`
		UPDATE meter_readings
		SET electricity_previous = 1, electricity_current = 101
		WHERE room_id = `+a107RoomID+`
		AND reading_date = '2026-03-01'
		AND deleted_at IS NULL
	`, apt.ID).Error; err != nil {
		slog.Warn("failed to fix post-rollover reading", "error", err)
	}

	return nil
}

// seedDevTenantHistory creates multi-tenant history for A108 & A109 to test tenant timeline overlay.
//
// | Room | Tenant 1 (ENDED)        | Tenant 2 (ENDED)         | Tenant 3 (ACTIVE)        |
// |------|-------------------------|--------------------------|--------------------------|
// | A108 | วรรณา (มิ.ย.-ก.ย. 68)   | —                        | ธนกร (ต.ค. 68-ปัจจุบัน)  |
// |      | ⚡~80, 💧~10 (4 เดือน)  |                          | ⚡~150, 💧~18 (6 เดือน)  |
// |      |                         |                          | ก.พ. 69: ⚡310 anomaly!  |
// | A109 | ปรีชา (เม.ย.-ธ.ค. 67)   | มนัส (ม.ค.-พ.ค. 68)     | สุนทร (มิ.ย.-พ.ย. 68)   | ดวงใจ (ธ.ค. 68-ปัจจุบัน) |
// |      | ⚡~70, 💧~9 (9 เดือน)   | ⚡~60, 💧~8 (5 เดือน)   | ⚡~100, 💧~12 (6 เดือน)  | ⚡~90, 💧~11 (4 เดือน)   |
// |      | Total: 24 readings → page 1 = 20, page 2 = 4 (test load more)
func seedDevTenantHistory(db *gorm.DB) error {
	var apt apartment.Apartment
	if err := db.Where("name = ?", "นานาคอร์ท").First(&apt).Error; err != nil {
		return fmt.Errorf("find apartment: %w", err)
	}

	// Check idempotent — skip if contracts already exist for A108
	var a108Room room.Room
	if err := db.Where("apartment_id = ? AND number = ?", apt.ID, "A108").First(&a108Room).Error; err != nil {
		slog.Warn("room A108 not found for tenant history seed")
		return nil
	}
	var contractCount int64
	if err := db.Model(&contract.Contract{}).Where("room_id = ?", a108Room.ID).Count(&contractCount).Error; err != nil {
		return fmt.Errorf("check A108 contracts: %w", err)
	}
	if contractCount > 0 {
		return nil // already seeded
	}

	var a109Room room.Room
	if err := db.Where("apartment_id = ? AND number = ?", apt.ID, "A109").First(&a109Room).Error; err != nil {
		slog.Warn("room A109 not found for tenant history seed")
		return nil
	}

	// --- Create additional tenants ---

	extraTenants := []struct {
		FullName         string
		IDCard           string
		Phone            string
		Address          string
		EmergencyContact string
	}{
		{FullName: "วรรณา ศรีสุข", IDCard: "1100100100101", Phone: "0891112233", Address: "55 ถ.เพชรบุรี กรุงเทพฯ", EmergencyContact: "วรพจน์ ศรีสุข 0891234567"},
		{FullName: "ธนกร วงษ์ดี", IDCard: "1100100100102", Phone: "0892223344", Address: "88 ถ.สีลม กรุงเทพฯ", EmergencyContact: "ธนพร วงษ์ดี 0892345678"},
		{FullName: "มนัส ทองคำ", IDCard: "1100100100103", Phone: "0893334455", Address: "99 ถ.สาทร กรุงเทพฯ", EmergencyContact: "มนต์ชัย ทองคำ 0893456789"},
		{FullName: "สุนทร ยิ้มแย้ม", IDCard: "1100100100104", Phone: "0894445566", Address: "77 ถ.บางนา กรุงเทพฯ", EmergencyContact: "สุนีย์ ยิ้มแย้ม 0894567890"},
		{FullName: "ดวงใจ เพ็ชรดี", IDCard: "1100100100105", Phone: "0895556677", Address: "33 ถ.อ่อนนุช กรุงเทพฯ", EmergencyContact: "ดวงตา เพ็ชรดี 0895678901"},
		{FullName: "ปรีชา สมบูรณ์", IDCard: "1100100100106", Phone: "0896667788", Address: "44 ถ.ประชาอุทิศ กรุงเทพฯ", EmergencyContact: "ปราณี สมบูรณ์ 0896789012"},
	}

	tenantMap := make(map[string]tenant.Tenant) // idCard → tenant
	for _, t := range extraTenants {
		var count int64
		if err := db.Model(&tenant.Tenant{}).Where("id_card = ?", t.IDCard).Count(&count).Error; err != nil {
			return fmt.Errorf("check tenant %s: %w", t.FullName, err)
		}
		tn := tenant.Tenant{
			FullName:         t.FullName,
			IDCard:           t.IDCard,
			Phone:            t.Phone,
			Address:          t.Address,
			EmergencyContact: t.EmergencyContact,
		}
		if count == 0 {
			if err := db.Create(&tn).Error; err != nil {
				return fmt.Errorf("create tenant %s: %w", t.FullName, err)
			}
		} else {
			if err := db.Where("id_card = ?", t.IDCard).First(&tn).Error; err != nil {
				return fmt.Errorf("find tenant %s: %w", t.FullName, err)
			}
		}
		tenantMap[t.IDCard] = tn
	}

	// --- A108: 2 tenants ---

	// Tenant 1: วรรณา ศรีสุข — Jun 2025 → Sep 2025 (ENDED)
	wannaStart := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	wannaEnd := time.Date(2025, 9, 30, 0, 0, 0, 0, time.UTC)
	wanna := tenantMap["1100100100101"]
	c1 := contract.Contract{
		TenantID:               wanna.ID,
		RoomID:                 a108Room.ID,
		StartDate:              wannaStart,
		MinMonths:              3,
		MonthlyRent:            a108Room.BaseRent,
		DepositAmount:          a108Room.BaseDeposit,
		DepositStatus:          contract.DepositStatusRefunded,
		ElectricityRatePerUnit: apt.ElectricityRatePerUnit,
		WaterRatePerUnit:       apt.WaterRatePerUnit,
		Status:                 contract.ContractStatusEnded,
		EndDate:                &wannaEnd,
		MoveOutDate:            &wannaEnd,
	}
	if err := db.Create(&c1).Error; err != nil {
		return fmt.Errorf("create A108 contract 1: %w", err)
	}

	// Tenant 2: ธนกร วงษ์ดี — Oct 2025 → present (ACTIVE)
	thanakorn := tenantMap["1100100100102"]
	thanakornStart := time.Date(2025, 10, 1, 0, 0, 0, 0, time.UTC)
	c2 := contract.Contract{
		TenantID:               thanakorn.ID,
		RoomID:                 a108Room.ID,
		StartDate:              thanakornStart,
		MinMonths:              6,
		MonthlyRent:            a108Room.BaseRent,
		DepositAmount:          a108Room.BaseDeposit,
		DepositStatus:          contract.DepositStatusCollected,
		ElectricityRatePerUnit: apt.ElectricityRatePerUnit,
		WaterRatePerUnit:       apt.WaterRatePerUnit,
		Status:                 contract.ContractStatusActive,
	}
	if err := db.Create(&c2).Error; err != nil {
		return fmt.Errorf("create A108 contract 2: %w", err)
	}
	if err := db.Model(&a108Room).Update("status", room.RoomStatusOccupied).Error; err != nil {
		return fmt.Errorf("mark A108 occupied: %w", err)
	}

	// A108 meter readings
	// Tenant 1 period: Jun-Sep 2025 — steady low usage
	// Tenant 2 period: Oct 2025-Mar 2026 — higher usage, Feb anomaly spike
	a108Profile := []struct {
		year, month, elec, water int
	}{
		{2025, 6, 78, 9},    // วรรณา
		{2025, 7, 82, 11},   // วรรณา
		{2025, 8, 75, 10},   // วรรณา
		{2025, 9, 85, 12},   // วรรณา
		{2025, 10, 145, 17}, // ธนกร — เดือนแรก
		{2025, 11, 155, 19}, // ธนกร — หลังเปลี่ยน +1
		{2025, 12, 148, 18}, // ธนกร — หลังเปลี่ยน +2
		{2026, 1, 152, 20},  // ธนกร
		{2026, 2, 310, 22},  // ธนกร — ⚡ anomaly! (baseline ~150, usage 310 >> 225 threshold)
		{2026, 3, 160, 19},  // ธนกร — กลับปกติ
	}
	elecPrev, waterPrev := 1000, 100
	for _, m := range a108Profile {
		readingDate := time.Date(m.year, time.Month(m.month), 1, 0, 0, 0, 0, time.UTC)
		elecCurr := elecPrev + m.elec
		waterCurr := waterPrev + m.water
		reading := meterreading.MeterReading{
			RoomID:              a108Room.ID,
			ReadingDate:         readingDate,
			ElectricityPrevious: elecPrev,
			ElectricityCurrent:  elecCurr,
			WaterPrevious:       waterPrev,
			WaterCurrent:        waterCurr,
		}
		if err := db.Create(&reading).Error; err != nil {
			return fmt.Errorf("create A108 reading %d-%02d: %w", m.year, m.month, err)
		}
		elecPrev = elecCurr
		waterPrev = waterCurr
	}

	// Mark A108 Feb 2026 as anomaly (seed bypasses service anomaly computation)
	if err := db.Exec(`
		UPDATE meter_readings
		SET is_anomaly_electricity = true
		WHERE room_id = ? AND reading_date = '2026-02-01' AND deleted_at IS NULL
	`, a108Room.ID).Error; err != nil {
		slog.Warn("failed to mark A108 anomaly", "error", err)
	}

	// --- A109: 4 tenants (24 readings → page 1 = 20, page 2 = 4) ---

	// Tenant 0: ปรีชา สมบูรณ์ — Apr 2024 → Dec 2024 (ENDED, 9 months)
	preecha := tenantMap["1100100100106"]
	preechaStart := time.Date(2024, 4, 1, 0, 0, 0, 0, time.UTC)
	preechaEnd := time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)
	c0 := contract.Contract{
		TenantID:               preecha.ID,
		RoomID:                 a109Room.ID,
		StartDate:              preechaStart,
		MinMonths:              6,
		MonthlyRent:            a109Room.BaseRent,
		DepositAmount:          a109Room.BaseDeposit,
		DepositStatus:          contract.DepositStatusRefunded,
		ElectricityRatePerUnit: apt.ElectricityRatePerUnit,
		WaterRatePerUnit:       apt.WaterRatePerUnit,
		Status:                 contract.ContractStatusEnded,
		EndDate:                &preechaEnd,
		MoveOutDate:            &preechaEnd,
	}
	if err := db.Create(&c0).Error; err != nil {
		return fmt.Errorf("create A109 contract 0: %w", err)
	}

	// Tenant 1: มนัส ทองคำ — Jan 2025 → May 2025 (ENDED)
	manat := tenantMap["1100100100103"]
	manatStart := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	manatEnd := time.Date(2025, 5, 31, 0, 0, 0, 0, time.UTC)
	c3 := contract.Contract{
		TenantID:               manat.ID,
		RoomID:                 a109Room.ID,
		StartDate:              manatStart,
		MinMonths:              3,
		MonthlyRent:            a109Room.BaseRent,
		DepositAmount:          a109Room.BaseDeposit,
		DepositStatus:          contract.DepositStatusRefunded,
		ElectricityRatePerUnit: apt.ElectricityRatePerUnit,
		WaterRatePerUnit:       apt.WaterRatePerUnit,
		Status:                 contract.ContractStatusEnded,
		EndDate:                &manatEnd,
		MoveOutDate:            &manatEnd,
	}
	if err := db.Create(&c3).Error; err != nil {
		return fmt.Errorf("create A109 contract 1: %w", err)
	}

	// Tenant 2: สุนทร ยิ้มแย้ม — Jun 2025 → Nov 2025 (ENDED)
	suntorn := tenantMap["1100100100104"]
	suntornStart := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	suntornEnd := time.Date(2025, 11, 30, 0, 0, 0, 0, time.UTC)
	c4 := contract.Contract{
		TenantID:               suntorn.ID,
		RoomID:                 a109Room.ID,
		StartDate:              suntornStart,
		MinMonths:              6,
		MonthlyRent:            a109Room.BaseRent,
		DepositAmount:          a109Room.BaseDeposit,
		DepositStatus:          contract.DepositStatusRefunded,
		ElectricityRatePerUnit: apt.ElectricityRatePerUnit,
		WaterRatePerUnit:       apt.WaterRatePerUnit,
		Status:                 contract.ContractStatusEnded,
		EndDate:                &suntornEnd,
		MoveOutDate:            &suntornEnd,
	}
	if err := db.Create(&c4).Error; err != nil {
		return fmt.Errorf("create A109 contract 2: %w", err)
	}

	// Tenant 3: ดวงใจ เพ็ชรดี — Dec 2025 → present (ACTIVE)
	duangjai := tenantMap["1100100100105"]
	duangjaiStart := time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC)
	c5 := contract.Contract{
		TenantID:               duangjai.ID,
		RoomID:                 a109Room.ID,
		StartDate:              duangjaiStart,
		MinMonths:              6,
		MonthlyRent:            a109Room.BaseRent,
		DepositAmount:          a109Room.BaseDeposit,
		DepositStatus:          contract.DepositStatusCollected,
		ElectricityRatePerUnit: apt.ElectricityRatePerUnit,
		WaterRatePerUnit:       apt.WaterRatePerUnit,
		Status:                 contract.ContractStatusActive,
	}
	if err := db.Create(&c5).Error; err != nil {
		return fmt.Errorf("create A109 contract 3: %w", err)
	}
	if err := db.Model(&a109Room).Update("status", room.RoomStatusOccupied).Error; err != nil {
		return fmt.Errorf("mark A109 occupied: %w", err)
	}

	// A109 meter readings — 24 months (Apr 2024 → Mar 2026)
	a109Profile := []struct {
		year, month, elec, water int
	}{
		{2024, 4, 68, 8},    // ปรีชา
		{2024, 5, 72, 10},   // ปรีชา
		{2024, 6, 75, 9},    // ปรีชา
		{2024, 7, 70, 11},   // ปรีชา
		{2024, 8, 65, 8},    // ปรีชา
		{2024, 9, 73, 10},   // ปรีชา
		{2024, 10, 69, 9},   // ปรีชา
		{2024, 11, 71, 8},   // ปรีชา
		{2024, 12, 67, 10},  // ปรีชา
		{2025, 1, 58, 7},    // มนัส — เดือนแรก
		{2025, 2, 62, 9},    // มนัส
		{2025, 3, 55, 8},    // มนัส
		{2025, 4, 65, 7},    // มนัส
		{2025, 5, 60, 9},    // มนัส
		{2025, 6, 98, 11},   // สุนทร — เดือนแรก
		{2025, 7, 105, 13},  // สุนทร
		{2025, 8, 102, 12},  // สุนทร
		{2025, 9, 95, 11},   // สุนทร
		{2025, 10, 108, 14}, // สุนทร
		{2025, 11, 100, 12}, // สุนทร
		{2025, 12, 88, 10},  // ดวงใจ — เดือนแรก
		{2026, 1, 92, 12},   // ดวงใจ
		{2026, 2, 85, 11},   // ดวงใจ
		{2026, 3, 95, 13},   // ดวงใจ
	}
	elecPrev, waterPrev = 500, 50
	for _, m := range a109Profile {
		readingDate := time.Date(m.year, time.Month(m.month), 1, 0, 0, 0, 0, time.UTC)
		elecCurr := elecPrev + m.elec
		waterCurr := waterPrev + m.water
		reading := meterreading.MeterReading{
			RoomID:              a109Room.ID,
			ReadingDate:         readingDate,
			ElectricityPrevious: elecPrev,
			ElectricityCurrent:  elecCurr,
			WaterPrevious:       waterPrev,
			WaterCurrent:        waterCurr,
		}
		if err := db.Create(&reading).Error; err != nil {
			return fmt.Errorf("create A109 reading %d-%02d: %w", m.year, m.month, err)
		}
		elecPrev = elecCurr
		waterPrev = waterCurr
	}

	slog.Info("seeded dev tenant history", "rooms", "A108, A109")
	return nil
}
