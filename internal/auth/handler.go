package auth

import (
	"time"

	"nana/internal/shared/bind"
	"nana/internal/shared/config"
	"nana/internal/shared/respond"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
)

type AuthHandler struct {
	authService AuthService
	cfg         *config.Config
}

func NewAuthHandler(authService AuthService, cfg *config.Config) *AuthHandler {
	return &AuthHandler{
		authService: authService,
		cfg:         cfg,
	}
}

// RegisterRoutes registers public auth routes under the given router.
func (h *AuthHandler) RegisterRoutes(router fiber.Router) {
	router.Post("/login", h.Login)
	router.Post("/refresh", h.Refresh)
}

// RegisterProtectedRoutes registers auth routes that require JWT authentication.
func (h *AuthHandler) RegisterProtectedRoutes(router fiber.Router) {
	router.Post("/logout", h.Logout)
	router.Post("/change-password", h.ChangePassword)
}

func (h *AuthHandler) Login(c fiber.Ctx) error {
	var req LoginRequest
	if err := bind.Body(c, &req); err != nil {
		return err
	}

	resp, refreshToken, err := h.authService.Login(c.Context(), req)
	if err != nil {
		return respond.Error(c, err)
	}

	h.setRefreshTokenCookie(c, refreshToken)

	return respond.Success(c, "เข้าสู่ระบบแล้ว", resp)
}

func (h *AuthHandler) Refresh(c fiber.Ctx) error {
	rawToken := c.Cookies("refresh_token")
	if rawToken == "" {
		return respond.Error(c, ErrTokenInvalid)
	}

	resp, newRefreshToken, err := h.authService.Refresh(c.Context(), rawToken)
	if err != nil {
		h.clearRefreshTokenCookie(c)
		return respond.Error(c, err)
	}

	h.setRefreshTokenCookie(c, newRefreshToken)

	return respond.Success(c, "รีเฟรชโทเค็นแล้ว", resp)
}

func (h *AuthHandler) Logout(c fiber.Ctx) error {
	userID, _ := c.Locals("userID").(uuid.UUID)
	rawToken := c.Cookies("refresh_token")

	_ = h.authService.Logout(c.Context(), userID, rawToken)

	h.clearRefreshTokenCookie(c)

	return respond.Success(c, "ออกจากระบบแล้ว", nil)
}

func (h *AuthHandler) ChangePassword(c fiber.Ctx) error {
	userID, ok := c.Locals("userID").(uuid.UUID)
	if !ok {
		return respond.Error(c, ErrTokenInvalid)
	}

	var req ChangePasswordRequest
	if err := bind.Body(c, &req); err != nil {
		return err
	}

	resp, refreshToken, err := h.authService.ChangePassword(c.Context(), userID, req)
	if err != nil {
		return respond.Error(c, err)
	}

	h.setRefreshTokenCookie(c, refreshToken)

	return respond.Success(c, "เปลี่ยนรหัสผ่านแล้ว", resp)
}

func (h *AuthHandler) setRefreshTokenCookie(c fiber.Ctx, token string) {
	cookie := &fiber.Cookie{
		Name:     "refresh_token",
		Value:    token,
		Path:     "/api/v1/auth",
		HTTPOnly: true,
		Secure:   h.cfg.IsProduction(),
		SameSite: fiber.CookieSameSiteStrictMode,
		MaxAge:   h.cfg.RefreshTokenExpiryDays * 24 * 60 * 60,
	}
	c.Cookie(cookie)
}

func (h *AuthHandler) clearRefreshTokenCookie(c fiber.Ctx) {
	cookie := &fiber.Cookie{
		Name:     "refresh_token",
		Value:    "",
		Path:     "/api/v1/auth",
		HTTPOnly: true,
		Secure:   h.cfg.IsProduction(),
		SameSite: fiber.CookieSameSiteStrictMode,
		MaxAge:   -1,
		Expires:  time.Now().Add(-1 * time.Hour),
	}
	c.Cookie(cookie)
}
