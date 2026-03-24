---
description: Handler shared helpers for request binding, validation, and response formatting
paths:
  - "internal/handler/**"
---

# Handler Shared Helpers

## Request Binding (handler/bind.go)
```go
// Parse body + validate struct tags. Returns error response on failure.
func BindBody(c fiber.Ctx, dst any) error

// Parse query params + validate struct tags.
func BindQuery(c fiber.Ctx, dst any) error
```

Usage in handler:
```go
var req dto.CreateRoomRequest
if err := BindBody(c, &req); err != nil {
    return err  // already sent validation error response
}
```

## Response Helpers (handler/response.go)
```go
Success(c, "สำเร็จ", data)                    // 200 + {"status": "success", ...}
Created(c, "สร้างสำเร็จ", data)                // 201 + {"status": "success", ...}
SuccessWithMeta(c, "สำเร็จ", data, meta)       // 200 + meta for pagination
Error(c, err)                                  // delegates to apperror.MapToHTTP()
ValidationError(c, []string{"field invalid"})  // 400 + validation errors
```

## DI Wiring Pattern (cmd/main.go)
```go
repo := repository.NewXxxRepository(db)
svc := service.NewXxxService(repo)
h := handler.NewXxxHandler(svc)
h.RegisterRoutes(v1.Group("/resources"))
h.RegisterProtectedRoutes(protected.Group("/resources"))
```
