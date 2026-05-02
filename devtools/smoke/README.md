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
| `make smoke-settlement-preview` | TC1/2/3/6/7/10/11 SettlementStep preview surface (drawer Step 2) |
| `make smoke-settlement-all` | preview แล้วต่อ scenario |
| `make smoke-draft` | TC-D01–D12 draft page + regenerate preservation smoke |
| `make smoke-moveout-step23` | Scenarios A–D drawer Step 2/3 commit boundary + edit-back + rent-mode continuity |
| `make smoke-moveout-step4` | Scenarios A–D Phase 2 close-with-unsettled + back-fill (~30 assertions) |
| `make smoke-moveout-detail` | T1–T8 MoveOutDetailPage per-state behavior (CTA / cancel / reopen / restart / direct URL / queue regression) |

หรือรันตรงจาก `backend/devtools/smoke/`:

```bash
npm run smoke:settlement
npm run smoke:settlement-preview
npm run smoke:settlement:all
npm run smoke:draft
npm run smoke:moveout-step23
npm run smoke:moveout-step4
npm run smoke:moveout-detail
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


### Settlement Preview — drawer Step 2 surface (TC1/2/3/6/7/10/11)

ไฟล์: `playwright-test-settlement-preview-smoke.js`

ทดสอบ surface ของ settlement preview ใน `RoomWorkflowDrawer` Step 2 (`SettlementStep`). Slim port จาก legacy preview-drawer suite — เก็บเฉพาะเคสที่ smoke อื่นไม่ครอบ:

| TC | เรื่อง | ล็อกอะไร |
|----|--------|----------|
| TC1 | Baseline preview renders ใน SettlementStep | drawer routes PENDING_SETTLEMENT → Step 2; SettlementMeta + OutcomeCard + ChargesSection + SettlementSection ปรากฏ; CTA "ยืนยันยอด" (NO_DRAFT path) |
| TC2 | Rent-mode toggle | mode label flip + outcome amount เปลี่ยน |
| TC3 | MinMonths threshold flip | outcome amount ต่างกันระหว่าง 2 mode (PRORATED fail / FULL_MONTH pass) |
| TC6 | rent_paid=true | ChargesSection ไม่มี rent line (carry-forward path) |
| TC7 | Absorbed bills | "บิลค้างที่ต้องชำระก่อนย้ายออก" ปรากฏ |
| TC10 | PENDING_METER routing | drawer เปิดที่ Step 1 (มิเตอร์), ไม่ใช่ Step 2; SettlementMeta + ChargesSection ต้อง **ไม่** ปรากฏ |
| TC11 | Mode-switch loading UX | charges card ยังเห็นระหว่าง refetch (ไม่ flash skeleton เต็มหน้า) |

ตัด TC4 / TC5 / TC8 / TC9 / TC12 (จาก legacy) — ครอบคลุมโดย `smoke:moveout-step23` Scenario A/B/D และ `smoke:draft` แล้ว.

Fixtures: TC1 (B105), TC3 (B201), TC6 (B203), TC7 (B204), TC10 (B205) ทั้งหมดจาก `seed_dev_smoke.go`.

Stable selectors:
- `[role="dialog"][aria-label="ดำเนินการย้ายออก"]` — drawer scope
- `[aria-label="ขั้นตอนที่ N <label>"]` + `aria-current="step"` — step routing
- `[data-testid="settlement-outcome-amount"]` — outcome amount (added in `OutcomeCard`)
- `button[aria-label="เปลี่ยนวิธีคิดค่าเช่า"]` — rent-mode toggle

### Move-out Phase 2 — Step 4 Close-with-Unsettled (Scenarios A–E)

ไฟล์: `playwright-test-moveout-step4-smoke.js`

ทดสอบ flow ปิดสัญญา Phase 2 + post-close back-fill บน RoomWorkflowDrawer:

| Scenario | เรื่อง |
|----------|--------|
| A | PENDING_PAYMENT + null → record settled → close → history (form + edit-back, COMPLETED+settled) |
| B | PENDING_PAYMENT + null → skip → ปิดงาน (ยังไม่ชำระ) → COMPLETED+nil, drawer **stays open** at Step 4 post-close view, card stays in payment tab. **Section grouping**: payment tab splits into "ยังไม่ปิดสัญญา" + "ปิดแล้ว • ค้างชำระ" headers when both populated |
| C | COMPLETED + nil re-entry from queue card → form direct (no edit-back) → record → COMPLETED+settled, history. **Pins blank-screen regression** |
| D | ZERO_BALANCE back-fill — payment_method must stay nil (service-side normalization invariant) |
| E | Step 4 → "ไปชำระเงิน" → Step 3 → record → Step 4 confirm. **Pins**: drawer NOT auto-dismissed, "ไปชำระเงิน" navigates to Step 3 (NOT Step 2), form not blank, no edit-back, status STAYS COMPLETED |

Fixtures: TC4 (B202), TC22 (D201), TC23 (D202), TC24 (D103). State setup ผ่าน API (finalize / generate / close-with-unsettled) เพื่อโฟกัส UI test ที่ flow ปลายทาง.

### MoveOutDetailPage — Per-State Behavior (T1–T8)

ไฟล์: `playwright-test-moveout-detail-smoke.js`

ทดสอบ behavior ของ detail page ทีละ state. ทุก assertion ที่เช็ค UI จะเช็ค consequence ด้วย — drawer step idx / API response / backend status re-fetch — เพื่อกัน "false pass" ที่เคยเกิด

| Test | เรื่อง |
|------|--------|
| T1 | PENDING_PAYMENT — CTA "ดำเนินการต่อ" เปิด drawer ที่ Step 3, ไม่มี cancel/reopen link |
| T2 | PENDING_SETTLEMENT (no draft + with draft) — แสดง "ยังไม่มีสรุปยอด", **ไม่** render ResultBlock "฿0 hero", CTA เปิด drawer ที่ Step 2 |
| T3 | READY_TO_CLOSE — CTA = "ปิดการย้ายออก", drawer ที่ Step 4. "กลับไปบันทึกการชำระ" → confirm → /reopen → backend = PENDING_PAYMENT (rewinds one step, not back to settlement; payment_outcome cleared) |
| T4 | Cancel flow — link visible เฉพาะ PENDING_METER/PENDING_SETTLEMENT, confirm → /cancel → CANCELLED |
| T5 | COMPLETED — terminal read-only, ไม่มี CTA / cancel / reopen / restart |
| T6 | CANCELLED — CTA = "เริ่มแจ้งย้ายออกใหม่" เปิด MoveOutNoticeModal prefilled (room + tenant) |
| T7 | Queue regression — card click เปิด drawer in-place, URL ค้างที่ /move-out |
| T8 | Direct URL — page.goto(/move-out/:id) **ไม่** auto-open drawer; ต้องคลิก CTA ก่อน |

Fixtures: TC1 (B105), TC4 (B202), TC10 (B205), TC22 (D201), TC23 (D202), TC24 (D103). State setup ผ่าน API (finalize / skip-payment / record-payment / close / cancel) เพื่อ isolate UI test จาก setup.

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
