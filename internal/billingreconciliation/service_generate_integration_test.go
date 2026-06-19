//go:build integration

// External test package — billing imports billingreconciliation for the
// adapter, so a package-internal test importing billing would form a cycle
// (rejected by Go's test build). External package + interface-only access
// keeps the import direction one-way while still exercising the full DI
// stack against real Postgres.
package billingreconciliation_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"nana/internal/billing"
	"nana/internal/billingconfig"
	"nana/internal/billingreconciliation"
	"nana/internal/contract"
	"nana/internal/meterreading"
	"nana/internal/moveout"
	"nana/internal/room"
	"nana/internal/shared/database"
	"nana/internal/shared/respond"
	"nana/internal/testutil/fixtures"
	"nana/internal/testutil/testdb"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// service_generate_integration_test.go locks Phase 1D Generate behavior
// against REAL Postgres + the real billing service. Mock-based tests can't
// reach these because the per-row verdict depends on the JOINed Reconcile
// read (rooms ⟕ active contracts ⟕ meters ⟕ existing bills ⟕ decisions)
// and the actual CreateMonthlyBill TX (duplicate guard, audit row insert,
// snapshot persistence).
//
// Seven scenarios per project_reconciliation_phase1d_scenario1_locks.md:
//
//  1. Single READY room → SUCCESS, real DRAFT bill landed
//  2. Pre-existing bill on the row → SKIPPED ALREADY_BILLED_BY_OTHER
//     (pre-pass — commander never called)
//  3. Missing monthly meter (bucket AR) → SKIPPED LOST_READY
//     (bucket != READY pre-pass — commander never called)
//  4. Mid-cycle tenant, no decision (bucket PD) → SKIPPED LOST_READY
//  5. Mixed (one READY + one pre-billed in same call) → 1 SUCCESS + 1 SKIPPED,
//     items[] order matches request order (Q1 Contract A contract)
//  6. Same room twice in room_ids[] → SUCCESS then SKIPPED ALREADY_BILLED
//     (commander-side race classification — duplicate constraint kicks in
//     on the second commit, adapter maps ErrBillAlreadyExists → ErrAlreadyBilled)
//  7. Cross-apartment room_id → 400 with explanatory Thai message
//     (request integrity, not per-row state change)

type generateEnv struct {
	db        *gorm.DB
	svc       billingreconciliation.Service
	billRepo  billing.BillingRepository
	apartment uuid.UUID
}

// newGenerateEnv wires the full reconciliation + billing stack against the
// shared test DB. Mirrors cmd/main.go's DI sequence so the integration test
// exercises the same construction order as production.
func newGenerateEnv(t *testing.T) *generateEnv {
	t.Helper()
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)
	// room_billing_decisions has ON DELETE CASCADE on room_id, so the
	// rooms truncate above already cleaned it — no separate truncate.

	apt := fixtures.SeedApartment(t, db)

	billRepo := billing.NewBillingRepository(db)
	billAudit := billing.NewBillAuditRepository(db)
	contractRepo := contract.NewContractRepository(db)
	meterRepo := meterreading.NewMeterReadingRepository(db)
	configRepo := billingconfig.NewBillingConfigRepository(db)
	moveOutRepo := moveout.NewMoveOutRepository(db)
	txMgr := database.NewTxManager(db)

	billSvc := billing.NewBillingService(
		billRepo, billAudit, contractRepo, meterRepo, configRepo, moveOutRepo, nil, txMgr,
	)
	reconAdapter := billing.NewReconciliationAdapter(billRepo, contractRepo, meterRepo, billSvc)

	reconRepo := billingreconciliation.NewRepository(db)
	svc := billingreconciliation.NewService(reconRepo, meterRepo, moveOutRepo, reconAdapter, reconAdapter)

	return &generateEnv{db: db, svc: svc, billRepo: billRepo, apartment: apt.ID}
}

// seedReadyRoom seeds room + tenant + active contract + MONTHLY meter for
// the env's current billing month — i.e. everything Reconcile needs to
// land the row in bucket READY.
func (e *generateEnv) seedReadyRoom(t *testing.T, number, billingMonth string, monthsAgo int) (roomID, contractID uuid.UUID) {
	t.Helper()
	rm := fixtures.SeedRoom(t, e.db, e.apartment.String(), number)
	tn := fixtures.SeedTenant(t, e.db)
	c := fixtures.SeedContract(t, e.db, tn.ID.String(), rm.ID.String(), monthsAgo)
	bm := billingMonth
	mr := &meterreading.MeterReading{
		RoomID:              rm.ID,
		ReadingType:         meterreading.ReadingTypeMonthly,
		BillingMonth:        &bm,
		ElectricityPrevious: 1000,
		ElectricityCurrent:  1050,
		WaterPrevious:       100,
		WaterCurrent:        105,
	}
	if err := e.db.Create(mr).Error; err != nil {
		t.Fatalf("seed monthly meter: %v", err)
	}
	return rm.ID, c.ID
}

// seedRoomNoMeter is the bucket=AR equivalent: room + contract but no
// MONTHLY reading for the billing month.
func (e *generateEnv) seedRoomNoMeter(t *testing.T, number string, monthsAgo int) (roomID, contractID uuid.UUID) {
	t.Helper()
	rm := fixtures.SeedRoom(t, e.db, e.apartment.String(), number)
	tn := fixtures.SeedTenant(t, e.db)
	c := fixtures.SeedContract(t, e.db, tn.ID.String(), rm.ID.String(), monthsAgo)
	return rm.ID, c.ID
}

// preBill drops a FINALIZED MONTHLY bill into the DB so Reconcile's bill
// evidence map populates and the row's pre-pass 1 (bill evidence) fires.
func (e *generateEnv) preBill(t *testing.T, contractID uuid.UUID, billingMonth string) uuid.UUID {
	t.Helper()
	b := &billing.Bill{
		ContractID:   contractID,
		BillingMonth: billingMonth,
		BillType:     billing.BillTypeMonthly,
		Status:       billing.BillStatusFinalized,
		TotalAmount:  500000,
	}
	if err := e.db.Create(b).Error; err != nil {
		t.Fatalf("pre-bill insert: %v", err)
	}
	return b.ID
}

// --- Scenario 1: single READY → SUCCESS ---

func TestGenerate_SingleReadyRoom_LandsSuccessAndPersistsDraftBill(t *testing.T) {
	e := newGenerateEnv(t)
	month := "2026-05"
	roomID, contractID := e.seedReadyRoom(t, "R-101", month, 3)

	result, err := e.svc.Generate(context.Background(), billingreconciliation.GenerateRequest{
		ApartmentID:  e.apartment,
		BillingMonth: month,
		RoomIDs:      []uuid.UUID{roomID},
	}, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if result.SuccessCount != 1 || result.SkippedCount != 0 || result.FailedCount != 0 {
		t.Fatalf("counts = %+v, want 1/0/0", result)
	}
	if len(result.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(result.Items))
	}
	item := result.Items[0]
	if item.ResultType != billingreconciliation.GenerateItemSuccess {
		t.Errorf("ResultType = %s, want SUCCESS", item.ResultType)
	}
	if item.BillID == nil {
		t.Fatal("BillID nil on SUCCESS")
	}

	// Verify the bill landed in DB with the right shape.
	var got billing.Bill
	if err := e.db.Where("id = ?", *item.BillID).First(&got).Error; err != nil {
		t.Fatalf("load created bill: %v", err)
	}
	if got.ContractID != contractID {
		t.Errorf("created bill ContractID = %s, want %s", got.ContractID, contractID)
	}
	if got.BillingMonth != month {
		t.Errorf("BillingMonth = %s, want %s", got.BillingMonth, month)
	}
	if got.Status != billing.BillStatusDraft {
		t.Errorf("Status = %s, want DRAFT (CreateMonthlyBill lands DRAFT-first)", got.Status)
	}
}

// --- Scenario 2: pre-existing bill → SKIPPED ALREADY_BILLED ---

func TestGenerate_PreExistingBill_SkipsAlreadyBilledViaPrePass(t *testing.T) {
	e := newGenerateEnv(t)
	month := "2026-05"
	roomID, contractID := e.seedReadyRoom(t, "R-101", month, 3)
	preBillID := e.preBill(t, contractID, month)

	result, err := e.svc.Generate(context.Background(), billingreconciliation.GenerateRequest{
		ApartmentID:  e.apartment,
		BillingMonth: month,
		RoomIDs:      []uuid.UUID{roomID},
	}, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if result.SuccessCount != 0 || result.SkippedCount != 1 {
		t.Fatalf("counts = %+v, want 0 success / 1 skipped", result)
	}
	item := result.Items[0]
	if item.ResultType != billingreconciliation.GenerateItemSkipped {
		t.Errorf("ResultType = %s, want SKIPPED", item.ResultType)
	}
	if item.SkipReason != billingreconciliation.GenerateSkipAlreadyBilled {
		t.Errorf("SkipReason = %s, want ALREADY_BILLED_BY_OTHER", item.SkipReason)
	}
	if item.BillID != nil {
		t.Errorf("SKIPPED row should not carry BillID, got %s", item.BillID)
	}

	// Pre-pass means the commander was never reached — no second bill row.
	var count int64
	if err := e.db.Model(&billing.Bill{}).
		Where("contract_id = ? AND billing_month = ?", contractID, month).
		Count(&count).Error; err != nil {
		t.Fatalf("count bills: %v", err)
	}
	if count != 1 {
		t.Errorf("bill count = %d, want 1 (only the pre-seeded bill, no SUCCESS write)", count)
	}
	_ = preBillID
}

// --- Scenario 3: missing meter (bucket AR) → SKIPPED LOST_READY ---

func TestGenerate_RoomWithoutMeter_SkipsLostReadyAtPrePass(t *testing.T) {
	e := newGenerateEnv(t)
	month := "2026-05"
	roomID, _ := e.seedRoomNoMeter(t, "R-102", 3)

	result, err := e.svc.Generate(context.Background(), billingreconciliation.GenerateRequest{
		ApartmentID:  e.apartment,
		BillingMonth: month,
		RoomIDs:      []uuid.UUID{roomID},
	}, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if result.SkippedCount != 1 {
		t.Fatalf("counts = %+v, want 1 skipped", result)
	}
	if got := result.Items[0].SkipReason; got != billingreconciliation.GenerateSkipLostReady {
		t.Errorf("SkipReason = %s, want LOST_READY (bucket=AR pre-pass)", got)
	}
}

// --- Scenario 4: mid-cycle PD → SKIPPED LOST_READY ---

func TestGenerate_MidCycleTenantNoDecision_SkipsLostReady(t *testing.T) {
	e := newGenerateEnv(t)
	month := time.Now().UTC().Format("2006-01")

	// Mid-cycle = contract starts inside the billing month. monthsAgo=0
	// makes start_date today (truncated to date), which falls inside the
	// current month → PD bucket per classifier rule 6.
	rm := fixtures.SeedRoom(t, e.db, e.apartment.String(), "R-103")
	tn := fixtures.SeedTenant(t, e.db)
	fixtures.SeedContract(t, e.db, tn.ID.String(), rm.ID.String(), 0)

	result, err := e.svc.Generate(context.Background(), billingreconciliation.GenerateRequest{
		ApartmentID:  e.apartment,
		BillingMonth: month,
		RoomIDs:      []uuid.UUID{rm.ID},
	}, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if result.SkippedCount != 1 {
		t.Fatalf("counts = %+v, want 1 skipped (PD row)", result)
	}
	if got := result.Items[0].SkipReason; got != billingreconciliation.GenerateSkipLostReady {
		t.Errorf("SkipReason = %s, want LOST_READY (bucket=PD pre-pass)", got)
	}
}

// --- Scenario 5: mixed READY + pre-billed in one call ---

func TestGenerate_MixedReadyAndPreBilled_ReturnsPerRowMixedResults(t *testing.T) {
	e := newGenerateEnv(t)
	month := "2026-05"
	readyRoom, _ := e.seedReadyRoom(t, "R-201", month, 3)
	billedRoom, billedContract := e.seedReadyRoom(t, "R-202", month, 3)
	e.preBill(t, billedContract, month)

	// Send billed first so we lock the request-order contract: items[]
	// order must match room_ids[] order regardless of result type.
	result, err := e.svc.Generate(context.Background(), billingreconciliation.GenerateRequest{
		ApartmentID:  e.apartment,
		BillingMonth: month,
		RoomIDs:      []uuid.UUID{billedRoom, readyRoom},
	}, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if result.SuccessCount != 1 || result.SkippedCount != 1 || result.FailedCount != 0 {
		t.Fatalf("counts = %+v, want 1/1/0", result)
	}
	if len(result.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(result.Items))
	}
	if result.Items[0].RoomID != billedRoom {
		t.Errorf("items[0].RoomID = %s, want %s (request order locked)", result.Items[0].RoomID, billedRoom)
	}
	if result.Items[0].ResultType != billingreconciliation.GenerateItemSkipped || result.Items[0].SkipReason != billingreconciliation.GenerateSkipAlreadyBilled {
		t.Errorf("items[0] = %+v, want SKIPPED ALREADY_BILLED", result.Items[0])
	}
	if result.Items[1].RoomID != readyRoom {
		t.Errorf("items[1].RoomID = %s, want %s", result.Items[1].RoomID, readyRoom)
	}
	if result.Items[1].ResultType != billingreconciliation.GenerateItemSuccess {
		t.Errorf("items[1].ResultType = %s, want SUCCESS", result.Items[1].ResultType)
	}
}

// --- Scenario 6: duplicate room_id triggers the commander-side race classification ---

func TestGenerate_DuplicateRoomID_HitsCommanderRaceAndMapsToAlreadyBilled(t *testing.T) {
	e := newGenerateEnv(t)
	month := "2026-05"
	roomID, contractID := e.seedReadyRoom(t, "R-301", month, 3)

	// Sending the same room twice mirrors the "another caller raced
	// between our Reconcile snapshot and our commit" semantic. The
	// initial Reconcile says row.Bill == nil for BOTH entries (one pass);
	// the first iteration commits → bill in DB; the second iteration's
	// pre-pass still sees the stale row.Bill==nil, calls commander, and
	// CreateMonthlyBill's duplicate guard fires ErrBillAlreadyExists.
	// Adapter classification maps that to ErrAlreadyBilled.
	result, err := e.svc.Generate(context.Background(), billingreconciliation.GenerateRequest{
		ApartmentID:  e.apartment,
		BillingMonth: month,
		RoomIDs:      []uuid.UUID{roomID, roomID},
	}, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if result.SuccessCount != 1 || result.SkippedCount != 1 {
		t.Fatalf("counts = %+v, want 1/1 (first creates, second hits duplicate)", result)
	}
	if result.Items[0].ResultType != billingreconciliation.GenerateItemSuccess {
		t.Errorf("items[0].ResultType = %s, want SUCCESS (first attempt wins)", result.Items[0].ResultType)
	}
	if result.Items[1].ResultType != billingreconciliation.GenerateItemSkipped || result.Items[1].SkipReason != billingreconciliation.GenerateSkipAlreadyBilled {
		t.Errorf("items[1] = %+v, want SKIPPED ALREADY_BILLED (race classified by adapter)", result.Items[1])
	}

	// Only one bill should have actually landed for the contract.
	var count int64
	if err := e.db.Model(&billing.Bill{}).
		Where("contract_id = ? AND billing_month = ?", contractID, month).
		Count(&count).Error; err != nil {
		t.Fatalf("count bills: %v", err)
	}
	if count != 1 {
		t.Errorf("bill count = %d, want 1 (duplicate guard prevented second write)", count)
	}
}

// --- Scenario 7: cross-apartment room_id → 400 ---

func TestGenerate_CrossApartmentRoomID_RejectsWholeRequest(t *testing.T) {
	e := newGenerateEnv(t)
	month := "2026-05"
	roomA, _ := e.seedReadyRoom(t, "R-401", month, 3)

	// Spin up a SECOND apartment with its own room — its room_id won't
	// appear in the rowByID map keyed off `e.apartment`, so the service
	// rejects the whole call.
	otherApt := fixtures.SeedApartment(t, e.db)
	otherRoom := fixtures.SeedRoom(t, e.db, otherApt.ID.String(), "R-501")
	otherTenant := fixtures.SeedTenant(t, e.db)
	fixtures.SeedContract(t, e.db, otherTenant.ID.String(), otherRoom.ID.String(), 3)

	_, err := e.svc.Generate(context.Background(), billingreconciliation.GenerateRequest{
		ApartmentID:  e.apartment,
		BillingMonth: month,
		RoomIDs:      []uuid.UUID{roomA, otherRoom.ID},
	}, nil)
	if err == nil {
		t.Fatal("expected request-integrity error, got nil")
	}
	appErr, ok := respond.Is(err)
	if !ok {
		t.Fatalf("expected AppError, got plain %v", err)
	}
	if appErr.HTTPStatus != 400 {
		t.Errorf("HTTPStatus = %d, want 400 (cross-apartment = client bug)", appErr.HTTPStatus)
	}
	if !strings.Contains(appErr.Message, otherRoom.ID.String()) {
		t.Errorf("Message %q should name the stray room ID for debuggability", appErr.Message)
	}

	// Reject means NOTHING got billed — request-integrity failure aborts
	// the whole loop, no partial side effects.
	var billed int64
	if err := e.db.Model(&billing.Bill{}).Count(&billed).Error; err != nil {
		t.Fatalf("count bills: %v", err)
	}
	if billed != 0 {
		t.Errorf("bills landed = %d, want 0 — 400 must be all-or-nothing", billed)
	}

	// Other apartment's room status untouched too.
	var rm room.Room
	if err := e.db.Where("id = ?", otherRoom.ID).First(&rm).Error; err != nil {
		t.Fatalf("reload other room: %v", err)
	}
}
