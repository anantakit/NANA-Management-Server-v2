// Monthly Bills Workspace — Meter-Then-Generate Smoke
// ----------------------------------------------------------------------
// Repurposed 2026-06-14: legacy BillBatchReviewPage retired.
// Pins the "unblock → finalize → generate" cycle in the new workspace:
//
// Two scenarios:
//   A. Meter → READY — D105 starts as ACTION_REQUIRED/MISSING_METER_READING
//      (data-action="open-meter"). Operator records the meter via
//      MonthlyMeterDrawer; row detaches from open-meter and rebuckets as
//      a non-actionable READY div. Finalize CTA count stays at 2
//      (D105 is READY, not DRAFT — only E101+E102 are DRAFT from seed).
//
//   B. Finalize → generate → DRAFT — operator clicks "ยืนยันบิล 2 ใบ" →
//      FinalizeAllModal opens → confirms "ออกบิล" → E101+E102 finalize →
//      finalize CTA disappears and generate CTA "ออกบิล 1 ห้อง" appears
//      (D105 is now the lone READY room) → operator clicks generate →
//      toast "สร้างบิลแล้ว 1 ห้อง" → D105 row becomes data-action="edit-draft".
//
// This pins the CTA state machine (finalize ↔ generate switch at draftCount=0)
// and the full MISSING_METER → record → READY → generate → DRAFT cycle.
// Not covered by playlist-test-monthly-bills-workspace-smoke.js Scenario C
// which only asserts row detachment, not the subsequent CTA flip + generate.
//
// Pre-state (cleanup + seed):
//   D105 (TC28) → ACTIVE contract + no MONTHLY meter (no bill)
//   E101 (TC26) + E102 (TC27) → ACTIVE contract + DRAFT MONTHLY bill
//   A101–A107 → FINALIZED/PAID monthly via seedDevMonthlyBills
//
// Selector contract:
//   Rows:     [data-test="reconciliation-row"][data-action="..."][data-room-number="..."]
//   READY row (non-actionable): div[data-test="reconciliation-row"][data-room-number="..."]
//   Drawer:   [role="dialog"]
//   Finalize CTA: button matching /ยืนยันบิล \d+ ใบ/
//   FinalizeAllModal: [aria-labelledby="finalize-all-confirm-title"]
//   Generate CTA: button matching /ออกบิล \d+ ห้อง/
//
// Screenshots: /tmp/batch-replan-*.png  exit(1) on failure.

const { chromium } = require('playwright')

const BACKEND = 'http://localhost:8080'
const FRONTEND = 'http://localhost:3001'
const ADMIN_USER = 'admin'
const ADMIN_PASS_FRESH = 'admin123'
const ADMIN_PASS_POST = 'admin1234'

const APARTMENT_NAME = 'นานาคอร์ท'
const ROOM_METER = 'D105' // TC28 — ACTION_REQUIRED / MISSING_METER_READING

// Dynamic billing month — dev seed always creates for time.Now().UTC() month.
const BILLING_MONTH = (() => {
  const d = new Date()
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}`
})()

async function postDev(path) {
  const res = await fetch(`${BACKEND}/api/v1/dev${path}`, { method: 'POST' })
  if (!res.ok) throw new Error(`POST /api/v1/dev${path} → HTTP ${res.status}`)
  const body = await res.json()
  if (body.status !== 'success') throw new Error(`POST /api/v1/dev${path} → ${JSON.stringify(body)}`)
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

async function navigateToWorkspace(page) {
  await page.goto(`${FRONTEND}/monthly-bills/${BILLING_MONTH}`, { waitUntil: 'networkidle' })
  await page.waitForTimeout(800)
}

// Parse the first integer from a button's inner text.
async function parseCountFromButton(btn) {
  const text = (await btn.innerText()).trim()
  const m = text.match(/(\d+)/)
  if (!m) throw new Error(`Cannot parse count from button text: "${text}"`)
  return Number(m[1])
}

;(async () => {
  console.log('🧪 batch-replan smoke (meter → READY → finalize → generate)  billing_month=' + BILLING_MONTH)

  await postDev('/smoke/cleanup')
  await postDev('/smoke/seed')

  const browser = await chromium.launch({
    headless: process.env.SMOKE_HEADLESS === '1',
    slowMo: process.env.SMOKE_HEADLESS === '1' ? 0 : 100,
  })
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  const page = await ctx.newPage()

  try {
    await login(page)
    await selectApartment(page, APARTMENT_NAME)
    await navigateToWorkspace(page)

    // ── Scenario A: Record meter → D105 rebuckets to READY ───────────────
    console.log(`\n🧪 SCENARIO A — meter ห้อง ${ROOM_METER} → READY (non-actionable div)`)

    // D105 must start as open-meter (ACTION_REQUIRED / MISSING_METER_READING).
    const meterRow = page.locator(
      `[data-test="reconciliation-row"][data-action="open-meter"][data-room-number="${ROOM_METER}"]`,
    )
    await meterRow.waitFor({ state: 'visible', timeout: 8000 })
    console.log(`  ${ROOM_METER} row: data-action="open-meter" ✅`)

    // Finalize CTA must be visible with count = 2 (E101 + E102 DRAFT in seed).
    // The generate CTA is hidden while draftCount > 0 — this is the CTA state machine.
    const finalizeCta = page.getByRole('button', { name: /ยืนยันบิล \d+ ใบ/ }).first()
    await finalizeCta.waitFor({ state: 'visible', timeout: 8000 })
    const finalizeCountBefore = await parseCountFromButton(finalizeCta)
    if (finalizeCountBefore < 2) {
      throw new Error(`Expected ≥ 2 DRAFT bills in seed (E101+E102), got ${finalizeCountBefore}`)
    }
    console.log(`  finalize CTA: "ยืนยันบิล ${finalizeCountBefore} ใบ" (E101+E102 DRAFT) ✅`)

    // Open MonthlyMeterDrawer and record D105 meter.
    await meterRow.click()
    const drawer = page.locator('[role="dialog"]')
    await drawer.waitFor({ state: 'visible', timeout: 5000 })
    await drawer.locator('h2', { hasText: `บันทึกมิเตอร์ห้อง ${ROOM_METER}` }).waitFor({ timeout: 5000 })
    console.log(`  MonthlyMeterDrawer opened ✅`)

    await drawer.locator('[data-testid="monthly-meter-ready"]').waitFor({ timeout: 8000 })
    await drawer.locator('input[name="electricity_current"]').fill('100')
    await drawer.locator('input[name="water_current"]').fill('50')
    await page.waitForTimeout(200)
    await drawer.locator('button[type="submit"][form="monthly-meter-form"]').click()

    // D105 has no baseline so anomaly step shouldn't appear; confirm defensively.
    const confirmAnomaly = page.locator('[role="dialog"] button', { hasText: 'ยืนยัน' })
    if (await confirmAnomaly.isVisible().catch(() => false)) {
      console.log('  anomaly review step appeared — confirming')
      await confirmAnomaly.click()
    }

    await drawer.waitFor({ state: 'hidden', timeout: 8000 })
    console.log('  meter saved, drawer closed ✅')

    // D105 detaches from open-meter DOM → rebucketed.
    await page.waitForSelector(
      `[data-test="reconciliation-row"][data-action="open-meter"][data-room-number="${ROOM_METER}"]`,
      { state: 'detached', timeout: 10000 },
    )
    console.log(`  ${ROOM_METER} detached from open-meter DOM ✅`)

    // D105 should now appear as a non-actionable READY div (no data-action on element).
    // ReconciliationRow renders READY rooms as <div> (no click handler, no data-action).
    const d105ReadyDiv = page.locator(
      `div[data-test="reconciliation-row"][data-room-number="${ROOM_METER}"]`,
    )
    await d105ReadyDiv.waitFor({ state: 'visible', timeout: 8000 })
    console.log(`  ${ROOM_METER} row: non-actionable div (READY bucket) ✅`)

    // Finalize CTA count stays at finalizeCountBefore — D105 is READY, not DRAFT.
    const finalizeCountStill = await parseCountFromButton(finalizeCta)
    if (finalizeCountStill !== finalizeCountBefore) {
      throw new Error(
        `Finalize CTA count should still be ${finalizeCountBefore}, got ${finalizeCountStill}`,
      )
    }
    console.log(
      `  finalize CTA still "ยืนยันบิล ${finalizeCountStill} ใบ" — READY rows do not inflate draft count ✅`,
    )

    await page.screenshot({ path: '/tmp/batch-replan-A-ready.png', fullPage: true })

    // ── Scenario B: Finalize → CTA flip → generate → D105 DRAFT ─────────
    console.log(`\n🧪 SCENARIO B — finalize ${finalizeCountBefore} DRAFTs → generate CTA → ${ROOM_METER} edit-draft`)

    // Click finalize CTA → FinalizeAllModal.
    await finalizeCta.click({ timeout: 5000 })
    const finalizeModal = page.locator('[aria-labelledby="finalize-all-confirm-title"]')
    await finalizeModal.waitFor({ state: 'visible', timeout: 5000 })
    console.log('  FinalizeAllModal opened ✅')

    // Confirm via the modal's primary "ออกบิล" button (exact — no numeric suffix).
    await finalizeModal.getByRole('button', { name: 'ออกบิล', exact: true }).click({ timeout: 5000 })

    // Modal closes once mutation resolves.
    await finalizeModal.waitFor({ state: 'hidden', timeout: 15000 })
    console.log('  finalization confirmed, modal closed ✅')

    // CTA state machine: finalize CTA disappears when reconciliation report
    // refetches with draftCount = 0. Wait for this transition — it proves the
    // report is fresh before we assert on row states.
    await finalizeCta.waitFor({ state: 'hidden', timeout: 15000 })
    console.log('  finalize CTA hidden (draftCount = 0, report refreshed) ✅')

    // Generate CTA appears once draftCount = 0 (toolbarRightOverride = undefined).
    const generateCta = page.getByRole('button', { name: /ออกบิล \d+ ห้อง/ }).first()
    await generateCta.waitFor({ state: 'visible', timeout: 10000 })

    await page.screenshot({ path: '/tmp/batch-replan-B-finalized.png', fullPage: true })

    const generateCount = await parseCountFromButton(generateCta)
    if (generateCount < 1) {
      throw new Error(`Expected ≥ 1 READY room after finalize (D105), got ${generateCount}`)
    }
    if (await generateCta.isDisabled()) {
      throw new Error('"ออกบิล N ห้อง" CTA should be enabled — D105 is READY')
    }
    console.log(`  CTA flipped: "ออกบิล ${generateCount} ห้อง" enabled (D105 READY) ✅`)

    // Generate → DRAFT bill for D105.
    await generateCta.click({ timeout: 5000 })
    await page
      .locator(`text=สร้างบิลแล้ว ${generateCount} ห้อง`)
      .first()
      .waitFor({ timeout: 10000 })
    console.log(`  toast: "สร้างบิลแล้ว ${generateCount} ห้อง" ✅`)

    await page
      .locator(`[data-test="reconciliation-row"][data-action="edit-draft"][data-room-number="${ROOM_METER}"]`)
      .waitFor({ state: 'visible', timeout: 10000 })
    console.log(`  ${ROOM_METER} row: data-action="edit-draft" (DRAFT bill created) ✅`)

    await page.screenshot({ path: '/tmp/batch-replan-B-d105-draft.png', fullPage: true })

    console.log('\n✅ batch-replan smoke PASS (meter → READY → finalize cycle → generate → draft)')
    console.log(`   billing_month=${BILLING_MONTH}`)
    console.log('   screenshots: /tmp/batch-replan-*.png')
  } catch (err) {
    console.error('\n❌ batch-replan smoke FAIL')
    console.error(err)
    await page.screenshot({ path: '/tmp/batch-replan-FAILURE.png', fullPage: true }).catch(() => {})
    process.exitCode = 1
  } finally {
    await browser.close()
  }
})()
