---
description: HOW to safely cross feature boundaries — ports, transactions, reads, logic leak prevention. Consult when writing code that touches multiple features or when designing port interfaces.
---

# Cross-Feature Patterns — ข้ามขอบเขตอย่างปลอดภัย

> ดู domain-ownership.md ก่อนสำหรับ "ใครเป็นเจ้าของอะไร"
> ไฟล์นี้ = "ข้ามขอบเขตยังไง"

## 1. Cross-Feature Write (Command Port)

ถ้า feature A ต้องเปลี่ยน state ของ feature B → **port interface เท่านั้น**

```
A's Service (workflow owner) → Port Interface (in A/port.go) → B's Repository (data owner)
```

```go
// contract/port.go — semantic method, consumer ไม่ต้องรู้ค่า constant ของ room
type RoomCommander interface {
    MarkOccupied(ctx context.Context, id uuid.UUID) error
}

// contract/service.go
s.tx.RunInTx(ctx, func(txCtx) error {
    s.repo.Create(txCtx, &contract)              // own table ✅
    s.roomCmd.MarkOccupied(txCtx, roomID)        // via port ✅
})
```

## 2. Transaction Rules

**Workflow owner เริ่ม transaction** — ไม่ใช่ feature ที่ถูกเปลี่ยน

| Workflow | Tx Owner |
|----------|----------|
| สร้าง contract + room OCCUPIED | contract |
| ตั้ง bank account primary | apartment |
| (future) จ่ายเงิน + bill PAID | payment |

```
✅ Transaction เริ่มที่ service layer เท่านั้น
❌ Repository ห้ามเริ่ม transaction
❌ ห้าม nested transaction ข้าม feature
```

### Context Propagation (CRITICAL)

```go
// ✅ ทุก call ใน tx ต้องใช้ txCtx
s.tx.RunInTx(ctx, func(txCtx context.Context) error {
    s.repo.Create(txCtx, &contract)                    // txCtx ✅
    s.rooms.UpdateStatus(txCtx, roomID, OCCUPIED)      // txCtx ✅
    return nil
})

// ❌ BUG: query หลุด tx → อ่าน stale data
s.tx.RunInTx(ctx, func(txCtx context.Context) error {
    s.repo.Create(txCtx, &contract)
    room, _ := s.rooms.FindByID(ctx, roomID)           // ctx แทน txCtx → หลุด tx!
    return nil
})
```

## 3. Cross-Feature Read (3 Types)

### 3a. Validation Read — เช็คก่อน create/update

```go
// contract/port.go — return feature model ได้
type TenantQuerier interface {
    FindByID(ctx context.Context, id uuid.UUID) (*tenant.Tenant, error)
}
```

### 3b. Guard Read — เช็คก่อน delete

```go
// tenant/port.go — return bool/count เท่านั้น
type ContractChecker interface {
    HasActiveByTenantID(ctx context.Context, tenantID uuid.UUID) (bool, error)
}
```

### 3c. Display Read — JOIN เพื่อ API response

**Owner = feature ที่เป็นเจ้าของ endpoint**

```
✅ JOIN table อื่นเพื่ออ่านได้ (SELECT)
✅ return flat DTO / projection
❌ ห้าม INSERT/UPDATE/DELETE table อื่น
❌ ห้าม return domain model ของ feature อื่น
❌ ห้าม reuse DTO ข้าม feature
❌ ห้าม JOIN > 2-3 features — ถ้าเกินให้ใช้ service assembly
```

### Logic Leak Rule (CRITICAL)

**ห้าม encode business logic ของ feature อื่นใน query**

```sql
-- ❌ room repo hardcode business rule ของ contract
LEFT JOIN contracts ON ... AND contracts.status = 'ACTIVE'
```

**วิธีแก้ 2 ระดับ:**

| | ระดับ 1: domain constant | ระดับ 2: port |
|---|---|---|
| วิธี | `contracts.status = ?`, `contract.ContractStatusActive` | contract expose `FindActiveByRoomIDs()` port |
| Coupling | low (traceable) | zero |
| Performance | 1 query (JOIN) | 2 queries + merge |
| **ใช้เมื่อ** | **status = simple enum (ตอนนี้)** | definition ซับซ้อนขึ้น |

## 4. Port Interface Design

### Naming: capability ที่ consumer ต้องการ

| Use Case | Port Name |
|----------|-----------|
| check contract exists | `ContractChecker` |
| update room status | `RoomCommander` |
| query tenant data | `TenantQuerier` |
| query apartment data | `ApartmentQuerier` |
| batch query contracts | `ActiveContractProvider` |

### Method Naming Convention

| Category | Verb | Return |
|----------|------|--------|
| Query: single | `FindByID`, `GetByX` | entity / projection |
| Query: list | `ListByX` | slice |
| Query: scalar | `Has`, `Exists`, `Count` | bool / int64 |
| Command: CRUD | `Create`, `Update`, `Delete` | error |
| Command: state | `UpdateStatus`, `Mark`, `Set` | error |
| Batch query | `FindActiveByIDs` | map |

### Constraints

```
✅ Port อยู่ฝั่ง consumer (A/port.go)
✅ Minimal — เฉพาะ method ที่ใช้จริง
✅ Query/Command แยกเสมอ
✅ Command port ใช้ semantic method (MarkOccupied, MarkPaid) — ไม่ส่ง constant ข้าม feature
❌ ห้าม expose full repository
❌ ห้าม feature อื่นใช้ constant ของ domain โดยตรง — ใช้ domain method แทนเสมอ
```

### Query Port Purity

```
✅ Pure read — no side effects
✅ ไม่ require transaction
✅ Safe ต่อการ cache
❌ ห้าม FindAndUpdateX() (read+write ซ่อน)
```

### Adapter Implementation Shape

**Consumer defines the port. Provider implements a small adapter struct that satisfies it.** All cross-feature adapters in the codebase MUST point the same direction — if one inverts, future authors copy the wrong precedent.

**Template:** `backend/internal/billing/payment_adapter.go` (provider = billing, consumer = payment).

```go
// payment/port.go — CONSUMER defines the port
type BillingPort interface {
    FindBillForPayment(ctx context.Context, id uuid.UUID) (*billing.Bill, error)
    LockBillForPayment(ctx context.Context, id uuid.UUID) (*billing.Bill, error)
    PersistBillPaid(ctx context.Context, b *billing.Bill) error
    EmitPaymentAudit(ctx context.Context, billID uuid.UUID, ...) error
}

// billing/payment_adapter.go — PROVIDER implements a small adapter struct
type PaymentAdapter struct {
    repo  BillingRepository
    audit BillAuditRepository
}

func NewPaymentAdapter(repo BillingRepository, audit BillAuditRepository) *PaymentAdapter {
    return &PaymentAdapter{repo: repo, audit: audit}
}

func (a *PaymentAdapter) FindBillForPayment(ctx, id) (*Bill, error) { ... }
// ...
```

**Constraints:**

```
✅ Consumer defines port; provider implements adapter struct
✅ Adapter lives in PROVIDER package (e.g. billing/payment_adapter.go)
✅ Adapter methods narrow + named around the consumer need
✅ Adapter dependencies = only what the adapter's methods touch (repo, audit, ...)
✅ Compile-time check: var _ consumer.Port = (*ProviderAdapter)(nil)
❌ ห้าม add consumer-shaped methods to the provider's main service interface
   (e.g. don't expose CreateMonthlyBillForReconciliation on BillingService —
   keep BillingService's surface scoped to billing's own concerns)
❌ ห้าม invert the direction (provider importing consumer types into its
   service interface). Inversion = future authors will guess wrong direction.
❌ Provider's service must not import consumer types if avoidable; if a
   shared shape is needed, define neutral types in the consumer's port.go
```

**DI wiring in `cmd/main.go`:**

```go
// Build provider's service first
billService := billing.NewBillingService(...)

// Build adapter wrapping the producer's internals
paymentAdapter := billing.NewPaymentAdapter(billRepo, billAuditRepo)

// Pass adapter (not full service) to consumer
paymentService := payment.NewPaymentService(paymentRepo, paymentAdapter, txManager)
```

## 5. Sharing Rules

### Feature Model

```
❌ ห้าม share ข้าม feature by default
✅ Port return *feature.Model ได้ (e.g. *tenant.Tenant ผ่าน TenantQuerier)
✅ Validation read → *feature.Model ได้
✅ Display read → lightweight projection / flat DTO
```

### DTO

```
❌ ห้าม reuse DTO ข้าม feature
✅ แต่ละ feature define DTO ของตัวเอง
✅ Pagination types (PaginationParams, Meta) เป็น shared ได้
```

## 6. Domain Events (Future — Document Now)

```
✅ Synchronous port = core workflow ที่ต้อง atomic
✅ Event = side effects ที่ eventual consistency ได้
❌ ห้าม emit event ก่อน transaction commit
❌ ห้าม call feature อื่นตรงๆ สำหรับ side effects
```

```go
// ✅ emit หลัง commit เท่านั้น
err := s.tx.RunInTx(ctx, func(txCtx) error { ... })
if err == nil {
    Emit(ContractCreatedEvent{...})  // หลัง commit ✅
}
```

## 7. Hidden Failure Guards

```
1. Query port ต้อง pure (no side effects, no tx requirement)
2. JOIN ไม่เกิน 2-3 features — ถ้าเกินใช้ service assembly
3. Transaction context ต้อง propagate ถูกต้อง (txCtx ไม่ใช่ ctx)
4. Event ต้อง emit หลัง commit เท่านั้น
5. ห้าม encode business logic ของ feature อื่น (แม้ใน service)
6. Display read return flat DTO — ห้ามซ้อน domain model ของ feature อื่น
```
