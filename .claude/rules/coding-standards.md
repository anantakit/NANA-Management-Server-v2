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

## Architecture: Hybrid DDD — Vertical Slice

ทุก feature อยู่ใน package ของตัวเอง (`internal/auth/`, `internal/apartment/`, etc.)

### เมื่อไหร่ต้องแยก domain + model (2-layer)?

| Feature มี... | แยก? | ตัวอย่าง |
|----------------|------|----------|
| Business logic (lifecycle, state transition, calculation) | ✅ YES | contract, room |
| แค่ CRUD, ไม่มี logic ใน struct | ❌ NO — GORM model = domain | auth, apartment, tenant |

```
✅ แยกเมื่อ: มี domain methods (ValidateForCreate, CalculateDeposit, CanTransition)
❌ ไม่แยกเมื่อ: struct เป็นแค่ data container ไม่มี behavior
```

### Feature ที่ไม่แยก domain (simple CRUD)
```go
// auth/model.go — GORM struct IS the domain
type User struct {
    ID   uuid.UUID  `gorm:"type:uuid;primaryKey" json:"id"`
    Role role.Role  `gorm:"type:varchar(20)" json:"role"`
    ...
}

// auth/repository.go — return *User ตรง ๆ
func (r *repo) FindByID(ctx, id) (*User, error)

// auth/service.go — ใช้ *User ตรง ๆ ไม่ต้อง mapping
```

### Feature ที่แยก domain (มี business logic)
```go
// domain/contract.go — pure struct + methods, NO gorm
type Contract struct { ... }
func (c *Contract) ValidateForCreate() error { ... }
func (c *Contract) CalculateDeposit() int64 { ... }

// contract/model.go — GORM struct + ToDomain/FromDomain
// contract/repository.go — return *domain.Contract
// contract/service.go — thin orchestration, เรียก domain methods
```

### Domain (internal/domain/) — เฉพาะ business entities ที่มี logic
- Pure Go structs + methods, JSON tags only
- NO gorm imports, NO BeforeCreate, NO gorm tags
- Typed string constants for statuses/types (RoomStatus, ContractStatus)
- **ห้ามเป็น shared dumping ground** — ถ้า struct ไม่มี logic ให้อยู่ใน feature

### Shared (internal/shared/) — cross-cutting concerns
- `shared/role/` — role.Admin, role.Manager (type-safe enum, ไม่มี logic)
- `shared/respond/` — AppError, Success, Error, BindBody
- `shared/config/`, `shared/database/`, `shared/middleware/`, `shared/money/`, `shared/logger/`
- **ใช้เมื่อ:** ข้าม feature + ไม่ใช่ business domain

```
❌ domain/ ≠ shared dumping ground
❌ shared/ ≠ catch-all (group เป็น subdomain)
✅ domain/ = business entities ที่มี behavior
✅ shared/ = infrastructure + cross-cutting concerns
```

### Model (feature package)
- GORM structs with `gorm:"..."` tags
- `BeforeCreate` hook: UUID generation
- ถ้าแยก domain: มี `ToDomain()` method + `XxxFromDomain()` function
- ถ้าไม่แยก domain: ไม่ต้อง conversion — GORM struct ใช้ตรง
- UUID PK: `gorm:"type:uuid;primaryKey;default:uuid_generate_v4()"`
- Soft deletes: `gorm.DeletedAt`

### DTO (feature package)
- Separate request/response structs
- Validation: `validate:"required,min=1,max=255"`
- JSON: `snake_case` tags
- Money: `float64` in DTO (satang → baht conversion)
- Pagination: embed `PaginationParams`, use `SafeSort()`

### Handler (feature package)
- `bind.Body(c, &req)` → service call → `respond.Success()`/`respond.Error()`
- `respond.Error(c, err)` delegates to centralized error mapping — no switch/if
- Each handler: `RegisterRoutes(router fiber.Router)`

### Service (feature package)
- Define interface first, then private struct
- Constructor returns interface: `NewXxxService(...) XxxService`
- All methods: `context.Context` as first param
- Max 4 deps, max 500 lines, max 10 methods
- Return `*respond.AppError` for business errors
- Cross-repo atomicity: inject `database.TxManager`, use `s.tx.RunInTx(ctx, func(txCtx) { ... })`

### Repository (feature package)
- Interface first, then private struct
- All methods: `context.Context` as first param
- `database.DB(ctx, r.db)` on every query (NOT `r.db.WithContext(ctx)`) — enables tx participation
- ถ้าแยก domain: return domain models via `model.ToDomain()`
- ถ้าไม่แยก: return GORM model ตรง ๆ
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
- Feature errors: define in `feature/errors.go` using `respond.New()`
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

- Goose SQL only in `internal/shared/database/migrations/`
- Naming: `00001_init.sql`, `00002_add_xxx.sql`
- Embedded: `//go:embed migrations/*.sql`

## Config

- Env vars with defaults, `Validate()` for production checks
- DSN: `DATABASE_URL` takes priority over individual `DB_*` vars
