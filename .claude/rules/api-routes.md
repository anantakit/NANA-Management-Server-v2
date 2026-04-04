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
