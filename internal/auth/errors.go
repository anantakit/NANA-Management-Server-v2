package auth

import "nana/internal/shared/respond"

var (
	ErrInvalidCredentials = respond.New("INVALID_CREDENTIALS", 401, "ชื่อผู้ใช้หรือรหัสผ่านไม่ถูกต้อง")
	ErrTokenExpired       = respond.New("TOKEN_EXPIRED", 401, "โทเค็นหมดอายุ")
	ErrTokenInvalid       = respond.New("TOKEN_INVALID", 401, "โทเค็นไม่ถูกต้อง")
	ErrTokenRevoked       = respond.New("TOKEN_REVOKED", 401, "โทเค็นถูกเพิกถอน")
	ErrPasswordTooWeak    = respond.New("PASSWORD_TOO_WEAK", 400, "รหัสผ่านต้องมีอย่างน้อย 8 ตัวอักษรและมีตัวเลขอย่างน้อย 1 ตัว")
	ErrUserNotFound       = respond.ErrNotFound.WithMessage("ไม่พบผู้ใช้")
)
