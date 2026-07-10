//go:build integration

package settlement

import (
	"context"
	"strings"
	"testing"
	"time"

	"nana/internal/billing"
	"nana/internal/meterreading"
	"nana/internal/moveout"
	"nana/internal/testutil/fixtures"
	"nana/internal/testutil/testdb"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Epic B — settlement recovery reconciliation, integration.
//
// These tests drive the REAL move-out → settlement chain (no seeding of the
// terminal ADJUSTMENT line) and prove the D1 deadlock is resolved: a recovery
// created while a settlement is in DRAFT blocks finalize (the shared gate),
// Regenerate emits the refund and clears the gate, and finalize then succeeds.
// Every test asserts the MONEY, not just that the gate cleared: the refund is
// priced at the SOURCE bill rate and the settlement total moves by exactly the
// refund (owner constraint #3).

// recoveryEnv is the lighter sibling of correctionEnv: it seeds the fixture and
// wires the real services but stops at PENDING_SETTLEMENT (no generate/finalize),
// so each test controls the generate → recovery → finalize timing itself.
type recoveryEnv struct {
	db         *gorm.DB
	moveOutSvc moveout.MoveOutService
	ctx        context.Context

	contractID  uuid.UUID
	roomID      uuid.UUID
	noticeID    uuid.UUID
	moveOutDate time.Time
}

func newRecoveryEnv(t *testing.T) *recoveryEnv {
	t.Helper()
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)

	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "SR-101")
	tn := fixtures.SeedTenant(t, db)
	c := fixtures.SeedContract(t, db, tn.ID.String(), rm.ID.String(), 8)
	fixtures.SeedProrateConfig(t, db, apt.ID.String(), 10000) // ฿100/day

	moveOutDate := truncateToDate(time.Now().UTC()).AddDate(0, 0, -5)
	fixtures.SeedExitMeter(t, db, rm.ID.String(), moveOutDate, 100, 20)
	notice := fixtures.SeedMoveOutNotice(t, db, c.ID.String(), moveOutDate)

	billSvc := buildRealSettlementServiceWithAudit(t, db, nil)
	return &recoveryEnv{
		db:          db,
		moveOutSvc:  buildRealMoveOutService(t, db, billSvc),
		ctx:         context.Background(),
		contractID:  c.ID,
		roomID:      rm.ID,
		noticeID:    notice.ID,
		moveOutDate: moveOutDate,
	}
}

// seedElecRecovery creates an F-APPLICABLE electricity over-record for the room:
// a source MONTHLY reading in sourceMonth, a FINALIZED source bill whose
// ELECTRICITY line carries sourceElecRate (the historical rate), and the
// READING_RECOVERY row pointing at that source with recorded > current. The
// forward credit the resolver must emit is (recorded - current) × sourceElecRate.
func (e *recoveryEnv) seedElecRecovery(t *testing.T, recoveryMonth, sourceMonth string, elecCurrent, elecRecorded int, sourceElecRate int64) *meterreading.MeterReading {
	t.Helper()
	src := &meterreading.MeterReading{
		RoomID: e.roomID, ReadingType: meterreading.ReadingTypeMonthly, BillingMonth: &sourceMonth,
		ElectricityPrevious: 900, ElectricityCurrent: elecRecorded, // the wrong-high value that was billed
		WaterPrevious: 100, WaterCurrent: 100,
	}
	if err := e.db.Create(src).Error; err != nil {
		t.Fatalf("seed source reading %s: %v", sourceMonth, err)
	}
	sb := &billing.Bill{ContractID: e.contractID, BillingMonth: sourceMonth, BillType: billing.BillTypeMonthly, Status: billing.BillStatusFinalized}
	if err := e.db.Create(sb).Error; err != nil {
		t.Fatalf("seed source bill %s: %v", sourceMonth, err)
	}
	sbLine := &billing.BillLineItem{
		BillID: sb.ID, LineType: billing.LineItemElectricity, Source: billing.LineItemSourceAuto,
		Description: "ค่าไฟฟ้า", Amount: int64(elecRecorded-900) * sourceElecRate, Quantity: elecRecorded - 900, UnitPrice: sourceElecRate, SortOrder: 2,
	}
	if err := e.db.Create(sbLine).Error; err != nil {
		t.Fatalf("seed source bill line %s: %v", sourceMonth, err)
	}

	reason := meterreading.AnchorReasonReadingRecovery
	note := "จดมิเตอร์ผิด " + recoveryMonth
	rec := &meterreading.MeterReading{
		RoomID: e.roomID, ReadingType: meterreading.ReadingTypeMonthly, BillingMonth: &recoveryMonth,
		RecoverySourceReadingID: &src.ID,
		ElectricityPrevious:     elecCurrent, ElectricityCurrent: elecCurrent, // prev=curr (recovery invariant)
		WaterPrevious: 100, WaterCurrent: 100,
		ElectricityRecorded: &elecRecorded,
		AnchorReason:        &reason, AnchorNote: &note,
	}
	if err := e.db.Create(rec).Error; err != nil {
		t.Fatalf("seed recovery %s: %v", recoveryMonth, err)
	}
	return rec
}

// seedSourcelessRecovery creates a re-anchor-only recovery (no source) — the
// ontology says it earns no forward credit and must NOT block finalize.
func (e *recoveryEnv) seedSourcelessRecovery(t *testing.T, recoveryMonth string, elecCurrent, elecRecorded int) *meterreading.MeterReading {
	t.Helper()
	reason := meterreading.AnchorReasonReadingRecovery
	note := "ปรับฐานไม่มีต้นทาง"
	rec := &meterreading.MeterReading{
		RoomID: e.roomID, ReadingType: meterreading.ReadingTypeMonthly, BillingMonth: &recoveryMonth,
		ElectricityPrevious: elecCurrent, ElectricityCurrent: elecCurrent,
		WaterPrevious: 100, WaterCurrent: 100,
		ElectricityRecorded: &elecRecorded,
		AnchorReason:        &reason, AnchorNote: &note,
	}
	if err := e.db.Create(rec).Error; err != nil {
		t.Fatalf("seed source-less recovery %s: %v", recoveryMonth, err)
	}
	return rec
}

// currentSettlementLines loads the notice's live settlement bill line items.
func (e *recoveryEnv) currentSettlementLines(t *testing.T) []billing.BillLineItem {
	t.Helper()
	var notice moveout.MoveOutNotice
	if err := e.db.Where("id = ?", e.noticeID).First(&notice).Error; err != nil {
		t.Fatalf("load notice: %v", err)
	}
	if notice.SettlementBillID == nil {
		t.Fatalf("notice has no settlement bill id")
	}
	var bill billing.Bill
	if err := e.db.Preload("LineItems").Where("id = ?", *notice.SettlementBillID).First(&bill).Error; err != nil {
		t.Fatalf("load settlement bill: %v", err)
	}
	return bill.LineItems
}

func sumAmounts(items []billing.BillLineItem) int64 {
	var s int64
	for _, li := range items {
		s += li.Amount
	}
	return s
}

func recoveryAdjustmentLines(items []billing.BillLineItem) []billing.BillLineItem {
	var out []billing.BillLineItem
	for _, li := range items {
		if li.LineType == billing.LineItemAdjustment && li.AdjustmentRecoveryReadingID != nil {
			out = append(out, li)
		}
	}
	return out
}

// TestSettlementRecovery_D1_RegenerateResolvesStaleDraft is the core Epic B
// proof: a recovery created after a settlement DRAFT blocks finalize, Regenerate
// emits the source-priced refund, the gate clears, finalize succeeds — and the
// settlement total moves by EXACTLY the refund.
func TestSettlementRecovery_D1_RegenerateResolvesStaleDraft(t *testing.T) {
	env := newRecoveryEnv(t)

	// 1. Generate the settlement DRAFT (no recovery yet) and snapshot the base.
	if _, err := env.moveOutSvc.GenerateSettlement(env.ctx, env.noticeID, moveout.RentModeProrated); err != nil {
		t.Fatalf("GenerateSettlement: %v", err)
	}
	baseSum := sumAmounts(env.currentSettlementLines(t))

	// 2. A recovery lands while the settlement is still DRAFT: elec recorded 1500
	//    vs physical 1200 → 300 units over, source rate 800 → refund −240,000.
	const sourceRate int64 = 800
	const overUnits = 300
	const wantRefund = int64(-overUnits) * sourceRate // −240,000
	rec := env.seedElecRecovery(t, "2026-07", "2025-11", 1200, 1500, sourceRate)

	// 3. Finalize is now BLOCKED by the shared freshness gate (the D1 symptom).
	_, err := env.moveOutSvc.FinalizeSettlement(env.ctx, env.noticeID)
	if err == nil {
		t.Fatal("expected FinalizeSettlement to be blocked by the freshness gate (D1), got nil")
	}
	if !strings.Contains(err.Error(), billing.ErrBillStaleAfterRecovery.Error()) {
		t.Fatalf("expected stale-after-recovery error, got: %v", err)
	}

	// 4. Regenerate discovers the recovery and emits the refund.
	if _, err := env.moveOutSvc.RegenerateSettlement(env.ctx, env.noticeID, ""); err != nil {
		t.Fatalf("RegenerateSettlement: %v", err)
	}
	lines := env.currentSettlementLines(t)

	// MONEY — prove the number, not just the unblock (owner constraint #3):
	adj := recoveryAdjustmentLines(lines)
	if len(adj) != 1 {
		t.Fatalf("want exactly 1 recovery ADJUSTMENT line, got %d", len(adj))
	}
	got := adj[0]
	if got.Amount != wantRefund {
		t.Errorf("refund amount = %d, want %d", got.Amount, wantRefund)
	}
	if got.Quantity != overUnits {
		t.Errorf("refund quantity = %d, want %d (over-record units)", got.Quantity, overUnits)
	}
	if got.UnitPrice != sourceRate {
		t.Errorf("refund unit_price = %d, want %d (SOURCE bill rate, not current contract rate)", got.UnitPrice, sourceRate)
	}
	if got.AdjustmentRecoveryReadingID == nil || *got.AdjustmentRecoveryReadingID != rec.ID {
		t.Errorf("refund recovery FK = %v, want %s", got.AdjustmentRecoveryReadingID, rec.ID)
	}
	if got.AdjustmentUtility == nil || *got.AdjustmentUtility != billing.AdjustmentUtilityElectricity {
		t.Errorf("refund utility = %v, want ELECTRICITY", got.AdjustmentUtility)
	}
	// Total moved by EXACTLY the refund — nothing else shifted.
	if newSum := sumAmounts(lines); newSum != baseSum+wantRefund {
		t.Errorf("line total = %d, want base(%d) + refund(%d) = %d", newSum, baseSum, wantRefund, baseSum+wantRefund)
	}

	// 5. Finalize now succeeds — the gate is cleared.
	if _, err := env.moveOutSvc.FinalizeSettlement(env.ctx, env.noticeID); err != nil {
		t.Fatalf("FinalizeSettlement after regenerate should succeed, got: %v", err)
	}
}

// TestSettlementRecovery_MultiRecoverySameUtility_PerPairProvenance locks Fork X
// (DQ4b): two same-utility recoveries produce TWO ADJUSTMENT lines (one per
// recovery_id), not one merged line — the domain preserves provenance; the FE
// aggregates for presentation.
func TestSettlementRecovery_MultiRecoverySameUtility_PerPairProvenance(t *testing.T) {
	env := newRecoveryEnv(t)
	if _, err := env.moveOutSvc.GenerateSettlement(env.ctx, env.noticeID, moveout.RentModeProrated); err != nil {
		t.Fatalf("GenerateSettlement: %v", err)
	}
	baseSum := sumAmounts(env.currentSettlementLines(t))

	const rate int64 = 800
	rec1 := env.seedElecRecovery(t, "2026-06", "2025-10", 1200, 1500, rate) // 300 over → −240,000
	rec2 := env.seedElecRecovery(t, "2026-07", "2025-11", 1000, 1100, rate) // 100 over → −80,000
	wantRefund := int64(-300)*rate + int64(-100)*rate                       // −320,000

	if _, err := env.moveOutSvc.RegenerateSettlement(env.ctx, env.noticeID, ""); err != nil {
		t.Fatalf("RegenerateSettlement: %v", err)
	}
	lines := env.currentSettlementLines(t)
	adj := recoveryAdjustmentLines(lines)
	if len(adj) != 2 {
		t.Fatalf("want 2 per-recovery ADJUSTMENT lines (Fork X provenance), got %d", len(adj))
	}
	seen := map[uuid.UUID]bool{}
	for _, li := range adj {
		if li.AdjustmentRecoveryReadingID != nil {
			seen[*li.AdjustmentRecoveryReadingID] = true
		}
	}
	if !seen[rec1.ID] || !seen[rec2.ID] {
		t.Errorf("both recovery FKs must be preserved; got %v", seen)
	}
	if newSum := sumAmounts(lines); newSum != baseSum+wantRefund {
		t.Errorf("line total = %d, want base(%d) + refund(%d)", newSum, baseSum, wantRefund)
	}
}

// TestSettlementRecovery_SourcelessRecovery_NoEmissionFinalizeOK: a source-less
// recovery (re-anchor only) emits no refund and does NOT block finalize.
func TestSettlementRecovery_SourcelessRecovery_NoEmissionFinalizeOK(t *testing.T) {
	env := newRecoveryEnv(t)
	if _, err := env.moveOutSvc.GenerateSettlement(env.ctx, env.noticeID, moveout.RentModeProrated); err != nil {
		t.Fatalf("GenerateSettlement: %v", err)
	}
	env.seedSourcelessRecovery(t, "2026-07", 1200, 1500)

	// Regenerate is a no-op for refunds; no ADJUSTMENT line appears.
	if _, err := env.moveOutSvc.RegenerateSettlement(env.ctx, env.noticeID, ""); err != nil {
		t.Fatalf("RegenerateSettlement: %v", err)
	}
	if adj := recoveryAdjustmentLines(env.currentSettlementLines(t)); len(adj) != 0 {
		t.Fatalf("source-less recovery must emit no refund, got %d ADJUSTMENT lines", len(adj))
	}
	// And finalize is not blocked.
	if _, err := env.moveOutSvc.FinalizeSettlement(env.ctx, env.noticeID); err != nil {
		t.Fatalf("finalize should not be blocked by a source-less recovery, got: %v", err)
	}
}

// TestSettlementRecovery_IdempotentRegenerate: regenerating twice yields the same
// lines and the same total — the refund is re-derived from source-of-truth, never
// accumulated (void-not-append).
func TestSettlementRecovery_IdempotentRegenerate(t *testing.T) {
	env := newRecoveryEnv(t)
	if _, err := env.moveOutSvc.GenerateSettlement(env.ctx, env.noticeID, moveout.RentModeProrated); err != nil {
		t.Fatalf("GenerateSettlement: %v", err)
	}
	env.seedElecRecovery(t, "2026-07", "2025-11", 1200, 1500, 800)

	if _, err := env.moveOutSvc.RegenerateSettlement(env.ctx, env.noticeID, ""); err != nil {
		t.Fatalf("RegenerateSettlement #1: %v", err)
	}
	sum1 := sumAmounts(env.currentSettlementLines(t))
	adj1 := len(recoveryAdjustmentLines(env.currentSettlementLines(t)))

	if _, err := env.moveOutSvc.RegenerateSettlement(env.ctx, env.noticeID, ""); err != nil {
		t.Fatalf("RegenerateSettlement #2: %v", err)
	}
	sum2 := sumAmounts(env.currentSettlementLines(t))
	adj2 := len(recoveryAdjustmentLines(env.currentSettlementLines(t)))

	if sum1 != sum2 {
		t.Errorf("total changed across idempotent regenerate: %d → %d", sum1, sum2)
	}
	if adj1 != 1 || adj2 != 1 {
		t.Errorf("want exactly 1 refund line each regenerate, got %d then %d", adj1, adj2)
	}
}

// TestSettlementRecovery_ZeroRateSource_NoBlockNoEmission locks review finding
// #1: a recovery whose SOURCE month charged rate 0 for the utility earned no
// money, so it is NOT F-applicable — the gate must NOT block finalize and no
// refund line is emitted. Without the predicate's `unit_price > 0` guard,
// discovery would flag the pair while emission (0 refund) writes nothing → the
// gate would deadlock finalize forever. This proves it does not.
func TestSettlementRecovery_ZeroRateSource_NoBlockNoEmission(t *testing.T) {
	env := newRecoveryEnv(t)
	if _, err := env.moveOutSvc.GenerateSettlement(env.ctx, env.noticeID, moveout.RentModeProrated); err != nil {
		t.Fatalf("GenerateSettlement: %v", err)
	}
	// Source elec charged at rate 0 → no money moved → not F-applicable.
	env.seedElecRecovery(t, "2026-07", "2025-11", 1200, 1500, 0)

	// Finalize must NOT be blocked — the zero-rate over-record is P-only.
	if _, err := env.moveOutSvc.FinalizeSettlement(env.ctx, env.noticeID); err != nil {
		t.Fatalf("zero-rate source must not block finalize (no deadlock), got: %v", err)
	}
	if adj := recoveryAdjustmentLines(env.currentSettlementLines(t)); len(adj) != 0 {
		t.Fatalf("zero-rate over-record must emit no refund, got %d ADJUSTMENT lines", len(adj))
	}
}

// TestSettlementRecovery_CorrectAfterFinalized: a recovery that lands AFTER the
// settlement is FINALIZED is reconciled through CorrectSettlement (the fallback
// trigger) — void+recreate, fresh refund emitted, money correct.
func TestSettlementRecovery_CorrectAfterFinalized(t *testing.T) {
	env := newRecoveryEnv(t)
	// Clean generate + finalize (no recovery yet).
	if _, err := env.moveOutSvc.GenerateSettlement(env.ctx, env.noticeID, moveout.RentModeProrated); err != nil {
		t.Fatalf("GenerateSettlement: %v", err)
	}
	baseSum := sumAmounts(env.currentSettlementLines(t))
	if _, err := env.moveOutSvc.FinalizeSettlement(env.ctx, env.noticeID); err != nil {
		t.Fatalf("FinalizeSettlement (clean): %v", err)
	}

	// Recovery lands after finalize.
	const rate int64 = 800
	const wantRefund = int64(-300) * rate
	env.seedElecRecovery(t, "2026-07", "2025-11", 1200, 1500, rate)

	// Correct reissues the settlement with the refund.
	if _, err := env.moveOutSvc.CorrectSettlement(env.ctx, env.noticeID,
		moveout.CorrectSettlementRequest{CorrectionReason: "พบว่าจดมิเตอร์เดือนก่อนผิด"}, nil); err != nil {
		t.Fatalf("CorrectSettlement: %v", err)
	}
	lines := env.currentSettlementLines(t)
	adj := recoveryAdjustmentLines(lines)
	if len(adj) != 1 {
		t.Fatalf("want 1 refund line after correction, got %d", len(adj))
	}
	if adj[0].Amount != wantRefund || adj[0].UnitPrice != rate {
		t.Errorf("refund = (amount %d, unit_price %d), want (%d, %d)", adj[0].Amount, adj[0].UnitPrice, wantRefund, rate)
	}
	if newSum := sumAmounts(lines); newSum != baseSum+wantRefund {
		t.Errorf("line total = %d, want base(%d) + refund(%d)", newSum, baseSum, wantRefund)
	}
}
