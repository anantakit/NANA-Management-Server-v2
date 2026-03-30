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

## Go Naming Conventions

```go
// Receivers — single letter matching type
func (h *bookingHandler) Create(c fiber.Ctx) error {}   // handler: h
func (s *bookingService) Create(ctx context.Context) {}  // service: s
func (r *bookingRepository) FindByID(ctx context.Context) {} // repo: r

// Constructors — return interface
func NewRoomService(repo RoomRepository) RoomService {
    return &roomService{repo: repo}
}

// Compile-time interface check
var _ RoomService = (*roomService)(nil)

// Constants — PascalCase name, SCREAMING value
const ContractStatusActive ContractStatus = "ACTIVE"

// Errors — exported with Err prefix
var ErrBookingNotFound = apperror.ErrNotFound.WithMessage("ไม่พบการจอง")

// Packages — lowercase, no underscore, no common/util/lib
```

## Error Handling

- `apperror.AppError`: Code, HTTPStatus, Message
- `apperror.MapToHTTP(c, err)`: single centralized function
- Predefined: ErrNotFound, ErrConflict, ErrBadRequest, ErrUnauthorized, ErrForbidden
- Service errors: define in `service/errors.go` using `apperror.New()`
- **Wrap with `%w`** (preserves `errors.Is/As` chain) — NOT `%v`
- **Handle once** — wrap OR log, never both

```go
// ✅ wrap with %w
return fmt.Errorf("create contract %s: %w", id, err)

// ❌ breaks error chain
return fmt.Errorf("create contract: %v", err)
```

## Query Anti-Patterns

```go
// ❌ N+1 query
for _, b := range bookings {
    b.Stays = repo.FindStays(b.ID)  // N queries!
}
// ✅ Preload
db.Preload("RoomStays").Find(&bookings)

// ❌ Unsafe sort — SQL injection
db.Order(fmt.Sprintf("%s %s", userInput, userOrder))
// ✅ SafeSort
db.Order(dto.SafeSort(col, order, allowedCols, "created_at"))

// ❌ BETWEEN for date ranges (inclusive both ends)
db.Where("date BETWEEN ? AND ?", start, end)
// ✅ Half-open interval for overlap check
db.Where("check_in < ? AND check_out > ?", rangeEnd, rangeStart)

// ❌ Read-then-write without lock — race condition
room := repo.FindByID(id)
if room.Status == VACANT { repo.UpdateStatus(id, OCCUPIED) }
// ✅ Pessimistic lock in transaction
tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).First(&room)
```

## Testing Strategy (3-Layer)

| Layer | What to test | Mocks? |
|-------|-------------|--------|
| **Domain** (pure) | Business logic, calculations | NO mocks — input → output |
| **Service** (wiring) | Orchestration, correct call order | Mock repos (hand-written) |
| **Handler** (outcome) | HTTP status, response shape | Mock service |

```go
// Domain test — pure, no mocks
func TestContract_CalculateDeposit(t *testing.T) { ... }

// Service test — hand-written mock, compile-time check
type mockRoomRepo struct { findByIDFn func(...) }
var _ RoomRepository = (*mockRoomRepo)(nil)

// Handler test — test visible behavior
```

**ห้าม:** snapshot tests, `data-testid` as primary selector, `.only`/`.skip` in committed code

## Migrations

- Goose SQL only in `internal/database/migrations/`
- Naming: `00001_init.sql`, `00002_add_xxx.sql`
- Embedded: `//go:embed migrations/*.sql`

## Config

- Env vars with defaults, `Validate()` for production checks
- DSN: `DATABASE_URL` takes priority over individual `DB_*` vars
