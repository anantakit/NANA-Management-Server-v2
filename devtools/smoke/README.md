# Settlement Smoke Tests

Dev-only Playwright smoke tests สำหรับ move-out settlement workflow

ไม่ใช่ CI test — รันมือก่อน deploy, หลัง refactor, หรือตอน debug

## Setup (ครั้งแรก)

```bash
cd backend
make smoke-install
```

## สิ่งที่ต้องรันอยู่ก่อน

```bash
make dev   # postgres + backend + frontend ต้องพร้อมทั้ง 3
```

## Commands

รันจาก `backend/`:

| Command | รันอะไร |
|---------|---------|
| `make smoke-settlement` | TC13–TC21 scenario smoke (28 assertions) |
| `make smoke-settlement-legacy` | TC1–TC12 preview interaction smoke (20 assertions) |
| `make smoke-settlement-all` | legacy แล้วต่อ scenario (45 assertions) |
| `make smoke-draft` | TC-D01–D12 draft page + regenerate preservation smoke |
| `make smoke-queue` | Scenarios A–F queue settlement smoke |

หรือรันตรงจาก `backend/devtools/smoke/`:

```bash
npm run smoke:settlement
npm run smoke:settlement:legacy
npm run smoke:settlement:all
npm run smoke:draft
npm run smoke:queue
```

## Test Suites

### Draft Page — Recalculation Surfaces (TC-D01–D08)

ไฟล์: `playwright-test-draft-settlement-smoke.js`

ทดสอบ settlement draft page (หน้าแก้ไขแบบร่าง):

**D01–D08: Recalculation surfaces**

| TC | เรื่อง |
|----|--------|
| TC-D01 | Happy path — page renders, all sections visible, no NaN |
| TC-D02 | Edit meter readings → usage + charges + total recalculate |
| TC-D03 | Change rent mode (PRORATED → FULL_MONTH) → total updates |
| TC-D04 | Deposit: "ใช้เงินประกันหัก" (FULL) → net reflects deduction |
| TC-D05 | Deposit: "ไม่ใช้เงินประกัน" (NONE) → net = total charges |
| TC-D06 | Deposit: "กำหนดเอง" (CUSTOM) → partial deposit applied |
| TC-D07 | Add extra charge via preset chip → total increases |
| TC-D08 | Sequential multi-edit (deposit toggle + extra charge) → no stale state |

**D09–D12: Regenerate preservation (high-risk area)**

| TC | เรื่อง |
|----|--------|
| TC-D09 | Regenerate preserves manual items (no loss, no duplication) |
| TC-D10 | Regenerate preserves deposit override (CUSTOM mode + amount) |
| TC-D11 | Regenerate preserves manual + override combined |
| TC-D12 | Multiple regenerations — idempotent (no additive drift) |


### Legacy — Preview Interaction (TC1–TC12)

ไฟล์: `playwright-test-settlement-preview-legacy.js`

ทดสอบ UI interaction ของ settlement preview drawer:

| TC | เรื่อง |
|----|--------|
| TC1 | Happy path — drawer เปิด, มี mode/charges/deposit/outcome |
| TC2 | สลับ rent mode (PRORATED / FULL_MONTH) |
| TC3 | MinMonths threshold flip |
| TC4 | มี draft — ปุ่มเปลี่ยนเป็น "ดูสรุปยอดใหม่" |
| TC5 | Mode change hint เมื่อเลือกต่างจาก draft |
| TC6 | Rent already paid |
| TC7 | Absorbed bills section |
| TC8 | Duplicate guard ไม่บล็อก preview |
| TC10 | Missing actual_move_out_date |
| TC11 | Loading UX ระหว่าง mode switch |
| TC12 | Create draft + navigate |

### Scenario — Business Correctness (TC13–TC20)

ไฟล์: `playwright-test-settlement-scenario-smoke.js`

ทดสอบ business logic จาก settlement scenario spec:

| TC | เรื่อง | ล็อกอะไร |
|----|--------|----------|
| TC13 | Paid monthly bill carry-forward | subtotal ตรงกับ usage จาก last committed reading |
| TC14 | Unpaid bill absorbed (1 ใบ) | absorbed section แสดง + แยกจาก exit charges |
| TC15 | Multiple outstanding (3 ใบ) | absorbed >= 2 rows |
| TC16 | Scheduled != actual date | header แสดง actual + subtotal ตรงกับ actual date |
| TC17 | Backdated actual date | preview ใช้วันย้อนหลังที่บันทึก |
| TC18 | Deposit refund | outcome = "คืนเงินผู้เช่า", refund > 0 |
| TC19 | Deposit shortfall | outcome = "ผู้เช่าชำระเพิ่ม", due > 0 |
| TC20 | Invalid exit reading | error + submit disabled + ไม่สร้าง draft |

## Fixtures

ทั้ง 2 suites ใช้ dev-only endpoints (ไม่ต้อง auth):

| Endpoint | หน้าที่ |
|----------|---------|
| `POST /api/v1/dev/smoke/seed` | สร้าง fixtures (tenant 9999*) |
| `POST /api/v1/dev/smoke/cleanup` | ลบ fixtures ทั้งหมด |
| `GET /api/v1/dev/smoke/fixtures` | ดูรายการ fixture ปัจจุบัน |

Fixtures อยู่ใน `internal/seed/seed_dev_smoke.go`

## Screenshots

ทุก TC จะบันทึก screenshot ไว้ที่ `/tmp/smoke-tc*.png`

เมื่อ fatal error: `/tmp/smoke-fatal.png` หรือ `/tmp/smoke-scenario-fatal.png`

## ใช้เมื่อไหร่

- ก่อน deploy settlement/billing/move-out
- หลัง refactor billing service หรือ settlement preview
- ตอน debug ปัญหาค่าเช่า/เงินประกัน
- หลังแก้ meter reading logic
