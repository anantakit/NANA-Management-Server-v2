package middleware

import (
	"strings"

	"nana/internal/shared/respond"
	"nana/internal/shared/config"
	"nana/internal/domain"

	"github.com/gofiber/fiber/v3"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

func JWTProtected(cfg *config.Config) fiber.Handler {
	return func(c fiber.Ctx) error {
		authHeader := c.Get("Authorization")
		if authHeader == "" {
			return sendAuthError(c, respond.ErrUnauthorized)
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
			return sendAuthError(c, respond.ErrUnauthorized)
		}

		tokenString := parts[1]

		token, err := jwt.Parse(tokenString, func(t *jwt.Token) (any, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, respond.ErrUnauthorized
			}
			return []byte(cfg.JWTSecret), nil
		})
		if err != nil || !token.Valid {
			return sendAuthError(c, respond.ErrUnauthorized)
		}

		claims, ok := token.Claims.(jwt.MapClaims)
		if !ok {
			return sendAuthError(c, respond.ErrUnauthorized)
		}

		sub, _ := claims["sub"].(string)
		userID, err := uuid.Parse(sub)
		if err != nil {
			return sendAuthError(c, respond.ErrUnauthorized)
		}

		role, _ := claims["role"].(string)

		c.Locals("userID", userID)
		c.Locals("role", domain.UserRole(role))

		return c.Next()
	}
}

func RequireRole(roles ...domain.UserRole) fiber.Handler {
	return func(c fiber.Ctx) error {
		role, ok := c.Locals("role").(domain.UserRole)
		if !ok {
			return sendAuthError(c, respond.ErrForbidden)
		}

		for _, r := range roles {
			if role == r {
				return c.Next()
			}
		}

		return sendAuthError(c, respond.ErrForbidden)
	}
}

func sendAuthError(c fiber.Ctx, appErr *respond.AppError) error {
	return c.Status(appErr.HTTPStatus).JSON(respond.ErrorResponse{
		Status:  "error",
		Code:    appErr.Code,
		Message: appErr.Message,
	})
}
