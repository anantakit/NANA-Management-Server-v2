package middleware

import (
	"strings"

	"nana/internal/shared/config"
	"nana/internal/shared/respond"
	"nana/internal/shared/role"

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

		r, _ := claims["role"].(string)

		c.Locals("userID", userID)
		c.Locals("role", role.Role(r))

		return c.Next()
	}
}

func RequireRole(roles ...role.Role) fiber.Handler {
	return func(c fiber.Ctx) error {
		r, ok := c.Locals("role").(role.Role)
		if !ok {
			return sendAuthError(c, respond.ErrForbidden)
		}

		for _, allowed := range roles {
			if r == allowed {
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

// ActorFromCtx returns the authenticated user's ID from request locals
// (set above by JWTProtected at line 52). Returns nil when the request is
// unauthenticated — handlers pass that nil through to service-layer audit
// recorders for system-triggered events. Single source of truth for the
// "read the actor" half of the userID lifecycle, paired with the
// JWTProtected writer in this same file.
func ActorFromCtx(c fiber.Ctx) *uuid.UUID {
	if uid, ok := c.Locals("userID").(uuid.UUID); ok {
		return &uid
	}
	return nil
}
