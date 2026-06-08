package billingreconciliation

import (
	"context"
	"fmt"
	"time"

	"nana/internal/meterreading"
	"nana/internal/moveout"
	"nana/internal/shared/money"
	"nana/internal/shared/respond"

	"github.com/google/uuid"
)

// Service is the reconciliation surface. Phase 1A added the read-only
// Reconcile audit; Phase 1B adds decision storage on the PD-origin axis
// (NEW_TENANT_MID_CYCLE rooms). The decision API is intentionally narrow:
// one room, one month — bulk is 1C, Generate is 1D.
type Service interface {
	Reconcile(ctx context.Context, q ReconciliationQuery) (*Report, error)

	// GetDecision returns the current decision for (room, month) or nil
	// when none exists. Read does not enforce the PD-origin guard — the FE
	// may want to surface stale rows in a future audit viewer; today it's
	// only used to hydrate the form before a PUT, so a stale read returns
	// the row verbatim.
	GetDecision(ctx context.Context, roomID uuid.UUID, billingMonth string) (*Decision, *DecisionAttribution, error)

	// SetDecision upserts a decision. Guards (in service, not handler):
	//   1. Room belongs to apartment
	//   2. Room currently classifies as PD / NEW_TENANT_MID_CYCLE
	//   3. No bill exists yet for (room, month)  ← reversal-boundary lock
	SetDecision(ctx context.Context, apartmentID, roomID uuid.UUID, billingMonth string, decision DecisionState, actor *uuid.UUID) (*Decision, *DecisionAttribution, error)

	// DeleteDecision removes the row (revert to Undecided). Same reversal
	// boundary as Set — once a bill exists, decision moves into bill
	// grammar (Phase 2.1 correction) and is no longer touchable here.
	DeleteDecision(ctx context.Context, apartmentID, roomID uuid.UUID, billingMonth string) error
}

type service struct {
	repo     Repository
	meters   meterreading.MeterReadingRepository
	moveOuts moveout.MoveOutRepository
	bills    BillsQuerier
}

var _ Service = (*service)(nil)

func NewService(
	repo Repository,
	meters meterreading.MeterReadingRepository,
	moveOuts moveout.MoveOutRepository,
	bills BillsQuerier,
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
	billMap := map[uuid.UUID]*BillSnapshot{}
	if len(contractIDs) > 0 {
		billMap, err = s.bills.FindExistingBillsByContractsAndMonth(ctx, contractIDs, q.BillingMonth)
		if err != nil {
			return nil, fmt.Errorf("find existing bills: %w", err)
		}
	}

	// Phase 1B — pull existing decisions so the classifier can rebucket
	// PD-origin rows. The map is keyed by room_id (NOT contract_id) because
	// decisions persist across contract churn (deposit refund / re-tenant
	// scenarios — the room is the long-term identity).
	decisionMap, err := s.repo.FindDecisionsForApartmentMonth(ctx, apartmentID, q.BillingMonth)
	if err != nil {
		return nil, fmt.Errorf("find decisions: %w", err)
	}

	rows := make([]RoomClassification, 0, len(candidates))
	summary := Summary{Total: len(candidates)}

	for _, cand := range candidates {
		hasPendingMoveOut := pendingMoveOuts[cand.RoomID]
		reading := meterMap[cand.RoomID]
		hasMeter := reading != nil

		attr := decisionMap[cand.RoomID]
		var decisionState DecisionState
		if attr != nil {
			decisionState = attr.State
		}
		bucket, reason := classifyRoom(cand, startOfMonth, endOfMonth, hasPendingMoveOut, hasMeter, decisionState)

		row := RoomClassification{
			Room:   cand,
			Bucket: bucket,
			Reason: reason,
		}
		// Surface attribution whenever the decision actually mattered, i.e.
		// the room was PD-origin (NEW_TENANT_MID_CYCLE under raw rules) and
		// the operator has recorded a decision. The "did the operator
		// drive this row" signal must NOT depend on the final reason code,
		// because INCLUDE on a meter-missing room legitimately lands on
		// AR / MISSING_METER_READING but the row was still operator-decided.
		if attr != nil && rawIsPDOrigin(cand, startOfMonth, endOfMonth, hasPendingMoveOut) {
			row.Attribution = attr
		}

		// Inline bill evidence — never shifts the bucket, only annotates the row.
		// BillsQuerier already returns *BillSnapshot (cross-feature read pattern:
		// flat projection at the boundary), so this is a direct pointer hand-off.
		if cand.ContractID != nil {
			if b, ok := billMap[*cand.ContractID]; ok && b != nil {
				row.Bill = b
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

// --- Phase 1B: per-room decision endpoints ---

// Sentinel errors surfaced as Thai-language AppErrors. Defined here so the
// handler can stay thin (single respond.Error path) and so each guard has a
// human-readable code the FE can branch on if needed.
var (
	errDecisionRoomNotFound = respond.ErrNotFound.WithMessage("ไม่พบห้องในอาคารนี้")
	errDecisionNotPDOrigin  = respond.New(
		"NOT_PENDING_DECISION", 409,
		"ห้องนี้ไม่อยู่ในสถานะที่ต้องตัดสินใจสำหรับเดือนนี้",
	)
	errDecisionBillExists = respond.New(
		"BILL_ALREADY_EXISTS", 409,
		"มีบิลออกในเดือนนี้แล้ว ไม่สามารถเปลี่ยนการตัดสินใจได้",
	)
	errDecisionNotFound = respond.ErrNotFound.WithMessage("ยังไม่มีการตัดสินใจสำหรับห้องนี้")
)

func (s *service) GetDecision(ctx context.Context, roomID uuid.UUID, billingMonth string) (*Decision, *DecisionAttribution, error) {
	if !billingMonthRe.MatchString(billingMonth) {
		return nil, nil, respond.ErrBadRequest.WithMessage("billing_month ต้องเป็นรูปแบบ YYYY-MM")
	}
	d, attr, err := s.repo.FindDecisionByRoomMonth(ctx, roomID, billingMonth)
	if err != nil {
		return nil, nil, fmt.Errorf("find decision: %w", err)
	}
	if d == nil {
		return nil, nil, errDecisionNotFound
	}
	return d, attr, nil
}

func (s *service) SetDecision(
	ctx context.Context,
	apartmentID, roomID uuid.UUID,
	billingMonth string,
	decision DecisionState,
	actor *uuid.UUID,
) (*Decision, *DecisionAttribution, error) {
	if !billingMonthRe.MatchString(billingMonth) {
		return nil, nil, respond.ErrBadRequest.WithMessage("billing_month ต้องเป็นรูปแบบ YYYY-MM")
	}
	if decision != DecisionInclude && decision != DecisionSkip {
		return nil, nil, respond.ErrBadRequest.WithMessage("decision ต้องเป็น INCLUDE หรือ SKIP")
	}
	if err := s.guardWritable(ctx, apartmentID, roomID, billingMonth); err != nil {
		return nil, nil, err
	}

	row := &Decision{
		RoomID:       roomID,
		BillingMonth: billingMonth,
		Decision:     decision,
		DecidedAt:    time.Now(),
		DecidedBy:    actor,
	}
	if err := s.repo.UpsertDecision(ctx, row); err != nil {
		return nil, nil, fmt.Errorf("upsert decision: %w", err)
	}
	// Re-fetch to pick up DB-generated id (upsert path) + the joined
	// username for attribution — cheaper than threading the auth.User
	// model through the service constructor.
	stored, attr, err := s.repo.FindDecisionByRoomMonth(ctx, roomID, billingMonth)
	if err != nil {
		return nil, nil, fmt.Errorf("reload decision: %w", err)
	}
	return stored, attr, nil
}

func (s *service) DeleteDecision(
	ctx context.Context,
	apartmentID, roomID uuid.UUID,
	billingMonth string,
) error {
	if !billingMonthRe.MatchString(billingMonth) {
		return respond.ErrBadRequest.WithMessage("billing_month ต้องเป็นรูปแบบ YYYY-MM")
	}
	if err := s.guardWritable(ctx, apartmentID, roomID, billingMonth); err != nil {
		return err
	}
	if err := s.repo.DeleteDecision(ctx, roomID, billingMonth); err != nil {
		// Best-effort: the guardWritable check already confirmed PD-origin,
		// which implies a row may or may not exist. Treat "row missing" as
		// idempotent success — operator clicked "ยกเลิกการตัดสิน" twice.
		if err.Error() == "decision not found" {
			return nil
		}
		return fmt.Errorf("delete decision: %w", err)
	}
	return nil
}

// guardWritable encapsulates the two write-side invariants from the walk
// locks: scope = PD-origin only, and reversal boundary = bill existence.
// Same function for Set + Delete so the contract can't drift between them.
func (s *service) guardWritable(ctx context.Context, apartmentID, roomID uuid.UUID, billingMonth string) error {
	cand, err := s.repo.FindRoomCandidate(ctx, apartmentID, roomID)
	if err != nil {
		return fmt.Errorf("find room candidate: %w", err)
	}
	if cand == nil {
		return errDecisionRoomNotFound
	}

	// Re-derive raw-rule classification (decision empty) so the PD-origin
	// check is the bucket the room WOULD be in without operator input.
	startOfMonth, endOfMonth := parseBillingMonthRange(billingMonth)

	pendingMap, err := s.moveOuts.FindRoomIDsWithMoveOutInMonth(ctx, []uuid.UUID{roomID}, billingMonth)
	if err != nil {
		return fmt.Errorf("check move-outs: %w", err)
	}
	meterMap, err := s.meters.FindMonthlyByRoomsAndMonth(ctx, []uuid.UUID{roomID}, billingMonth)
	if err != nil {
		return fmt.Errorf("find meter: %w", err)
	}
	bucket, reason := classifyRoom(
		*cand,
		startOfMonth, endOfMonth,
		pendingMap[roomID],
		meterMap[roomID] != nil,
		"", // raw rules — no decision applied
	)
	if bucket != BucketPendingDecision || reason != ReasonNewTenantMidCycle {
		return errDecisionNotPDOrigin
	}

	// Reversal boundary: once a bill exists for this contract+month,
	// decision affordance shuts down — Q9 architecture gate.
	if cand.ContractID != nil {
		bills, err := s.bills.FindExistingBillsByContractsAndMonth(ctx, []uuid.UUID{*cand.ContractID}, billingMonth)
		if err != nil {
			return fmt.Errorf("check existing bill: %w", err)
		}
		if b, ok := bills[*cand.ContractID]; ok && b != nil {
			return errDecisionBillExists
		}
	}
	return nil
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
	if row.Attribution != nil {
		item.Decision = toDecisionEvidence(row.Attribution)
	}
	return item
}

// toDecisionEvidence is shared by the per-row inline attribution AND by the
// standalone GET/PUT decision response (which wraps it with room_id +
// billing_month) — same wire shape so the FE renders attribution once.
func toDecisionEvidence(a *DecisionAttribution) *DecisionEvidence {
	return &DecisionEvidence{
		State:         string(a.State),
		DecidedAt:     a.DecidedAt.Format(time.RFC3339),
		DecidedByName: a.DecidedByName,
	}
}

// ToDecisionResponse maps the decision row + joined attribution to wire.
// `d` carries id/room/month, `attr` carries the joined username — kept
// split because GET / SetDecision both have a stored row AND a freshly
// joined attribution and we want a single render path.
func ToDecisionResponse(d *Decision, attr *DecisionAttribution) DecisionResponse {
	out := DecisionResponse{
		RoomID:       d.RoomID.String(),
		BillingMonth: d.BillingMonth,
		State:        string(d.Decision),
		DecidedAt:    d.DecidedAt.Format(time.RFC3339),
	}
	if attr != nil {
		out.DecidedByName = attr.DecidedByName
	}
	return out
}
