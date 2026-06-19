package settlement

// Settlement DTOs (CreateSettlementBillRequest, UpdateSettlementDraftRequest,
// PreviewSettlementInput, SettlementPreview, PreviewSettlementRequest,
// SettlementPreviewResponse, PreviewLineItemResponse, DepositBreakdownResponse,
// AbsorbedBillResponse, SettlementOutcome + variants, ToSettlementPreviewResponse
// converter) migrate in commit 6 of the W4 plan
// (project_billing_extraction_plan_locked.md). Until then they remain in
// billing/dto.go.
//
// ManualLineItemRequest is shared with UpdateMonthlyDraft and stays at
// billing root permanently — referenced here as billing.ManualLineItemRequest
// once the settlement DTOs migrate.
