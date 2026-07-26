//go:build integration

package billing

import (
	"context"
	"testing"

	"nana/internal/meterreading"
	"nana/internal/shared/database"
	"nana/internal/testutil/fixtures"
	"nana/internal/testutil/testdb"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Replace Meter — pre-UI E2E checkpoint (owner GO 2026-07-21). Proves the whole
// billing pipeline on a real DB: one metered line = one charge/authority, the
// frozen usage_breakdown explains the total from the SAME CanonicalPeriodUsage,
// Monthly=Settlement parity, stuck-meter measured-only, multi-replacement, the
// R-b collision guard (never silently under-bill), and bill-immutability no-drift.

type rmEnv struct {
	svc      BillingService
	billRepo BillingRepository
	db       *gorm.DB
	roomID   uuid.UUID
}

func newReplaceMeterEnv(t *testing.T, db *gorm.DB, roomNum string) *rmEnv {
	t.Helper()
	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), roomNum)
	tn := fixtures.SeedTenant(t, db)
	c := fixtures.SeedContract(t, db, tn.ID.String(), rm.ID.String(), 6) // elec 800, water 1800
	billRepo := NewBillingRepository(db)
	meters := meterreading.NewMeterReadingRepository(db)
	svc := NewBillingService(billRepo, NewBillAuditRepository(db), &integrationContractStub{c: c}, meters, &integrationConfigStub{}, nil, database.NewTxManager(db))
	return &rmEnv{svc: svc, billRepo: billRepo, db: db, roomID: rm.ID}
}

func (e *rmEnv) seedReading(t *testing.T, month string, ep, ec, wp, wc int) *meterreading.MeterReading {
	t.Helper()
	m := &meterreading.MeterReading{
		RoomID: e.roomID, ReadingType: meterreading.ReadingTypeMonthly, BillingMonth: &month,
		ElectricityPrevious: ep, ElectricityCurrent: ec, WaterPrevious: wp, WaterCurrent: wc,
	}
	if err := e.db.Create(m).Error; err != nil {
		t.Fatalf("seed reading %s: %v", month, err)
	}
	return m
}

// seedReplacement builds a PHYSICAL_REPLACEMENT event via the domain factory
// (frozen old_previous snapshot from `latest`) and persists it.
func (e *rmEnv) seedReplacement(t *testing.T, latest *meterreading.MeterReading, month string, elec, water meterreading.ReplacementUtilityInput) *meterreading.MeterReading {
	t.Helper()
	a, err := meterreading.NewReplacementAnchor(e.roomID, latest, month, "เปลี่ยนมิเตอร์ (E2E)", elec, water)
	if err != nil {
		t.Fatalf("NewReplacementAnchor: %v", err)
	}
	if err := e.db.Create(a).Error; err != nil {
		t.Fatalf("persist replacement anchor: %v", err)
	}
	return a
}

func (e *rmEnv) bill(t *testing.T, contractID uuid.UUID, month string, meterReadingID uuid.UUID) (*BillWithRelations, error) {
	t.Helper()
	created, err := e.svc.CreateMonthlyBill(context.Background(), CreateMonthlyBillRequest{
		ContractID: contractID.String(), BillingMonth: month, MeterReadingID: meterReadingID.String(),
	}, nil)
	if err != nil {
		return nil, err
	}
	full, ferr := e.billRepo.FindByIDWithRelations(context.Background(), created.Bill.ID)
	if ferr != nil {
		t.Fatalf("reload bill: %v", ferr)
	}
	return full, nil
}

func intp(v int) *int { return &v }

// assertBreakdownSums is the load-bearing invariant: the explanation and the
// charge came from ONE computation — sum(segments.usage) == the line Quantity.
func assertBreakdownSums(t *testing.T, li *BillLineItem) {
	t.Helper()
	if li.UsageBreakdown.SumUsage() != li.Quantity {
		t.Errorf("breakdown drift: sum(usage)=%d != line quantity=%d (%+v)", li.UsageBreakdown.SumUsage(), li.Quantity, li.UsageBreakdown)
	}
}

// S1 — Normal → Monthly: byte-identical to before (no breakdown, meter pair kept).
func TestReplaceMeter_E2E_NormalMonthly(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)
	e := newReplaceMeterEnv(t, db, "RM-N1")
	cid := contractIDForRoom(t, db, e.roomID)
	r := e.seedReading(t, "2026-07", 1000, 1200, 50, 90)
	bill, err := e.bill(t, cid, "2026-07", r.ID)
	if err != nil {
		t.Fatalf("bill: %v", err)
	}
	elec := lineByType(bill.LineItems, LineItemElectricity)
	if elec.Quantity != 200 || len(elec.UsageBreakdown) != 0 || elec.MeterPrevious == nil {
		t.Errorf("normal elec = qty %d, breakdown %d, meterPrev %v — want 200 / 0 / set", elec.Quantity, len(elec.UsageBreakdown), elec.MeterPrevious)
	}
}

// S2 — Replacement → Monthly: qty 200 with a 2-segment frozen breakdown
// (120 old tail + 80 new reading); water unaffected (per-utility).
func TestReplaceMeter_E2E_ReplacementMonthly(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)
	e := newReplaceMeterEnv(t, db, "RM-R1")
	cid := contractIDForRoom(t, db, e.roomID)
	prior := e.seedReading(t, "2026-06", 900, 1000, 200, 200)
	e.seedReplacement(t, prior, "2026-07",
		meterreading.ReplacementUtilityInput{Replaced: true, OldFinal: intp(1120), NewInitial: 350},
		meterreading.ReplacementUtilityInput{Replaced: false})
	cons := e.seedReading(t, "2026-07", 350, 430, 200, 210) // new-meter segment 80; water 10 (unaffected)

	bill, err := e.bill(t, cid, "2026-07", cons.ID)
	if err != nil {
		t.Fatalf("bill: %v", err)
	}
	elec := lineByType(bill.LineItems, LineItemElectricity)
	if elec.Quantity != 200 || elec.Amount != 200*800 {
		t.Errorf("elec = qty %d / ฿%d, want 200 / %d", elec.Quantity, elec.Amount, 200*800)
	}
	if len(elec.UsageBreakdown) != 2 {
		t.Fatalf("want 2 breakdown segments, got %d: %+v", len(elec.UsageBreakdown), elec.UsageBreakdown)
	}
	if s := elec.UsageBreakdown[0]; s.Kind != "REPLACEMENT_OLD_TAIL" || s.Previous != 1000 || s.Current != 1120 || s.Usage != 120 {
		t.Errorf("segment[0] = %+v, want old tail 1000→1120 = 120", s)
	}
	if s := elec.UsageBreakdown[1]; s.Kind != "READING" || s.Previous != 350 || s.Current != 430 || s.Usage != 80 {
		t.Errorf("segment[1] = %+v, want reading 350→430 = 80", s)
	}
	assertBreakdownSums(t, elec)
	if elec.MeterPrevious != nil || elec.MeterCurrent != nil {
		t.Error("replacement line must clear the single meter pair (breakdown supersedes)")
	}
	water := lineByType(bill.LineItems, LineItemWater)
	if water.Quantity != 10 || len(water.UsageBreakdown) != 0 {
		t.Errorf("water (unaffected) = qty %d, breakdown %d, want 10 / 0", water.Quantity, len(water.UsageBreakdown))
	}
}

// S3 — Stuck meter: old_final == old_previous → tail 0 → measured-only (80).
func TestReplaceMeter_E2E_StuckMeterMeasuredOnly(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)
	e := newReplaceMeterEnv(t, db, "RM-S1")
	cid := contractIDForRoom(t, db, e.roomID)
	prior := e.seedReading(t, "2026-06", 900, 1000, 0, 0)
	e.seedReplacement(t, prior, "2026-07",
		meterreading.ReplacementUtilityInput{Replaced: true, OldFinal: intp(1000), NewInitial: 350}, // stuck
		meterreading.ReplacementUtilityInput{Replaced: false})
	cons := e.seedReading(t, "2026-07", 350, 430, 0, 0)
	bill, err := e.bill(t, cid, "2026-07", cons.ID)
	if err != nil {
		t.Fatalf("bill: %v", err)
	}
	elec := lineByType(bill.LineItems, LineItemElectricity)
	if elec.Quantity != 80 {
		t.Errorf("stuck elec qty = %d, want 80 (tail 0 + 80), NOT reconstructed", elec.Quantity)
	}
	if elec.UsageBreakdown[0].Usage != 0 {
		t.Errorf("stuck tail usage = %d, want 0", elec.UsageBreakdown[0].Usage)
	}
	assertBreakdownSums(t, elec)
}

// S4 — two replacements in one period → 230 with a 3-segment breakdown.
func TestReplaceMeter_E2E_TwoReplacements(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)
	e := newReplaceMeterEnv(t, db, "RM-2X")
	cid := contractIDForRoom(t, db, e.roomID)
	prior := e.seedReading(t, "2026-06", 900, 1000, 0, 0)
	r1 := e.seedReplacement(t, prior, "2026-07",
		meterreading.ReplacementUtilityInput{Replaced: true, OldFinal: intp(1120), NewInitial: 350},
		meterreading.ReplacementUtilityInput{Replaced: false})
	e.seedReplacement(t, r1, "2026-07", // old_previous snapshots r1's current (350)
		meterreading.ReplacementUtilityInput{Replaced: true, OldFinal: intp(410), NewInitial: 20},
		meterreading.ReplacementUtilityInput{Replaced: false})
	cons := e.seedReading(t, "2026-07", 20, 70, 0, 0)
	bill, err := e.bill(t, cid, "2026-07", cons.ID)
	if err != nil {
		t.Fatalf("bill: %v", err)
	}
	elec := lineByType(bill.LineItems, LineItemElectricity)
	if elec.Quantity != 230 {
		t.Errorf("2× elec qty = %d, want 230 (120+60+50)", elec.Quantity)
	}
	if len(elec.UsageBreakdown) != 3 {
		t.Fatalf("want 3 segments, got %d", len(elec.UsageBreakdown))
	}
	assertBreakdownSums(t, elec)
}

// S5 — Recovery + Replacement, same utility, same month → explicit collision.
// The replacement event persists fine; only the BILL is refused (never under-bills).
func TestReplaceMeter_E2E_RecoveryReplacementCollision(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)
	e := newReplaceMeterEnv(t, db, "RM-COL")
	cid := contractIDForRoom(t, db, e.roomID)
	prior := e.seedReading(t, "2026-06", 900, 1000, 0, 0)

	// Recovery on electricity (recorded 1500 > physical 1240).
	rr := meterreading.AnchorReasonReadingRecovery
	note := "จดไฟเกิน"
	rec := 1500
	month := "2026-07"
	recovery := &meterreading.MeterReading{
		RoomID: e.roomID, ReadingType: meterreading.ReadingTypeMonthly, BillingMonth: &month,
		AnchorReason: &rr, AnchorNote: &note, RecoverySourceReadingID: &prior.ID,
		ElectricityPrevious: 1240, ElectricityCurrent: 1240, ElectricityRecorded: &rec,
		WaterPrevious: 0, WaterCurrent: 0,
	}
	if err := db.Create(recovery).Error; err != nil {
		t.Fatalf("seed recovery: %v", err)
	}
	// Replacement on electricity (same utility, same month) — always recordable.
	e.seedReplacement(t, prior, month,
		meterreading.ReplacementUtilityInput{Replaced: true, OldFinal: intp(1120), NewInitial: 350},
		meterreading.ReplacementUtilityInput{Replaced: false})
	cons := e.seedReading(t, month, 350, 430, 0, 0)

	_, err := e.bill(t, cid, month, cons.ID)
	if err != ErrRecoveryReplacementCollision {
		t.Fatalf("want ErrRecoveryReplacementCollision (surface, not silent under-bill), got %v", err)
	}
}

// S6 — Monthly=Settlement parity: both bill through the SAME CanonicalPeriodUsage,
// so identical facts (a MONTHLY consumption row vs an EXIT row) yield identical
// usage + segments. Proven at the shared domain function (settlement calls it via
// SetMeteredLineUsage; slice 6).
func TestReplaceMeter_E2E_MonthlySettlementParity(t *testing.T) {
	repl := &meterreading.MeterReading{
		ElectricityReplaced: true, ElectricityOldPrevious: intp(1000), ElectricityOldFinal: intp(1120),
	}
	repl.AnchorReason = func() *meterreading.AnchorReason { r := meterreading.AnchorReasonPhysicalReplacement; return &r }()
	monthly := &meterreading.MeterReading{ReadingType: meterreading.ReadingTypeMonthly, ElectricityPrevious: 350, ElectricityCurrent: 430}
	exit := &meterreading.MeterReading{ReadingType: meterreading.ReadingTypeExit, ElectricityPrevious: 350, ElectricityCurrent: 430}
	me, _ := meterreading.CanonicalPeriodUsage(monthly, []*meterreading.MeterReading{repl})
	se, _ := meterreading.CanonicalPeriodUsage(exit, []*meterreading.MeterReading{repl})
	if me.Total != se.Total || me.Total != 200 || len(me.Segments) != len(se.Segments) {
		t.Errorf("parity broken: monthly total %d / settlement total %d (want both 200, equal segments)", me.Total, se.Total)
	}
}

// S7 — no drift: a persisted replacement bill keeps its frozen breakdown even
// after the replacement event is later soft-deleted (bill immutability).
func TestReplaceMeter_E2E_FinalizedBreakdownNoDrift(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)
	e := newReplaceMeterEnv(t, db, "RM-ND")
	cid := contractIDForRoom(t, db, e.roomID)
	prior := e.seedReading(t, "2026-06", 900, 1000, 0, 0)
	anchor := e.seedReplacement(t, prior, "2026-07",
		meterreading.ReplacementUtilityInput{Replaced: true, OldFinal: intp(1120), NewInitial: 350},
		meterreading.ReplacementUtilityInput{Replaced: false})
	cons := e.seedReading(t, "2026-07", 350, 430, 0, 0)
	bill, err := e.bill(t, cid, "2026-07", cons.ID)
	if err != nil {
		t.Fatalf("bill: %v", err)
	}
	before := lineByType(bill.LineItems, LineItemElectricity).Quantity

	// Mutate live meter state: soft-delete the replacement event.
	if err := db.Delete(anchor).Error; err != nil {
		t.Fatalf("soft-delete anchor: %v", err)
	}
	// Re-read the SAME persisted bill — its frozen evidence must NOT move.
	reloaded, _ := e.billRepo.FindByIDWithRelations(context.Background(), bill.ID)
	elec := lineByType(reloaded.LineItems, LineItemElectricity)
	if elec.Quantity != before || elec.Quantity != 200 {
		t.Errorf("drift: reloaded qty %d, want frozen %d (200)", elec.Quantity, before)
	}
	if elec.UsageBreakdown.SumUsage() != 200 || len(elec.UsageBreakdown) != 2 {
		t.Errorf("frozen breakdown drifted after event delete: %+v", elec.UsageBreakdown)
	}
}

// S8 (check 1) — correction re-snapshots FRESH physical truth; the VOID keeps its
// own frozen breakdown; NO path copies the old usage_breakdown stale.
func TestReplaceMeter_E2E_CorrectionFreshBreakdown(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)
	e := newReplaceMeterEnv(t, db, "RM-CORR")
	cid := contractIDForRoom(t, db, e.roomID)
	prior := e.seedReading(t, "2026-06", 900, 1000, 0, 0)
	e.seedReplacement(t, prior, "2026-07",
		meterreading.ReplacementUtilityInput{Replaced: true, OldFinal: intp(1120), NewInitial: 350},
		meterreading.ReplacementUtilityInput{Replaced: false})
	cons := e.seedReading(t, "2026-07", 350, 430, 0, 0)

	ctx := context.Background()
	created, err := e.svc.CreateMonthlyBill(ctx, CreateMonthlyBillRequest{
		ContractID: cid.String(), BillingMonth: "2026-07", MeterReadingID: cons.ID.String(),
	}, nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := e.svc.FinalizeBill(ctx, created.Bill.ID, nil); err != nil {
		t.Fatalf("finalize: %v", err)
	}

	// Physical truth changes AFTER the bill is finalized: the new meter ran further.
	if err := db.Model(&meterreading.MeterReading{}).Where("id = ?", cons.ID).Update("electricity_current", 450).Error; err != nil {
		t.Fatalf("update meter: %v", err)
	}

	corrected, err := e.svc.CorrectBill(ctx, created.Bill.ID, CorrectBillRequest{CorrectionReason: "meter re-read after finalize"}, nil)
	if err != nil {
		t.Fatalf("correct: %v", err)
	}
	// New DRAFT: FRESH breakdown reflecting the re-read (120 + (450-350)=100 = 220).
	newElec := lineByType(corrected.LineItems, LineItemElectricity)
	if newElec.Quantity != 220 || newElec.UsageBreakdown.SumUsage() != 220 || newElec.UsageBreakdown[1].Usage != 100 {
		t.Errorf("corrected elec = qty %d, breakdown %+v — want 220 (120+100), fresh re-read", newElec.Quantity, newElec.UsageBreakdown)
	}
	// Old bill: VOID + its ORIGINAL frozen breakdown (120+80=200), never stale-copied/mutated.
	old, _ := e.billRepo.FindByIDWithRelations(ctx, created.Bill.ID)
	if !old.IsVoid() {
		t.Errorf("old bill status = %s, want VOID", old.Status)
	}
	oldElec := lineByType(old.LineItems, LineItemElectricity)
	if oldElec.Quantity != 200 || oldElec.UsageBreakdown.SumUsage() != 200 || oldElec.UsageBreakdown[1].Usage != 80 {
		t.Errorf("VOID bill breakdown drifted: qty %d, %+v — want frozen 200 (120+80)", oldElec.Quantity, oldElec.UsageBreakdown)
	}
}

// S9 (check 4) — ordinary (no-replacement) Monthly stays unchanged: NO
// usage_breakdown, single meter pair kept, on BOTH utilities.
func TestReplaceMeter_E2E_NoReplacement_NoBreakdownBothUtilities(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)
	e := newReplaceMeterEnv(t, db, "RM-N2")
	cid := contractIDForRoom(t, db, e.roomID)
	r := e.seedReading(t, "2026-07", 1000, 1200, 40, 90)
	bill, err := e.bill(t, cid, "2026-07", r.ID)
	if err != nil {
		t.Fatalf("bill: %v", err)
	}
	for _, lt := range []LineItemType{LineItemElectricity, LineItemWater} {
		li := lineByType(bill.LineItems, lt)
		if len(li.UsageBreakdown) != 0 {
			t.Errorf("%s: ordinary bill must have NO usage_breakdown, got %+v", lt, li.UsageBreakdown)
		}
		if li.MeterPrevious == nil || li.MeterCurrent == nil {
			t.Errorf("%s: ordinary bill must keep the single meter pair", lt)
		}
	}
	if e := lineByType(bill.LineItems, LineItemElectricity); e.Quantity != 200 {
		t.Errorf("elec qty %d, want 200 (unchanged)", e.Quantity)
	}
	if w := lineByType(bill.LineItems, LineItemWater); w.Quantity != 50 {
		t.Errorf("water qty %d, want 50 (unchanged)", w.Quantity)
	}
}

// contractIDForRoom loads the active contract id for a room (test helper).
func contractIDForRoom(t *testing.T, db *gorm.DB, roomID uuid.UUID) uuid.UUID {
	t.Helper()
	var idStr string
	if err := db.Raw("SELECT id::text FROM contracts WHERE room_id = ? AND deleted_at IS NULL LIMIT 1", roomID).Scan(&idStr).Error; err != nil {
		t.Fatalf("contract for room: %v", err)
	}
	return uuid.MustParse(idStr)
}
