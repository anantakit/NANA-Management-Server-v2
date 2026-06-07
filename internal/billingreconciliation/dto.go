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
