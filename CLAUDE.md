# Backend — CLAUDE.md

> Go + Fiber v3 + GORM + PostgreSQL. See root `CLAUDE.md` for shared rules (API spec, domain model, auth).

## Tech Stack (MANDATORY)

- **Go 1.26+**
- **Fiber v3** (`github.com/gofiber/fiber/v3`) — NOT v2
- **GORM** (`gorm.io/gorm`) with PostgreSQL driver
- **PostgreSQL 17+**
- **slog** for structured logging (stdlib, no third-party loggers)
- **go-playground/validator/v10** for struct validation
- **golang-jwt/jwt/v5** for JWT auth
- **google/uuid** for UUIDs
- **pressly/goose/v3** for migrations (SQL-only, no GORM AutoMigrate)
- Module name: `nana`

---

## Architecture: Clean Architecture (handler → service → repository)

```
backend/
├── cmd/
│   └── main.go                    # Entrypoint, DI wiring, server startup
├── internal/
│   ├── config/config.go           # Env-based config with Validate()
│   ├── database/
│   │   ├── database.go            # GORM connect + Goose migrate
│   │   └── migrations/            # Goose SQL files (//go:embed)
│   ├── domain/                    # Pure Go structs (NO GORM imports)
│   ├── model/                     # GORM model structs (tags, hooks) — maps to/from domain
│   ├── dto/                       # Request/Response DTOs + pagination
│   ├── handler/                   # HTTP handlers + bind.go + response.go
│   ├── service/                   # Business logic (interfaces + impl)
│   ├── repository/                # Data access (interfaces + GORM impl)
│   ├── middleware/                 # JWT, CORS, security headers, rate limit
│   ├── apperror/                  # Centralized error types + MapToHTTP + ErrorResponse
│   ├── logger/                    # slog request-scoped context
│   ├── money/                     # Satang (int64) utilities
│   └── seed/                      # Seed data
├── .env.example
├── Dockerfile
├── docker-compose.dev.yml
└── go.mod
```

---

## Coding Standards (ENFORCED)

### 1. Fiber v3 API — CRITICAL
- `fiber.Ctx` is an **interface** in v3 (not `*fiber.Ctx`)
- Use `BindBody(c, &req)` / `BindQuery(c, &req)` — NOT `c.Bind().Body()` directly
- Route params: `c.Params("id")`
- JSON response: `c.JSON(data)` or `c.Status(code).JSON(data)`
- Middleware signature: `func(fiber.Ctx) error`
- Pass `c.Context()` to service/repo for context propagation

### 2. Domain Layer (pure Go — NO GORM)
- Pure Go structs with JSON tags only
- NO `gorm.DeletedAt`, NO `BeforeCreate` hooks, NO GORM struct tags
- Business constants (statuses, types) defined here as typed string constants

### 3. Model Layer (GORM-specific)
- GORM model structs with `gorm:"..."` tags
- `BeforeCreate` hook for UUID generation
- `ToDomain()` and `FromDomain()` methods for mapping
- UUID primary keys: `gorm:"type:uuid;primaryKey;default:uuid_generate_v4()"`
- Soft deletes: `gorm.DeletedAt`

### 4. DTOs
- Separate request/response structs — NEVER expose domain models in API responses
- Validation tags: `validate:"required,min=1,max=255"`
- JSON tags in `snake_case`
- Money fields: `int64` (satang) in domain, `float64` in DTO for JSON serialization
- Pagination: embed `PaginationParams` in query DTOs, always use `SafeSort()` for sort params

### 5. Handlers
- Use `BindBody(c, &req)` / `BindQuery(c, &req)` for parsing + validation
- Use `Success()`, `Created()`, `Error()` response helpers
- `Error()` delegates to `apperror.MapToHTTP()` — NO switch/if in handlers
- Consistent response: `{"status": "success/error", "message": "...", "data": ..., "meta": ...}`
- Each handler struct exposes `RegisterRoutes(router fiber.Router)`

### 6. Services
- Define **interface first**, then **private struct** implementation
- Constructor: `NewXxxService(repo XxxRepository) XxxService`
- All methods accept `context.Context` as first param
- Max 4 dependencies per service — split if more
- Max 500 lines / 10 methods per service
- Business logic lives HERE — handlers and repos are dumb
- Return `*apperror.AppError`, not raw GORM errors

### 7. Repositories
- **Interface** + private GORM implementation
- Methods accept `context.Context` as first param
- Use `db.WithContext(ctx)` on every query
- Return domain models (via model.ToDomain()), not DTOs
- CRUD only — no analytics, no business logic
- Max 400 lines / 12 methods per repository

### 8. Error Handling (centralized)
```go
// apperror/error.go
type AppError struct {
  Code       string `json:"code"`
  HTTPStatus int    `json:"-"`
  Message    string `json:"message"`
}

type ErrorResponse struct {
  Status  string   `json:"status"`
  Code    string   `json:"code"`
  Message string   `json:"message"`
  Errors  []string `json:"errors,omitempty"`
}

// Single function — handler calls Error(c, err) which delegates here
func MapToHTTP(c fiber.Ctx, err error) error { ... }
```

### 9. Migrations (Goose ONLY)
- SQL files in `internal/database/migrations/`
- Naming: `00001_init.sql`, `00002_xxx.sql`
- Embedded via `//go:embed migrations/*.sql`
- Run via `goose.Up()` in database.go

### 10. Config
- Load from env vars with sensible defaults
- `Validate()` method with production-specific checks
- DSN builder prioritizes `DATABASE_URL` over individual `DB_*` vars
- `.env.example` committed, `.env` gitignored

---

## Shared Helpers

| Helper | Location | Purpose |
|--------|----------|---------|
| `BindBody(c, &req)` | `handler/bind.go` | Parse body + validate with go-playground/validator |
| `BindQuery(c, &req)` | `handler/bind.go` | Parse query params + validate |
| `Success(c, msg, data)` | `handler/response.go` | 200 success response |
| `Created(c, msg, data)` | `handler/response.go` | 201 created response |
| `SuccessWithMeta(c, msg, data, meta)` | `handler/response.go` | 200 with pagination meta |
| `Error(c, err)` | `handler/response.go` | Delegates to `apperror.MapToHTTP()` |
| `ValidationError(c, errs)` | `handler/response.go` | 400 validation error |

---

## Dependency Injection

Manual wiring in `main.go` — NO DI framework:
```go
repo := repository.NewXxxRepository(db)
svc := service.NewXxxService(repo)
handler := handler.NewXxxHandler(svc)
handler.RegisterRoutes(publicGroup)
handler.RegisterProtectedRoutes(protectedGroup)
```

---

## Anti-Patterns (NEVER DO THESE)

1. ❌ **No God Services** — Any service > 500 lines or > 10 methods MUST be split
2. ❌ **No scattered error handling** — Use `apperror.MapToHTTP()`, never switch/if in handlers
3. ❌ **No GORM in domain layer** — Domain = pure Go structs. GORM lives in model/ and repository/ only
4. ❌ **No analytics in CRUD repositories** — Extract to dedicated AnalyticsRepository
5. ❌ **No audit logging inside transactions** — Audit writes outside business transaction
6. ❌ **No inconsistent error response shapes** — Middleware and handlers use same `ErrorResponse`
7. ❌ **Single migration system** — Goose SQL only, no GORM AutoMigrate
8. ❌ **No bloated constructors** — Max 4 dependencies per service
9. ❌ Never use Fiber v2 syntax (`*fiber.Ctx`, `c.BodyParser`, `c.QueryParser`)
10. ❌ Never expose domain models directly in API responses — always use DTOs
11. ❌ Never put business logic in handlers or repositories
12. ❌ Never use `float64` for money in domain layer — use `int64` (satang)
13. ❌ Never call `c.Bind().Body()` directly — use `BindBody()`
14. ❌ Never define repo/service as public struct — interface first, then private impl

### Size Rules (ENFORCED)
| Layer | Max Lines | Max Methods | Action if exceeded |
|-------|-----------|-------------|-------------------|
| Service | 500 | 10 | Split into separate service |
| Handler | 300 | 8 | Split by resource/action group |
| Repository | 400 | 12 | Extract analytics/reports |
