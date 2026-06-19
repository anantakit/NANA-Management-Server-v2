package payment

import (
	"context"
	"fmt"
	"time"

	"nana/internal/billing"
	"nana/internal/shared/database"
	"nana/internal/shared/money"
	"nana/internal/shared/paymentmethod"
	"nana/internal/shared/respond"

	"github.com/google/uuid"
)

type PaymentService interface {
	RecordBillPayment(ctx context.Context, billID uuid.UUID, req RecordBillPaymentRequest, actorID *uuid.UUID) (*BillPaymentResponse, error)
}

type paymentService struct {
	repo    PaymentRepository
	billing BillingPort
	tx      database.TxManager
}

var _ PaymentService = (*paymentService)(nil)

func NewPaymentService(repo PaymentRepository, billingPort BillingPort, tx database.TxManager) PaymentService {
	return &paymentService{repo: repo, billing: billingPort, tx: tx}
}

func (s *paymentService) RecordBillPayment(ctx context.Context, billID uuid.UUID, req RecordBillPaymentRequest, actorID *uuid.UUID) (*BillPaymentResponse, error) {
	amountSatang := money.ToSatang(req.Amount)

	// Pre-TX fast reject — validates without acquiring a row lock.
	bill, err := s.billing.FindBillForPayment(ctx, billID)
	if err != nil {
		if database.IsNotFound(err) {
			return nil, ErrBillNotFound
		}
		return nil, fmt.Errorf("find bill: %w", err)
	}
	if err := validateBillForPayment(bill, amountSatang); err != nil {
		return nil, err
	}

	var p BillPayment
	if err := s.tx.RunInTx(ctx, func(txCtx context.Context) error {
		// Re-validate with SELECT FOR UPDATE — serializes concurrent payment attempts.
		locked, err := s.billing.LockBillForPayment(txCtx, billID)
		if err != nil {
			if database.IsNotFound(err) {
				return ErrBillNotFound
			}
			return fmt.Errorf("lock bill: %w", err)
		}
		if err := validateBillForPayment(locked, amountSatang); err != nil {
			return err
		}

		// Create payment record — UNIQUE index on bill_id is the final race guard.
		p = BillPayment{
			BillID:     billID,
			Amount:     amountSatang,
			Method:     paymentmethod.PaymentMethod(req.Method),
			Note:       req.Note,
			PaidAt:     time.Now(),
			ReceivedBy: actorID,
		}
		if err := s.repo.Create(txCtx, &p); err != nil {
			return fmt.Errorf("create payment: %w", err)
		}

		// Transition bill PAID.
		if err := locked.MarkPaid(); err != nil {
			return respond.ErrBadRequest.WithMessage(err.Error())
		}
		if err := s.billing.PersistBillPaid(txCtx, locked); err != nil {
			return fmt.Errorf("persist bill paid: %w", err)
		}

		// Audit.
		return s.billing.EmitPaymentAudit(txCtx, billID, actorID, amountSatang, req.Method)
	}); err != nil {
		return nil, err
	}

	return toBillPaymentResponse(&p), nil
}

func validateBillForPayment(b *billing.Bill, amountSatang int64) error {
	if !b.IsMonthly() {
		return ErrInvalidBillType
	}
	switch b.Status {
	case billing.BillStatusPaid:
		return ErrBillAlreadyPaid
	case billing.BillStatusDraft, billing.BillStatusVoid:
		return ErrBillNotFinalized
	case billing.BillStatusFinalized:
		// ok — proceed
	}
	if amountSatang != b.TotalAmount {
		return ErrAmountMismatch
	}
	return nil
}
