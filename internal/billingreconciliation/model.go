package billingreconciliation

import (
	"regexp"
	"strconv"
	"time"

	"github.com/google/uuid"
)

// --- Bucket / Reason taxonomy ---
//
// Phase 1A is a read-only Audit (not a Decision Workspace) — the operator
// question is "is the categorization right?". Reason codes are intentionally
// granular so every "why is this room in this bucket?" question has a single
// answer. Do not umbrella-collapse codes until taxonomy is operator-validated.
//
// Math invariant: total = Ready + ActionRequired + PendingDecision + NotBillable.
// "Should Bill" (SB) = Ready + ActionRequired (see direction doc).

type Bucket string

const (
	BucketReady           Bucket = "READY"
	BucketActionRequired  Bucket = "ACTION_REQUIRED"
	BucketPendingDecision Bucket = "PENDING_DECISION"
	BucketNotBillable     Bucket = "NOT_BILLABLE"
)

type ReasonCode string

const (
	ReasonVacant              ReasonCode = "VACANT"
	ReasonMaintenance         ReasonCode = "MAINTENANCE"
	ReasonNoActiveContract    ReasonCode = "NO_ACTIVE_CONTRACT"
	ReasonMoveOutPending      ReasonCode = "MOVE_OUT_PENDING"
	ReasonContractNotStarted  ReasonCode = "CONTRACT_NOT_STARTED"
	ReasonMissingMeterReading ReasonCode = "MISSING_METER_READING"
	ReasonNewTenantMidCycle   ReasonCode = "NEW_TENANT_MID_CYCLE"
)

// --- Projection types (internal, mapped to DTO at the boundary) ---

// RoomCandidate is one row from the rooms ⟕ active-contracts ⟕ tenants
// JOIN. Active contract + tenant fields are nil/empty for rooms without an
// ACTIVE contract (vacant or maintenance).
type RoomCandidate struct {
	RoomID     uuid.UUID
	RoomNumber string
	RoomFloor  int
	RoomStatus string // room.RoomStatus stored as string (display read)

	ContractID        *uuid.UUID
	ContractStartDate *time.Time
	TenantName        string // empty when no active contract
}

// BillSnapshot is inline evidence of an existing MONTHLY bill for this
// (contract, billing_month). Surfacing it lets the operator see
// already-generated rooms WITHOUT shifting the room's bucket — a locked
// invariant from the 1A spec.
type BillSnapshot struct {
	BillID            uuid.UUID
	Status            string // billing.BillStatus as string
	TotalAmountSatang int64
}

// AnomalyFlags mirrors the meter reading's IsAnomaly* booleans for inline
// trust display. Nil when there's no meter reading for this room+month.
type AnomalyFlags struct {
	Electricity bool
	Water       bool
}

// RoomClassification = one row in the reconciliation report.
type RoomClassification struct {
	Room    RoomCandidate
	Bucket  Bucket
	Reason  ReasonCode // empty for READY
	Bill    *BillSnapshot
	Anomaly *AnomalyFlags
}

// Summary mirrors the math invariant. PD as a category disappears from the
// UI once empty (see direction doc § Math Invariant) — value still reported.
type Summary struct {
	Total           int
	Ready           int
	ActionRequired  int
	PendingDecision int
	NotBillable     int
	AnomalyCount    int
}

// Report is the full reconciliation payload returned by the service.
type Report struct {
	ApartmentID  uuid.UUID
	BillingMonth string
	Summary      Summary
	Rooms        []RoomClassification
}

// --- Pure classifier ---

// classifyRoom is the pure rule that assigns one room to a bucket + reason.
// First match wins:
//
//  1. No active contract + Room.Status=MAINTENANCE → NB / MAINTENANCE
//  2. No active contract + Room.Status=VACANT      → NB / VACANT
//  3. No active contract + other status            → NB / NO_ACTIVE_CONTRACT
//     (data inconsistency — OCCUPIED room without an active contract)
//  4. Pending move-out for this month              → NB / MOVE_OUT_PENDING
//  5. Contract.StartDate > endOfMonth              → NB / CONTRACT_NOT_STARTED
//  6. Contract.StartDate within billing window     → PD / NEW_TENANT_MID_CYCLE
//  7. No monthly meter reading                     → AR / MISSING_METER_READING
//  8. Else                                         → READY / ""
//
// Order rationale: PD (mid-cycle tenant) comes BEFORE missing-meter so the
// classification stays a decision problem first, a data problem second — a
// new tenant operator never decided to include should not be hidden behind
// a meter gap.
//
// Pre-existing bills are evidence only — passed in the response, never used
// to shift the bucket. Lock from session 2026-06-06.
func classifyRoom(
	c RoomCandidate,
	startOfMonth, endOfMonth time.Time,
	hasPendingMoveOut bool,
	hasMeter bool,
) (Bucket, ReasonCode) {
	if c.ContractID == nil {
		switch c.RoomStatus {
		case "MAINTENANCE":
			return BucketNotBillable, ReasonMaintenance
		case "VACANT":
			return BucketNotBillable, ReasonVacant
		default:
			return BucketNotBillable, ReasonNoActiveContract
		}
	}
	if hasPendingMoveOut {
		return BucketNotBillable, ReasonMoveOutPending
	}
	if c.ContractStartDate != nil && c.ContractStartDate.After(endOfMonth) {
		return BucketNotBillable, ReasonContractNotStarted
	}
	if c.ContractStartDate != nil &&
		!c.ContractStartDate.Before(startOfMonth) &&
		!c.ContractStartDate.After(endOfMonth) {
		return BucketPendingDecision, ReasonNewTenantMidCycle
	}
	if !hasMeter {
		return BucketActionRequired, ReasonMissingMeterReading
	}
	return BucketReady, ""
}

// --- Month helpers ---

var billingMonthRe = regexp.MustCompile(`^\d{4}-(0[1-9]|1[0-2])$`)

// parseBillingMonthRange converts "YYYY-MM" to first/last day of that month
// at 00:00 UTC. Matches billing/service_batch.go semantics intentionally —
// classification windows agree across endpoints.
func parseBillingMonthRange(billingMonth string) (start, end time.Time) {
	year, _ := strconv.Atoi(billingMonth[:4])
	month, _ := strconv.Atoi(billingMonth[5:7])
	start = time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
	end = start.AddDate(0, 1, -1)
	return start, end
}
