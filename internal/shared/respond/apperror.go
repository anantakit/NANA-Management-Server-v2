package respond

import (
	"errors"
	"log/slog"

	"github.com/gofiber/fiber/v3"
)

type AppError struct {
	Code       string `json:"code"`
	HTTPStatus int    `json:"-"`
	Message    string `json:"message"`
}

func (e *AppError) Error() string {
	return e.Message
}

func New(code string, status int, message string) *AppError {
	return &AppError{
		Code:       code,
		HTTPStatus: status,
		Message:    message,
	}
}

func Is(err error) (*AppError, bool) {
	var appErr *AppError
	if errors.As(err, &appErr) {
		return appErr, true
	}
	return nil, false
}

func (e *AppError) WithMessage(msg string) *AppError {
	return &AppError{
		Code:       e.Code,
		HTTPStatus: e.HTTPStatus,
		Message:    msg,
	}
}

var (
	ErrNotFound     = New("NOT_FOUND", 404, "ไม่พบข้อมูล")
	ErrConflict     = New("CONFLICT", 409, "ข้อมูลซ้ำ")
	ErrBadRequest   = New("BAD_REQUEST", 400, "ข้อมูลไม่ถูกต้อง")
	ErrUnauthorized = New("UNAUTHORIZED", 401, "ไม่ได้รับอนุญาต")
	ErrForbidden    = New("FORBIDDEN", 403, "ไม่มีสิทธิ์เข้าถึง")
	ErrInternal     = New("INTERNAL", 500, "เกิดข้อผิดพลาด")
)

// ErrorResponse is the standard error response shape used by both handlers and middleware.
type ErrorResponse struct {
	Status  string   `json:"status"`
	Code    string   `json:"code"`
	Message string   `json:"message"`
	Errors  []string `json:"errors,omitempty"`
}

// MapToHTTP maps any error to a consistent HTTP JSON response.
// If the error is an *AppError, it uses its HTTPStatus and Code.
// Otherwise, it logs the unhandled error and returns a 500.
func MapToHTTP(c fiber.Ctx, err error) error {
	if appErr, ok := Is(err); ok {
		return c.Status(appErr.HTTPStatus).JSON(ErrorResponse{
			Status:  "error",
			Code:    appErr.Code,
			Message: appErr.Message,
		})
	}

	slog.Error("unhandled error", "error", err)
	return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{
		Status:  "error",
		Code:    "INTERNAL",
		Message: "เกิดข้อผิดพลาด",
	})
}
