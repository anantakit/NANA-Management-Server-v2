package service

import (
	"context"
	"fmt"

	"nana/internal/apperror"
	"nana/internal/database"
	"nana/internal/domain"
	"nana/internal/dto"
	"nana/internal/repository"

	"github.com/google/uuid"
)

type BankAccountService interface {
	ListByApartment(ctx context.Context, apartmentID uuid.UUID) ([]domain.ApartmentBankAccount, error)
	Create(ctx context.Context, apartmentID uuid.UUID, req dto.CreateBankAccountRequest) (*domain.ApartmentBankAccount, error)
	Update(ctx context.Context, id uuid.UUID, req dto.UpdateBankAccountRequest) (*domain.ApartmentBankAccount, error)
	Delete(ctx context.Context, id uuid.UUID) error
}

type bankAccountService struct {
	repo    repository.BankAccountRepository
	aptRepo repository.ApartmentRepository
	tx      database.TxManager
}

func NewBankAccountService(repo repository.BankAccountRepository, aptRepo repository.ApartmentRepository, tx database.TxManager) BankAccountService {
	return &bankAccountService{repo: repo, aptRepo: aptRepo, tx: tx}
}

func (s *bankAccountService) ListByApartment(ctx context.Context, apartmentID uuid.UUID) ([]domain.ApartmentBankAccount, error) {
	if _, err := s.aptRepo.FindByID(ctx, apartmentID); err != nil {
		return nil, apperror.ErrNotFound.WithMessage("ไม่พบอาคาร")
	}
	return s.repo.FindByApartmentID(ctx, apartmentID)
}

func (s *bankAccountService) Create(ctx context.Context, apartmentID uuid.UUID, req dto.CreateBankAccountRequest) (*domain.ApartmentBankAccount, error) {
	if _, err := s.aptRepo.FindByID(ctx, apartmentID); err != nil {
		return nil, apperror.ErrNotFound.WithMessage("ไม่พบอาคาร")
	}

	account := domain.ApartmentBankAccount{
		ApartmentID:   apartmentID,
		BankName:      req.BankName,
		AccountName:   req.AccountName,
		AccountNumber: req.AccountNumber,
		PromptPayID:   req.PromptPayID,
		IsPrimary:     req.IsPrimary,
		Note:          req.Note,
	}

	if req.IsPrimary {
		if err := s.tx.RunInTx(ctx, func(txCtx context.Context) error {
			if err := s.repo.ClearPrimary(txCtx, apartmentID); err != nil {
				return err
			}
			return s.repo.Create(txCtx, &account)
		}); err != nil {
			return nil, fmt.Errorf("create bank account: %w", err)
		}
	} else {
		if err := s.repo.Create(ctx, &account); err != nil {
			return nil, fmt.Errorf("create bank account: %w", err)
		}
	}

	return &account, nil
}

func (s *bankAccountService) Update(ctx context.Context, id uuid.UUID, req dto.UpdateBankAccountRequest) (*domain.ApartmentBankAccount, error) {
	account, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, apperror.ErrNotFound.WithMessage("ไม่พบบัญชีธนาคาร")
	}

	changingToPrimary := req.IsPrimary != nil && *req.IsPrimary && !account.IsPrimary

	if req.BankName != nil {
		account.BankName = *req.BankName
	}
	if req.AccountName != nil {
		account.AccountName = *req.AccountName
	}
	if req.AccountNumber != nil {
		account.AccountNumber = *req.AccountNumber
	}
	if req.PromptPayID != nil {
		account.PromptPayID = req.PromptPayID
	}
	if req.IsPrimary != nil {
		account.IsPrimary = *req.IsPrimary
	}
	if req.Note != nil {
		account.Note = req.Note
	}

	if changingToPrimary {
		if err := s.tx.RunInTx(ctx, func(txCtx context.Context) error {
			if err := s.repo.ClearPrimary(txCtx, account.ApartmentID); err != nil {
				return err
			}
			return s.repo.Update(txCtx, account)
		}); err != nil {
			return nil, fmt.Errorf("update bank account: %w", err)
		}
	} else {
		if err := s.repo.Update(ctx, account); err != nil {
			return nil, fmt.Errorf("update bank account: %w", err)
		}
	}

	return account, nil
}

func (s *bankAccountService) Delete(ctx context.Context, id uuid.UUID) error {
	if _, err := s.repo.FindByID(ctx, id); err != nil {
		return apperror.ErrNotFound.WithMessage("ไม่พบบัญชีธนาคาร")
	}
	return s.repo.Delete(ctx, id)
}
