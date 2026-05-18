# Smoke Formula Contract

> Locked formula + selector contract for the numeric smoke (`smoke:numeric`)
> and any other smoke that asserts settlement bill totals.
>
> Reading this **before** refactoring billing semantics avoids the class of
> "test world drifted out of production world" failures that surfaced on
> 2026-05-16 (11/22 numeric fails turned out to be alignment drift, not a
> regen idempotency bug).

## 1. Settlement totals — canonical formula

`smoke:numeric` (`playwright-test-draft-numeric-smoke.js`) reproduces what
`prepareSettlementPlan + addConfigFees + addRentAdjustment` produces. The
constants must stay byte-equivalent to the apartment's `billing_configs`
defaults seeded by `seedBillingConfigs` in `internal/seed/seed.go`.

### Constants
| Symbol           | Smoke value     | Source of truth                                    |
|------------------|-----------------|----------------------------------------------------|
| `MONTHLY_RENT`   | `250000` satang | smoke contract default (created by `createSmokeScenarioReturn`) |
| `DEPOSIT`        | `200000` satang | smoke contract default                             |
| `ELEC_RATE`      | `800`           | smoke contract `electricity_rate_per_unit`         |
| `WATER_RATE`     | `1800`          | smoke contract `water_rate_per_unit`               |
| `ELEC_UNITS`     | `135`           | exit meter delta in `testExitReading`              |
| `WATER_UNITS`    | `18`            | exit meter delta in `testExitReading`              |
| `CLEANING_FEE`   | `30000`         | `seedBillingConfigs` `FeeTypeCleaningFee.DefaultAmount` |
| `KEY_SERVICE`    | `5000`          | `seedBillingConfigs` `FeeTypeKeyService.DefaultAmount`  |
| `PRORATE_RATE`   | `10000`/day     | `seedBillingConfigs` `FeeTypeProrateDailyRate.DefaultAmount` |

### PRORATED rent

```text
proRateRent = PRORATE_RATE × day   // flat per-day rate × usedDays
```

Source: `addRentAdjustment` in `internal/billing/service_settlement_plan.go`.
The legacy `MONTHLY_RENT × day / dim` formula was retired by the
PRORATE_DAILY_RATE config-driven refactor on 2026-05-10.

### FULL_MONTH rent

```text
fullMonthRent = MONTHLY_RENT   // contract.monthly_rent, charged whole
```

Used when `SettlementRentMode = FULL_MONTH_KEEP_DEPOSIT`.

### Auto-charge total (settlement)

```text
autoTotal_PRORATED  = proRateRent + WATER + ELEC + CLEANING_FEE + KEY_SERVICE
autoTotal_FULLMONTH = MONTHLY_RENT + WATER + ELEC + CLEANING_FEE + KEY_SERVICE
```

`KEY_SERVICE` is always included — it's a flat config fee injected by
`addConfigFees`. Even though [backlog_key_incident_tracking.md] flags this
as transitional behavior (the long-term intent is per-incident monthly
billing), the smoke contract mirrors **current production**, not
aspirational. If/when KEY_SERVICE is moved off settlement, update this
doc + `computeExpected` + every `attachDraftSettlement*` seed helper in
the **same commit**.

### Total + net

```text
total = autoTotal + sum(manual line items)
net   = total - DEPOSIT   // before DepositApplication (FULL/NONE/CUSTOM)
```

`DepositApp` policies (`FULL` = `min(DEPOSIT, total)`, `NONE` = `0`,
`CUSTOM` = `CustomDepositApplied`) override `net` post-calc; smoke checks
mostly assert pre-policy `total` and let the FE display drive deposit math.

## 2. DOM selectors

The settlement page renders existing-draft line items via
`getAutoChargesFromBill` (`frontend/src/features/move-out/domain/settlement.ts`),
which uses the **backend description** as the row label — not
`LINE_TYPE_LABEL`. Selectors must match what's actually rendered.

| Line type        | Selector text (substring)   | Notes                                  |
|------------------|----------------------------|----------------------------------------|
| PRORATE_RENT     | `วัน × ฿`                   | unique fragment of `"%d วัน × ฿100/วัน"` description |
| ROOM_RENT (full) | `ค่าห้องเดือน`              | from `"ค่าห้องเดือน YYYY-MM"`         |
| WATER            | `ค่าน้ำ`                    | from `"ค่าน้ำ N หน่วย"`                |
| ELECTRICITY      | `ค่าไฟฟ้า`                  | from `"ค่าไฟฟ้า N หน่วย"`              |
| CLEANING_FEE     | `ค่าทำความสะอาด`            | literal feed description               |
| KEY_SERVICE      | `ค่าบริการกุญแจ`            | from `feeDescriptions[FeeTypeKeyService]` |
| Subtotal row     | `รวมค่าใช้จ่าย`             | section header inside SettlementChargesCard |
| Net amount       | `.tabular-nums.text-[24px], .tabular-nums.text-xl` (action bar) | mobile + desktop sizes |

Do **not** use `LINE_TYPE_LABEL` text (`ค่าเช่าห้อง`) for rent — that
constant is rendered only by `computeAutoCharges` (preview path with no
existing draft). All smoke fixtures pre-attach a draft, so the page goes
through `getAutoChargesFromBill` and the rendered label is the BE
description.

## 3. Seed canonicalization

Any `attachDraftSettlement*` helper in `internal/seed/seed_dev_smoke.go`
MUST include every line type that `prepareSettlementPlan + addConfigFees`
would emit for the same contract+date+rentMode. The smoke regen flow will
overwrite the baseline with planner output; if the seed handcrafted a
subset, the smoke fails with `regen1 != baseline` (which can look like a
regen bug but isn't).

Current canonical seeds:
- `attachDraftSettlementBill` (TC4): PRORATED — 5 AUTO lines (rent, water, elec, cleaning, key_service)
- `attachDraftSettlementBillWithManualItems` (TC22): PRORATED — same 5 AUTO + 2 MANUAL
- `attachDraftSettlementBillFullMonth` (TC23): FULL_MONTH — same 5 AUTO (`ROOM_RENT` not `PRORATE_RENT`)

If a fee is added to `feeLineTypes` in `service_settlement_plan.go`, every
helper above + the smoke `computeExpected` need updating in the same diff.

## 4. Unit-test lock

`TestRegenerateSettlement_IsIdempotent` (in
`internal/billing/service_settlement_test.go`) exercises the full planner
+ addConfigFees + commit path with prod-default configs and asserts
`regen1 == regen2` on `TotalAmount` + line-item multiset. This is the
preventive bound — if it ever fails, that **IS** a real regen bug, not
seed drift. The smoke `D17` block + this unit test together cover both
sides of the "baseline = canonical" + "regen is deterministic" contract.

## 5. Drift signature cheat-sheet

| Symptom                                  | What it means                          |
|------------------------------------------|----------------------------------------|
| `regen1 != baseline` AND `regen1 == regen2` | Stale seed (non-canonical baseline). Fix seed, not regen. |
| `regen1 != regen2` (continues drifting)    | **Real regen idempotency bug.** Look for tx-state leak, mutating reads, or non-deterministic ordering. |
| Duplicate line items after regen           | Carry-over logic double-inserted. Audit `RegenerateSettlement` MANUAL preservation block. |
| Line-item count grows each regen           | Same as above — `addConfigFees` or absorption running twice. |
| Net drifts but total stable                | `DepositApp` policy or override key mapping mismatch. Check `PruneStaleOverrides`. |
