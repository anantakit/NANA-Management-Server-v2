package service

import "nana/internal/apperror"

var (
	ErrInvalidCredentials = apperror.New("INVALID_CREDENTIALS", 401, "ชื่อผู้ใช้หรือรหัสผ่านไม่ถูกต้อง")
	ErrTokenExpired       = apperror.New("TOKEN_EXPIRED", 401, "โทเค็นหมดอายุ")
	ErrTokenInvalid       = apperror.New("TOKEN_INVALID", 401, "โทเค็นไม่ถูกต้อง")
	ErrTokenRevoked       = apperror.New("TOKEN_REVOKED", 401, "โทเค็นถูกเพิกถอน")
	ErrPasswordTooWeak    = apperror.New("PASSWORD_TOO_WEAK", 400, "รหัสผ่านต้องมีอย่างน้อย 8 ตัวอักษรและมีตัวเลขอย่างน้อย 1 ตัว")
	ErrUserNotFound       = apperror.ErrNotFound.WithMessage("ไม่พบผู้ใช้")
)
