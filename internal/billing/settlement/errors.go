package settlement

// Settlement-specific error sentinels migrate in commit 4 of the W4 plan
// (project_billing_extraction_plan_locked.md). Until then, ErrMoveOutNotFound,
// ErrActualDateRequired, and ErrExitReadingMissing remain in billing/errors.go
// and the soon-to-be-moved settlement service files reach them as billing.ErrXxx.
//
// Shared sentinels (ErrBillNotFound, ErrBillAlreadyExists, ErrContractNotFound,
// ErrMeterNotFound) stay at billing root permanently — they're shared with
// monthly and the bill repo.
