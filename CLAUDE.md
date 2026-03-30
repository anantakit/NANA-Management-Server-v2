# Backend — Go + Fiber v3 + GORM

> See root CLAUDE.md for API spec, domain model, auth rules.

## Quick Reference Index
| Resource | Path | Content |
|----------|------|---------|
| Coding Standards | `.claude/rules/coding-standards.md` | Layer rules, error handling, update patterns |
| Shared Helpers | `.claude/rules/shared-helpers.md` | BindBody, response helpers, DI wiring |
| Transaction Pattern | `.claude/rules/transaction-pattern.md` | TxManager, cross-repo transactions, guidelines |
| Domain Ownership | `.claude/rules/domain-ownership.md` | WHO owns what table, decision checklist — consult for every DDD decision |
| Cross-Feature Patterns | `.claude/rules/cross-feature-patterns.md` | HOW to cross boundaries: ports, reads, logic leak, sharing rules |

## Stack
Go 1.26+ / Fiber v3 (NOT v2) / GORM + PostgreSQL 17+ / slog / go-playground/validator/v10 / golang-jwt/jwt/v5 / google/uuid / pressly/goose/v3 (SQL only) / Module: `nana`

## Architecture: handler → service → repository
- **domain/** — Pure Go structs, NO gorm imports, typed string constants
- **model/** — GORM structs, `ToDomain()`/`FromDomain()`, `BeforeCreate` UUID hook
- **dto/** — Request/response, `validate` tags, snake_case JSON, money as float64
- **handler/** — `BindBody()`/`BindQuery()` → service call → `Success()`/`Error()` response
- **service/** — Interface first, private impl, `context.Context` first param, business logic HERE
- **repository/** — Interface first, private impl, `context.Context`, `database.DB(ctx, r.db)`, CRUD only
- **database/tx.go** — `TxManager` + `DB()` helper — see `.claude/rules/transaction-pattern.md`
- **apperror/** — `AppError` + `MapToHTTP()` + `ErrorResponse` — centralized, no switch in handlers

## DI (manual in main.go)
```
repo → service → handler → handler.RegisterRoutes(router)
```

## Anti-Patterns
- ❌ Fiber v2 syntax (`*fiber.Ctx`, `c.BodyParser`)
- ❌ GORM in domain layer
- ❌ `c.Bind().Body()` directly — use `BindBody()`
- ❌ Public service/repo structs — interface first, private impl
- ❌ Domain models in API responses — use DTOs
- ❌ Business logic in handlers or repos
- ❌ `r.db.WithContext(ctx)` in repos — use `database.DB(ctx, r.db)` for tx-awareness
- ❌ Cross-entity operations in repo (e.g. room update in contract repo)
- ❌ `gorm.io/gorm` or `model` imports in service layer — use TxManager
- ❌ `float64` for money in domain — use `int64` satang
- ❌ GORM AutoMigrate — Goose SQL only
- ❌ Switch/if error handling in handlers — use `Error(c, err)`

## Size Limits
| Layer | Lines | Methods |
|-------|-------|---------|
| Service | 500 | 10 |
| Handler | 300 | 8 |
| Repository | 400 | 12 |
