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

// TestResolvePaymentDestination_MultipleRanges_FirstMatchWins verifies that when
// multiple ROOM_RANGE rules overlap, the resolver returns the first match in the
// slice order (which mirrors FindByApartmentID ORDER BY created_at ASC).
func TestResolvePaymentDestination_MultipleRanges_FirstMatchWins(t *testing.T) {
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
