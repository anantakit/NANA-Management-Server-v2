//go:build integration

package payment

import (
	"context"
	"errors"
	"sync"
	"testing"

	"nana/internal/billing"
	"nana/internal/shared/database"
	"nana/internal/shared/money"
	"nana/internal/testutil/fixtures"
	"nana/internal/testutil/testdb"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// service_payment_integration_test.go pins the RecordBillPayment flow against
// real Postgres constraints that mock-based tests cannot reach (8 tests, T1–T8):
//
//   - T1 CASH happy path — response shape, bill PAID, payment row, audit row
//   - T2 TRANSFER + note — note persisted in response and DB
//   - T3 DRAFT bill rejected → ErrBillNotFinalized
//   - T4 PAID bill rejects double payment → ErrBillAlreadyPaid
//   - T5 VOID bill rejected → ErrBillNotFinalized
//   - T6 SETTLEMENT bill rejected → ErrInvalidBillType
//   - T7 amount mismatch rejected → ErrAmountMismatch
//   - T8 concurrent race → exactly 1 success, 1 failure, 1 payment row
//
// Key Postgres invariants exercised:
//   - SELECT FOR UPDATE serializes concurrent payment attempts (T8)
//   - UNIQUE INDEX on bill_id is the final race backstop (T8)
//   - FK bill_payments.bill_id REFERENCES bills(id) — referential integrity
//   - Atomic status flip — bill.status=PAID + INSERT bill_payments in one TX

var nilPaymentActor *uuid.UUID

// ── Shared test-env ──────────────────────────────────────────────────────────

type paymentTestEnv struct {
	db     *gorm.DB
	svc    PaymentService
	billID uuid.UUID
	total  int64 // satang
}

// newPaymentEnv builds a clean DB with one FINALIZED MONTHLY bill ready to pay.
func newPaymentEnv(t *testing.T) *paymentTestEnv {
	t.Helper()
	return newPaymentEnvWithBill(t, billing.BillStatusFinalized, billing.BillTypeMonthly)
}

// newPaymentEnvWithBill seeds a bill with arbitrary status/type.
func newPaymentEnvWithBill(t *testing.T, status billing.BillStatus, billType billing.BillType) *paymentTestEnv {
	t.Helper()
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)

	apt := fixtures.SeedApartment(t, db)
	rm := fixtures.SeedRoom(t, db, apt.ID.String(), "P-101")
	tn := fixtures.SeedTenant(t, db)
	c := fixtures.SeedContract(t, db, tn.ID.String(), rm.ID.String(), 3)

	const total int64 = 349000 // ฿3,490
	billID := uuid.New()
	bill := &billing.Bill{
		ID:           billID,
		ContractID:   c.ID,
		BillingMonth: "2026-05",
		BillType:     billType,
		Status:       status,
		TotalAmount:  total,
	}
	if err := db.Create(bill).Error; err != nil {
		t.Fatalf("seed bill: %v", err)
	}

	svc := NewPaymentService(
		NewPaymentRepository(db),
		billing.NewPaymentAdapter(billing.NewBillingRepository(db), billing.NewBillAuditRepository(db)),
		database.NewTxManager(db),
	)

	return &paymentTestEnv{db: db, svc: svc, billID: billID, total: total}
}

// ── T1: CASH happy path ──────────────────────────────────────────────────────

func TestRecordBillPayment_Integration_CashHappyPath(t *testing.T) {
	env := newPaymentEnv(t)

	resp, err := env.svc.RecordBillPayment(
		context.Background(), env.billID,
		RecordBillPaymentRequest{
			Amount: money.ToBaht(env.total),
			Method: "CASH",
		},
		nilPaymentActor,
	)
	if err != nil {
		t.Fatalf("RecordBillPayment: %v", err)
	}

	// ─── Response shape ───
	if resp.BillID != env.billID {
		t.Errorf("resp.BillID = %s; want %s", resp.BillID, env.billID)
	}
	if resp.Method != "CASH" {
		t.Errorf("resp.Method = %s; want CASH", resp.Method)
	}
	if resp.Amount != money.ToBaht(env.total) {
		t.Errorf("resp.Amount = %.2f; want %.2f", resp.Amount, money.ToBaht(env.total))
	}

	// ─── Bill status flipped to PAID ───
	var b billing.Bill
	if err := env.db.Where("id = ?", env.billID).First(&b).Error; err != nil {
		t.Fatalf("re-fetch bill: %v", err)
	}
	if b.Status != billing.BillStatusPaid {
		t.Errorf("bill.Status = %s; want PAID", b.Status)
	}

	// ─── Payment row persisted ───
	var p BillPayment
	if err := env.db.Where("bill_id = ?", env.billID).First(&p).Error; err != nil {
		t.Fatalf("fetch payment: %v", err)
	}
	if p.Amount != env.total {
		t.Errorf("payment.Amount = %d; want %d", p.Amount, env.total)
	}

	// ─── Audit row emitted ───
	var auditCount int64
	env.db.Table("bill_audit_log").
		Where("bill_id = ? AND action = ?", env.billID, billing.AuditRecordPayment).
		Count(&auditCount)
	if auditCount != 1 {
		t.Errorf("audit rows = %d; want 1", auditCount)
	}
}

// ── T2: TRANSFER + note ──────────────────────────────────────────────────────

func TestRecordBillPayment_Integration_TransferWithNote(t *testing.T) {
	env := newPaymentEnv(t)
	note := "โอนแล้ว สลิปแนบ"

	resp, err := env.svc.RecordBillPayment(
		context.Background(), env.billID,
		RecordBillPaymentRequest{
			Amount: money.ToBaht(env.total),
			Method: "TRANSFER",
			Note:   note,
		},
		nilPaymentActor,
	)
	if err != nil {
		t.Fatalf("RecordBillPayment: %v", err)
	}
	if resp.Method != "TRANSFER" {
		t.Errorf("resp.Method = %s; want TRANSFER", resp.Method)
	}
	if resp.Note != note {
		t.Errorf("resp.Note = %q; want %q", resp.Note, note)
	}

	var p BillPayment
	if err := env.db.Where("bill_id = ?", env.billID).First(&p).Error; err != nil {
		t.Fatalf("fetch payment: %v", err)
	}
	if p.Note != note {
		t.Errorf("payment.Note = %q; want %q", p.Note, note)
	}
}

// ── T3: DRAFT bill rejected ──────────────────────────────────────────────────

func TestRecordBillPayment_Integration_DraftBillRejected(t *testing.T) {
	env := newPaymentEnvWithBill(t, billing.BillStatusDraft, billing.BillTypeMonthly)

	_, err := env.svc.RecordBillPayment(
		context.Background(), env.billID,
		RecordBillPaymentRequest{Amount: money.ToBaht(env.total), Method: "CASH"},
		nilPaymentActor,
	)
	if !errors.Is(err, ErrBillNotFinalized) {
		t.Errorf("err = %v; want ErrBillNotFinalized", err)
	}
}

// ── T4: PAID bill rejects double payment ─────────────────────────────────────

func TestRecordBillPayment_Integration_DoublePayRejected(t *testing.T) {
	env := newPaymentEnvWithBill(t, billing.BillStatusPaid, billing.BillTypeMonthly)

	_, err := env.svc.RecordBillPayment(
		context.Background(), env.billID,
		RecordBillPaymentRequest{Amount: money.ToBaht(env.total), Method: "CASH"},
		nilPaymentActor,
	)
	if !errors.Is(err, ErrBillAlreadyPaid) {
		t.Errorf("err = %v; want ErrBillAlreadyPaid", err)
	}
}

// ── T5: VOID bill rejected ───────────────────────────────────────────────────

func TestRecordBillPayment_Integration_VoidBillRejected(t *testing.T) {
	env := newPaymentEnvWithBill(t, billing.BillStatusVoid, billing.BillTypeMonthly)

	_, err := env.svc.RecordBillPayment(
		context.Background(), env.billID,
		RecordBillPaymentRequest{Amount: money.ToBaht(env.total), Method: "CASH"},
		nilPaymentActor,
	)
	if !errors.Is(err, ErrBillNotFinalized) {
		t.Errorf("err = %v; want ErrBillNotFinalized", err)
	}
}

// ── T6: SETTLEMENT bill rejected ─────────────────────────────────────────────

func TestRecordBillPayment_Integration_SettlementRejected(t *testing.T) {
	env := newPaymentEnvWithBill(t, billing.BillStatusFinalized, billing.BillTypeSettlement)

	_, err := env.svc.RecordBillPayment(
		context.Background(), env.billID,
		RecordBillPaymentRequest{Amount: money.ToBaht(env.total), Method: "CASH"},
		nilPaymentActor,
	)
	if !errors.Is(err, ErrInvalidBillType) {
		t.Errorf("err = %v; want ErrInvalidBillType", err)
	}
}

// ── T7: amount mismatch rejected ─────────────────────────────────────────────

func TestRecordBillPayment_Integration_AmountMismatch(t *testing.T) {
	env := newPaymentEnv(t)

	_, err := env.svc.RecordBillPayment(
		context.Background(), env.billID,
		RecordBillPaymentRequest{
			Amount: money.ToBaht(env.total) + 100, // off by ฿100
			Method: "CASH",
		},
		nilPaymentActor,
	)
	if !errors.Is(err, ErrAmountMismatch) {
		t.Errorf("err = %v; want ErrAmountMismatch", err)
	}
}

// ── T8: concurrent race — only one payment wins ───────────────────────────────
//
// Two goroutines attempt to pay the same FINALIZED bill simultaneously.
// The SELECT FOR UPDATE inside the TX serializes them: the loser re-validates
// after the winner commits, sees Status=PAID, and returns ErrBillAlreadyPaid.
// The UNIQUE INDEX on bill_payments.bill_id is the final backstop if both
// goroutines enter the TX before either commits.

func TestRecordBillPayment_Integration_ConcurrentRace(t *testing.T) {
	env := newPaymentEnv(t)

	type result struct {
		resp *BillPaymentResponse
		err  error
	}

	ch := make(chan result, 2)
	var wg sync.WaitGroup
	wg.Add(2)

	pay := func() {
		defer wg.Done()
		r, err := env.svc.RecordBillPayment(
			context.Background(), env.billID,
			RecordBillPaymentRequest{Amount: money.ToBaht(env.total), Method: "CASH"},
			nilPaymentActor,
		)
		ch <- result{r, err}
	}

	go pay()
	go pay()
	wg.Wait()
	close(ch)

	var successes, failures int
	for r := range ch {
		if r.err == nil {
			successes++
		} else {
			failures++
			// The loser must surface as a domain error (AlreadyPaid or Conflict),
			// not an opaque infrastructure 500.
			if !errors.Is(r.err, ErrBillAlreadyPaid) && !isUniqueViolation(r.err) {
				t.Errorf("losing goroutine err = %v; want ErrBillAlreadyPaid or unique-violation", r.err)
			}
		}
	}

	if successes != 1 {
		t.Errorf("successes = %d; want exactly 1", successes)
	}
	if failures != 1 {
		t.Errorf("failures = %d; want exactly 1", failures)
	}

	// Exactly one payment row must exist.
	var count int64
	env.db.Table("bill_payments").Where("bill_id = ?", env.billID).Count(&count)
	if count != 1 {
		t.Errorf("bill_payments rows = %d; want 1", count)
	}
}

// isUniqueViolation reports whether err is a Postgres unique-constraint error
// (code 23505). Used only as a fallback in the race test — the SELECT FOR UPDATE
// guard normally catches the loser before the INSERT, but if both goroutines
// reach the INSERT simultaneously the DB unique index is the backstop.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	return containsCode23505(err.Error())
}

func containsCode23505(msg string) bool {
	for i := 0; i+5 <= len(msg); i++ {
		if msg[i:i+5] == "23505" {
			return true
		}
	}
	return false
}
