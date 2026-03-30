package apartment

import "nana/internal/domain"

type CreateBankAccountRequest struct {
	BankName      string  `json:"bank_name" validate:"required,min=1,max=100"`
	AccountName   string  `json:"account_name" validate:"required,min=1,max=255"`
	AccountNumber string  `json:"account_number" validate:"required,min=1,max=50"`
	PromptPayID   *string `json:"promptpay_id" validate:"omitempty,max=20"`
	IsPrimary     bool    `json:"is_primary"`
	Note          *string `json:"note" validate:"omitempty,max=500"`
}

type UpdateBankAccountRequest struct {
	BankName      *string `json:"bank_name" validate:"omitempty,min=1,max=100"`
	AccountName   *string `json:"account_name" validate:"omitempty,min=1,max=255"`
	AccountNumber *string `json:"account_number" validate:"omitempty,min=1,max=50"`
	PromptPayID   *string `json:"promptpay_id" validate:"omitempty,max=20"`
	IsPrimary     *bool   `json:"is_primary"`
	Note          *string `json:"note" validate:"omitempty,max=500"`
}

type BankAccountResponse struct {
	ID            string  `json:"id"`
	ApartmentID   string  `json:"apartment_id"`
	BankName      string  `json:"bank_name"`
	AccountName   string  `json:"account_name"`
	AccountNumber string  `json:"account_number"`
	PromptPayID   *string `json:"promptpay_id"`
	IsPrimary     bool    `json:"is_primary"`
	Note          *string `json:"note"`
	CreatedAt     string  `json:"created_at"`
	UpdatedAt     string  `json:"updated_at"`
}

func ToBankAccountResponse(b domain.ApartmentBankAccount) BankAccountResponse {
	return BankAccountResponse{
		ID:            b.ID.String(),
		ApartmentID:   b.ApartmentID.String(),
		BankName:      b.BankName,
		AccountName:   b.AccountName,
		AccountNumber: b.AccountNumber,
		PromptPayID:   b.PromptPayID,
		IsPrimary:     b.IsPrimary,
		Note:          b.Note,
		CreatedAt:     b.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:     b.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

func ToBankAccountResponseList(accounts []domain.ApartmentBankAccount) []BankAccountResponse {
	result := make([]BankAccountResponse, len(accounts))
	for i, a := range accounts {
		result[i] = ToBankAccountResponse(a)
	}
	return result
}
