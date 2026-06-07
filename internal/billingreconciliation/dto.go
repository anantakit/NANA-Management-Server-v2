package billingreconciliation

// --- Request ---

type ReconciliationQuery struct {
	ApartmentID  string `query:"apartment_id" validate:"required,uuid"`
	BillingMonth string `query:"billing_month" validate:"required"`
}

// --- Response ---

// ReportResponse is the top-level payload — one apartment, one billing month.
// No pagination by design: 1A is an audit surface, the operator scans every
// row of one building. ~200 rooms per apartment is well within a single
// JSON response.
type ReportResponse struct {
	ApartmentID  string              `json:"apartment_id"`
	BillingMonth string              `json:"billing_month"`
	Summary      SummaryResponse     `json:"summary"`
	Rooms        []RoomReconcileItem `json:"rooms"`
}

type SummaryResponse struct {
	Total      int               `json:"total"`
	ShouldBill ShouldBillSummary `json:"should_bill"`
	// PendingDecision and NotBillable stay top-level — they sit outside the
	// "should bill" axis (see direction doc § Three-Axis Taxonomy).
	PendingDecision int `json:"pending_decision"`
	NotBillable     int `json:"not_billable"`
	AnomalyCount    int `json:"anomaly_count"`
}

type ShouldBillSummary struct {
	Total          int `json:"total"`
	Ready          int `json:"ready"`
	ActionRequired int `json:"action_required"`
}

type RoomReconcileItem struct {
	RoomID            string  `json:"room_id"`
	RoomNumber        string  `json:"room_number"`
	Floor             int     `json:"floor"`
	Bucket            string  `json:"bucket"`
	ReasonCode        *string `json:"reason_code"`
	TenantName        *string `json:"tenant_name"`
	ContractID        *string `json:"contract_id"`
	ContractStartDate *string `json:"contract_start_date"`

	// Bill = inline evidence only. Operators see "already generated" without
	// the room shifting buckets — read-only audit invariant.
	Bill *BillEvidence `json:"bill"`

	// Anomaly = inline trust flag. Null when there's no meter reading
	// (e.g. ACTION_REQUIRED rows with reason MISSING_METER_READING).
	Anomaly *AnomalyEvidence `json:"anomaly"`

	// Decision = inline operator attribution. Non-null only on rows whose
	// underlying classification was PD-origin (NEW_TENANT_MID_CYCLE) AND
	// where the operator has recorded INCLUDE / SKIP. Stale decisions on
	// non-PD rows are silently dropped at the service layer (scope guard).
	Decision *DecisionEvidence `json:"decision"`
}

type BillEvidence struct {
	BillID      string  `json:"bill_id"`
	Status      string  `json:"status"`
	TotalAmount float64 `json:"total_amount"` // baht
}

type AnomalyEvidence struct {
	Electricity bool `json:"electricity"`
	Water       bool `json:"water"`
}

// --- Phase 1B decision DTOs ---

// DecisionEvidence is the per-row attribution shown inline on a decided row
// ("เพิ่มโดยคุณ · 15 มิ.ย."). Null when the room has no decision.
type DecisionEvidence struct {
	State         string  `json:"state"`             // "INCLUDE" | "SKIP"
	DecidedAt     string  `json:"decided_at"`        // RFC3339
	DecidedByName *string `json:"decided_by_name"`   // username/full_name; nil if actor deleted
}

// DecisionPathParams carries the room+month path params. Validator runs at
// bind time so we get the same Thai-language 400 surface as elsewhere.
type DecisionPathParams struct {
	RoomID       string `query:"-" validate:"required,uuid"`
	BillingMonth string `query:"-" validate:"required"`
}

// SetDecisionRequest is the PUT body. ApartmentID is required so the
// service can scope the room lookup (PD-origin guard reads from there).
type SetDecisionRequest struct {
	ApartmentID string `json:"apartment_id" validate:"required,uuid"`
	Decision    string `json:"decision" validate:"required,oneof=INCLUDE SKIP"`
}

// DeleteDecisionRequest mirrors SetDecisionRequest's apartment scope —
// reversal-boundary guard needs the apartment to re-classify the room.
type DeleteDecisionRequest struct {
	ApartmentID string `json:"apartment_id" validate:"required,uuid"`
}

// DecisionResponse is the GET payload (also returned on PUT for
// optimistic-update use on the FE). Carries the same attribution shape as
// the inline `DecisionEvidence` so the FE can reuse one renderer.
type DecisionResponse struct {
	RoomID        string  `json:"room_id"`
	BillingMonth  string  `json:"billing_month"`
	State         string  `json:"state"`
	DecidedAt     string  `json:"decided_at"`
	DecidedByName *string `json:"decided_by_name"`
}
