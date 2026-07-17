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

// TestSettlementRecovery_OverReadNoDoubleCount proves the MONEY for the
// exit-triggered over-read the owner raised, and that the exit-usage line and the
// recovery refund do NOT double-count.
//
// Numbers (satang; rate ฿8 = 800):
//   - meter physically: 12,000 → 12,500 = 500 real units over the whole period.
//   - the source month MIS-READ it as 12,000 → 13,500 and BILLED (+PAID) 1,500
//     units = ฿12,000.
//   - the recovery re-anchors physical truth to 12,500 (prev=curr, usage 0) and
//     records the over-read (recorded 13,500).
//   - the exit reading chains off the corrected baseline: prev=12,500, curr=12,500
//     → usage 0 (the domain rejects current<previous, so the exit can NEVER carry
//     the mis-read 13,500 as its previous — no phantom usage is possible).
//
// Correct settlement: elec USAGE line = 0 (nothing new consumed after the
// re-anchor) + a source-priced refund of (13,500-12,500)×8 = ฿8,000. The tenant
// paid ฿12,000, gets ฿8,000 back → net ฿4,000 = the 500 real units. The exit line
// and the refund cover disjoint facts (new consumption vs the source over-charge),
// so there is no double count.
func TestSettlementRecovery_OverReadNoDoubleCount(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)
	ctx := context.Background()

	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "OR-101")
	tn := fixtures.SeedTenant(t, db)
	c := fixtures.SeedContract(t, db, tn.ID.String(), rm.ID.String(), 8)
	fixtures.SeedProrateConfig(t, db, apt.ID.String(), 10000)

	const rate = int64(800)
	now := time.Now().UTC()
	cycleStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	sourceMonth := cycleStart.AddDate(0, -2, 0).Format("2006-01")
	recoveryMonth := cycleStart.Format("2006-01")
	moveOutDate := now.AddDate(0, 0, -3)

	// Source reading: mis-read 12,000 → 13,500.
	src := &meterreading.MeterReading{
		RoomID: rm.ID, ReadingType: meterreading.ReadingTypeMonthly, BillingMonth: &sourceMonth,
		ElectricityPrevious: 12000, ElectricityCurrent: 13500,
		WaterPrevious: 100, WaterCurrent: 100,
	}
	if err := db.Create(src).Error; err != nil {
		t.Fatalf("seed source: %v", err)
	}
	// PAID source bill charged the mis-read 1,500 units at rate (so it is NOT
	// absorbed by the settlement, and carries the S0 + refund rate basis).
	finalizedAt := now
	sb := &billing.Bill{
		ContractID: c.ID, BillingMonth: sourceMonth, BillType: billing.BillTypeMonthly,
		Status: billing.BillStatusPaid, TotalAmount: c.MonthlyRent + 1500*rate, FinalizedAt: &finalizedAt,
	}
	if err := db.Create(sb).Error; err != nil {
		t.Fatalf("seed source bill: %v", err)
	}
	if err := db.Create(&billing.BillLineItem{
		BillID: sb.ID, LineType: billing.LineItemElectricity, Source: billing.LineItemSourceAuto,
		Description: "ค่าไฟฟ้า", Amount: 1500 * rate, Quantity: 1500, UnitPrice: rate, SortOrder: 2,
	}).Error; err != nil {
		t.Fatalf("seed source line: %v", err)
	}

	// Recovery re-anchors to physical 12,500 (prev=curr, usage 0), records 13,500.
	reason := meterreading.AnchorReasonReadingRecovery
	note := "จดค่าไฟเดือนก่อนเกินจริง"
	recorded := 13500
	rec := &meterreading.MeterReading{
		RoomID: rm.ID, ReadingType: meterreading.ReadingTypeMonthly, BillingMonth: &recoveryMonth,
		RecoverySourceReadingID: &src.ID,
		ElectricityPrevious:     12500, ElectricityCurrent: 12500,
		WaterPrevious: 100, WaterCurrent: 100,
		ElectricityRecorded: &recorded,
		AnchorReason:        &reason, AnchorNote: &note,
	}
	if err := db.Create(rec).Error; err != nil {
		t.Fatalf("seed recovery: %v", err)
	}

	// Exit reading chains off the corrected baseline: 12,500 → 12,500 → usage 0.
	exit := &meterreading.MeterReading{
		RoomID: rm.ID, ReadingType: meterreading.ReadingTypeExit, ReadingDateActual: &moveOutDate,
		ElectricityPrevious: 12500, ElectricityCurrent: 12500,
		WaterPrevious: 100, WaterCurrent: 100,
	}
	if err := db.Create(exit).Error; err != nil {
		t.Fatalf("seed exit: %v", err)
	}
	notice := fixtures.SeedMoveOutNotice(t, db, c.ID.String(), moveOutDate)

	billSvc := buildRealSettlementServiceWithAudit(t, db, nil)
	moveOutSvc := buildRealMoveOutService(t, db, billSvc)
	if _, err := moveOutSvc.GenerateSettlement(ctx, notice.ID, moveout.RentModeFullMonthKeepDeposit); err != nil {
		t.Fatalf("GenerateSettlement: %v", err)
	}

	var reloaded moveout.MoveOutNotice
	if err := db.Where("id = ?", notice.ID).First(&reloaded).Error; err != nil || reloaded.SettlementBillID == nil {
		t.Fatalf("no settlement bill: %v", err)
	}
	var bill billing.Bill
	if err := db.Preload("LineItems").Where("id = ?", *reloaded.SettlementBillID).First(&bill).Error; err != nil {
		t.Fatalf("load settlement: %v", err)
	}

	var usageLines, refundLines []billing.BillLineItem
	for _, li := range bill.LineItems {
		switch {
		case li.LineType == billing.LineItemElectricity:
			usageLines = append(usageLines, li)
		case li.LineType == billing.LineItemAdjustment && li.AdjustmentRecoveryReadingID != nil:
			refundLines = append(refundLines, li)
		}
	}

	// Exit-usage line: exactly one, and ZERO (no phantom usage, no double count).
	if len(usageLines) != 1 {
		t.Fatalf("want 1 electricity usage line, got %d", len(usageLines))
	}
	if usageLines[0].Quantity != 0 || usageLines[0].Amount != 0 {
		t.Errorf("exit elec usage = %d units / %d satang, want 0/0 (usage is anchored to the re-anchor, not the mis-read)", usageLines[0].Quantity, usageLines[0].Amount)
	}
	// Refund: exactly one, (13,500-12,500)*800 = 800,000 satang = ฿8,000, at source rate.
	if len(refundLines) != 1 {
		t.Fatalf("want 1 recovery refund line, got %d", len(refundLines))
	}
	if got := refundLines[0].Amount; got != -800000 {
		t.Errorf("refund = %d satang, want -800000 (฿-8,000 = 1000 over-units × source ฿8)", got)
	}
	if refundLines[0].UnitPrice != rate || refundLines[0].Quantity != 1000 {
		t.Errorf("refund = %d units × %d, want 1000 × 800 (source rate)", refundLines[0].Quantity, refundLines[0].UnitPrice)
	}
	// Net electricity impact at settlement = usage(0) + refund(-8,000) = -฿8,000.
	// Combined with the ฿12,000 already paid on the source bill, the tenant's true
	// utility cost is ฿4,000 — the 500 real units. No unit is counted twice.
	if net := usageLines[0].Amount + refundLines[0].Amount; net != -800000 {
		t.Errorf("net elec at settlement = %d satang, want -800000 (no double count)", net)
	}
}

// TestSettlementRecovery_OverReadWithNewUsage is the stronger no-double-count
// proof: the tenant keeps consuming AFTER the re-anchor (physical 12,500 →
// 13,000 = 500 genuinely new units). The exit line owns those 500 NEW units; the
// refund owns the 1,000 PAST phantom units from the source over-charge. The two
// ranges are disjoint physical facts, so both appear in full with no overlap —
// confirming the exit row (not the recovery) owns the post-re-anchor meter calc,
// exactly as Monthly would bill the next cycle's real usage on top of the refund.
func TestSettlementRecovery_OverReadWithNewUsage(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)
	ctx := context.Background()

	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "OR-201")
	tn := fixtures.SeedTenant(t, db)
	c := fixtures.SeedContract(t, db, tn.ID.String(), rm.ID.String(), 8)
	fixtures.SeedProrateConfig(t, db, apt.ID.String(), 10000)

	const rate = int64(800)
	now := time.Now().UTC()
	cycleStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	sourceMonth := cycleStart.AddDate(0, -2, 0).Format("2006-01")
	recoveryMonth := cycleStart.Format("2006-01")
	moveOutDate := now.AddDate(0, 0, -3)

	src := &meterreading.MeterReading{
		RoomID: rm.ID, ReadingType: meterreading.ReadingTypeMonthly, BillingMonth: &sourceMonth,
		ElectricityPrevious: 12000, ElectricityCurrent: 13500, WaterPrevious: 100, WaterCurrent: 100,
	}
	if err := db.Create(src).Error; err != nil {
		t.Fatalf("seed source: %v", err)
	}
	finalizedAt := now
	sb := &billing.Bill{
		ContractID: c.ID, BillingMonth: sourceMonth, BillType: billing.BillTypeMonthly,
		Status: billing.BillStatusPaid, TotalAmount: c.MonthlyRent + 1500*rate, FinalizedAt: &finalizedAt,
	}
	if err := db.Create(sb).Error; err != nil {
		t.Fatalf("seed source bill: %v", err)
	}
	if err := db.Create(&billing.BillLineItem{
		BillID: sb.ID, LineType: billing.LineItemElectricity, Source: billing.LineItemSourceAuto,
		Description: "ค่าไฟฟ้า", Amount: 1500 * rate, Quantity: 1500, UnitPrice: rate, SortOrder: 2,
	}).Error; err != nil {
		t.Fatalf("seed source line: %v", err)
	}
	reason := meterreading.AnchorReasonReadingRecovery
	note := "จดค่าไฟเดือนก่อนเกินจริง"
	recorded := 13500
	rec := &meterreading.MeterReading{
		RoomID: rm.ID, ReadingType: meterreading.ReadingTypeMonthly, BillingMonth: &recoveryMonth,
		RecoverySourceReadingID: &src.ID,
		ElectricityPrevious:     12500, ElectricityCurrent: 12500, WaterPrevious: 100, WaterCurrent: 100,
		ElectricityRecorded: &recorded, AnchorReason: &reason, AnchorNote: &note,
	}
	if err := db.Create(rec).Error; err != nil {
		t.Fatalf("seed recovery: %v", err)
	}
	// Exit chains off the re-anchor and adds 500 NEW real units: 12,500 → 13,000.
	exit := &meterreading.MeterReading{
		RoomID: rm.ID, ReadingType: meterreading.ReadingTypeExit, ReadingDateActual: &moveOutDate,
		ElectricityPrevious: 12500, ElectricityCurrent: 13000, WaterPrevious: 100, WaterCurrent: 100,
	}
	if err := db.Create(exit).Error; err != nil {
		t.Fatalf("seed exit: %v", err)
	}
	notice := fixtures.SeedMoveOutNotice(t, db, c.ID.String(), moveOutDate)

	billSvc := buildRealSettlementServiceWithAudit(t, db, nil)
	moveOutSvc := buildRealMoveOutService(t, db, billSvc)
	if _, err := moveOutSvc.GenerateSettlement(ctx, notice.ID, moveout.RentModeFullMonthKeepDeposit); err != nil {
		t.Fatalf("GenerateSettlement: %v", err)
	}
	var reloaded moveout.MoveOutNotice
	if err := db.Where("id = ?", notice.ID).First(&reloaded).Error; err != nil || reloaded.SettlementBillID == nil {
		t.Fatalf("no settlement bill: %v", err)
	}
	var bill billing.Bill
	if err := db.Preload("LineItems").Where("id = ?", *reloaded.SettlementBillID).First(&bill).Error; err != nil {
		t.Fatalf("load settlement: %v", err)
	}

	var usage, refund *billing.BillLineItem
	for i := range bill.LineItems {
		li := &bill.LineItems[i]
		if li.LineType == billing.LineItemElectricity {
			usage = li
		}
		if li.LineType == billing.LineItemAdjustment && li.AdjustmentRecoveryReadingID != nil {
			refund = li
		}
	}
	if usage == nil || refund == nil {
		t.Fatalf("want both a usage line and a refund line; usage=%v refund=%v", usage, refund)
	}
	// Exit line owns the 500 NEW units (priced at the contract rate).
	if usage.Quantity != 500 {
		t.Errorf("exit elec usage = %d units, want 500 (new consumption after re-anchor)", usage.Quantity)
	}
	// Refund owns the 1,000 PAST phantom units at the source rate — disjoint.
	if refund.Quantity != 1000 || refund.Amount != -800000 {
		t.Errorf("refund = %d units / %d satang, want 1000 / -800000 (source over-charge)", refund.Quantity, refund.Amount)
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

// --- Cancel preserves recovery (owner-locked invariant, 2026-07-16) ---
//
// A Move-out cancellation cancels the WORKFLOW only. It does NOT invalidate the
// fact that a previous month's meter was over-recorded. So cancel removes the
// EXIT reading and VOIDs the settlement bill, but the READING_RECOVERY anchor —
// which represents meter truth, not the move-out — MUST survive, unmutated
// (append-only). After cancel the recovery is unreflected again, so the
// over-charge is refunded exactly once on the next cycle (a re-issued settlement
// or the next monthly bill, via the same shared resolver). This is an owner
// decision, not accidental: do NOT delete the anchor on cancel.
//
// Owner scenarios A/B/C (2026-07-16):
//   C (append-only)        — TestSettlementRecovery_CancelPreservesRecovery below.
//   A (cancel → monthly)   — the STRUCTURAL half (EXIT removed, settlement VOID,
//                            recovery survives + unreflected) is asserted below;
//                            the "monthly refund exactly once" half then follows
//                            from the UNCHANGED shared resolver — the surviving
//                            recovery is byte-identical to the one already proven
//                            by billing.TestCreateMonthlyBill_OverRecord_PersistsRefund
//                            (which seeds exactly this {recovery + billed source,
//                            unreflected} state and refunds once).
//   B (cancel → re-move-out) — TestSettlementRecovery_CancelThenReMoveOut_NoDuplicateRecovery.

func TestSettlementRecovery_CancelPreservesRecovery(t *testing.T) {
	env := newRecoveryEnv(t)
	if _, err := env.moveOutSvc.GenerateSettlement(env.ctx, env.noticeID, moveout.RentModeProrated); err != nil {
		t.Fatalf("GenerateSettlement: %v", err)
	}
	const sourceRate int64 = 800
	rec := env.seedElecRecovery(t, "2026-07", "2025-11", 1200, 1500, sourceRate)
	// Reflect the recovery on the settlement so we can prove cancel VOIDs it.
	if _, err := env.moveOutSvc.RegenerateSettlement(env.ctx, env.noticeID, ""); err != nil {
		t.Fatalf("RegenerateSettlement: %v", err)
	}
	if n := len(recoveryAdjustmentLines(env.currentSettlementLines(t))); n != 1 {
		t.Fatalf("precondition: settlement should carry the recovery refund before cancel, got %d", n)
	}
	var before meterreading.MeterReading
	if err := env.db.Where("id = ?", rec.ID).First(&before).Error; err != nil {
		t.Fatalf("load recovery before cancel: %v", err)
	}
	var notice moveout.MoveOutNotice
	if err := env.db.Where("id = ?", env.noticeID).First(&notice).Error; err != nil || notice.SettlementBillID == nil {
		t.Fatalf("load notice / settlement id: %v", err)
	}
	settlementBillID := *notice.SettlementBillID

	// --- CANCEL ---
	if _, err := env.moveOutSvc.Cancel(env.ctx, env.noticeID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	// 1. Recovery SURVIVES, not soft-deleted, not mutated (append-only, owner-locked).
	var after meterreading.MeterReading
	if err := env.db.Unscoped().Where("id = ?", rec.ID).First(&after).Error; err != nil {
		t.Fatalf("recovery row must still exist after cancel: %v", err)
	}
	if after.DeletedAt.Valid {
		t.Error("recovery must NOT be soft-deleted by cancel (append-only)")
	}
	if after.ElectricityCurrent != before.ElectricityCurrent ||
		after.ElectricityRecorded == nil || *after.ElectricityRecorded != *before.ElectricityRecorded ||
		after.AnchorReason == nil || *after.AnchorReason != meterreading.AnchorReasonReadingRecovery ||
		after.RecoverySourceReadingID == nil || *after.RecoverySourceReadingID != *before.RecoverySourceReadingID {
		t.Error("recovery must NOT be mutated by cancel (append-only)")
	}

	// 2. EXIT reading removed (the workflow artifact vanishes).
	var activeExit int64
	env.db.Model(&meterreading.MeterReading{}).
		Where("room_id = ? AND reading_type = ? AND deleted_at IS NULL", env.roomID, meterreading.ReadingTypeExit).
		Count(&activeExit)
	if activeExit != 0 {
		t.Errorf("EXIT reading must be removed by cancel, %d still active", activeExit)
	}

	// 3. Settlement bill VOID.
	var sb billing.Bill
	if err := env.db.Where("id = ?", settlementBillID).First(&sb).Error; err != nil {
		t.Fatalf("load settlement bill: %v", err)
	}
	if sb.Status != billing.BillStatusVoid {
		t.Errorf("settlement bill status = %s, want VOID", sb.Status)
	}

	// 4. Recovery is UNREFLECTED after cancel — no live (non-VOID) adjustment line
	//    reflects it, so the over-charge refunds exactly once on the next cycle.
	var reflected int64
	env.db.Table("bill_line_items AS li").
		Joins("JOIN bills b ON b.id = li.bill_id").
		Where("li.adjustment_recovery_reading_id = ? AND b.status <> ? AND li.deleted_at IS NULL",
			rec.ID, billing.BillStatusVoid).
		Count(&reflected)
	if reflected != 0 {
		t.Errorf("recovery must be unreflected after cancel, found %d live reflection(s)", reflected)
	}
}

func TestSettlementRecovery_CancelThenReMoveOut_NoDuplicateRecovery(t *testing.T) {
	env := newRecoveryEnv(t)
	if _, err := env.moveOutSvc.GenerateSettlement(env.ctx, env.noticeID, moveout.RentModeProrated); err != nil {
		t.Fatalf("GenerateSettlement: %v", err)
	}
	const sourceRate int64 = 800
	const wantRefund = int64(-(1500 - 1200)) * sourceRate
	rec := env.seedElecRecovery(t, "2026-07", "2025-11", 1200, 1500, sourceRate)
	if _, err := env.moveOutSvc.RegenerateSettlement(env.ctx, env.noticeID, ""); err != nil {
		t.Fatalf("RegenerateSettlement: %v", err)
	}
	if _, err := env.moveOutSvc.Cancel(env.ctx, env.noticeID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	// The tenant stays, then later moves out again. New notice + new exit for the
	// now-ACTIVE contract; the meter reads above the recovery baseline (no new
	// over-record), so NO new anchor is created.
	reDate := env.moveOutDate.AddDate(0, 0, 3)
	fixtures.SeedExitMeter(t, env.db, env.roomID.String(), reDate, 400, 40)
	notice2 := fixtures.SeedMoveOutNotice(t, env.db, env.contractID.String(), reDate)
	env.noticeID = notice2.ID

	if _, err := env.moveOutSvc.GenerateSettlement(env.ctx, env.noticeID, moveout.RentModeProrated); err != nil {
		t.Fatalf("re-move-out GenerateSettlement: %v", err)
	}
	if _, err := env.moveOutSvc.RegenerateSettlement(env.ctx, env.noticeID, ""); err != nil {
		t.Fatalf("re-move-out RegenerateSettlement: %v", err)
	}

	// No duplicate recovery row for the room.
	var recCount int64
	env.db.Model(&meterreading.MeterReading{}).
		Where("room_id = ? AND anchor_reason = ?", env.roomID, meterreading.AnchorReasonReadingRecovery).
		Count(&recCount)
	if recCount != 1 {
		t.Errorf("expected exactly 1 recovery row (no duplicate from re-move-out), got %d", recCount)
	}

	// The re-issued settlement reflects the SAME recovery exactly once — refund
	// not duplicated.
	adj := recoveryAdjustmentLines(env.currentSettlementLines(t))
	if len(adj) != 1 {
		t.Fatalf("re-move-out settlement must reflect the existing recovery exactly once, got %d lines", len(adj))
	}
	if adj[0].AdjustmentRecoveryReadingID == nil || *adj[0].AdjustmentRecoveryReadingID != rec.ID {
		t.Errorf("re-move-out settlement must reflect the SAME recovery %s, got %v", rec.ID, adj[0].AdjustmentRecoveryReadingID)
	}
	if adj[0].Amount != wantRefund {
		t.Errorf("re-move-out refund = %d, want %d (not duplicated)", adj[0].Amount, wantRefund)
	}
}
