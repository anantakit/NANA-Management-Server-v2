package repository

import (
	"context"
	"time"

	"nana/internal/shared/database"
	"nana/internal/domain"
	"nana/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type UserRepository interface {
	FindByUsername(ctx context.Context, username string) (*domain.User, error)
	FindByID(ctx context.Context, id uuid.UUID) (*domain.User, error)
	Create(ctx context.Context, user *domain.User) error
	Update(ctx context.Context, user *domain.User) error
	ExistsByUsername(ctx context.Context, username string) (bool, error)

	CreateRefreshToken(ctx context.Context, token *domain.RefreshToken) error
	FindRefreshTokenByHash(ctx context.Context, tokenHash string) (*domain.RefreshToken, error)
	RevokeRefreshToken(ctx context.Context, id uuid.UUID) error
	RevokeTokenFamily(ctx context.Context, familyID uuid.UUID) error
	RevokeAllUserTokens(ctx context.Context, userID uuid.UUID) error
	CleanupExpiredTokens(ctx context.Context) error
}

type userRepository struct {
	db *gorm.DB
}

func NewUserRepository(db *gorm.DB) UserRepository {
	return &userRepository{db: db}
}

func (r *userRepository) FindByUsername(ctx context.Context, username string) (*domain.User, error) {
	var m model.User
	if err := database.DB(ctx, r.db).Where("username = ?", username).First(&m).Error; err != nil {
		return nil, err
	}
	u := m.ToDomain()
	return &u, nil
}

func (r *userRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.User, error) {
	var m model.User
	if err := database.DB(ctx, r.db).Where("id = ?", id).First(&m).Error; err != nil {
		return nil, err
	}
	u := m.ToDomain()
	return &u, nil
}

func (r *userRepository) Create(ctx context.Context, user *domain.User) error {
	m := model.UserFromDomain(*user)
	if err := database.DB(ctx, r.db).Create(&m).Error; err != nil {
		return err
	}
	*user = m.ToDomain()
	return nil
}

func (r *userRepository) Update(ctx context.Context, user *domain.User) error {
	m := model.UserFromDomain(*user)
	if err := database.DB(ctx, r.db).Model(&m).Select("*").Omit("deleted_at").Updates(&m).Error; err != nil {
		return err
	}
	*user = m.ToDomain()
	return nil
}

func (r *userRepository) ExistsByUsername(ctx context.Context, username string) (bool, error) {
	var count int64
	err := database.DB(ctx, r.db).Model(&model.User{}).Where("username = ?", username).Count(&count).Error
	return count > 0, err
}

func (r *userRepository) CreateRefreshToken(ctx context.Context, token *domain.RefreshToken) error {
	m := model.RefreshTokenFromDomain(*token)
	if err := database.DB(ctx, r.db).Create(&m).Error; err != nil {
		return err
	}
	*token = m.ToDomain()
	return nil
}

func (r *userRepository) FindRefreshTokenByHash(ctx context.Context, tokenHash string) (*domain.RefreshToken, error) {
	var m model.RefreshToken
	if err := database.DB(ctx, r.db).Where("token_hash = ?", tokenHash).First(&m).Error; err != nil {
		return nil, err
	}
	t := m.ToDomain()
	return &t, nil
}

func (r *userRepository) RevokeRefreshToken(ctx context.Context, id uuid.UUID) error {
	return database.DB(ctx, r.db).Model(&model.RefreshToken{}).Where("id = ?", id).Update("revoked", true).Error
}

func (r *userRepository) RevokeTokenFamily(ctx context.Context, familyID uuid.UUID) error {
	return database.DB(ctx, r.db).Model(&model.RefreshToken{}).Where("family_id = ?", familyID).Update("revoked", true).Error
}

func (r *userRepository) RevokeAllUserTokens(ctx context.Context, userID uuid.UUID) error {
	return database.DB(ctx, r.db).Model(&model.RefreshToken{}).Where("user_id = ?", userID).Update("revoked", true).Error
}

func (r *userRepository) CleanupExpiredTokens(ctx context.Context) error {
	return database.DB(ctx, r.db).Where("expires_at < ? OR revoked = true", time.Now()).Delete(&model.RefreshToken{}).Error
}
