// Payment Destination Routing — Epic Smoke
// ----------------------------------------------------------------------
// Verifies end-to-end routing snapshot behavior from real operator flows.
// Run ONLY against the local dev stack (backend :8080, frontend :3001).
//
// Scenarios:
//   #2/#5  No Config / Legacy  — null snapshot → delivery blocked ("ส่งไม่ได้")
//   #4     Rule Priority       — Override > Range > Default via API verification
//   #1     Happy Path          — finalized bill with snapshot → delivery enabled
//   #3     Snapshot Immutability — rule change doesn't retroactively change snapshot
//
// Pre-state (after cleanup + seed + reset-base-bills):
//   A101–A104, A107 → FINALIZED, null payment_bank_name  (used for #2/#5 UI check)
//   A205, A207, B101 → READY bucket; any existing MONTHLY bill is voided before re-generate
//   0 bank accounts, 0 routing rules in นานาคอร์ท
//
// Screenshots: /tmp/routing-smoke-*.png   exit(1) on any assertion failure.

const { chromium } = require('playwright')

const BACKEND = 'http://localhost:8080'
const FRONTEND = 'http://localhost:3001'
const ADMIN_USER = 'admin'
const ADMIN_PASS_FRESH = 'admin123'
const ADMIN_PASS_POST = 'admin1234'

const APARTMENT_NAME = 'นานาคอร์ท'
const BILLING_MONTH = '2026-06'

// Room IDs (stable across seeds — seeded with fixed UUIDs)
// A205: READY after seed → void existing MONTHLY + re-generate to test RANGE rule (A200-A299)
// A207: READY after seed → void existing MONTHLY + re-generate to test OVERRIDE rule
// B101: READY after seed (cancelled move-out, still OCCUPIED) → for APARTMENT_DEFAULT rule
// We intentionally avoid A101-A107: those are managed by reset-base-bills and must stay null-bank
// for the Scenario #2/#5 delivery-page check.
const ROOM_A205 = '9053c83b-31a3-4395-afc1-dadc59bc4f44'
const ROOM_A207 = '77d6c1e7-ff46-433c-a8fe-c6051098f487'
const ROOM_B101 = 'c336358c-c528-47bb-b649-19e5cbb5f922'

// ── helpers ──────────────────────────────────────────────────────────────────

async function postDev(path) {
  const res = await fetch(`${BACKEND}/api/v1/dev${path}`, { method: 'POST' })
  if (!res.ok) throw new Error(`POST /api/v1/dev${path} → HTTP ${res.status}`)
  const body = await res.json()
  if (body.status !== 'success') throw new Error(`POST /api/v1/dev${path} → ${JSON.stringify(body)}`)
}

// Get a fresh admin bearer token via username/password.
async function getToken() {
  const res = await fetch(`${BACKEND}/api/v1/auth/login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username: ADMIN_USER, password: ADMIN_PASS_POST }),
  })
  if (!res.ok) throw new Error(`login failed: HTTP ${res.status}`)
  const body = await res.json()
  const token = body?.data?.access_token
  if (!token) throw new Error(`no access_token in login response`)
  return token
}

async function apiGet(token, path) {
  const res = await fetch(`${BACKEND}/api/v1${path}`, {
    headers: { Authorization: `Bearer ${token}` },
  })
  if (!res.ok) throw new Error(`GET ${path} → HTTP ${res.status}`)
  return res.json()
}

async function apiPost(token, path, body) {
  const res = await fetch(`${BACKEND}/api/v1${path}`, {
    method: 'POST',
    headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  const data = await res.json()
  if (!res.ok) throw new Error(`POST ${path} → HTTP ${res.status}: ${JSON.stringify(data)}`)
  return data
}

async function apiDelete(token, path) {
  const res = await fetch(`${BACKEND}/api/v1${path}`, {
    method: 'DELETE',
    headers: { Authorization: `Bearer ${token}` },
  })
  const data = await res.json().catch(() => ({}))
  if (!res.ok) throw new Error(`DELETE ${path} → HTTP ${res.status}: ${JSON.stringify(data)}`)
  return data
}

async function apiPut(token, path, body) {
  const res = await fetch(`${BACKEND}/api/v1${path}`, {
    method: 'PUT',
    headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  const data = await res.json()
  if (!res.ok) throw new Error(`PUT ${path} → HTTP ${res.status}: ${JSON.stringify(data)}`)
  return data
}

async function apiPatch(token, path, body) {
  const res = await fetch(`${BACKEND}/api/v1${path}`, {
    method: 'PATCH',
    headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' },
    body: body ? JSON.stringify(body) : undefined,
  })
  const data = await res.json()
  if (!res.ok) throw new Error(`PATCH ${path} → HTTP ${res.status}: ${JSON.stringify(data)}`)
  return data
}

// Find the apartment ID for นานาคอร์ท from the rooms API response.
async function getApartmentID(token) {
  // Infer from any room's apartment_id
  const body = await apiGet(token, `/apartments/${APT_ID}/rooms?limit=1`)
  return body.data?.[0]?.apartment_id
}

// Hard-code the apartment ID we verified earlier in the session.
// Stable across seeds (apartments have fixed IDs in seed data).
const APT_ID = 'afb9a3a2-f792-4dbb-9bf0-95a56346e934'

function assert(cond, msg) {
  if (!cond) throw new Error(`ASSERTION FAILED: ${msg}`)
}

async function login(page) {
  await page.goto(`${FRONTEND}/login`)
  await page.fill('input[name="username"]', ADMIN_USER)
  await page.fill('input[name="password"]', ADMIN_PASS_FRESH)
  await page.click('button[type="submit"]')
  await page.waitForTimeout(1200)
  if (page.url().includes('/login')) {
    await page.fill('input[name="username"]', ADMIN_USER)
    await page.fill('input[name="password"]', ADMIN_PASS_POST)
    await page.click('button[type="submit"]')
  }
  await page.waitForLoadState('networkidle')
  if (page.url().includes('/change-password')) {
    await page.fill('input[name="new_password"]', ADMIN_PASS_POST)
    await page.fill('input[name="confirm_password"]', ADMIN_PASS_POST)
    await page.click('button[type="submit"]')
    await page.waitForLoadState('networkidle')
  }
  await page.waitForFunction(() => !window.location.pathname.includes('/login'), { timeout: 10000 })
}

async function selectApartment(page, name) {
  const trigger = page.locator('[data-test="apartment-selector-trigger"]')
  await trigger.scrollIntoViewIfNeeded()
  await trigger.click()
  await page.waitForTimeout(200)
  await page
    .locator('[data-test="apartment-selector-panel"] [data-test="apartment-selector-option"]', { hasText: name })
    .first()
    .click()
  await page.waitForTimeout(400)
}

// ── main ─────────────────────────────────────────────────────────────────────

;(async () => {
  console.log('🧪 payment-destination-routing-smoke  billing_month=' + BILLING_MONTH)

  // Fresh state: wipes DB → re-seeds → resets A101-A107 to FINALIZED
  await postDev('/smoke/cleanup')
  await postDev('/smoke/seed')
  await postDev('/smoke/reset-base-bills')
  console.log('  seed complete ✅')

  const browser = await chromium.launch({
    headless: process.env.SMOKE_HEADLESS === '1',
    slowMo: process.env.SMOKE_HEADLESS === '1' ? 0 : 100,
  })
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  const page = await ctx.newPage()

  try {
    await login(page)
    console.log('  login ✅')

    // Get fresh API token (browser session and API token are independent)
    const token = await getToken()

    // ── Pre-test cleanup (idempotency) ───────────────────────────────────────
    // cleanup+seed does NOT wipe bank accounts, routing rules, or bills generated
    // by prior routing-smoke runs. Clear them all so Scenario #2/#5 sees a clean
    // no-config delivery page.

    // 1) Delete leftover routing rules + bank accounts so rule-creation won't 409.
    {
      const rules = (await apiGet(token, `/apartments/${APT_ID}/payment-destination-rules`)).data ?? []
      for (const rule of rules) {
        await apiDelete(token, `/apartments/${APT_ID}/payment-destination-rules/${rule.id}`)
      }
      const accounts = (await apiGet(token, `/apartments/${APT_ID}/bank-accounts`)).data ?? []
      for (const acct of accounts) {
        await apiDelete(token, `/apartments/${APT_ID}/bank-accounts/${acct.id}`)
      }
      console.log(`  Cleared ${rules.length} routing rule(s) + ${accounts.length} bank account(s) from prior run`)
    }

    // 2) Void any active MONTHLY bills for A205/A207/B101 left by prior routing runs.
    //    These rooms' bills have frozen bank names → they would show "เตรียมส่ง" on the
    //    delivery page and break the Scenario #2/#5 "0 เตรียมส่ง" assertion.
    //    Only MONTHLY bills are voided — SETTLEMENT bills for those rooms stay intact.
    console.log('\n🔧 Voiding leftover MONTHLY bills for A205, A207, B101')
    {
      const bills2606 = (await apiGet(token, `/bills?apartment_id=${APT_ID}&billing_month=${BILLING_MONTH}&limit=100`)).data ?? []
      const voidTargets = ['A205', 'A207', 'B101']
      for (const roomNum of voidTargets) {
        const bill = bills2606.find(
          b => b.room_number === roomNum && b.bill_type === 'MONTHLY' && b.status !== 'VOID'
        )
        if (!bill) {
          console.log(`  ${roomNum}: no active MONTHLY bill (already voided or never created)`)
          continue
        }
        await apiPatch(token, `/bills/${bill.id}/void`, { reason: 'routing-smoke-test-setup' })
        console.log(`  ${roomNum} bill ${bill.id.slice(0, 8)}... voided ✅`)
      }
    }

    // ── Scenario #2 / #5 — No Config + Legacy Bills (UI) ─────────────────────
    console.log('\n🧪 #2 / #5 — No Config / Legacy Bills')
    console.log('  Expected: null snapshot → "ส่งไม่ได้" badge + "ยังไม่ได้กำหนดบัญชีรับเงิน" text')

    await page.goto(`${FRONTEND}/bills/delivery`, { waitUntil: 'networkidle' })
    await selectApartment(page, APARTMENT_NAME)
    await page.waitForTimeout(1200)
    await page.screenshot({ path: '/tmp/routing-smoke-2-delivery-no-config.png', fullPage: false })

    // A101–A104/A107 are FINALIZED with null bank (from reset-base-bills).
    // A205/A207/B101 bills were just voided — they don't appear on the delivery page.
    // Verify that at least one row shows "ส่งไม่ได้" badge (the <span> with that text).
    const noDestBadge = page.locator('span', { hasText: 'ส่งไม่ได้' }).first()
    await noDestBadge.waitFor({ state: 'visible', timeout: 8000 })
    console.log('  "ส่งไม่ได้" badge visible on delivery page ✅')

    // Verify the warning text "ยังไม่ได้กำหนดบัญชีรับเงิน" appears in a row
    const warningText = page.locator('p', { hasText: 'ยังไม่ได้กำหนดบัญชีรับเงิน' }).first()
    await warningText.waitFor({ state: 'visible', timeout: 5000 })
    console.log('  "ยังไม่ได้กำหนดบัญชีรับเงิน" warning text visible ✅')

    // Verify "เตรียมส่ง" button is NOT present for any row (all visible bills have null bank)
    const prepareBtns = page.locator('button', { hasText: 'เตรียมส่ง' })
    const prepareBtnCount = await prepareBtns.count()
    assert(prepareBtnCount === 0, `expected 0 "เตรียมส่ง" buttons, got ${prepareBtnCount}`)
    console.log('  "เตรียมส่ง" button absent for all rows (0 buttons) ✅')

    await page.setViewportSize({ width: 375, height: 812 })
    await page.waitForTimeout(400)
    await page.screenshot({ path: '/tmp/routing-smoke-2-mobile.png', fullPage: false })
    console.log('  mobile screenshot saved')
    await page.setViewportSize({ width: 1440, height: 900 })
    console.log('  ✅ Scenario #2/#5 PASS')

    // ── API Setup: Create Bank Accounts + Routing Rules ───────────────────────
    console.log('\n🔧 Setting up bank accounts and routing rules via API')

    // Account A — กสิกรไทย (apartment default)
    const bankA = await apiPost(token, `/apartments/${APT_ID}/bank-accounts`, {
      bank_name: 'กสิกรไทย',
      account_name: 'บริษัท นานาคอร์ท จำกัด',
      account_number: '012-3-45678-9',
      is_primary: true,
    })
    const accountA = bankA.data
    console.log(`  Account A created: ${accountA.bank_name} ${accountA.account_number} (id ${accountA.id})`)

    // Account B — ไทยพาณิชย์ (range A101-A120)
    const bankB = await apiPost(token, `/apartments/${APT_ID}/bank-accounts`, {
      bank_name: 'ไทยพาณิชย์',
      account_name: 'บริษัท นานาคอร์ท จำกัด',
      account_number: '111-2-22222-2',
      is_primary: false,
    })
    const accountB = bankB.data
    console.log(`  Account B created: ${accountB.bank_name} ${accountB.account_number} (id ${accountB.id})`)

    // Account C — กรุงเทพ (override A103)
    const bankC = await apiPost(token, `/apartments/${APT_ID}/bank-accounts`, {
      bank_name: 'กรุงเทพ',
      account_name: 'บริษัท นานาคอร์ท จำกัด',
      account_number: '333-4-55555-6',
      is_primary: false,
    })
    const accountC = bankC.data
    console.log(`  Account C created: ${accountC.bank_name} ${accountC.account_number} (id ${accountC.id})`)

    // Rule 1: APARTMENT_DEFAULT → Account A
    const ruleDefault = await apiPost(token, `/apartments/${APT_ID}/payment-destination-rules`, {
      bank_account_id: accountA.id,
      rule_type: 'APARTMENT_DEFAULT',
    })
    console.log(`  Rule DEFAULT → Account A created (id ${ruleDefault.data.id})`)

    // Rule 2: ROOM_RANGE A200-A299 → Account B (covers A205; A207 is overridden below)
    const ruleRange = await apiPost(token, `/apartments/${APT_ID}/payment-destination-rules`, {
      bank_account_id: accountB.id,
      rule_type: 'ROOM_RANGE',
      range_start: 'A200',
      range_end: 'A299',
    })
    console.log(`  Rule RANGE A200-A299 → Account B created (id ${ruleRange.data.id})`)

    // Rule 3: ROOM_OVERRIDE A207 → Account C (beats the A200-A299 range for A207)
    const ruleOverride = await apiPost(token, `/apartments/${APT_ID}/payment-destination-rules`, {
      bank_account_id: accountC.id,
      rule_type: 'ROOM_OVERRIDE',
      room_number: 'A207',
    })
    console.log(`  Rule OVERRIDE A207 → Account C created (id ${ruleOverride.data.id})`)

    // ── Scenario #4 — Rule Priority (API) ────────────────────────────────────
    console.log('\n🧪 #4 — Rule Priority: Override > Range > Default')

    // Generate bills for A207 (override), A205 (range), B101 (default).
    // All three had their MONTHLY bills voided above — now clean for re-generation.
    const genResult = await apiPost(token, `/billing-reconciliation/generate`, {
      apartment_id: APT_ID,
      billing_month: BILLING_MONTH,
      room_ids: [ROOM_A207, ROOM_A205, ROOM_B101],
    })

    console.log(`  Generate result: success=${genResult.data.success_count} skipped=${genResult.data.skipped_count} failed=${genResult.data.failed_count}`)

    const items = genResult.data.items
    const a207Item = items.find(i => i.room_id === ROOM_A207)
    const a205Item = items.find(i => i.room_id === ROOM_A205)
    const b101Item = items.find(i => i.room_id === ROOM_B101)

    assert(a207Item?.result === 'SUCCESS', `A207 generate expected SUCCESS, got ${a207Item?.result} (skip: ${a207Item?.skip_reason})`)
    assert(a205Item?.result === 'SUCCESS', `A205 generate expected SUCCESS, got ${a205Item?.result} (skip: ${a205Item?.skip_reason})`)
    assert(b101Item?.result === 'SUCCESS', `B101 generate expected SUCCESS, got ${b101Item?.result} (skip: ${b101Item?.skip_reason})`)

    const billIdA207 = a207Item.bill_id
    const billIdA205 = a205Item.bill_id
    const billIdB101 = b101Item.bill_id
    console.log(`  A207 bill: ${billIdA207}`)
    console.log(`  A205 bill: ${billIdA205}`)
    console.log(`  B101 bill: ${billIdB101}`)

    const billA207 = (await apiGet(token, `/bills/${billIdA207}`)).data
    const billA205 = (await apiGet(token, `/bills/${billIdA205}`)).data
    const billB101 = (await apiGet(token, `/bills/${billIdB101}`)).data

    // A207 — Override A207 → Account C (กรุงเทพ) — Override beats Range A200-A299 + Default
    console.log(`  A207 snapshot: bank=${billA207.payment_bank_name} acct=${billA207.payment_account_number}`)
    assert(
      billA207.payment_bank_name === accountC.bank_name,
      `A207: expected bank="${accountC.bank_name}" (Override C), got "${billA207.payment_bank_name}"`
    )
    assert(
      billA207.payment_account_number === accountC.account_number,
      `A207: expected acct="${accountC.account_number}", got "${billA207.payment_account_number}"`
    )
    console.log('  A207 → Account C (Override wins over Range A200-A299 + Default) ✅')

    // A205 — Range A200-A299 → Account B (ไทยพาณิชย์) — Range beats Default, no override for A205
    console.log(`  A205 snapshot: bank=${billA205.payment_bank_name} acct=${billA205.payment_account_number}`)
    assert(
      billA205.payment_bank_name === accountB.bank_name,
      `A205: expected bank="${accountB.bank_name}" (Range B), got "${billA205.payment_bank_name}"`
    )
    assert(
      billA205.payment_account_number === accountB.account_number,
      `A205: expected acct="${accountB.account_number}", got "${billA205.payment_account_number}"`
    )
    console.log('  A205 → Account B (Range A200-A299 wins over Default) ✅')

    // B101 — Default → Account A (กสิกรไทย) — no range or override matches B101 (building B)
    console.log(`  B101 snapshot: bank=${billB101.payment_bank_name} acct=${billB101.payment_account_number}`)
    assert(
      billB101.payment_bank_name === accountA.bank_name,
      `B101: expected bank="${accountA.bank_name}" (Default A), got "${billB101.payment_bank_name}"`
    )
    assert(
      billB101.payment_account_number === accountA.account_number,
      `B101: expected acct="${accountA.account_number}", got "${billB101.payment_account_number}"`
    )
    console.log('  B101 → Account A (Default — no Range or Override matches building B) ✅')
    console.log('  ✅ Scenario #4 PASS — Override > Range > Default hierarchy verified')

    // ── Scenario #1 — Happy Path (UI) ────────────────────────────────────────
    console.log('\n🧪 #1 — Happy Path: finalized bill with snapshot → delivery enabled')

    // Finalize B101's bill so it appears on the delivery page (FINALIZED only)
    await apiPatch(token, `/bills/${billIdB101}/finalize`, null)
    console.log(`  B101 bill finalized ✅`)

    // Reload delivery page — new FINALIZED bill with bank snapshot should appear
    await page.goto(`${FRONTEND}/bills/delivery`, { waitUntil: 'networkidle' })
    await selectApartment(page, APARTMENT_NAME)
    await page.waitForTimeout(1200)
    await page.screenshot({ path: '/tmp/routing-smoke-1-delivery-with-config.png', fullPage: false })

    // B101 row should show "เตรียมส่ง" button (not "ส่งไม่ได้")
    const prepareBtn = page.locator('button', { hasText: 'เตรียมส่ง' }).first()
    await prepareBtn.waitFor({ state: 'visible', timeout: 8000 })
    console.log('  "เตรียมส่ง" button visible for B101 (has payment_bank_name snapshot) ✅')

    // Confirm "ส่งไม่ได้" badge also still appears — A102/A104/A107 still have null snapshots
    const noDestBadge2 = page.locator('span', { hasText: 'ส่งไม่ได้' }).first()
    await noDestBadge2.waitFor({ state: 'visible', timeout: 5000 })
    console.log('  "ส่งไม่ได้" still visible for no-snapshot rows (coexistence verified) ✅')

    await page.setViewportSize({ width: 375, height: 812 })
    await page.waitForTimeout(400)
    await page.screenshot({ path: '/tmp/routing-smoke-1-mobile.png', fullPage: false })
    console.log('  mobile screenshot saved')
    await page.setViewportSize({ width: 1440, height: 900 })
    console.log('  ✅ Scenario #1 PASS')

    // ── Scenario #3 — Snapshot Immutability (API) ────────────────────────────
    console.log('\n🧪 #3 — Snapshot Immutability: rule change must not alter existing bill')

    // Change APARTMENT_DEFAULT rule to point to Account C (not A)
    await apiPut(token, `/apartments/${APT_ID}/payment-destination-rules/${ruleDefault.data.id}`, {
      bank_account_id: accountC.id,
    })
    console.log(`  APARTMENT_DEFAULT rule updated to Account C (${accountC.bank_name})`)

    // Re-fetch B101's bill — snapshot must still show Account A (frozen at DRAFT time)
    const billB101Refetch = (await apiGet(token, `/bills/${billIdB101}`)).data
    console.log(`  B101 re-fetched: bank=${billB101Refetch.payment_bank_name} acct=${billB101Refetch.payment_account_number}`)

    assert(
      billB101Refetch.payment_bank_name === accountA.bank_name,
      `Immutability violated: B101 expected bank="${accountA.bank_name}", got "${billB101Refetch.payment_bank_name}"`
    )
    assert(
      billB101Refetch.payment_account_number === accountA.account_number,
      `Immutability violated: B101 expected acct="${accountA.account_number}", got "${billB101Refetch.payment_account_number}"`
    )
    console.log('  B101 snapshot unchanged after rule update (still Account A = กสิกรไทย) ✅')

    // Cross-check: A205 was generated with Range A200-A299 → Account B.
    // Changing DEFAULT rule (A→C) must not affect Range-matched bills.
    const billA205Refetch = (await apiGet(token, `/bills/${billIdA205}`)).data
    assert(
      billA205Refetch.payment_bank_name === accountB.bank_name,
      `Immutability violated: A205 expected bank="${accountB.bank_name}" (Range, unchanged), got "${billA205Refetch.payment_bank_name}"`
    )
    console.log('  A205 snapshot also unchanged after default-rule update (still Account B = ไทยพาณิชย์) ✅')
    console.log('  ✅ Scenario #3 PASS — payment snapshots are immutable once frozen')

    // ── Summary ───────────────────────────────────────────────────────────────
    console.log('\n════════════════════════════════════════════════')
    console.log('✅ ALL SCENARIOS PASS')
    console.log('  #1 Happy Path             PASS')
    console.log('  #2 No Config              PASS')
    console.log('  #3 Snapshot Immutability  PASS')
    console.log('  #4 Rule Priority          PASS')
    console.log('  #5 Legacy Bills           PASS  (covered by #2: A102/A104/A107 null-snapshot)')
    console.log('')
    console.log('Screenshots saved to /tmp/routing-smoke-*.png')
    console.log('════════════════════════════════════════════════')

    await page.screenshot({ path: '/tmp/routing-smoke-final.png', fullPage: false })
  } catch (err) {
    await page.screenshot({ path: '/tmp/routing-smoke-FAIL.png', fullPage: false }).catch(() => {})
    console.error('\n❌ SMOKE TEST FAILED:', err.message)
    await browser.close()
    process.exit(1)
  }

  await browser.close()
})()
