package billing

import (
	"testing"

	"nana/internal/meterreading"

	"github.com/google/uuid"
)

func intptr(v int) *int { return &v }

// elecRecoveryAnchor builds a READING_RECOVERY row where ELECTRICITY was
// over-recorded (recorded 1500 > re-anchored current 1240) and water is NOT
// corrected (recorded nil, prev==current baseline carry). Mirrors the
// move-out over-record shape.
func elecRecoveryAnchor() *meterreading.MeterReading {
	reason := meterreading.AnchorReasonReadingRecovery
	src := uuid.New()
	recorded := 1500
	return &meterreading.MeterReading{
		ID:                      uuid.New(),
		AnchorReason:            &reason,
		RecoverySourceReadingID: &src,
		ElectricityPrevious:     1240,
		ElectricityCurrent:      1240, // usage 0 (re-anchored); recorded 1500 → over-record
		ElectricityRecorded:     &recorded,
		WaterPrevious:           220,
		WaterCurrent:            220, // baseline carry, not corrected
	}
}

// realConsumption is the coexisting non-anchor MONTHLY row for the same month:
// water advanced 220→228 (8 units), electricity flat at the re-anchored 1240.
func realConsumption() *meterreading.MeterReading {
	return &meterreading.MeterReading{
		ID:                  uuid.New(),
		ElectricityPrevious: 1240,
		ElectricityCurrent:  1240,
		WaterPrevious:       220,
		WaterCurrent:        228,
	}
}

func TestProjectRecoveryUsageOverlay_ElecRecovery_BillsRealWater(t *testing.T) {
	rec := elecRecoveryAnchor()
	got := ProjectRecoveryUsageOverlay(rec, realConsumption())

	// Unaffected utility (water) adopts the real consumption usage.
	if got.WaterPrevious != 220 || got.WaterCurrent != 228 {
		t.Errorf("water = %d→%d, want 220→228 (real usage)", got.WaterPrevious, got.WaterCurrent)
	}
	if got.WaterUsed() != 8 {
		t.Errorf("water usage = %d, want 8", got.WaterUsed())
	}
	// Affected utility (electricity) keeps the anchor's 0-usage re-baseline.
	if got.ElectricityPrevious != 1240 || got.ElectricityCurrent != 1240 || got.ElectricityUsed() != 0 {
		t.Errorf("elec = %d→%d (usage %d), want 1240→1240 usage 0", got.ElectricityPrevious, got.ElectricityCurrent, got.ElectricityUsed())
	}
	// Identity + recovery metadata preserved (adjustment FK / resolver / lineage).
	if got.ID != rec.ID {
		t.Errorf("ID changed: got %v, want %v (recovery identity must survive)", got.ID, rec.ID)
	}
	if got.AnchorReason == nil || *got.AnchorReason != meterreading.AnchorReasonReadingRecovery {
		t.Errorf("AnchorReason lost: %v", got.AnchorReason)
	}
	if got.RecoverySourceReadingID == nil || *got.RecoverySourceReadingID != *rec.RecoverySourceReadingID {
		t.Errorf("RecoverySourceReadingID lost: %v", got.RecoverySourceReadingID)
	}
	if got.ElectricityRecorded == nil || *got.ElectricityRecorded != 1500 {
		t.Errorf("ElectricityRecorded lost: %v (needed for refund)", got.ElectricityRecorded)
	}
	// Must NOT mutate the input.
	if rec.WaterCurrent != 220 {
		t.Errorf("input mutated: rec.WaterCurrent = %d, want 220", rec.WaterCurrent)
	}
}

func TestProjectRecoveryUsageOverlay_WaterRecovery_BillsRealElec(t *testing.T) {
	reason := meterreading.AnchorReasonReadingRecovery
	src := uuid.New()
	recorded := 300
	rec := &meterreading.MeterReading{
		ID:                      uuid.New(),
		AnchorReason:            &reason,
		RecoverySourceReadingID: &src,
		ElectricityPrevious:     500,
		ElectricityCurrent:      500, // baseline carry (not corrected)
		WaterPrevious:           240,
		WaterCurrent:            240, // usage 0; recorded 300 → over-record
		WaterRecorded:           &recorded,
	}
	consumption := &meterreading.MeterReading{
		ElectricityPrevious: 500,
		ElectricityCurrent:  515, // 15 units of real electricity usage
		WaterPrevious:       240,
		WaterCurrent:        240,
	}
	got := ProjectRecoveryUsageOverlay(rec, consumption)

	if got.ElectricityUsed() != 15 {
		t.Errorf("elec usage = %d, want 15 (real)", got.ElectricityUsed())
	}
	if got.WaterUsed() != 0 {
		t.Errorf("water usage = %d, want 0 (recovery-anchored)", got.WaterUsed())
	}
	if got.WaterRecorded == nil || *got.WaterRecorded != 300 {
		t.Errorf("WaterRecorded lost: %v (needed for refund)", got.WaterRecorded)
	}
}

func TestProjectRecoveryUsageOverlay_BothAffected_NoRealUsageAdopted(t *testing.T) {
	reason := meterreading.AnchorReasonReadingRecovery
	src := uuid.New()
	er, wr := 1500, 300
	rec := &meterreading.MeterReading{
		ID: uuid.New(), AnchorReason: &reason, RecoverySourceReadingID: &src,
		ElectricityPrevious: 1240, ElectricityCurrent: 1240, ElectricityRecorded: &er,
		WaterPrevious: 240, WaterCurrent: 240, WaterRecorded: &wr,
	}
	consumption := &meterreading.MeterReading{
		ElectricityPrevious: 1240, ElectricityCurrent: 1299,
		WaterPrevious: 240, WaterCurrent: 299,
	}
	got := ProjectRecoveryUsageOverlay(rec, consumption)
	if got.ElectricityUsed() != 0 || got.WaterUsed() != 0 {
		t.Errorf("both affected → both usage 0, got elec %d water %d", got.ElectricityUsed(), got.WaterUsed())
	}
}

func TestProjectRecoveryUsageOverlay_NilConsumption_NoOp(t *testing.T) {
	rec := elecRecoveryAnchor()
	got := ProjectRecoveryUsageOverlay(rec, nil)
	if got != rec {
		t.Errorf("nil consumption must return the recovery unchanged (same pointer)")
	}
}

func TestProjectRecoveryUsageOverlay_NonAnchorReading_NoOp(t *testing.T) {
	plain := realConsumption() // anchor_reason nil
	got := ProjectRecoveryUsageOverlay(plain, elecRecoveryAnchor())
	if got != plain {
		t.Errorf("non-anchor reading must be returned unchanged (same pointer)")
	}
}
