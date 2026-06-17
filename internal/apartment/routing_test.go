package apartment

import (
	"testing"

	"github.com/google/uuid"
)

func TestResolvePaymentDestination_Priority(t *testing.T) {
	acctA := ApartmentBankAccount{ID: uuid.MustParse("aaaaaaaa-0000-0000-0000-000000000001"), BankName: "Bank A", AccountNumber: "111", AccountName: "Account A"}
	acctB := ApartmentBankAccount{ID: uuid.MustParse("bbbbbbbb-0000-0000-0000-000000000002"), BankName: "Bank B", AccountNumber: "222", AccountName: "Account B"}
	acctC := ApartmentBankAccount{ID: uuid.MustParse("cccccccc-0000-0000-0000-000000000003"), BankName: "Bank C", AccountNumber: "333", AccountName: "Account C"}

	rangeStart := "A101"
	rangeEnd := "A120"
	overrideRoom := "A109"

	rules := []PaymentDestinationRule{
		{RuleType: RuleTypeApartmentDefault, BankAccountID: acctA.ID},
		{RuleType: RuleTypeRoomRange, BankAccountID: acctB.ID, RangeStart: &rangeStart, RangeEnd: &rangeEnd},
		{RuleType: RuleTypeRoomOverride, BankAccountID: acctC.ID, RoomNumber: &overrideRoom},
	}
	accounts := map[uuid.UUID]ApartmentBankAccount{acctA.ID: acctA, acctB.ID: acctB, acctC.ID: acctC}

	// Override beats range beats default
	dest := ResolvePaymentDestination(rules, accounts, "A109")
	if dest == nil || dest.AccountNumber != "333" {
		t.Errorf("expected room override (acctC), got %+v", dest)
	}

	// Range beats default
	dest = ResolvePaymentDestination(rules, accounts, "A115")
	if dest == nil || dest.AccountNumber != "222" {
		t.Errorf("expected range (acctB), got %+v", dest)
	}

	// Apartment default for unmatched room
	dest = ResolvePaymentDestination(rules, accounts, "B201")
	if dest == nil || dest.AccountNumber != "111" {
		t.Errorf("expected apartment default (acctA), got %+v", dest)
	}
}

func TestResolvePaymentDestination_NoRules(t *testing.T) {
	dest := ResolvePaymentDestination(nil, nil, "A101")
	if dest != nil {
		t.Errorf("expected nil when no rules, got %+v", dest)
	}
}

func TestResolvePaymentDestination_RangeEdges(t *testing.T) {
	acct := ApartmentBankAccount{ID: uuid.MustParse("aaaaaaaa-0000-0000-0000-000000000001"), AccountNumber: "111"}
	start, end := "A101", "A120"
	rules := []PaymentDestinationRule{
		{RuleType: RuleTypeRoomRange, BankAccountID: acct.ID, RangeStart: &start, RangeEnd: &end},
	}
	accounts := map[uuid.UUID]ApartmentBankAccount{acct.ID: acct}

	for _, room := range []string{"A101", "A110", "A120"} {
		if d := ResolvePaymentDestination(rules, accounts, room); d == nil {
			t.Errorf("room %s should be in range A101-A120", room)
		}
	}
	for _, room := range []string{"A100", "A121", "B110"} {
		if d := ResolvePaymentDestination(rules, accounts, room); d != nil {
			t.Errorf("room %s should NOT be in range A101-A120", room)
		}
	}
}

func TestValidateRoomRange(t *testing.T) {
	cases := []struct {
		start, end string
		wantErr    bool
	}{
		{"A101", "A120", false},
		{"101", "120", false},
		{"A101", "A101", false}, // single room range
		{"A101", "B120", true},  // different prefix
		{"A120", "A101", true},  // start > end
		{"ABC", "ABD", true},    // no numeric suffix
		{"A101", "A100", true},  // start > end
	}
	for _, c := range cases {
		err := ValidateRoomRange(c.start, c.end)
		if (err != nil) != c.wantErr {
			t.Errorf("ValidateRoomRange(%q, %q): wantErr=%v, got err=%v", c.start, c.end, c.wantErr, err)
		}
	}
}

func TestSplitRoomNumber(t *testing.T) {
	cases := []struct {
		input      string
		wantPrefix string
		wantNum    int
		wantOK     bool
	}{
		{"A101", "A", 101, true},
		{"101", "", 101, true},
		{"AB205", "AB", 205, true},
		{"ABC", "", 0, false},
		{"", "", 0, false},
	}
	for _, c := range cases {
		p, n, ok := splitRoomNumber(c.input)
		if ok != c.wantOK || p != c.wantPrefix || n != c.wantNum {
			t.Errorf("splitRoomNumber(%q): got (%q, %d, %v), want (%q, %d, %v)",
				c.input, p, n, ok, c.wantPrefix, c.wantNum, c.wantOK)
		}
	}
}

// TestRangesOverlap verifies the pure overlap predicate used at rule-creation time.
// Two ranges overlap when they share the same building prefix and their numeric
// intervals intersect (inclusive). Adjacent ranges (A101-A110, A111-A120) do NOT overlap.
func TestRangesOverlap(t *testing.T) {
	cases := []struct {
		s1, e1, s2, e2 string
		want           bool
		desc           string
	}{
		{"A101", "A120", "A110", "A130", true, "partial overlap"},
		{"A101", "A120", "A101", "A120", true, "identical ranges"},
		{"A101", "A120", "A105", "A110", true, "range2 inside range1"},
		{"A101", "A120", "A100", "A101", true, "touches start"},
		{"A101", "A120", "A120", "A130", true, "touches end"},
		{"A101", "A110", "A111", "A120", false, "adjacent — no overlap"},
		{"A101", "A110", "A100", "A100", false, "just below"},
		{"A101", "A110", "A111", "A111", false, "just above"},
		{"A101", "A120", "B101", "B120", false, "different building prefix"},
		{"A101", "A120", "A201", "A220", false, "same prefix letter, different range"},
	}
	for _, c := range cases {
		got := RangesOverlap(c.s1, c.e1, c.s2, c.e2)
		if got != c.want {
			t.Errorf("[%s] RangesOverlap(%q,%q,%q,%q) = %v, want %v",
				c.desc, c.s1, c.e1, c.s2, c.e2, got, c.want)
		}
	}
}

// TestResolvePaymentDestination_MultipleRanges_DeterministicOrder verifies that
// the resolver handles overlapping ranges deterministically (first in slice wins),
// ordered by created_at ASC. Overlapping ranges are rejected at create time, but
// the resolver remains deterministic as a defensive guarantee.
func TestResolvePaymentDestination_MultipleRanges_DeterministicOrder(t *testing.T) {
	acctA := ApartmentBankAccount{ID: uuid.MustParse("aaaaaaaa-0000-0000-0000-000000000001"), AccountNumber: "111"}
	acctB := ApartmentBankAccount{ID: uuid.MustParse("bbbbbbbb-0000-0000-0000-000000000002"), AccountNumber: "222"}

	s1, e1 := "A101", "A120" // first range  — covers A101-A120
	s2, e2 := "A110", "A130" // second range — overlaps A110-A120

	// Slice order = creation order (oldest first) — acctA rule was created first.
	rules := []PaymentDestinationRule{
		{RuleType: RuleTypeRoomRange, BankAccountID: acctA.ID, RangeStart: &s1, RangeEnd: &e1},
		{RuleType: RuleTypeRoomRange, BankAccountID: acctB.ID, RangeStart: &s2, RangeEnd: &e2},
	}
	accounts := map[uuid.UUID]ApartmentBankAccount{acctA.ID: acctA, acctB.ID: acctB}

	// A115 is in both ranges — first rule (acctA) must win.
	dest := ResolvePaymentDestination(rules, accounts, "A115")
	if dest == nil || dest.AccountNumber != "111" {
		t.Errorf("A115: expected first range (acctA/111), got %+v", dest)
	}

	// A125 is only in acctB's range.
	dest = ResolvePaymentDestination(rules, accounts, "A125")
	if dest == nil || dest.AccountNumber != "222" {
		t.Errorf("A125: expected second range (acctB/222), got %+v", dest)
	}

	// A100 is in neither range.
	if dest = ResolvePaymentDestination(rules, accounts, "A100"); dest != nil {
		t.Errorf("A100: expected nil (no match), got %+v", dest)
	}
}
