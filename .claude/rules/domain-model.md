---
description: Domain model details, entity relationships, billing logic, status constants
paths:
  - "internal/domain/**"
  - "internal/*/model.go"
  - "internal/*/dto.go"
---

# Domain Model Reference

## Entity Relationships
```
Apartment 1──N Room 1──N Contract N──1 Tenant
                         │
                    MeterReading
                         │
                    Bill 1──N Payment
```

## Status Constants (typed strings in feature/model.go)
- RoomType: `air`, `fan`
- RoomStatus: `VACANT`, `OCCUPIED`, `MAINTENANCE`
- ContractStatus: `ACTIVE`, `ENDED`, `TERMINATED`
- DepositStatus: `COLLECTED`, `REFUNDED`, `FORFEITED`
- BillType: `MONTHLY`, `SETTLEMENT`
- BillStatus: `DRAFT`, `FINALIZED`, `PAID`, `VOID`
- MoveOutStatus: `PENDING_METER`, `PENDING_SETTLEMENT`, `PENDING_PAYMENT`, `READY_TO_CLOSE`, `COMPLETED`, `CANCELLED`
- PaymentOutcome: `PAID_EXTRA`, `REFUNDED`, `ZERO_BALANCE`
- PaymentMethod: `CASH`, `TRANSFER`
- UserRole: `admin`, `manager`

## Apartment Fields
ID, Name, DisplayOrder, Address (text), TaxID
ElectricityRatePerUnit (int64 satang), WaterRatePerUnit (int64 satang)

## Apartment Bank Accounts (separate table)
ID, ApartmentID (FK), BankName, AccountName, AccountNumber, PromptPayID, IsPrimary, Note

## Room Fields
ID, ApartmentID (FK), Number, Type (RoomType), Floor
BaseRent (int64 satang), BaseDeposit (int64 satang), Status (RoomStatus)

## Contract Fields
ID, TenantID (FK), RoomID (FK), StartDate, MinMonths (default 6, min 1)
MonthlyRent (int64 satang), DepositAmount (int64 satang)
ElectricityRatePerUnit (int64 satang), WaterRatePerUnit (int64 satang)
DepositStatus, Status (ContractStatus), EndDate (nullable)

## Bill Fields
ID, ContractID (FK), RoomID (FK), BillingMonth (YYYY-MM), BillDate
Type (BillType), RoomCharge, ElectricityUnits, ElectricityCharge
WaterUnits, WaterCharge, TotalAmount, DueDate, Status (BillStatus)
DepositDeduction (settlement only)

## Billing Logic (CRITICAL)
- **Monthly** = ค่าห้องเดือนถัดไป (advance) + ค่าน้ำไฟเดือนนี้ (from meter)
- **Settlement** (ย้ายออก):
  - ย้ายภายในเดือนที่จ่ายแล้ว → ค่าน้ำไฟอย่างเดียว
  - ย้ายเกินสิ้นเดือน → pro-rate: `(rent / daysInMonth) × extraDays` + ค่าน้ำไฟ
  - หักเงินประกันคืน (ถ้าอยู่ครบ minMonths)
  - เดือนย้ายออก → settlement bill แทน monthly bill
  - Line item order: prorate rent → ค่าน้ำ → ค่าไฟ → ค่าทำห้อง (config) → prepaid credit
  - Line item source: AUTO (computed) vs MANUAL (user-added)

## Deposit Refund Rule (LOCKED)
- **Rule:** คืนประกันเมื่อ `moveOutDate >= addMonthsClamped(startDate, minMonths)`
- **Calendar-month clamp** (ไม่ใช่ Go AddDate ตรง):
  - Jan 31 + 1m = Feb 28 (ไม่ใช่ Mar 3)
  - Aug 31 + 1m = Sep 30
  - Jan 31 + 1m (leap year) = Feb 29
- **ไม่ครบ → ริบประกัน:** deposit = 0 ในบิลสรุป, ผู้เช่าจ่ายค่าใช้จ่ายเต็ม
- **ครบ → คืนประกัน:** หักค่าใช้จ่าย แล้วคืนส่วนที่เหลือ
- **Guard:** moveOut < start → ไม่คืน, minMonths = 0 → คืนเสมอ
- **Implementation:** `effectiveDeposit(contract, moveOutDate)` ใน billing/service.go — ห้ามใช้ `c.DepositAmount` ตรง
- **Override (phase ถัดไป):** admin override พร้อมเหตุผล + audit สำหรับเคสเฉียด
