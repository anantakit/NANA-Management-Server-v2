---
description: Quick reference for all API routes, methods, auth requirements
paths:
  - "cmd/**"
  - "internal/*/handler.go"
  - "internal/*/bank_account_handler.go"
---

# API Routes

> Update this file every time a new route is added.

## Auth (`/api/v1/auth`)
| Method | Path | Auth | Description |
|--------|------|------|-------------|
| POST | `/login` | Public | Login → access token + refresh cookie |
| POST | `/refresh` | Public (cookie) | Rotate refresh token → new access token |
| POST | `/logout` | JWT | Revoke token family, clear cookie |
| POST | `/change-password` | JWT | Change password, revoke all tokens |

## Apartments (`/api/v1/apartments`) — Admin only
| Method | Path | Description |
|--------|------|-------------|
| GET | `/` | List all apartments (ordered by display_order) |
| GET | `/:id` | Get apartment by ID |
| POST | `/` | Create apartment (name must be unique) |
| PUT | `/:id` | Update apartment (partial update via nullable fields) |

## Bank Accounts (`/api/v1/apartments/:id/bank-accounts`) — Admin only
| Method | Path | Description |
|--------|------|-------------|
| GET | `/` | List bank accounts for apartment |
| POST | `/` | Add bank account (is_primary auto-clears others) |
| PUT | `/:accountId` | Update bank account |
| DELETE | `/:accountId` | Soft delete bank account |

## Rooms (`/api/v1/apartments/:id/rooms`) — Admin only
| Method | Path | Description |
|--------|------|-------------|
| GET | `/` | List rooms with active contract summary (LEFT JOIN contracts+tenants) |
| GET | `/:roomId` | Get room with active contract summary |
| POST | `/` | Create room (number unique per apartment) |
| PUT | `/:roomId` | Update room (status: VACANT↔MAINTENANCE only, OCCUPIED blocked) |
| DELETE | `/:roomId` | Soft delete room (blocked if OCCUPIED) |

Room response includes `active_contract`: contract_id, tenant_id, tenant_name, tenant_phone, monthly_rent, deposit_amount, electricity_rate_per_unit, water_rate_per_unit, start_date, min_months

## Tenants (`/api/v1/tenants`) — Admin only
| Method | Path | Description |
|--------|------|-------------|
| GET | `/` | List tenants (paginated, search by name/idCard/phone) |
| GET | `/:id` | Get tenant by ID |
| POST | `/` | Create tenant (idCard 13 digits, unique) |
| PUT | `/:id` | Update tenant (idCard uniqueness checked) |
| DELETE | `/:id` | Soft delete tenant (blocked if active contract) |

## Contracts (`/api/v1/contracts`) — Admin only
| Method | Path | Description |
|--------|------|-------------|
| GET | `/` | List contracts (paginated, filter by status/apartment_id, search tenant/room) |
| GET | `/:contractId` | Get contract with tenant + room + apartment info (JOIN) |
| POST | `/` | Create contract (room must be VACANT, auto-sets room OCCUPIED in transaction) |
| PUT | `/:contractId` | Update contract (ACTIVE only: rent, deposit, rates, min_months) |
| DELETE | `/:contractId` | Soft delete contract (blocked if ACTIVE) |

## Move-Out Notices (`/api/v1/move-out-notices`) — Admin only
| Method | Path | Description |
|--------|------|-------------|
| GET | `/` | List move-out notices (paginated, filter by status/apartment_id, search tenant/room) |
| GET | `/queue` | Queue view: 4 sections (pending_meter/settlement/payment/ready_to_close) + summary + history. COMPLETED+UNSETTLED notices route into `pending_payment` (financial backlog stays surfaced); urgency counts skip COMPLETED rows |
| GET | `/:id` | Get move-out notice with tenant + room + apartment info (JOIN) |
| POST | `/` | Create move-out notice (contract must be ACTIVE, one active per contract) → PENDING_METER |
| PUT | `/:id` | Update move-out notice (PENDING_METER only: scheduled_move_out_date, note) |
| PATCH | `/:id/actual-date` | Set actual move-out date (any non-terminal status) |
| POST | `/:id/cancel` | Cancel (PENDING_METER/PENDING_SETTLEMENT → CANCELLED, reverts EXIT meter) |
| POST | `/:id/record-exit-meter` | Record EXIT meter (PENDING_METER → PENDING_SETTLEMENT) |
| POST | `/:id/update-exit-meter` | Update EXIT meter (PENDING_SETTLEMENT stays; PENDING_PAYMENT voids draft → PENDING_SETTLEMENT) |
| GET | `/:id/settlement-preview` | Preview settlement (non-persisting; query `rent_mode`) |
| POST | `/:id/generate-settlement` | Generate DRAFT settlement bill (PENDING_SETTLEMENT, attaches draft) |
| POST | `/:id/finalize-settlement` | Finalize settlement DRAFT → FINALIZED (PENDING_SETTLEMENT → PENDING_PAYMENT) |
| POST | `/:id/regenerate-settlement` | Void old draft + create new (PENDING_SETTLEMENT stays, draft re-attached) |
| POST | `/:id/record-payment` | Record payment outcome + method (PENDING_PAYMENT → READY_TO_CLOSE; also accepts READY_TO_CLOSE for back-fill / correction; Phase-2 also accepts COMPLETED + nil for post-close back-fill — status stays COMPLETED, no contract reopen. ZERO_BALANCE normalizes method to nil) |
| POST | `/:id/skip-payment` | Defer payment without outcome (PENDING_PAYMENT → READY_TO_CLOSE; idempotent — no-op if already past PENDING_PAYMENT) |
| POST | `/:id/reopen` | Reopen for correction (READY_TO_CLOSE → PENDING_PAYMENT, **clears payment_outcome + payment_note**) |
| POST | `/:id/close` | Close move-out (READY_TO_CLOSE → COMPLETED, tx: contract ENDED + room VACANT; requires `payment_outcome != null`) |
| POST | `/:id/close-with-unsettled` | Phase-2 explicit "ปิดงาน (ยังไม่ชำระ)" path. Accepts PENDING_PAYMENT / READY_TO_CLOSE / COMPLETED — all with `payment_outcome == null`. Transitions to COMPLETED + tx: contract ENDED + room VACANT (idempotent on COMPLETED — no-op, no side effects). Settled notices rejected (must use `/close`); preserves `payment_outcome = null` for post-close back-fill via `/record-payment` |

## Billing (`/api/v1/bills`) — Admin only
| Method | Path | Description |
|--------|------|-------------|
| GET | `/` | List bills (paginated, filter by contract_id/apartment_id/month/status/bill_type, search) |
| GET | `/:id` | Get bill with tenant/room/apartment relations + line items |
| POST | `/monthly` | Create single monthly bill (FINALIZED) |
| POST | `/settlement` | Create settlement bill (DRAFT) |
| POST | `/batch-monthly` | Trigger batch monthly generation (compute snapshots, no bills yet) |
| PATCH | `/:id/finalize` | Transition DRAFT → FINALIZED |
| PATCH | `/:id/void` | Void bill (DRAFT/FINALIZED → VOID, requires reason) |
| PATCH | `/:id/paid` | Mark bill as paid (FINALIZED → PAID) |
| GET | `/batches` | List batch runs (paginated, filter by apartment_id/billing_month/status) |
| GET | `/batches/:id` | Get batch header with summary counts |
| GET | `/batches/:id/items` | Get batch items with computed snapshots |
| POST | `/batches/:id/commit` | Commit batch: create FINALIZED bills from snapshots (idempotent, per-item tx) |
