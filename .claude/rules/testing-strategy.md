# Testing Strategy — What to test, what to skip

> See `coding-standards.md` for the 3-layer overview (Domain / Service / Handler).
> This file = "ภายใน service layer, อะไรควร test อะไรไม่ควร" — optimized for signal/noise ratio.

## TL;DR

1. **Domain** → test ทุก method ทันที (mandatory, pure I/O, no mocks)
2. **Service** → happy path + edge cases ที่มี business logic (truncation, branching, calculation, state transition, tx ordering)
3. **Repo / Handler / validation guards / error passthrough** → ❌ ข้าม
4. **Decision question:** *"ถ้าพัง มันพังเงียบไหม?"* เงียบ = test, crash/obvious = skip

## 1. Workflow

```
Domain method → เขียน test ทันที → แล้วค่อยไป service
```

| Layer | Coverage | Mocks |
|-------|----------|-------|
| **Domain** | ✅ Mandatory — pure functions, ทุก method | ❌ none |
| **Service** | ⚠️ Happy path + edge cases ที่มี business logic | hand-written, compile-time `var _ I = (*mock)(nil)` |
| **Repository** | ❌ ไม่ต้อง test | — |
| **Handler** | (later, when stable) | mock service |

## 2. Why this rule exists

Optimize สำหรับ **signal/noise ratio**.

ใน clean-ish architecture (handler → service → repo) bug ส่วนใหญ่ไม่ได้เกิดจาก:
- Invalid input (handler/validator จับ)
- Repo error (passthrough ด้วย `%w`)
- Empty slice (Go จัดการอยู่แล้ว)
- DB errors (Postgres complains loud)

Bug ส่วนใหญ่เกิดจาก **logic ผิดใน flow หลัก**:
- ลืม set field
- คำนวณผิด (off-by-one, wrong unit)
- Branch routing ผิด
- State ไม่ sync กัน (forgot to call port X in tx)
- Sort/partition ผิด

ทั้งหมดนี้อยู่ใน **happy path** ของ service. ถ้าเขียน test ครอบทุก validation/error-passthrough จะได้:
- Test เยอะมาก
- Value ต่ำ
- Maintenance cost สูง
- Refactor ที = test พังครึ่ง repo
- Coverage % สูงแต่ bug ยังหลุด

## 3. Decision heuristic — "พังเงียบไหม?"

ทุกครั้งที่กำลังจะเขียน service test ใหม่ ถามว่า:

> **"ถ้า code เคสนี้พัง มันจะพังแบบเงียบๆ ไหม?"**

- **เงียบ → ✅ ต้อง test** (logic แอบฝังอยู่, ไม่มีอะไรจับ)
- **Crash / obvious / framework จับให้ → ❌ ไม่ต้อง test**

## 4. Decision matrix

| Case | Test? | เหตุผล |
|---|---|---|
| Happy path (main flow) | ✅ | core business logic |
| Truncation/cap + flag + count | ✅ | off-by-one risk, ผิดเงียบ |
| Multiple branches/scope (active/history/all) | ✅ | คนละ code path |
| Calculation, partition, sort | ✅ | logic หลัก |
| State transition (PENDING→COMPLETED) | ✅ | business invariant |
| Cross-feature side-effect order in tx (commands fired in correct order, atomicity) | ✅ | สำคัญต่อ correctness |
| Domain validation surfacing as AppError (not opaque 500) | ✅ | guards `respond.Is()` propagation |
| Invalid scope/uuid → 400 | ❌ | reject เฉยๆ ไม่มี logic, handler ก็ทำได้ |
| Repo error passthrough (`%w`) | ❌ | boilerplate |
| Empty slice handling | ❌ | Go จัดการอยู่แล้ว |
| Validation message wording | ❌ | ไม่ใช่ behavior |
| `respond.Error()` formatting | ❌ | shared infra responsibility |
| Constructor field assignment | ❌ | compile-time check ของ Go จับ |

## 5. Mental model

> Happy path = coverage ของ **business logic หลัก** ไม่ใช่ coverage ของ **ทุก possible input**

เป้าหมาย: **test น้อย แต่จับ bug ได้จริง**

ถ้าจะ refactor service tier, test ที่เหลือควรเป็นแค่ test ที่ break เพราะ behavior เปลี่ยนจริง — ไม่ใช่เพราะ method signature ขยับ.

## 6. Examples from this codebase

**✅ Worth testing** (ดู `backend/internal/moveout/service_queue_test.go`):
- `Queue_PartitionsSortsSummarizes` — partition + sort + summary = main logic
- `Queue_TruncatesSectionAtCap` — cap boundary, off-by-one risk, frontend depends on TotalCount
- `Queue_ScopeHistoryAndAll` — branch routing, typo in if-conditions พังเงียบ

**❌ Not worth testing** (ตั้งใจไม่เขียน):
- `Queue` กับ scope ที่ผิด → 400 (validation guard ไม่มี logic)
- `Queue` กับ apartment_id ที่ไม่ใช่ uuid → 400
- Repo error → wrap แล้ว return
- Active list ว่าง → ก็ return empty section (Go zero-value)

## 7. Anti-patterns

```
❌ Snapshot tests (lock อยู่กับ formatting, refactor พัง)
❌ "test ทุก public method ของ service เพราะ coverage tool บอก"
❌ Test ที่ assert เฉพาะ error message string (เปลี่ยน wording = test พัง)
❌ Test ที่ mock ทุก collaborator แล้ว assert call count แม้กับ method ที่ไม่มี business meaning
❌ Repo test (DB integration test ก็ยังไม่ต้อง — ทำตอน billing/payment ที่ logic ซับซ้อนขึ้น)
```
