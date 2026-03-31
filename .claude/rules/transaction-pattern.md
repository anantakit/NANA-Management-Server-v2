---
description: TxManager pattern for cross-repo transactions in service layer
paths:
  - "internal/*/service.go"
  - "internal/*/repository.go"
  - "internal/shared/database/**"
  - "cmd/**"
---

# Transaction Pattern — TxManager

## Overview
ใช้ `database.TxManager` สำหรับ operations ที่ต้องการ atomicity ข้าม repos
Service เป็น **orchestrator**, repo ไม่รู้เรื่อง entity อื่น

## Architecture

```
shared/database/tx.go  → TxManager interface + DB() helper
feature/service.go     → inject TxManager, orchestrate cross-repo operations
feature/repository.go  → ใช้ database.DB(ctx, r.db) ทุก method (tx-aware อัตโนมัติ)
```

## Key Components

### database.TxManager
```go
type TxManager interface {
    RunInTx(ctx context.Context, fn func(ctx context.Context) error) error
}
```

### database.DB(ctx, fallback)
```go
// ทุก repo method ต้องใช้แทน r.db.WithContext(ctx)
database.DB(ctx, r.db).Where("id = ?", id).First(&m)
```
- ถ้า ctx มี transaction → ใช้ tx นั้น (ร่วม transaction เดียวกัน)
- ถ้าไม่มี → ใช้ fallback DB ปกติ

## Usage in Service

### Single-repo operation — ไม่ต้องใช้ TxManager
```go
func (s *roomService) Create(ctx context.Context, ...) error {
    return s.repo.Create(ctx, &room) // ใช้ DB ปกติ
}
```

### Cross-repo operation — ใช้ TxManager
```go
func (s *contractService) Create(ctx context.Context, ...) error {
    // Validation ก่อน transaction (ลด lock time)
    room, _ := s.roomRepo.FindByID(ctx, roomID)
    if room.Status != roomPkg.RoomStatusVacant { return ... }

    // Transaction: หลาย repo ใน tx เดียว
    err := s.tx.RunInTx(ctx, func(txCtx context.Context) error {
        if err := s.contractRepo.Create(txCtx, &contract); err != nil {
            return err
        }
        return s.roomRepo.UpdateStatus(txCtx, roomID, roomPkg.RoomStatusOccupied)
    })
    if err != nil {
        return fmt.Errorf("create contract: %w", err)
    }

    // Re-fetch after tx (with relations)
    return s.contractRepo.FindByID(ctx, contract.ID)
}
```

## Rules

### DO
- ✅ ใช้ `database.DB(ctx, r.db)` ทุก repo method
- ✅ Inject `database.TxManager` ใน service ที่ต้องการ cross-repo tx
- ✅ ทำ validation **ก่อน** `RunInTx` เพื่อลด lock time
- ✅ Pass `txCtx` (ไม่ใช่ `ctx`) ให้ repo methods ภายใน `RunInTx`
- ✅ Re-fetch after transaction ถ้าต้องการ response พร้อม relations

### DON'T
- ❌ ใช้ `r.db.WithContext(ctx)` ตรงๆ ใน repo (จะไม่ participate ใน tx)
- ❌ Repo เรียก entity อื่น (e.g. `room.Room{}` ใน contract repo)
- ❌ Service import `gorm.io/gorm`
- ❌ สร้าง `XxxAsPrimary()` หรือ `CreateAndUpdateYyy()` ที่ repo level
- ❌ ทำ heavy work (HTTP calls, file I/O) ภายใน `RunInTx`

## DI Wiring

```go
// cmd/main.go
txManager := database.NewTxManager(db)

// Service ที่ต้อง cross-repo tx
contractSvc := contract.NewContractService(contractRepo, roomRepo, roomRepo, tenantRepo, txManager)
bankAcctSvc := apartment.NewBankAccountService(bankRepo, aptRepo, txManager)

// Service ที่ไม่ต้อง — ไม่ต้อง inject
roomSvc := room.NewRoomService(roomRepo, aptRepo)
```

## Future Use Cases
| Feature | Repos in Transaction |
|---------|---------------------|
| ย้ายออก (end contract) | contractRepo + roomRepo (status → VACANT) + billRepo (settlement) |
| จ่ายบิล (payment) | paymentRepo + billRepo (status → PAID) |
| บันทึกมิเตอร์ + ออกบิล | meterReadingRepo + billRepo |

Pattern เดิม: service orchestrate ด้วย `s.tx.RunInTx(ctx, func(txCtx) { ... })`
