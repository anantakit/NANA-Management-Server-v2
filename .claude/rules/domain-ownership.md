---
description: DDD ownership rules — who owns which table, where code belongs, decision checklist. Consult EVERY TIME when deciding which feature package a piece of code belongs to, during refactoring or adding features.
---

# Domain Ownership — ใครเป็นเจ้าของอะไร

> ใช้ตัดสินใจว่า code ควรอยู่ feature ไหน — zero ambiguity

## 1. Write Ownership (Hard Boundary)

แต่ละ feature เป็นเจ้าของ data แบบ **exclusive write access**

| Feature | Owns Tables |
|---------|-------------|
| **apartment** | `apartments`, `apartment_bank_accounts` |
| **room** | `rooms` |
| **tenant** | `tenants` |
| **contract** | `contracts` |
| **auth** | `users`, `refresh_tokens` |
| **billing** (future) | `bills` |
| **meter** (future) | `meter_readings` |
| **payment** (future) | `payments` |

```
✅ Feature เขียน table ของตัวเองเท่านั้น
❌ ห้าม INSERT/UPDATE/DELETE table ของ feature อื่น — ต้องผ่าน port
❌ ห้าม bypass ผ่าน raw SQL ข้าม feature
```

## 2. Nana Ownership Map

### Current

```
apartment ─────── owns: apartments, apartment_bank_accounts
  │                reads: rooms (count by status, for stats)
  │
  ├── room ─────── owns: rooms
  │     │           reads: contracts, tenants (JOIN for display — logic leak ระดับ 1)
  │     │
  │     └── contract ── owns: contracts
  │           │          cross-write: rooms.status (via RoomStatusUpdater port)
  │           │          reads: rooms, tenants (validation), apartments (display JOIN)
  │           │
  │           └── tenant ── owns: tenants
  │                          reads: contracts (guard — HasActiveByTenantID)
  │
  └── auth ─────── owns: users, refresh_tokens
                    reads: nothing cross-feature
```

### Future

```
meter ──── owns: meter_readings      reads: rooms, users
billing ── owns: bills               cross-write: contracts? (via port)
                                     reads: contracts, rooms, meter_readings
payment ── owns: payments            cross-write: bills.status (via port)
                                     reads: bills, users
```

## 3. Decision Checklist

### Q1: Code นี้ควรอยู่ feature ไหน?

| ดูจาก | Owner |
|-------|-------|
| เขียน table ไหน | owner ของ table |
| เป็น workflow / use case | owner ของ use case |
| เป็น query สำหรับ endpoint | owner ของ endpoint |

### Q2: ควรใช้ port ไหม?

| Case | Port? |
|------|-------|
| เรียก method ของ feature อื่น | ✅ ใช้ |
| SQL JOIN table อื่น (read-only) | ❌ ไม่ต้อง (ระวัง logic leak) |

### Q3: ใครเริ่ม transaction?

Workflow owner เท่านั้น — feature ที่ trigger การเปลี่ยนแปลง

### Q4: Port = query หรือ command?

| Method... | Port type |
|-----------|-----------|
| return data, ไม่เปลี่ยน state | Query port |
| เปลี่ยน state | Command port |
| ทำทั้งสอง | **แยกเป็น 2 ports** |

### Q5: Share domain model ไหม?

| Case | Share? |
|------|--------|
| Validation read (เช็คว่ามีจริง) | ✅ `*feature.Model` ได้ (ผ่าน port) |
| Display read (แสดงบาง field) | ❌ projection / flat DTO |
| Business logic ที่ต้องรู้ internal state | ❌ ผ่าน port method |

### Q6: ควรแยก domain + model ไหม?

**ไม่ — ทุก feature ใช้ GORM model = domain**

```
✅ ทุก feature (auth, apartment, tenant, room, contract) → GORM model ตรง + domain methods
❌ ห้ามแยก domain struct ออกจาก GORM model (ไม่ต้อง ToDomain/FromDomain)
```

Domain methods (pure, no DB, no side effects) ใส่ที่ model.go ของ feature ตรง ๆ

### Q7: ควรอยู่ feature/ หรือ shared/?

| เป็น... | ที่อยู่ |
|---------|--------|
| Business entity + types + methods | feature package (`contract/model.go`) |
| Cross-cutting concern (role, pagination, errors) | `shared/` |
| Future entity ที่ยังไม่มี feature | `domain/` (ย้ายเข้า feature เมื่อสร้าง) |

```
❌ domain/ ≠ shared dumping ground — เก็บเฉพาะ future entities
✅ feature/model.go = single source of truth (struct + types + methods)
✅ shared/ = infrastructure + cross-cutting concerns
```

## 4. Summary: 10 Principles

```
1.  Write = owner only
2.  Cross-write = command port only (semantic method: MarkOccupied, MarkPaid)
3.  Query & Command ports ต้องแยก
4.  Query port = pure read, no side effects, no tx
5.  Query = owner of endpoint
6.  JOIN allowed, logic NOT allowed (ใช้ domain constant เป็น minimum)
7.  JOIN ≤ 2-3 features, ถ้าเกินให้ assembly ที่ service
8.  Port = minimal + capability-based + consumer-defined
9.  DTO ≠ shared, Domain ≠ shared by default
10. Side effects → event (future), emit after commit only
11. ห้าม feature อื่นใช้ constant ของ domain โดยตรง — ใช้ domain method แทนเสมอ
```
