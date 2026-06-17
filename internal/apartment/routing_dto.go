package apartment

type CreatePaymentDestinationRuleRequest struct {
	BankAccountID string  `json:"bank_account_id" validate:"required,uuid"`
	RuleType      string  `json:"rule_type" validate:"required,oneof=APARTMENT_DEFAULT ROOM_RANGE ROOM_OVERRIDE"`
	RoomNumber    *string `json:"room_number" validate:"omitempty,min=1,max=20"`
	RangeStart    *string `json:"range_start" validate:"omitempty,min=1,max=20"`
	RangeEnd      *string `json:"range_end" validate:"omitempty,min=1,max=20"`
}

type UpdatePaymentDestinationRuleRequest struct {
	BankAccountID *string `json:"bank_account_id" validate:"omitempty,uuid"`
}

type PaymentDestinationRuleResponse struct {
	ID            string  `json:"id"`
	ApartmentID   string  `json:"apartment_id"`
	BankAccountID string  `json:"bank_account_id"`
	RuleType      string  `json:"rule_type"`
	RoomNumber    *string `json:"room_number,omitempty"`
	RangeStart    *string `json:"range_start,omitempty"`
	RangeEnd      *string `json:"range_end,omitempty"`
	CreatedAt     string  `json:"created_at"`
	UpdatedAt     string  `json:"updated_at"`
}

func ToPaymentDestinationRuleResponse(r PaymentDestinationRule) PaymentDestinationRuleResponse {
	return PaymentDestinationRuleResponse{
		ID:            r.ID.String(),
		ApartmentID:   r.ApartmentID.String(),
		BankAccountID: r.BankAccountID.String(),
		RuleType:      string(r.RuleType),
		RoomNumber:    r.RoomNumber,
		RangeStart:    r.RangeStart,
		RangeEnd:      r.RangeEnd,
		CreatedAt:     r.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:     r.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

func ToPaymentDestinationRuleResponseList(rules []PaymentDestinationRule) []PaymentDestinationRuleResponse {
	result := make([]PaymentDestinationRuleResponse, len(rules))
	for i, r := range rules {
		result[i] = ToPaymentDestinationRuleResponse(r)
	}
	return result
}
