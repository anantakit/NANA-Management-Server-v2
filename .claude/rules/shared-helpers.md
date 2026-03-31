---
description: Handler shared helpers for request binding, validation, and response formatting
paths:
  - "internal/**/handler.go"
  - "internal/**/bank_account_handler.go"
  - "internal/shared/bind/**"
  - "internal/shared/respond/**"
  - "cmd/**"
---

# Handler Shared Helpers

## Request Binding (shared/bind/)
```go
import "nana/internal/shared/bind"

// Parse body + validate struct tags. Returns error response on failure.
bind.Body(c, &req)

// Parse query params + validate struct tags.
bind.Query(c, &req)
```

Usage in handler:
```go
var req CreateRoomRequest
if err := bind.Body(c, &req); err != nil {
    return err  // already sent validation error response
}
```

## Response Helpers (shared/respond/)
```go
import "nana/internal/shared/respond"

respond.Success(c, "สำเร็จ", data)                    // 200 + {"status": "success", ...}
respond.Created(c, "สร้างสำเร็จ", data)                // 201 + {"status": "success", ...}
respond.SuccessWithMeta(c, "สำเร็จ", data, meta)       // 200 + meta for pagination
respond.Error(c, err)                                  // delegates to respond.MapToHTTP()
respond.ValidationError(c, []string{"field invalid"})  // 400 + validation errors
```

## AppError (shared/respond/apperror.go)
```go
// Predefined errors
respond.ErrNotFound     // 404
respond.ErrConflict     // 409
respond.ErrBadRequest   // 400
respond.ErrUnauthorized // 401
respond.ErrForbidden    // 403

// Feature-specific error
var ErrRoomOccupied = respond.New("CONFLICT", 409, "ห้องนี้มีผู้เช่าอยู่")

// WithMessage — customize message
respond.ErrNotFound.WithMessage("ไม่พบห้องพัก")
```

## DI Wiring Pattern (cmd/main.go)
```go
txManager := database.NewTxManager(db)

// Feature: single-repo
aptRepo := apartment.NewApartmentRepository(db)
aptService := apartment.NewApartmentService(aptRepo)
aptHandler := apartment.NewApartmentHandler(aptService)
aptHandler.RegisterRoutes(admin.Group("/apartments"))

// Feature: cross-repo with TxManager
contractRepo := contract.NewContractRepository(db)
contractService := contract.NewContractService(contractRepo, roomRepo, roomRepo, tenantRepo, txManager)
contractHandler := contract.NewContractHandler(contractService)
contractHandler.RegisterRoutes(admin.Group("/contracts"))

// Feature: cross-repo with port injection
tenantService := tenant.NewTenantService(tenantRepo, contractRepo)  // contractRepo satisfies ContractChecker port
```
