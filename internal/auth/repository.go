package auth

import (
	"context"
	"time"

	"nana/internal/shared/database"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type UserRepository interface {
	FindByUsername(ctx context.Context, username string) (*User, error)
	FindByID(ctx context.Context, id uuid.UUID) (*User, error)
	Create(ctx context.Context, user *User) error
	Update(ctx context.Context, user *User) error
	ExistsByUsername(ctx context.Context, username string) (bool, error)

	CreateRefreshToken(ctx context.Context, token *RefreshToken) error
	FindRefreshTokenByHash(ctx context.Context, tokenHash string) (*RefreshToken, error)
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

func (r *userRepository) FindByUsername(ctx context.Context, username string) (*User, error) {
	var u User
	if err := database.DB(ctx, r.db).Where("username = ?", username).First(&u).Error; err != nil {
		return nil, err
	}
	return &u, nil
}

func (r *userRepository) FindByID(ctx context.Context, id uuid.UUID) (*User, error) {
	var u User
	if err := database.DB(ctx, r.db).Where("id = ?", id).First(&u).Error; err != nil {
		return nil, err
	}
	return &u, nil
}

func (r *userRepository) Create(ctx context.Context, user *User) error {
	return database.DB(ctx, r.db).Create(user).Error
}

func (r *userRepository) Update(ctx context.Context, user *User) error {
	return database.DB(ctx, r.db).Model(user).Select("*").Omit("deleted_at").Updates(user).Error
}

func (r *userRepository) ExistsByUsername(ctx context.Context, username string) (bool, error) {
	var count int64
	err := database.DB(ctx, r.db).Model(&User{}).Where("username = ?", username).Count(&count).Error
	return count > 0, err
}

func (r *userRepository) CreateRefreshToken(ctx context.Context, token *RefreshToken) error {
	return database.DB(ctx, r.db).Create(token).Error
}

func (r *userRepository) FindRefreshTokenByHash(ctx context.Context, tokenHash string) (*RefreshToken, error) {
	var t RefreshToken
	if err := database.DB(ctx, r.db).Where("token_hash = ?", tokenHash).First(&t).Error; err != nil {
		return nil, err
	}
	return &t, nil
}

func (r *userRepository) RevokeRefreshToken(ctx context.Context, id uuid.UUID) error {
	return database.DB(ctx, r.db).Model(&RefreshToken{}).Where("id = ?", id).Update("revoked", true).Error
}

func (r *userRepository) RevokeTokenFamily(ctx context.Context, familyID uuid.UUID) error {
	return database.DB(ctx, r.db).Model(&RefreshToken{}).Where("family_id = ?", familyID).Update("revoked", true).Error
}

func (r *userRepository) RevokeAllUserTokens(ctx context.Context, userID uuid.UUID) error {
	return database.DB(ctx, r.db).Model(&RefreshToken{}).Where("user_id = ?", userID).Update("revoked", true).Error
}

func (r *userRepository) CleanupExpiredTokens(ctx context.Context) error {
	return database.DB(ctx, r.db).Where("expires_at < ? OR revoked = true", time.Now()).Delete(&RefreshToken{}).Error
}
