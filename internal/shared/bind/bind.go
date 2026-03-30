package bind

import (
	"nana/internal/shared/respond"

	"github.com/go-playground/validator/v10"
	"github.com/gofiber/fiber/v3"
)

var validate = validator.New()

// Body parses the request body into dst and validates it.
// Returns a validation error response if parsing or validation fails.
func Body(c fiber.Ctx, dst any) error {
	if err := c.Bind().Body(dst); err != nil {
		return respond.ValidationError(c, []string{"ข้อมูลไม่ถูกต้อง"})
	}

	if err := validate.Struct(dst); err != nil {
		errs := extractValidationErrors(err)
		return respond.ValidationError(c, errs)
	}

	return nil
}

// Query parses query parameters into dst and validates it.
func Query(c fiber.Ctx, dst any) error {
	if err := c.Bind().Query(dst); err != nil {
		return respond.ValidationError(c, []string{"พารามิเตอร์ไม่ถูกต้อง"})
	}

	if err := validate.Struct(dst); err != nil {
		errs := extractValidationErrors(err)
		return respond.ValidationError(c, errs)
	}

	return nil
}

func extractValidationErrors(err error) []string {
	var errs []string
	if ve, ok := err.(validator.ValidationErrors); ok {
		for _, fe := range ve {
			errs = append(errs, fe.Field()+" ไม่ผ่านการตรวจสอบ ("+fe.Tag()+")")
		}
	} else {
		errs = append(errs, "ข้อมูลไม่ถูกต้อง")
	}
	return errs
}
