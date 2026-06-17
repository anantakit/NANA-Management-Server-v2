package billdelivery

import (
	"context"
	"fmt"
	"time"

	"nana/internal/shared/database"

	"github.com/google/uuid"
)

type DeliveryService interface {
	RecordDelivery(ctx context.Context, req RecordDeliveryRequest, actorID *uuid.UUID) (*DeliveryResponse, error)
}

type deliveryService struct {
	repo  DeliveryRepository
	bills BillQuerier
}

var _ DeliveryService = (*deliveryService)(nil)

func NewDeliveryService(repo DeliveryRepository, bills BillQuerier) DeliveryService {
	return &deliveryService{repo: repo, bills: bills}
}

func (s *deliveryService) RecordDelivery(ctx context.Context, req RecordDeliveryRequest, actorID *uuid.UUID) (*DeliveryResponse, error) {
	billID, _ := uuid.Parse(req.BillID) // validated upstream by bind

	bill, err := s.bills.FindByID(ctx, billID)
	if err != nil {
		if database.IsNotFound(err) {
			return nil, ErrBillNotFound
		}
		return nil, fmt.Errorf("find bill: %w", err)
	}

	if !bill.IsDeliverable() {
		return nil, ErrBillNotDeliverable
	}
	if !bill.HasPaymentDestination() {
		return nil, ErrBillMissingPaymentDestination
	}

	d := BillDelivery{
		BillID:      billID,
		ActorID:     actorID,
		Channel:     "LINE_MANUAL",
		DeliveredAt: time.Now(),
	}
	if req.Note != nil {
		d.Note = *req.Note
	}

	if err := s.repo.Create(ctx, &d); err != nil {
		return nil, fmt.Errorf("record delivery: %w", err)
	}

	return toDeliveryResponse(&d), nil
}
