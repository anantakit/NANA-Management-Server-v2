# Backend — Go + Fiber v3 + GORM

> See root CLAUDE.md for API spec, domain model, auth rules.

## Quick Reference Index
| Resource | Path | Content |
|----------|------|---------|
| Coding Standards | `.claude/rules/coding-standards.md` | Layer rules, error handling, update patterns |
| Shared Helpers | `.claude/rules/shared-helpers.md` | bind.Body, respond helpers, DI wiring |
| Transaction Pattern | `.claude/rules/transaction-pattern.md` | TxManager, cross-repo transactions, guidelines |
| Domain Ownership | `.claude/rules/domain-ownership.md` | WHO owns what table, decision checklist — consult for every DDD decision |
| Cross-Feature Patterns | `.claude/rules/cross-feature-patterns.md` | HOW to cross boundaries: ports, reads, logic leak, sharing rules |

## Stack
Go 1.26+ / Fiber v3 (NOT v2) / GORM + PostgreSQL 17+ / slog / go-playground/validator/v10 / golang-jwt/jwt/v5 / google/uuid / pressly/goose/v3 (SQL only) / Module: `nana`

## Architecture: Vertical Slice (feature per package)

Each feature lives in its own package under `internal/`:

```
internal/
├── apartment/    ← handler + service + repo + dto + model (+ bank_account files)
├── auth/         ← handler + service + repo + dto + model + errors
├── contract/     ← handler + service + repo + dto + model + port.go
├── room/         ← handler + service + repo + dto + model + port.go
├── tenant/       ← handler + service + repo + dto + model + port.go
├── domain/       ← future entities only (bill, meter_reading, payment)
├── model/        ← GORM structs for future entities (bill, meter_reading, payment)
├── seed/         ← database seeding
└── shared/       ← cross-cutting infrastructure
    ├── bind/       ← bind.Body(), bind.Query() + validator
    ├── respond/    ← AppError, MapToHTTP, Success(), Error()
    ├── pagination/ ← PaginationParams, SafeSort, ComputeMeta
    ├── config/     ← env config loading
    ├── database/   ← DB connection, TxManager, Goose migrations
    ├── middleware/  ← JWT, CORS, security headers
    ├── money/      ← satang ↔ baht conversion
    ├── logger/     ← slog configuration
    └── role/       ← role.Admin, role.Manager (type-safe enum)
```

### Hybrid DDD — GORM model = domain (ทุก feature)
- **feature/model.go** — GORM struct + typed constants + domain methods (pure, no DB)
- **feature/dto.go** — Request/response, `validate` tags, snake_case JSON, money as float64
- **feature/handler.go** — `bind.Body()` → service → `respond.Success()`/`respond.Error()`
- **feature/service.go** — Interface first, private impl, `context.Context` first param, business logic HERE
- **feature/repository.go** — Interface first, private impl, `database.DB(ctx, r.db)`, CRUD only
- **feature/port.go** — Cross-feature interfaces (consumer-defined) — see `cross-feature-patterns.md`

## DI (manual in cmd/main.go)
```
feature.NewRepo(db) → feature.NewService(repo, ports..., txManager?) → feature.NewHandler(svc)
handler.RegisterRoutes(router)
```

## Anti-Patterns
- ❌ Fiber v2 syntax (`*fiber.Ctx`, `c.BodyParser`)
- ❌ Separate domain + model structs (GORM model = domain, no ToDomain/FromDomain)
- ❌ `c.Bind().Body()` directly — use `bind.Body()`
- ❌ Public service/repo structs — interface first, private impl
- ❌ Domain models in API responses — use DTOs
- ❌ Business logic in handlers or repos
- ❌ `r.db.WithContext(ctx)` in repos — use `database.DB(ctx, r.db)` for tx-awareness
- ❌ Cross-entity operations in repo (e.g. room update in contract repo)
- ❌ `gorm.io/gorm` imports in service layer — use TxManager
- ❌ `float64` for money in domain — use `int64` satang
- ❌ GORM AutoMigrate — Goose SQL only
- ❌ Switch/if error handling in handlers — use `Error(c, err)`

## Size Limits
| Layer | Lines | Methods |
|-------|-------|---------|
| Service | 500 | 10 |
| Handler | 300 | 8 |
| Repository | 400 | 12 |
