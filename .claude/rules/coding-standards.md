---
description: Detailed Go coding standards for all backend layers
paths:
  - "internal/**"
---

# Backend Coding Standards

## Fiber v3 API (CRITICAL — not v2)
- `fiber.Ctx` is an interface (not `*fiber.Ctx`)
- Use `BindBody(c, &req)` / `BindQuery(c, &req)` from handler/bind.go
- Route params: `c.Params("id")`
- Response: `c.Status(code).JSON(data)`
- Middleware: `func(fiber.Ctx) error`
- Context: pass `c.Context()` to service → repo

## Layer Rules

### Domain (internal/domain/)
- Pure Go structs, JSON tags only
- NO gorm imports, NO BeforeCreate, NO gorm tags
- Typed string constants for all statuses/types

### Model (internal/model/)
- GORM structs with `gorm:"..."` tags
- `BeforeCreate` hook: UUID generation
- `ToDomain()` method + `XxxFromDomain()` function
- UUID PK: `gorm:"type:uuid;primaryKey;default:uuid_generate_v4()"`
- Soft deletes: `gorm.DeletedAt`

### DTO (internal/dto/)
- Separate request/response structs
- Validation: `validate:"required,min=1,max=255"`
- JSON: `snake_case` tags
- Money: `float64` in DTO (satang → baht conversion)
- Pagination: embed `PaginationParams`, use `SafeSort()`

### Handler (internal/handler/)
- `BindBody(c, &req)` → service call → `Success()`/`Created()`/`Error()`
- `Error(c, err)` delegates to `apperror.MapToHTTP()` — no switch/if
- Each handler: `RegisterRoutes(router fiber.Router)`

### Service (internal/service/)
- Define interface first, then private struct
- Constructor returns interface: `NewXxxService(...) XxxService`
- All methods: `context.Context` as first param
- Max 4 deps, max 500 lines, max 10 methods
- Return `*apperror.AppError` for business errors
- Cross-repo atomicity: inject `database.TxManager`, use `s.tx.RunInTx(ctx, func(txCtx) { ... })`
- Do NOT import `gorm.io/gorm` or `model` package in services

### Repository (internal/repository/)
- Interface first, then private struct
- All methods: `context.Context` as first param
- `database.DB(ctx, r.db)` on every query (NOT `r.db.WithContext(ctx)`) — enables tx participation
- Return domain models via `model.ToDomain()`
- Max 400 lines, max 12 methods, CRUD only — single entity per repo
- **Update pattern**: use `db.Model(&m).Select("*").Omit("deleted_at").Updates(&m)` — NOT `Save()` which skips zero values (false, 0, "")

## Error Handling
- `apperror.AppError`: Code, HTTPStatus, Message
- `apperror.MapToHTTP(c, err)`: single centralized function
- Predefined: ErrNotFound, ErrConflict, ErrBadRequest, ErrUnauthorized, ErrForbidden
- Service errors: define in `service/errors.go` using `apperror.New()`

## Migrations
- Goose SQL only in `internal/database/migrations/`
- Naming: `00001_init.sql`, `00002_add_xxx.sql`
- Embedded: `//go:embed migrations/*.sql`

## Config
- Env vars with defaults, `Validate()` for production checks
- DSN: `DATABASE_URL` takes priority over individual `DB_*` vars
