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
