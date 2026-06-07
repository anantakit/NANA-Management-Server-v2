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

// seedDevReconciliationFixtures augments the dev seed with controlled cases
// that exercise the Phase 1A Billing Reconciliation taxonomy branches the
// rest of the dev seed leaves blank:
//
//   - NEW_TENANT_MID_CYCLE  (A205 — contract starts day-15 of current month)
//   - CONTRACT_NOT_STARTED  (A206 — contract starts day-15 of next month)
//   - Inline anomaly         (A101 — existing MONTHLY meter flagged IsAnomalyElectricity)
//
// Room choice rationale: A205 + A206 are confirmed VACANT in dev seed and
// have no prior tenant history (B-series rooms each carry active contracts
// or multi-tenant history from earlier seed passes — picking those would
// idempotent-skip without creating the fixture, masking taxonomy coverage).
//
// Scoped to นานาคอร์ท so the 4-building checkpoint walk sees the cases
// without polluting Place / Mansion / Easy comparisons. All operations are
// idempotent — re-seeding (or `make dev-clean && make dev`) reproduces the
// same state.
//
// Lifetime: temporary until production data covers these taxonomy branches.
// Delete this file + its caller in seed.go when retiring. Do NOT modify the
// classifier to make fixtures pass — if classification surprises the
// operator, diagnose per `feedback_checkpoint_decision_rules` instead.
func seedDevReconciliationFixtures(db *gorm.DB) error {
	var apt apartment.Apartment
	if err := db.Where("name = ?", "นานาคอร์ท").First(&apt).Error; err != nil {
		return fmt.Errorf("find apartment: %w", err)
	}

	now := time.Now().UTC()
	cycleStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	midCycleStart := cycleStart.AddDate(0, 0, 14) // current month, day 15
	futureStart := cycleStart.AddDate(0, 1, 14)   // next month, day 15
	billingMonth := cycleStart.Format("2006-01")

	if err := seedReconciliationActiveContract(
		db, apt, "A205", "1100100100201",
		"นภสร เข้าใหม่", "0890000201", midCycleStart,
	); err != nil {
		return fmt.Errorf("seed PD fixture (A205): %w", err)
	}

	if err := seedReconciliationActiveContract(
		db, apt, "A206", "1100100100202",
		"ภัทร เริ่มเดือนหน้า", "0890000202", futureStart,
	); err != nil {
		return fmt.Errorf("seed CNS fixture (A206): %w", err)
	}

	if err := seedReconciliationAnomaly(db, apt, "A101", billingMonth); err != nil {
		return fmt.Errorf("seed anomaly fixture (A101): %w", err)
	}

	return nil
}

// seedReconciliationActiveContract creates one ACTIVE contract for a target
// vacant room at the requested startDate. Idempotent on `rooms.number` —
// skips when the room is already occupied OR the tenant id_card already
// exists from a prior run.
func seedReconciliationActiveContract(
	db *gorm.DB,
	apt apartment.Apartment,
	roomNumber, idCard, fullName, phone string,
	startDate time.Time,
) error {
	var rm room.Room
	if err := db.Where("apartment_id = ? AND number = ?", apt.ID, roomNumber).First(&rm).Error; err != nil {
		slog.Warn("reconciliation fixture: room not found", "room", roomNumber, "err", err)
		return nil
	}

	var activeCount int64
	if err := db.Model(&contract.Contract{}).
		Where("room_id = ? AND status = ?", rm.ID, contract.ContractStatusActive).
		Count(&activeCount).Error; err != nil {
		return fmt.Errorf("check existing contract on %s: %w", roomNumber, err)
	}
	if activeCount > 0 {
		return nil
	}

	tn, err := upsertReconciliationTenant(db, idCard, fullName, phone)
	if err != nil {
		return err
	}

	c := contract.Contract{
		TenantID:               tn.ID,
		RoomID:                 rm.ID,
		StartDate:              startDate,
		MinMonths:              6,
		MonthlyRent:            rm.BaseRent,
		DepositAmount:          rm.BaseDeposit,
		DepositStatus:          contract.DepositStatusCollected,
		ElectricityRatePerUnit: apt.ElectricityRatePerUnit,
		WaterRatePerUnit:       apt.WaterRatePerUnit,
		Status:                 contract.ContractStatusActive,
	}
	if err := db.Create(&c).Error; err != nil {
		return fmt.Errorf("create reconciliation contract %s: %w", roomNumber, err)
	}
	if err := db.Model(&rm).Update("status", room.RoomStatusOccupied).Error; err != nil {
		return fmt.Errorf("mark room %s occupied: %w", roomNumber, err)
	}
	slog.Info("seeded reconciliation contract fixture",
		"room", roomNumber, "start_date", startDate.Format("2006-01-02"))
	return nil
}

func upsertReconciliationTenant(db *gorm.DB, idCard, fullName, phone string) (tenant.Tenant, error) {
	var existing int64
	if err := db.Model(&tenant.Tenant{}).Where("id_card = ?", idCard).Count(&existing).Error; err != nil {
		return tenant.Tenant{}, fmt.Errorf("check tenant %s: %w", idCard, err)
	}
	tn := tenant.Tenant{
		FullName:         fullName,
		IDCard:           idCard,
		Phone:            phone,
		Address:          "Reconciliation fixture (dev seed)",
		EmergencyContact: "—",
	}
	if existing == 0 {
		if err := db.Create(&tn).Error; err != nil {
			return tenant.Tenant{}, fmt.Errorf("create tenant %s: %w", fullName, err)
		}
		return tn, nil
	}
	if err := db.Where("id_card = ?", idCard).First(&tn).Error; err != nil {
		return tenant.Tenant{}, fmt.Errorf("find tenant %s: %w", fullName, err)
	}
	return tn, nil
}

// seedReconciliationAnomaly flips IsAnomalyElectricity on the existing
// MONTHLY meter for (roomNumber, billingMonth). Side-steps ComputeAnomalies
// — the fixture's job is to assert the FE / API surface for an anomaly row,
// not to retest the detection thresholds (those have their own tests).
func seedReconciliationAnomaly(db *gorm.DB, apt apartment.Apartment, roomNumber, billingMonth string) error {
	var rm room.Room
	if err := db.Where("apartment_id = ? AND number = ?", apt.ID, roomNumber).First(&rm).Error; err != nil {
		slog.Warn("reconciliation anomaly fixture: room not found", "room", roomNumber, "err", err)
		return nil
	}

	var mr meterreading.MeterReading
	if err := db.Where(
		"room_id = ? AND reading_type = ? AND billing_month = ?",
		rm.ID, meterreading.ReadingTypeMonthly, billingMonth,
	).First(&mr).Error; err != nil {
		slog.Warn("reconciliation anomaly fixture: no MONTHLY meter to flag",
			"room", roomNumber, "billing_month", billingMonth, "err", err)
		return nil
	}
	if mr.IsAnomalyElectricity {
		return nil
	}
	if err := db.Model(&mr).Update("is_anomaly_electricity", true).Error; err != nil {
		return fmt.Errorf("flag anomaly on %s: %w", roomNumber, err)
	}
	slog.Info("seeded reconciliation anomaly fixture",
		"room", roomNumber, "billing_month", billingMonth)
	return nil
}
