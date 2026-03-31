---
description: Code review checklist for backend Go code
paths:
  - "internal/**"
  - "cmd/**"
---

# Backend Code Review

## Architecture
- [ ] Vertical slice: handler + service + repo + dto + model อยู่ใน feature package เดียวกัน
- [ ] Business logic อยู่ใน service + domain methods (ไม่อยู่ใน handler หรือ repo)
- [ ] Interface first, private struct implementation
- [ ] GORM model = domain (ไม่แยก, ไม่มี ToDomain/FromDomain)
- [ ] Cross-feature write ผ่าน port เท่านั้น (feature/port.go)
- [ ] DTOs ใช้ `float64` baht, model ใช้ `int64` satang

## Query & Performance
- [ ] `database.DB(ctx, r.db)` ทุก query (ไม่ใช่ `r.db.WithContext(ctx)`)
- [ ] N+1 query: ถ้า list endpoint มี related data → ใช้ JOIN ไม่ใช่ loop query
- [ ] Pagination ทุก list endpoint (`LIMIT` + `OFFSET`)
- [ ] Index: column ที่ใช้ใน `WHERE`, `JOIN`, `ORDER BY` มี index
- [ ] `SELECT` เฉพาะ column ที่ใช้ (ไม่ `SELECT *` ถ้าไม่จำเป็น)
- [ ] Count query แยกจาก data query (ไม่ `COUNT(*)` บน JOIN ที่ซับซ้อน)
- [ ] Transaction ใช้เฉพาะ operation ที่ต้อง atomic (ไม่ wrap ทั้ง function)

## Update Pattern
- [ ] ใช้ `db.Model(&m).Select("*").Omit("deleted_at").Updates(&m)` — ไม่ใช่ `Save()` ที่ skip zero values
- [ ] Pointer fields สำหรับ partial update (`*string`, `*int`)

## Error Handling
- [ ] `respond.ErrNotFound.WithMessage()` สำหรับ business errors
- [ ] `fmt.Errorf("context: %w", err)` สำหรับ internal errors
- [ ] Handler ใช้ `respond.Error(c, err)` — ไม่มี switch/if
- [ ] Error message เป็นภาษาไทย (user-facing)

## Security
- [ ] SQL parameterized (`WHERE field = ?`) — ไม่ concatenate string
- [ ] UUID parse ก่อนใช้ (`uuid.Parse()`)
- [ ] Validate tags บน DTO ทุก field
- [ ] Endpoint อยู่ใต้ middleware ถูก group (admin/protected/public)
- [ ] Soft delete เท่านั้น (ไม่ hard delete)

## Money
- [ ] Domain: `int64` satang เสมอ
- [ ] DTO: `float64` baht เสมอ
- [ ] Convert ผ่าน `money.ToSatang()` / `money.ToBaht()` เท่านั้น

## Size Limits
- [ ] Service ≤ 500 lines, ≤ 10 methods
- [ ] Handler ≤ 300 lines, ≤ 8 methods
- [ ] Repository ≤ 400 lines, ≤ 12 methods
