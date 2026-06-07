package billingreconciliation

import (
	"context"
	"fmt"

	"nana/internal/billing"
	"nana/internal/meterreading"
	"nana/internal/moveout"
	"nana/internal/shared/money"
	"nana/internal/shared/respond"

	"github.com/google/uuid"
)

// Service is the read-only reconciliation surface. 1A intentionally exposes
// only one method — there's no decision storage, no write path, no CTA. The
// next phases (1B = decisions, 1C = bulk, 1D = generate) extend this surface
// additively without breaking the 1A contract.
type Service interface {
	Reconcile(ctx context.Context, q ReconciliationQuery) (*Report, error)
}

type service struct {
	repo     Repository
	meters   meterreading.MeterReadingRepository
	moveOuts moveout.MoveOutRepository
	bills    billing.BillingRepository
}

var _ Service = (*service)(nil)

func NewService(
	repo Repository,
	meters meterreading.MeterReadingRepository,
	moveOuts moveout.MoveOutRepository,
	bills billing.BillingRepository,
) Service {
	return &service{
		repo:     repo,
		meters:   meters,
		moveOuts: moveOuts,
		bills:    bills,
	}
}

// Reconcile produces the 4-bucket audit report for one (apartment, month).
//
// Read order is identical to billing/service_classification.go's loadBatchInputs
// so PD = 0 + AR = MissingMeterCount + NB ⊇ {move-out-pending, not-billable}
// agree with the existing preflight counts where their definitions overlap —
// objective cross-check usable as the 1A operator-validation sanity gate.
func (s *service) Reconcile(ctx context.Context, q ReconciliationQuery) (*Report, error) {
	apartmentID, err := uuid.Parse(q.ApartmentID)
	if err != nil {
		return nil, respond.ErrBadRequest.WithMessage("apartment_id ไม่ถูกต้อง")
	}
	if !billingMonthRe.MatchString(q.BillingMonth) {
		return nil, respond.ErrBadRequest.WithMessage("billing_month ต้องเป็นรูปแบบ YYYY-MM")
	}

	candidates, err := s.repo.ListRoomCandidatesByApartment(ctx, apartmentID)
	if err != nil {
		return nil, fmt.Errorf("list rooms: %w", err)
	}
	startOfMonth, endOfMonth := parseBillingMonthRange(q.BillingMonth)

	// Empty apartment short-circuit — no rooms means no joins to run.
	if len(candidates) == 0 {
		return &Report{
			ApartmentID:  apartmentID,
			BillingMonth: q.BillingMonth,
			Rooms:        []RoomClassification{},
		}, nil
	}

	roomIDs := make([]uuid.UUID, 0, len(candidates))
	contractIDs := make([]uuid.UUID, 0, len(candidates))
	for _, c := range candidates {
		roomIDs = append(roomIDs, c.RoomID)
		if c.ContractID != nil {
			contractIDs = append(contractIDs, *c.ContractID)
		}
	}

	pendingMoveOuts, err := s.moveOuts.FindRoomIDsWithMoveOutInMonth(ctx, roomIDs, q.BillingMonth)
	if err != nil {
		return nil, fmt.Errorf("check move-outs: %w", err)
	}
	meterMap, err := s.meters.FindMonthlyByRoomsAndMonth(ctx, roomIDs, q.BillingMonth)
	if err != nil {
		return nil, fmt.Errorf("find meters: %w", err)
	}
	billMap := map[uuid.UUID]*billing.Bill{}
	if len(contractIDs) > 0 {
		billMap, err = s.bills.FindExistingByContractsAndMonth(ctx, contractIDs, q.BillingMonth)
		if err != nil {
			return nil, fmt.Errorf("find existing bills: %w", err)
		}
	}

	rows := make([]RoomClassification, 0, len(candidates))
	summary := Summary{Total: len(candidates)}

	for _, cand := range candidates {
		hasPendingMoveOut := pendingMoveOuts[cand.RoomID]
		reading := meterMap[cand.RoomID]
		hasMeter := reading != nil

		bucket, reason := classifyRoom(cand, startOfMonth, endOfMonth, hasPendingMoveOut, hasMeter)

		row := RoomClassification{
			Room:   cand,
			Bucket: bucket,
			Reason: reason,
		}

		// Inline bill evidence — never shifts the bucket, only annotates the row.
		if cand.ContractID != nil {
			if b, ok := billMap[*cand.ContractID]; ok && b != nil {
				row.Bill = &BillSnapshot{
					BillID:            b.ID,
					Status:            string(b.Status),
					TotalAmountSatang: b.TotalAmount,
				}
			}
		}

		// Inline anomaly evidence — only meaningful when a reading exists.
		if reading != nil {
			row.Anomaly = &AnomalyFlags{
				Electricity: reading.IsAnomalyElectricity,
				Water:       reading.IsAnomalyWater,
			}
			if reading.IsAnomalyElectricity || reading.IsAnomalyWater {
				summary.AnomalyCount++
			}
		}

		switch bucket {
		case BucketReady:
			summary.Ready++
		case BucketActionRequired:
			summary.ActionRequired++
		case BucketPendingDecision:
			summary.PendingDecision++
		case BucketNotBillable:
			summary.NotBillable++
		}

		rows = append(rows, row)
	}

	return &Report{
		ApartmentID:  apartmentID,
		BillingMonth: q.BillingMonth,
		Summary:      summary,
		Rooms:        rows,
	}, nil
}

// --- DTO mapping ---

// ToReportResponse maps the domain report to wire format. Money normalized to
// baht at the DTO boundary per coding-standards.md.
func ToReportResponse(r *Report) ReportResponse {
	items := make([]RoomReconcileItem, 0, len(r.Rooms))
	for _, row := range r.Rooms {
		items = append(items, toRoomItem(row))
	}
	return ReportResponse{
		ApartmentID:  r.ApartmentID.String(),
		BillingMonth: r.BillingMonth,
		Summary: SummaryResponse{
			Total: r.Summary.Total,
			ShouldBill: ShouldBillSummary{
				Total:          r.Summary.Ready + r.Summary.ActionRequired,
				Ready:          r.Summary.Ready,
				ActionRequired: r.Summary.ActionRequired,
			},
			PendingDecision: r.Summary.PendingDecision,
			NotBillable:     r.Summary.NotBillable,
			AnomalyCount:    r.Summary.AnomalyCount,
		},
		Rooms: items,
	}
}

func toRoomItem(row RoomClassification) RoomReconcileItem {
	item := RoomReconcileItem{
		RoomID:     row.Room.RoomID.String(),
		RoomNumber: row.Room.RoomNumber,
		Floor:      row.Room.RoomFloor,
		Bucket:     string(row.Bucket),
	}
	if row.Reason != "" {
		s := string(row.Reason)
		item.ReasonCode = &s
	}
	if row.Room.TenantName != "" {
		t := row.Room.TenantName
		item.TenantName = &t
	}
	if row.Room.ContractID != nil {
		s := row.Room.ContractID.String()
		item.ContractID = &s
	}
	if row.Room.ContractStartDate != nil {
		s := row.Room.ContractStartDate.Format("2006-01-02")
		item.ContractStartDate = &s
	}
	if row.Bill != nil {
		item.Bill = &BillEvidence{
			BillID:      row.Bill.BillID.String(),
			Status:      row.Bill.Status,
			TotalAmount: money.ToBaht(row.Bill.TotalAmountSatang),
		}
	}
	if row.Anomaly != nil {
		item.Anomaly = &AnomalyEvidence{
			Electricity: row.Anomaly.Electricity,
			Water:       row.Anomaly.Water,
		}
	}
	return item
}
