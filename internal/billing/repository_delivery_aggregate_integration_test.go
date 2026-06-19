//go:build integration

package billing

import (
	"context"
	"testing"
	"time"

	"nana/internal/testutil/fixtures"
	"nana/internal/testutil/testdb"

	"github.com/google/uuid"
)

// deliveryRow is a minimal local mirror of billdelivery.BillDelivery so this
// test can insert rows directly without importing billdelivery (which would
// re-introduce a billing → billdelivery → billing import cycle via port.go).
// Only the columns the aggregate reads/keys on are mapped.
type deliveryRow struct {
	ID          uuid.UUID `gorm:"type:uuid;primaryKey;default:uuid_generate_v4()"`
	BillID      uuid.UUID `gorm:"type:uuid;not null"`
	Channel     string    `gorm:"type:text;not null;default:'LINE_MANUAL'"`
	DeliveredAt time.Time `gorm:"type:timestamptz;not null;default:now()"`
}

func (deliveryRow) TableName() string { return "bill_deliveries" }

// TestFindAll_DeliveryAggregate pins the LEFT JOIN LATERAL aggregate that
// powers `delivery_count` + `last_delivered_at` on the bill list. The
// aggregate is the substrate behind the FE BillList delivery chip; a subtle
// SQL change (dropping the correlation predicate, missing soft-delete handling,
// counting wrong rows) would silently break the chip without surfacing in any
// existing unit test.
//
// Three bills exercised in one query: zero deliveries, one delivery,
// three deliveries. last_delivered_at must equal the MAX over the three-delivery
// case — proves the aggregate isn't returning the first row.
func TestFindAll_DeliveryAggregate(t *testing.T) {
	db := testdb.Open(t)
	testdb.TruncateAll(t, db)

	apt := fixtures.SeedApartment(t, db)
	tn1 := fixtures.SeedTenant(t, db)
	tn2 := fixtures.SeedTenant(t, db)
	tn3 := fixtures.SeedTenant(t, db)
	rm1 := fixtures.SeedRoom(t, db, apt.ID.String(), "T-301")
	rm2 := fixtures.SeedRoom(t, db, apt.ID.String(), "T-302")
	rm3 := fixtures.SeedRoom(t, db, apt.ID.String(), "T-303")
	c1 := fixtures.SeedContract(t, db, tn1.ID.String(), rm1.ID.String(), 3)
	c2 := fixtures.SeedContract(t, db, tn2.ID.String(), rm2.ID.String(), 3)
	c3 := fixtures.SeedContract(t, db, tn3.ID.String(), rm3.ID.String(), 3)

	const month = "2026-04"

	billZero := Bill{ContractID: c1.ID, BillingMonth: month, BillType: BillTypeMonthly, Status: BillStatusFinalized}
	if err := db.Create(&billZero).Error; err != nil {
		t.Fatalf("create bill zero: %v", err)
	}
	billOne := Bill{ContractID: c2.ID, BillingMonth: month, BillType: BillTypeMonthly, Status: BillStatusFinalized}
	if err := db.Create(&billOne).Error; err != nil {
		t.Fatalf("create bill one: %v", err)
	}
	billMany := Bill{ContractID: c3.ID, BillingMonth: month, BillType: BillTypeMonthly, Status: BillStatusFinalized}
	if err := db.Create(&billMany).Error; err != nil {
		t.Fatalf("create bill many: %v", err)
	}

	// One delivery on billOne.
	deliveryOne := deliveryRow{
		BillID:      billOne.ID,
		Channel:     "LINE_MANUAL",
		DeliveredAt: time.Date(2026, 5, 1, 9, 30, 0, 0, time.UTC),
	}
	if err := db.Create(&deliveryOne).Error; err != nil {
		t.Fatalf("create delivery one: %v", err)
	}

	// Three deliveries on billMany at distinct timestamps; latest must surface.
	deliveryTimes := []time.Time{
		time.Date(2026, 5, 3, 8, 0, 0, 0, time.UTC),
		time.Date(2026, 5, 5, 14, 15, 0, 0, time.UTC),
		time.Date(2026, 5, 7, 10, 45, 0, 0, time.UTC), // latest
	}
	for _, ts := range deliveryTimes {
		row := deliveryRow{
			BillID:      billMany.ID,
			Channel:     "LINE_MANUAL",
			DeliveredAt: ts,
		}
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("create delivery many @ %s: %v", ts, err)
		}
	}

	repo := NewBillingRepository(db)
	params := BillListParams{
		ApartmentID: apt.ID.String(),
		Month:       month,
	}
	params.Normalize()
	rows, _, err := repo.FindAll(context.Background(), params)
	if err != nil {
		t.Fatalf("FindAll: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(rows))
	}

	byID := make(map[uuid.UUID]BillWithRelations, len(rows))
	for _, r := range rows {
		byID[r.ID] = r
	}

	got := byID[billZero.ID]
	if got.DeliveryCount != 0 {
		t.Errorf("billZero delivery_count = %d, want 0", got.DeliveryCount)
	}
	if got.LastDeliveredAt != nil {
		t.Errorf("billZero last_delivered_at = %v, want nil", got.LastDeliveredAt)
	}

	got = byID[billOne.ID]
	if got.DeliveryCount != 1 {
		t.Errorf("billOne delivery_count = %d, want 1", got.DeliveryCount)
	}
	if got.LastDeliveredAt == nil || !got.LastDeliveredAt.Equal(deliveryOne.DeliveredAt) {
		t.Errorf("billOne last_delivered_at = %v, want %v", got.LastDeliveredAt, deliveryOne.DeliveredAt)
	}

	got = byID[billMany.ID]
	if got.DeliveryCount != 3 {
		t.Errorf("billMany delivery_count = %d, want 3", got.DeliveryCount)
	}
	wantLatest := deliveryTimes[len(deliveryTimes)-1]
	if got.LastDeliveredAt == nil || !got.LastDeliveredAt.Equal(wantLatest) {
		t.Errorf("billMany last_delivered_at = %v, want %v (MAX), got is NOT the latest",
			got.LastDeliveredAt, wantLatest)
	}
}
