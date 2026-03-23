package dto

import "nana/internal/domain"

type LoginRequest struct {
	Username string `json:"username" validate:"required,min=1,max=100"`
	Password string `json:"password" validate:"required,min=1"`
}

type LoginResponse struct {
	AccessToken string       `json:"access_token"`
	User        UserResponse `json:"user"`
}

type UserResponse struct {
	ID                 string `json:"id"`
	Username           string `json:"username"`
	FullName           string `json:"full_name"`
	Role               string `json:"role"`
	MustChangePassword bool   `json:"must_change_password"`
}

func ToUserResponse(u domain.User) UserResponse {
	return UserResponse{
		ID:                 u.ID.String(),
		Username:           u.Username,
		FullName:           u.FullName,
		Role:               string(u.Role),
		MustChangePassword: u.MustChangePassword,
	}
}

type ChangePasswordRequest struct {
	CurrentPassword string `json:"current_password" validate:"required,min=1"`
	NewPassword     string `json:"new_password" validate:"required,min=8"`
}
