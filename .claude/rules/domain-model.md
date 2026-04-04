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
- BillStatus: `PENDING`, `PAID`
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
