package settlement

import (
	"nana/internal/shared/database"
)

// Service is the settlement billing workflow's public surface.
//
// Concrete struct (NOT an interface) in commit 2 of the W4 extraction —
// no workflow methods have migrated yet, so an interface contract would
// be empty and misleading. Methods land in commits 4 (workflow methods)
// and 5 (moveout-port satisfaction); the struct stays concrete because
// the compile-time check `var _ moveout.BillingCommander = (*Service)(nil)`
// (added in commit 5) requires a concrete type to bind against.
//
// SCOPE NOTE: this is the SETTLEMENT billing workflow only. It is not a
// general document-replacement engine. Two correction flavors stay
// intentionally separate — MONTHLY correction lives at billing root,
// SETTLEMENT correction lives here. See doc.go for the full scope boundary.
type Service struct {
	bills          BillStore
	audit          AuditStore
	contracts      ContractSource
	meters         MeterReadingSource
	configs        BillingConfigSource
	moveOuts       MoveOutSource
	paymentRouting PaymentRoutingSource
	tx             database.TxManager
}

// NewService constructs the settlement workflow service. All port
// dependencies are injected; the constructor returns *Service (concrete)
// rather than an interface because the moveout-port compile-check in
// commit 5 binds against the concrete type.
//
// PaymentRoutingSource may be nil — settlement degrades gracefully to a
// null payment-destination snapshot when no rules are configured, mirroring
// the existing billingService behavior.
func NewService(
	bills BillStore,
	audit AuditStore,
	contracts ContractSource,
	meters MeterReadingSource,
	configs BillingConfigSource,
	moveOuts MoveOutSource,
	paymentRouting PaymentRoutingSource,
	tx database.TxManager,
) *Service {
	return &Service{
		bills:          bills,
		audit:          audit,
		contracts:      contracts,
		meters:         meters,
		configs:        configs,
		moveOuts:       moveOuts,
		paymentRouting: paymentRouting,
		tx:             tx,
	}
}
