package handler

import (
	"nana/internal/apperror"

	"github.com/gofiber/fiber/v3"
)

type successResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type successWithMetaResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
	Data    any    `json:"data"`
	Meta    any    `json:"meta"`
}

func Success(c fiber.Ctx, message string, data any) error {
	return c.Status(fiber.StatusOK).JSON(successResponse{
		Status:  "success",
		Message: message,
		Data:    data,
	})
}

func Created(c fiber.Ctx, message string, data any) error {
	return c.Status(fiber.StatusCreated).JSON(successResponse{
		Status:  "success",
		Message: message,
		Data:    data,
	})
}

func SuccessWithMeta(c fiber.Ctx, message string, data any, meta any) error {
	return c.Status(fiber.StatusOK).JSON(successWithMetaResponse{
		Status:  "success",
		Message: message,
		Data:    data,
		Meta:    meta,
	})
}

// Error delegates to centralized apperror.MapToHTTP for consistent error responses.
func Error(c fiber.Ctx, err error) error {
	return apperror.MapToHTTP(c, err)
}

func ValidationError(c fiber.Ctx, errors []string) error {
	return c.Status(fiber.StatusBadRequest).JSON(apperror.ErrorResponse{
		Status:  "error",
		Code:    "VALIDATION_ERROR",
		Message: "ข้อมูลไม่ถูกต้อง",
		Errors:  errors,
	})
}
