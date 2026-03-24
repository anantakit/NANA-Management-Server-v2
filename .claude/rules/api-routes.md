---
description: Quick reference for all API routes, methods, auth requirements
paths:
  - "cmd/**"
  - "internal/handler/**"
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
