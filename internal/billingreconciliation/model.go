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
	// Phase 1B reasons — surface only on rooms that were PD-origin
	// (NEW_TENANT_MID_CYCLE) and now carry an operator decision.
	ReasonIncludedByOperator ReasonCode = "INCLUDED_BY_OPERATOR"
	ReasonSkippedByOperator  ReasonCode = "SKIPPED_BY_OPERATOR"
)

// DecisionState is the operator's per-room choice for one billing month.
// Storage is INCLUDE / SKIP / absence — there is no UNDECIDED enum value
// because the row's existence carries that signal (no row = Undecided).
// Pre-spec walk locks this as the state grammar.
type DecisionState string

const (
	DecisionInclude DecisionState = "INCLUDE"
	DecisionSkip    DecisionState = "SKIP"
)

// Decision is the persisted row from `room_billing_decisions`. The struct
// doubles as the domain object the service passes around — there is no
// derived state to compute, so a separate domain type would only echo
// the GORM struct (per backend rules: GORM model = domain).
type Decision struct {
	ID           uuid.UUID     `gorm:"type:uuid;primaryKey;default:uuid_generate_v4()"`
	RoomID       uuid.UUID     `gorm:"type:uuid;not null"`
	BillingMonth string        `gorm:"type:varchar(7);not null"`
	Decision     DecisionState `gorm:"type:varchar(20);not null"`
	DecidedAt    time.Time     `gorm:"not null"`
	DecidedBy    *uuid.UUID    `gorm:"type:uuid"`
	CreatedAt    time.Time     `gorm:"not null;default:now()"`
	UpdatedAt    time.Time     `gorm:"not null;default:now()"`
}

func (Decision) TableName() string { return "room_billing_decisions" }

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

// DecisionAttribution is the inline "เพิ่มโดยคุณ · 15 มิ.ย." stamp on a
// decided row — joined from `users` at read time so the row carries its
// own attribution (per walk lock: no separate audit viewer). Nil when the
// room has no decision row for the month.
type DecisionAttribution struct {
	State     DecisionState
	DecidedAt time.Time
	// DecidedByName is the display name (auth.User.Username; FullName falls
	// back when present). Nullable to tolerate ON DELETE SET NULL — a row
	// whose actor was removed still surfaces "ระบบ"-style "—" attribution.
	DecidedByName *string
}

// RoomClassification = one row in the reconciliation report.
type RoomClassification struct {
	Room        RoomCandidate
	Bucket      Bucket
	Reason      ReasonCode // empty for READY
	Bill        *BillSnapshot
	Anomaly     *AnomalyFlags
	Attribution *DecisionAttribution
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

// --- Generate (1D ออกบิล) types ---
//
// Per-row commit outcome from the reconciliation workspace's Generate fan-out.
// Vocabulary follows the Option A pattern (project_billing_batch_option_a.md):
// SUCCESS = bill created; SKIPPED = business-rule guard fired before/during
// commit; FAILED = system error.
//
// Locked vocabulary per project_reconciliation_phase1d_scenario1_locks.md Q1.

type GenerateItemResultType string

const (
	GenerateItemSuccess GenerateItemResultType = "SUCCESS"
	GenerateItemSkipped GenerateItemResultType = "SKIPPED"
	GenerateItemFailed  GenerateItemResultType = "FAILED"
)

// GenerateSkipReason names the specific business-rule reason a Generate
// attempt did NOT create a bill. Vocabulary frozen at the walk and surfaced
// on the workspace's transient feedback line.
type GenerateSkipReason string

const (
	// GenerateSkipLostReady — room was no longer in a billable state at
	// commit time (meter missing/changed, contract status flipped, bucket
	// re-classified between Reconcile and Generate). Operator-facing
	// message: "สถานะเปลี่ยนระหว่างการตรวจสอบและการบันทึก".
	GenerateSkipLostReady GenerateSkipReason = "LOST_READY_BETWEEN_PREVIEW_AND_COMMIT"

	// GenerateSkipAlreadyBilled — a non-VOID monthly bill already exists
	// for the (contract, billing_month). Could be a pre-existing bill
	// (operator drove past the inline evidence on the row) or a race with
	// another caller mid-fan-out.
	GenerateSkipAlreadyBilled GenerateSkipReason = "ALREADY_BILLED_BY_OTHER"
)

// GenerateRequest is the per-call input to the Generate fan-out. room_ids[]
// is the reviewed set — CTA count is contractual (Q1 Contract A): the
// caller sends what it believes is the READY-and-not-yet-billed set,
// service re-validates per-row and emits ≤ len(room_ids) results.
//
// Anti-promotion: room_ids[] is a slice on the request, not a
// "ReviewedSet" entity. The fan-out is a service-level loop, not a batch
// orchestration object.
type GenerateRequest struct {
	ApartmentID  uuid.UUID
	BillingMonth string
	RoomIDs      []uuid.UUID
}

// GenerateItemResult is one entry in the fan-out response — one row per
// requested room_id (same order as the request).
type GenerateItemResult struct {
	RoomID       uuid.UUID
	ResultType   GenerateItemResultType
	BillID       *uuid.UUID         // non-nil when ResultType == GenerateItemSuccess
	SkipReason   GenerateSkipReason // non-empty when ResultType == GenerateItemSkipped
	ErrorCode    string             // non-empty when ResultType == GenerateItemFailed
	ErrorMessage string             // Thai user-facing string for FAILED rows
}

// GenerateResult bundles the per-item results plus the per-call counts the
// FE banner / toast renders. Counts mirror Items but precomputed so the
// FE doesn't have to scan.
type GenerateResult struct {
	BillingMonth string
	Items        []GenerateItemResult
	SuccessCount int
	SkippedCount int
	FailedCount  int
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
//  6. Contract.StartDate within billing window:
//     6a. decision = SKIP    → NB    / SKIPPED_BY_OPERATOR     (Phase 1B)
//     6b. decision = INCLUDE → fall through (meter check still applies)
//     6c. decision = none    → PD    / NEW_TENANT_MID_CYCLE
//  7. No monthly meter reading                     → AR / MISSING_METER_READING
//  8. Else:
//     8a. carries INCLUDE decision (was mid-cycle) → READY / INCLUDED_BY_OPERATOR
//     8b. otherwise                                 → READY / ""
//
// Order rationale: PD (mid-cycle tenant) comes BEFORE missing-meter so the
// classification stays a decision problem first, a data problem second — a
// new tenant operator never decided to include should not be hidden behind
// a meter gap. Walk-lock invariant (Phase 1B): "Rebucket rule: PD + INCLUDE
// → READY (if meter present) / AR (if meter missing). PD + SKIP → NB."
// INCLUDE never forces past the meter check — the data gap stays an
// honest AR row that's actionable by the operator.
//
// Scope guard (walk lock): decisions only carry meaning on PD-origin rows.
// If a row is already READY/AR/NB on the raw rules and somehow has a stale
// decision row, the decision is silently ignored — see step 6 entry guard.
//
// Pre-existing bills are evidence only — passed in the response, never used
// to shift the bucket. Lock from session 2026-06-06.
func classifyRoom(
	c RoomCandidate,
	startOfMonth, endOfMonth time.Time,
	hasPendingMoveOut bool,
	hasMeter bool,
	decision DecisionState, // empty string = Undecided / no row
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
	isMidCycle := c.ContractStartDate != nil &&
		!c.ContractStartDate.Before(startOfMonth) &&
		!c.ContractStartDate.After(endOfMonth)
	if isMidCycle {
		switch decision {
		case DecisionSkip:
			return BucketNotBillable, ReasonSkippedByOperator
		case DecisionInclude:
			// fall through — meter check still applies, READY case carries
			// the INCLUDED_BY_OPERATOR reason so the row reads as
			// operator-resolved rather than a generic ready.
		default:
			return BucketPendingDecision, ReasonNewTenantMidCycle
		}
	}
	if !hasMeter {
		return BucketActionRequired, ReasonMissingMeterReading
	}
	if isMidCycle && decision == DecisionInclude {
		return BucketReady, ReasonIncludedByOperator
	}
	return BucketReady, ""
}

// rawIsPDOrigin answers "would this room land in PD without an operator
// decision?". Cheap pure check that lets the service decide whether to
// surface decision attribution on the row: attribution only matters when
// the raw rules would have asked the operator to decide.
//
// Mirrors steps 1–6 of classifyRoom — kept narrowly here so the predicate
// stays a single expression instead of inverting a classifier call. If
// classifyRoom's rule order ever changes, both must move together.
func rawIsPDOrigin(c RoomCandidate, startOfMonth, endOfMonth time.Time, hasPendingMoveOut bool) bool {
	if c.ContractID == nil {
		return false
	}
	if hasPendingMoveOut {
		return false
	}
	if c.ContractStartDate == nil {
		return false
	}
	if c.ContractStartDate.After(endOfMonth) {
		return false
	}
	return !c.ContractStartDate.Before(startOfMonth)
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
