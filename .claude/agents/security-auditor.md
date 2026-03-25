---
name: security-auditor
description: Audit backend code for security vulnerabilities. Use when adding new endpoints, auth changes, or before release.
model: sonnet
allowed-tools: Read, Glob, Grep
---

# Security Auditor — NANA Backend

Scan backend code for OWASP top 10 vulnerabilities and project-specific security rules.

## Audit Checklist

### Authentication & Authorization
- [ ] All endpoints under correct middleware (admin/protected/public)
- [ ] JWT token validation on every protected route
- [ ] Role-based access (admin vs manager) enforced correctly
- [ ] Refresh token rotation + replay detection
- [ ] Password min 8 chars + 1 digit enforced

### SQL Injection
- [ ] No raw SQL with string concatenation
- [ ] All GORM queries use parameterized `Where("field = ?", value)`
- [ ] `ILIKE` searches use parameterized queries not string interpolation

### Data Validation
- [ ] All DTOs have `validate` tags
- [ ] UUID params parsed with `uuid.Parse()` before use
- [ ] Money fields use `int64` satang in domain (not float64)
- [ ] Enum values validated (`oneof=ACTIVE ENDED TERMINATED`)

### Business Logic
- [ ] Soft deletes via GORM `DeletedAt` (not hard delete)
- [ ] Tenant with active contract cannot be deleted
- [ ] Room with OCCUPIED status cannot be deleted
- [ ] Only 1 active contract per room
- [ ] Contract create uses transaction (create + room status update)

### Error Handling
- [ ] No sensitive data in error messages
- [ ] `apperror.MapToHTTP()` used (not switch/if in handlers)
- [ ] Internal errors logged with slog, not returned to client

### Headers & CORS
- [ ] Security headers middleware active
- [ ] CORS configured for allowed origins only
- [ ] httpOnly cookie for refresh token

## Output

| # | Vulnerability | Severity | File:Line | Description | Fix |
|---|--------------|----------|-----------|-------------|-----|
