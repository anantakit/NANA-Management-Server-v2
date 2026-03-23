package model

import (
	"time"

	"nana/internal/domain"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type User struct {
	ID                 uuid.UUID      `gorm:"type:uuid;primaryKey;default:uuid_generate_v4()"`
	Username           string         `gorm:"type:varchar(100);not null;uniqueIndex"`
	PasswordHash       string         `gorm:"type:varchar(255);not null"`
	FullName           string         `gorm:"type:varchar(255);not null;default:''"`
	Role               string         `gorm:"type:varchar(20);not null"`
	MustChangePassword bool           `gorm:"not null;default:false"`
	CreatedAt          time.Time      `gorm:"not null;default:now()"`
	UpdatedAt          time.Time      `gorm:"not null;default:now()"`
	DeletedAt          gorm.DeletedAt `gorm:"index"`
}

func (User) TableName() string { return "users" }

func (u *User) BeforeCreate(tx *gorm.DB) error {
	if u.ID == uuid.Nil {
		u.ID = uuid.New()
	}
	return nil
}

func (u *User) ToDomain() domain.User {
	return domain.User{
		ID:                 u.ID,
		Username:           u.Username,
		PasswordHash:       u.PasswordHash,
		FullName:           u.FullName,
		Role:               domain.UserRole(u.Role),
		MustChangePassword: u.MustChangePassword,
		CreatedAt:          u.CreatedAt,
		UpdatedAt:          u.UpdatedAt,
	}
}

func UserFromDomain(d domain.User) User {
	return User{
		ID:                 d.ID,
		Username:           d.Username,
		PasswordHash:       d.PasswordHash,
		FullName:           d.FullName,
		Role:               string(d.Role),
		MustChangePassword: d.MustChangePassword,
		CreatedAt:          d.CreatedAt,
		UpdatedAt:          d.UpdatedAt,
	}
}

type RefreshToken struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey;default:uuid_generate_v4()"`
	UserID    uuid.UUID `gorm:"type:uuid;not null"`
	TokenHash string    `gorm:"type:varchar(255);not null"`
	FamilyID  uuid.UUID `gorm:"type:uuid;not null"`
	ExpiresAt time.Time `gorm:"not null"`
	Revoked   bool      `gorm:"not null;default:false"`
	CreatedAt time.Time `gorm:"not null;default:now()"`

	User *User `gorm:"foreignKey:UserID"`
}

func (RefreshToken) TableName() string { return "refresh_tokens" }

func (r *RefreshToken) BeforeCreate(tx *gorm.DB) error {
	if r.ID == uuid.Nil {
		r.ID = uuid.New()
	}
	return nil
}

func (r *RefreshToken) ToDomain() domain.RefreshToken {
	return domain.RefreshToken{
		ID:        r.ID,
		UserID:    r.UserID,
		TokenHash: r.TokenHash,
		FamilyID:  r.FamilyID,
		ExpiresAt: r.ExpiresAt,
		Revoked:   r.Revoked,
		CreatedAt: r.CreatedAt,
	}
}

func RefreshTokenFromDomain(d domain.RefreshToken) RefreshToken {
	return RefreshToken{
		ID:        d.ID,
		UserID:    d.UserID,
		TokenHash: d.TokenHash,
		FamilyID:  d.FamilyID,
		ExpiresAt: d.ExpiresAt,
		Revoked:   d.Revoked,
		CreatedAt: d.CreatedAt,
	}
}
