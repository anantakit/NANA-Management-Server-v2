package billingreconciliation

import (
	"time"

	"nana/internal/shared/money"
)

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
			DraftCount:      r.Summary.DraftCount,
			FinalizedCount:  r.Summary.FinalizedCount,
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
	item.PendingBaselineCorrectionsCount = row.PendingBaselineCorrectionsCount
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

// ToGenerateResponse maps the Generate fan-out result to wire shape.
// Pointer fields preserve omitempty semantics — SUCCESS rows omit
// skip_reason / error_*, SKIPPED rows omit bill_id, FAILED rows omit
// bill_id / skip_reason.
func ToGenerateResponse(r *GenerateResult) GenerateResponse {
	items := make([]GenerateItemPayload, 0, len(r.Items))
	for _, it := range r.Items {
		items = append(items, toGenerateItem(it))
	}
	return GenerateResponse{
		BillingMonth: r.BillingMonth,
		SuccessCount: r.SuccessCount,
		SkippedCount: r.SkippedCount,
		FailedCount:  r.FailedCount,
		Items:        items,
	}
}

func toGenerateItem(it GenerateItemResult) GenerateItemPayload {
	out := GenerateItemPayload{
		RoomID: it.RoomID.String(),
		Result: string(it.ResultType),
	}
	if it.BillID != nil {
		s := it.BillID.String()
		out.BillID = &s
	}
	if it.SkipReason != "" {
		s := string(it.SkipReason)
		out.SkipReason = &s
	}
	if it.ErrorCode != "" {
		c := it.ErrorCode
		out.ErrorCode = &c
	}
	if it.ErrorMessage != "" {
		m := it.ErrorMessage
		out.ErrorMessage = &m
	}
	return out
}
