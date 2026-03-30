package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"time"
	"unicode"

	"nana/internal/domain"
	"nana/internal/dto"
	"nana/internal/repository"
	"nana/internal/shared/config"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

type AuthService interface {
	Login(ctx context.Context, req dto.LoginRequest) (*dto.LoginResponse, string, error)
	Refresh(ctx context.Context, rawToken string) (*dto.LoginResponse, string, error)
	Logout(ctx context.Context, userID uuid.UUID, rawToken string) error
	ChangePassword(ctx context.Context, userID uuid.UUID, req dto.ChangePasswordRequest) error
	StartTokenCleanup(ctx context.Context, interval time.Duration)
}

type authService struct {
	userRepo repository.UserRepository
	cfg      *config.Config
}

func NewAuthService(userRepo repository.UserRepository, cfg *config.Config) AuthService {
	return &authService{
		userRepo: userRepo,
		cfg:      cfg,
	}
}

func (s *authService) Login(ctx context.Context, req dto.LoginRequest) (*dto.LoginResponse, string, error) {
	if req.Username == "" || req.Password == "" {
		return nil, "", ErrInvalidCredentials
	}

	user, err := s.userRepo.FindByUsername(ctx, req.Username)
	if err != nil {
		return nil, "", ErrInvalidCredentials
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
		return nil, "", ErrInvalidCredentials
	}

	accessToken, err := s.generateAccessToken(user)
	if err != nil {
		return nil, "", fmt.Errorf("generate access token: %w", err)
	}

	refreshToken, err := s.createRefreshToken(ctx, user.ID, uuid.New())
	if err != nil {
		return nil, "", fmt.Errorf("create refresh token: %w", err)
	}

	return &dto.LoginResponse{
		AccessToken: accessToken,
		User:        dto.ToUserResponse(*user),
	}, refreshToken, nil
}

func (s *authService) Refresh(ctx context.Context, rawToken string) (*dto.LoginResponse, string, error) {
	if rawToken == "" {
		return nil, "", ErrTokenInvalid
	}

	tokenHash := hashToken(rawToken)

	stored, err := s.userRepo.FindRefreshTokenByHash(ctx, tokenHash)
	if err != nil {
		return nil, "", ErrTokenInvalid
	}

	if stored.Revoked {
		slog.Warn("refresh token replay detected", "family_id", stored.FamilyID)
		_ = s.userRepo.RevokeTokenFamily(ctx, stored.FamilyID)
		return nil, "", ErrTokenRevoked
	}

	if time.Now().After(stored.ExpiresAt) {
		return nil, "", ErrTokenExpired
	}

	if err := s.userRepo.RevokeRefreshToken(ctx, stored.ID); err != nil {
		return nil, "", fmt.Errorf("revoke old token: %w", err)
	}

	user, err := s.userRepo.FindByID(ctx, stored.UserID)
	if err != nil {
		return nil, "", ErrUserNotFound
	}

	accessToken, err := s.generateAccessToken(user)
	if err != nil {
		return nil, "", fmt.Errorf("generate access token: %w", err)
	}

	newRefreshToken, err := s.createRefreshToken(ctx, user.ID, stored.FamilyID)
	if err != nil {
		return nil, "", fmt.Errorf("create refresh token: %w", err)
	}

	return &dto.LoginResponse{
		AccessToken: accessToken,
		User:        dto.ToUserResponse(*user),
	}, newRefreshToken, nil
}

func (s *authService) Logout(ctx context.Context, userID uuid.UUID, rawToken string) error {
	if rawToken != "" {
		tokenHash := hashToken(rawToken)
		stored, err := s.userRepo.FindRefreshTokenByHash(ctx, tokenHash)
		if err == nil {
			_ = s.userRepo.RevokeTokenFamily(ctx, stored.FamilyID)
		}
	}
	return nil
}

func (s *authService) ChangePassword(ctx context.Context, userID uuid.UUID, req dto.ChangePasswordRequest) error {
	user, err := s.userRepo.FindByID(ctx, userID)
	if err != nil {
		return ErrUserNotFound
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.CurrentPassword)); err != nil {
		return ErrInvalidCredentials
	}

	if err := validatePassword(req.NewPassword); err != nil {
		return err
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), 12)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	user.PasswordHash = string(hash)
	user.MustChangePassword = false

	if err := s.userRepo.Update(ctx, user); err != nil {
		return fmt.Errorf("update user: %w", err)
	}

	return s.userRepo.RevokeAllUserTokens(ctx, userID)
}

func (s *authService) StartTokenCleanup(ctx context.Context, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := s.userRepo.CleanupExpiredTokens(context.Background()); err != nil {
					slog.Error("token cleanup failed", "error", err)
				}
			case <-ctx.Done():
				return
			}
		}
	}()
}

func (s *authService) generateAccessToken(user *domain.User) (string, error) {
	claims := jwt.MapClaims{
		"sub":  user.ID.String(),
		"role": string(user.Role),
		"exp":  time.Now().Add(time.Duration(s.cfg.JWTExpiryHours) * time.Hour).Unix(),
		"iat":  time.Now().Unix(),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(s.cfg.JWTSecret))
}

func (s *authService) createRefreshToken(ctx context.Context, userID, familyID uuid.UUID) (string, error) {
	rawToken, err := generateRandomToken()
	if err != nil {
		return "", err
	}

	tokenHash := hashToken(rawToken)

	rt := domain.RefreshToken{
		ID:        uuid.New(),
		UserID:    userID,
		TokenHash: tokenHash,
		FamilyID:  familyID,
		ExpiresAt: time.Now().AddDate(0, 0, s.cfg.RefreshTokenExpiryDays),
		Revoked:   false,
	}

	if err := s.userRepo.CreateRefreshToken(ctx, &rt); err != nil {
		return "", err
	}

	return rawToken, nil
}

func generateRandomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

func validatePassword(password string) error {
	if len(password) < 8 {
		return ErrPasswordTooWeak
	}
	hasDigit := false
	for _, ch := range password {
		if unicode.IsDigit(ch) {
			hasDigit = true
			break
		}
	}
	if !hasDigit {
		return ErrPasswordTooWeak
	}
	return nil
}
