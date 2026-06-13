package billingreconciliation

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

// classifyRoom is the entire taxonomy in one pure function. Every bucket +
// reason must be exercised here — "ถ้าพังจะพังเงียบ" per testing-strategy.md.
func TestClassifyRoom(t *testing.T) {
	startOfMonth, endOfMonth := parseBillingMonthRange("2026-06")
	contractID := uuid.New()

	mkDate := func(s string) *time.Time {
		v, err := time.Parse("2006-01-02", s)
		if err != nil {
			t.Fatalf("bad fixture date %q: %v", s, err)
		}
		return &v
	}

	cases := []struct {
		name           string
		room           RoomCandidate
		pendingMoveOut bool
		hasMeter       bool
		decision       DecisionState
		wantBucket     Bucket
		wantReason     ReasonCode
	}{
		{
			name:       "vacant room with no contract → NB / VACANT",
			room:       RoomCandidate{RoomStatus: "VACANT"},
			wantBucket: BucketNotBillable,
			wantReason: ReasonVacant,
		},
		{
			name:       "maintenance room with no contract → NB / MAINTENANCE",
			room:       RoomCandidate{RoomStatus: "MAINTENANCE"},
			wantBucket: BucketNotBillable,
			wantReason: ReasonMaintenance,
		},
		{
			name:       "occupied room with no active contract → NB / NO_ACTIVE_CONTRACT (data inconsistency)",
			room:       RoomCandidate{RoomStatus: "OCCUPIED"},
			wantBucket: BucketNotBillable,
			wantReason: ReasonNoActiveContract,
		},
		{
			name: "active contract + pending move-out → NB / MOVE_OUT_PENDING",
			room: RoomCandidate{
				RoomStatus:        "OCCUPIED",
				ContractID:        &contractID,
				ContractStartDate: mkDate("2026-01-01"),
			},
			pendingMoveOut: true,
			hasMeter:       true,
			wantBucket:     BucketNotBillable,
			wantReason:     ReasonMoveOutPending,
		},
		{
			name: "contract starts after this month → NB / CONTRACT_NOT_STARTED",
			room: RoomCandidate{
				RoomStatus:        "OCCUPIED",
				ContractID:        &contractID,
				ContractStartDate: mkDate("2026-07-15"),
			},
			wantBucket: BucketNotBillable,
			wantReason: ReasonContractNotStarted,
		},
		{
			name: "contract starts mid-cycle this month → PD / NEW_TENANT_MID_CYCLE",
			room: RoomCandidate{
				RoomStatus:        "OCCUPIED",
				ContractID:        &contractID,
				ContractStartDate: mkDate("2026-06-15"),
			},
			hasMeter:   true,
			wantBucket: BucketPendingDecision,
			wantReason: ReasonNewTenantMidCycle,
		},
		{
			name: "contract starts on 1st of cycle still counts as mid-cycle (operator decides)",
			room: RoomCandidate{
				RoomStatus:        "OCCUPIED",
				ContractID:        &contractID,
				ContractStartDate: mkDate("2026-06-01"),
			},
			hasMeter:   true,
			wantBucket: BucketPendingDecision,
			wantReason: ReasonNewTenantMidCycle,
		},
		{
			name: "contract starts on last day of cycle → PD (boundary)",
			room: RoomCandidate{
				RoomStatus:        "OCCUPIED",
				ContractID:        &contractID,
				ContractStartDate: mkDate("2026-06-30"),
			},
			hasMeter:   true,
			wantBucket: BucketPendingDecision,
			wantReason: ReasonNewTenantMidCycle,
		},
		{
			name: "existing tenant (started prior month), missing meter → AR / MISSING_METER_READING",
			room: RoomCandidate{
				RoomStatus:        "OCCUPIED",
				ContractID:        &contractID,
				ContractStartDate: mkDate("2026-03-01"),
			},
			hasMeter:   false,
			wantBucket: BucketActionRequired,
			wantReason: ReasonMissingMeterReading,
		},
		{
			name: "existing tenant + meter present → READY",
			room: RoomCandidate{
				RoomStatus:        "OCCUPIED",
				ContractID:        &contractID,
				ContractStartDate: mkDate("2026-03-01"),
			},
			hasMeter:   true,
			wantBucket: BucketReady,
			wantReason: "",
		},
		{
			name: "PD must beat AR when contract is mid-cycle AND meter missing (rule order lock)",
			room: RoomCandidate{
				RoomStatus:        "OCCUPIED",
				ContractID:        &contractID,
				ContractStartDate: mkDate("2026-06-10"),
			},
			hasMeter:   false,
			wantBucket: BucketPendingDecision,
			wantReason: ReasonNewTenantMidCycle,
		},
		{
			name: "move-out beats every other rule (NB / MOVE_OUT_PENDING)",
			room: RoomCandidate{
				RoomStatus:        "OCCUPIED",
				ContractID:        &contractID,
				ContractStartDate: mkDate("2026-06-10"), // would be PD without move-out
			},
			pendingMoveOut: true,
			hasMeter:       false,
			wantBucket:     BucketNotBillable,
			wantReason:     ReasonMoveOutPending,
		},
	}

	// Phase 1B decision-aware cases. Cover both rebucket targets (INCLUDE
	// → READY, SKIP → NB), the AR fallback when INCLUDE meets a missing
	// meter, and the silent-ignore guard when a decision sits on a non-PD
	// row (scope = PD-origin only, per walk lock).
	cases = append(cases,
		struct {
			name           string
			room           RoomCandidate
			pendingMoveOut bool
			hasMeter       bool
			decision       DecisionState
			wantBucket     Bucket
			wantReason     ReasonCode
		}{
			name: "mid-cycle + INCLUDE + meter present → READY / INCLUDED_BY_OPERATOR",
			room: RoomCandidate{
				RoomStatus:        "OCCUPIED",
				ContractID:        &contractID,
				ContractStartDate: mkDate("2026-06-15"),
			},
			hasMeter:   true,
			decision:   DecisionInclude,
			wantBucket: BucketReady,
			wantReason: ReasonIncludedByOperator,
		},
		struct {
			name           string
			room           RoomCandidate
			pendingMoveOut bool
			hasMeter       bool
			decision       DecisionState
			wantBucket     Bucket
			wantReason     ReasonCode
		}{
			name: "mid-cycle + INCLUDE + meter missing → AR / MISSING_METER_READING (operator intent honored, data gap still actionable)",
			room: RoomCandidate{
				RoomStatus:        "OCCUPIED",
				ContractID:        &contractID,
				ContractStartDate: mkDate("2026-06-15"),
			},
			hasMeter:   false,
			decision:   DecisionInclude,
			wantBucket: BucketActionRequired,
			wantReason: ReasonMissingMeterReading,
		},
		struct {
			name           string
			room           RoomCandidate
			pendingMoveOut bool
			hasMeter       bool
			decision       DecisionState
			wantBucket     Bucket
			wantReason     ReasonCode
		}{
			name: "mid-cycle + SKIP → NB / SKIPPED_BY_OPERATOR (meter state irrelevant)",
			room: RoomCandidate{
				RoomStatus:        "OCCUPIED",
				ContractID:        &contractID,
				ContractStartDate: mkDate("2026-06-15"),
			},
			hasMeter:   false,
			decision:   DecisionSkip,
			wantBucket: BucketNotBillable,
			wantReason: ReasonSkippedByOperator,
		},
		struct {
			name           string
			room           RoomCandidate
			pendingMoveOut bool
			hasMeter       bool
			decision       DecisionState
			wantBucket     Bucket
			wantReason     ReasonCode
		}{
			name: "stale INCLUDE on a non-mid-cycle room is silently ignored (scope = PD-origin only)",
			room: RoomCandidate{
				RoomStatus:        "OCCUPIED",
				ContractID:        &contractID,
				ContractStartDate: mkDate("2026-03-01"),
			},
			hasMeter:   true,
			decision:   DecisionInclude,
			wantBucket: BucketReady,
			wantReason: "", // not INCLUDED_BY_OPERATOR — the row was never PD
		},
		struct {
			name           string
			room           RoomCandidate
			pendingMoveOut bool
			hasMeter       bool
			decision       DecisionState
			wantBucket     Bucket
			wantReason     ReasonCode
		}{
			name: "move-out beats SKIP decision (raw rule order preserved)",
			room: RoomCandidate{
				RoomStatus:        "OCCUPIED",
				ContractID:        &contractID,
				ContractStartDate: mkDate("2026-06-10"),
			},
			pendingMoveOut: true,
			decision:       DecisionSkip,
			wantBucket:     BucketNotBillable,
			wantReason:     ReasonMoveOutPending,
		},
	)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotBucket, gotReason := classifyRoom(tc.room, startOfMonth, endOfMonth, tc.pendingMoveOut, tc.hasMeter, tc.decision)
			if gotBucket != tc.wantBucket {
				t.Errorf("bucket: got %q, want %q", gotBucket, tc.wantBucket)
			}
			if gotReason != tc.wantReason {
				t.Errorf("reason: got %q, want %q", gotReason, tc.wantReason)
			}
		})
	}
}

// rawIsPDOrigin is the attribution gate — must agree with classifyRoom's
// PD branch so attribution only surfaces when the row is genuinely operator-
// facing. Any divergence is a silent correctness bug (attribution on a
// non-PD row, or missing attribution on a real PD row).
func TestRawIsPDOrigin(t *testing.T) {
	start, end := parseBillingMonthRange("2026-06")
	cid := uuid.New()

	mkDate := func(s string) *time.Time {
		v, err := time.Parse("2006-01-02", s)
		if err != nil {
			t.Fatalf("bad fixture date %q: %v", s, err)
		}
		return &v
	}

	cases := []struct {
		name           string
		contractID     *uuid.UUID
		startDate      *time.Time
		pendingMoveOut bool
		want           bool
	}{
		{
			name:       "no contract → false",
			contractID: nil,
			startDate:  mkDate("2026-06-15"),
			want:       false,
		},
		{
			name:           "pending move-out beats mid-cycle → false",
			contractID:     &cid,
			startDate:      mkDate("2026-06-15"),
			pendingMoveOut: true,
			want:           false,
		},
		{
			name:       "nil start date → false",
			contractID: &cid,
			startDate:  nil,
			want:       false,
		},
		{
			name:       "contract started prior month → false (existing tenant)",
			contractID: &cid,
			startDate:  mkDate("2026-05-01"),
			want:       false,
		},
		{
			name:       "contract starts on 1st of month → true (lower boundary)",
			contractID: &cid,
			startDate:  mkDate("2026-06-01"),
			want:       true,
		},
		{
			name:       "contract starts mid-month → true",
			contractID: &cid,
			startDate:  mkDate("2026-06-15"),
			want:       true,
		},
		{
			name:       "contract starts on last day of month → true (upper boundary)",
			contractID: &cid,
			startDate:  mkDate("2026-06-30"),
			want:       true,
		},
		{
			name:       "contract starts after end-of-month → false (next cycle)",
			contractID: &cid,
			startDate:  mkDate("2026-07-01"),
			want:       false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := RoomCandidate{
				ContractID:        tc.contractID,
				ContractStartDate: tc.startDate,
			}
			got := rawIsPDOrigin(c, start, end, tc.pendingMoveOut)
			if got != tc.want {
				t.Errorf("rawIsPDOrigin = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParseBillingMonthRange(t *testing.T) {
	start, end := parseBillingMonthRange("2026-06")
	if !start.Equal(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("start: got %v, want 2026-06-01", start)
	}
	if !end.Equal(time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("end: got %v, want 2026-06-30", end)
	}

	// February non-leap
	_, feb := parseBillingMonthRange("2026-02")
	if feb.Day() != 28 {
		t.Errorf("Feb 2026 end day: got %d, want 28", feb.Day())
	}

	// Leap year
	_, febLeap := parseBillingMonthRange("2024-02")
	if febLeap.Day() != 29 {
		t.Errorf("Feb 2024 end day: got %d, want 29", febLeap.Day())
	}
}
