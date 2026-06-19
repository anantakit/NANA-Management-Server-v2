// Package paymentmethod holds the PaymentMethod primitive value type.
//
// PaymentMethod is a primitive — like Money or PhoneNumber — and has no
// workflow owner. It is consumed by the payment feature (bill_payments),
// the moveout feature (move_out_notices.payment_method snapshot), seed,
// and billing integration tests. Living in shared/ avoids duplicating the
// enum across features or parking it in a single-file domain/ junk drawer.
package paymentmethod

// PaymentMethod identifies how a payment was tendered.
type PaymentMethod string

const (
	Cash     PaymentMethod = "CASH"
	Transfer PaymentMethod = "TRANSFER"
)
