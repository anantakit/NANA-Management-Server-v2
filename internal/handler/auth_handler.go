package handler

import (
	"time"

	"nana/internal/config"
	"nana/internal/dto"
	"nana/internal/service"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
)

type AuthHandler struct {
	authService service.AuthService
	cfg         *config.Config
}

func NewAuthHandler(authService service.AuthService, cfg *config.Config) *AuthHandler {
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
	var req dto.LoginRequest
	if err := BindBody(c, &req); err != nil {
		return err
	}

	resp, refreshToken, err := h.authService.Login(c.Context(), req)
	if err != nil {
		return Error(c, err)
	}

	h.setRefreshTokenCookie(c, refreshToken)

	return Success(c, "เข้าสู่ระบบสำเร็จ", resp)
}

func (h *AuthHandler) Refresh(c fiber.Ctx) error {
	rawToken := c.Cookies("refresh_token")
	if rawToken == "" {
		return Error(c, service.ErrTokenInvalid)
	}

	resp, newRefreshToken, err := h.authService.Refresh(c.Context(), rawToken)
	if err != nil {
		h.clearRefreshTokenCookie(c)
		return Error(c, err)
	}

	h.setRefreshTokenCookie(c, newRefreshToken)

	return Success(c, "รีเฟรชโทเค็นสำเร็จ", resp)
}

func (h *AuthHandler) Logout(c fiber.Ctx) error {
	userID, _ := c.Locals("userID").(uuid.UUID)
	rawToken := c.Cookies("refresh_token")

	_ = h.authService.Logout(c.Context(), userID, rawToken)

	h.clearRefreshTokenCookie(c)

	return Success(c, "ออกจากระบบสำเร็จ", nil)
}

func (h *AuthHandler) ChangePassword(c fiber.Ctx) error {
	userID, ok := c.Locals("userID").(uuid.UUID)
	if !ok {
		return Error(c, service.ErrTokenInvalid)
	}

	var req dto.ChangePasswordRequest
	if err := BindBody(c, &req); err != nil {
		return err
	}

	if err := h.authService.ChangePassword(c.Context(), userID, req); err != nil {
		return Error(c, err)
	}

	h.clearRefreshTokenCookie(c)

	return Success(c, "เปลี่ยนรหัสผ่านสำเร็จ", nil)
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
