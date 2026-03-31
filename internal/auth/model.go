package auth

import (
	"time"

	"nana/internal/shared/role"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type User struct {
	ID                 uuid.UUID      `gorm:"type:uuid;primaryKey;default:uuid_generate_v4()" json:"id"`
	Username           string         `gorm:"type:varchar(100);not null;uniqueIndex" json:"username"`
	PasswordHash       string         `gorm:"type:varchar(255);not null" json:"-"`
	FullName           string         `gorm:"type:varchar(255);not null;default:''" json:"full_name"`
	Role               role.Role      `gorm:"type:varchar(20);not null" json:"role"`
	MustChangePassword bool           `gorm:"not null;default:false" json:"must_change_password"`
	CreatedAt          time.Time      `gorm:"not null;default:now()" json:"created_at"`
	UpdatedAt          time.Time      `gorm:"not null;default:now()" json:"updated_at"`
	DeletedAt          gorm.DeletedAt `gorm:"index" json:"-"`
}

func (User) TableName() string { return "users" }

func (u *User) BeforeCreate(tx *gorm.DB) error {
	if u.ID == uuid.Nil {
		u.ID = uuid.New()
	}
	return nil
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
