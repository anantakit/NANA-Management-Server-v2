// Monthly Bills Workspace — Phase 1D behavior smoke
// ----------------------------------------------------------------------
// Replaces playwright-test-draft-bills-surface-smoke.js which pinned the
// now-deprecated DraftBillsSurface (surface-swap architecture, Lock 2).
// Phase 1D unified the workspace: single room-centric surface at
// /monthly-bills/:YYYY-MM, no content swap, row click = work item.
//
// Six assertions:
//   A. Page identity   — h1 "ออกบิลเดือน {month}" (workspace identity,
//                        not "กระทบยอดการออกบิล" or "บิลร่างเดือน")
//   B. DRAFT row click — E101 (TC26) → BillEditDrawer opens
//   C. Meter row click — D105 (TC28) → MonthlyMeterDrawer → save →
//                        row detaches from "open-meter" DOM node
//   D. Finalized row   — A101 → BillDrawer view (readonly) — no
//                        "ยืนยันรับชำระ" / "ยืนยันคืนเงิน" CTA visible
//   E. Filter chips    — "บิลร่าง" chip ≥ 2, "ออกบิลแล้ว" chip ≥ 5
//   F. Finalize CTA    — "ยืนยันบิลทั้งหมด N ใบ" visible + enabled,
//                        count matches DRAFT chip count
//
// Pre-state (guaranteed by cleanup + seed at test start):
//   TC26 (E101), TC27 (E102)  → ACTIVE contract + DRAFT MONTHLY bill
//   TC28 (D105)               → ACTIVE contract + no meter → ACTION_REQUIRED
//   A101–A107                 → FINALIZED/PAID monthly via seedDevMonthlyBills
//
// Selector contract:
//   Rows:   [data-test="reconciliation-row"][data-action="{action}"][data-room-number="{n}"]
//   Chips:  [role="radiogroup"][aria-label="กรองห้องตามสถานะ"] button[role="radio"]
//   Drawer: [role="dialog"]
//
// Screenshots: /tmp/workspace-smoke-*.png  exit(1) on failure.

const { chromium } = require('playwright')

const BACKEND = 'http://localhost:8080'
const FRONTEND = 'http://localhost:3001'
const ADMIN_USER = 'admin'
const ADMIN_PASS_FRESH = 'admin123'
const ADMIN_PASS_POST = 'admin1234'

const APARTMENT_NAME = 'นานาคอร์ท'
const ROOM_DRAFT = 'E101'    // TC26 — DRAFT MONTHLY, clean
const ROOM_METER = 'D105'    // TC28 — ACTION_REQUIRED / MISSING_METER_READING
const ROOM_FINAL = 'A101'    // seedDevMonthlyBills — FINALIZED

// Dynamic billing month so the test stays green when the calendar rolls.
// dev seed always creates bills for time.Now().UTC() billing month.
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

// Read the numeric count from inside a chip button. Chips render as:
// <button><span class="tabular-nums ...">N</span><span>label</span></button>
async function readChipCount(chip) {
  const countSpan = chip.locator('span.tabular-nums').first()
  const text = (await countSpan.innerText()).trim()
  // "999+" → 999, "2" → 2
  return Number(text.replace(/[^0-9]/g, ''))
}

;(async () => {
  console.log('🧪 monthly-bills-workspace smoke  billing_month=' + BILLING_MONTH)

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

    // ── A: Page identity ──────────────────────────────────────────────
    console.log('\n🧪 A — Page identity')

    // h1 must contain "ออกบิลเดือน" — unified workspace title.
    // Old DraftBillsSurface said "บิลร่างเดือน"; old audit said "กระทบยอดการออกบิล".
    const h1 = page.getByRole('heading', { level: 1, name: /ออกบิลเดือน/ })
    await h1.waitFor({ timeout: 8000 })
    const h1Text = (await h1.innerText()).trim()
    console.log(`  h1: "${h1Text}" ✅`)

    await page.screenshot({ path: '/tmp/workspace-smoke-A-identity.png', fullPage: true })

    // ── B: DRAFT row → BillEditDrawer ────────────────────────────────
    console.log(`\n🧪 B — DRAFT row click → BillEditDrawer (${ROOM_DRAFT})`)

    const draftRow = page.locator(
      `[data-test="reconciliation-row"][data-action="edit-draft"][data-room-number="${ROOM_DRAFT}"]`,
    )
    await draftRow.waitFor({ state: 'visible', timeout: 8000 })
    console.log(`  row found: data-action="edit-draft" data-room-number="${ROOM_DRAFT}" ✅`)

    await draftRow.click()

    const drawerB = page.locator('[role="dialog"]')
    await drawerB.waitFor({ state: 'visible', timeout: 5000 })
    // Title starts "กำลังโหลด..." while bill API resolves — wait for room number.
    await drawerB.locator('h2', { hasText: `ห้อง ${ROOM_DRAFT}` }).waitFor({ timeout: 8000 })
    console.log(`  BillEditDrawer opened: "ห้อง ${ROOM_DRAFT}" ✅`)

    await page.screenshot({ path: '/tmp/workspace-smoke-B-edit-drawer.png', fullPage: true })
    await page.keyboard.press('Escape')
    await drawerB.waitFor({ state: 'hidden', timeout: 3000 })
    console.log('  drawer closed cleanly ✅')

    // ── C: Meter row → MonthlyMeterDrawer → save → rebucket ──────────
    console.log(`\n🧪 C — Meter row click → MonthlyMeterDrawer → save (${ROOM_METER})`)

    const meterRow = page.locator(
      `[data-test="reconciliation-row"][data-action="open-meter"][data-room-number="${ROOM_METER}"]`,
    )
    await meterRow.waitFor({ state: 'visible', timeout: 8000 })
    console.log(`  row found: data-action="open-meter" data-room-number="${ROOM_METER}" ✅`)

    await meterRow.click()

    const drawerC = page.locator('[role="dialog"]')
    await drawerC.waitFor({ state: 'visible', timeout: 5000 })
    await drawerC.locator('h2', { hasText: `บันทึกมิเตอร์ห้อง ${ROOM_METER}` }).waitFor({ timeout: 5000 })
    console.log(`  MonthlyMeterDrawer opened: "บันทึกมิเตอร์ห้อง ${ROOM_METER}" ✅`)

    // Wait for form to be ready (skeleton gone)
    await drawerC.locator('[data-testid="monthly-meter-ready"]').waitFor({ timeout: 8000 })

    // D105 has no previous readings (createMissingMeterScenario = contract only).
    // elecPrev=0, waterPrev=0, no baseline → isAnomalous=false for any positive value.
    await drawerC.locator('input[name="electricity_current"]').fill('100')
    await drawerC.locator('input[name="water_current"]').fill('50')
    await page.waitForTimeout(200)

    await page.screenshot({ path: '/tmp/workspace-smoke-C-meter-filled.png', fullPage: true })

    // Submit via the form's save button
    await drawerC.locator('button[type="submit"][form="monthly-meter-form"]').click()

    // If anomaly review phase appears (shouldn't for D105 with no baseline), confirm it.
    const confirmAnomaly = page.locator('[role="dialog"] button', { hasText: 'ยืนยัน' })
    const isAnomalyStep = await confirmAnomaly.isVisible().catch(() => false)
    if (isAnomalyStep) {
      console.log('  anomaly review step appeared — confirming')
      await confirmAnomaly.click()
    }

    // Drawer closes on save success (onSaveSuccess → setActiveDrawer(null))
    await drawerC.waitFor({ state: 'hidden', timeout: 8000 })
    console.log('  meter saved, drawer closed ✅')

    // Reconciliation report invalidated → refetch in flight.
    // Wait for D105 to leave the "open-meter" DOM node (rebucketed to READY).
    await page.waitForSelector(
      `[data-test="reconciliation-row"][data-action="open-meter"][data-room-number="${ROOM_METER}"]`,
      { state: 'detached', timeout: 10000 },
    )
    console.log(`  ${ROOM_METER} row rebucketed (detached from open-meter DOM node) ✅`)

    await page.screenshot({ path: '/tmp/workspace-smoke-C-rebucketed.png', fullPage: true })

    // ── D: Finalized row → BillDrawer readonly ────────────────────────
    console.log(`\n🧪 D — Finalized row click → BillDrawer readonly (${ROOM_FINAL})`)

    const finalRow = page.locator(
      `[data-test="reconciliation-row"][data-action="view-bill"][data-room-number="${ROOM_FINAL}"]`,
    )
    await finalRow.waitFor({ state: 'visible', timeout: 8000 })
    console.log(`  row found: data-action="view-bill" data-room-number="${ROOM_FINAL}" ✅`)

    await finalRow.click()

    const drawerD = page.locator('[role="dialog"]')
    await drawerD.waitFor({ state: 'visible', timeout: 5000 })
    await drawerD.locator('h2', { hasText: `ห้อง ${ROOM_FINAL}` }).waitFor({ timeout: 8000 })
    console.log(`  BillDrawer opened: "ห้อง ${ROOM_FINAL}" ✅`)

    // readonly=true → no payment / correction CTAs in footer
    const paymentCta = drawerD.getByRole('button', { name: /ยืนยันรับชำระ|ยืนยันคืนเงิน|ยกเลิกแล้วออกใหม่/ })
    const paymentCtaVisible = await paymentCta.isVisible().catch(() => false)
    if (paymentCtaVisible) {
      throw new Error('BillDrawer shown in readonly mode but payment/correction CTA is visible')
    }
    console.log('  no payment CTA (readonly enforced) ✅')

    await page.screenshot({ path: '/tmp/workspace-smoke-D-bill-drawer.png', fullPage: true })
    await page.keyboard.press('Escape')
    await drawerD.waitFor({ state: 'hidden', timeout: 3000 })
    console.log('  drawer closed cleanly ✅')

    // ── E: Filter chips match counts ──────────────────────────────────
    console.log('\n🧪 E — Filter chips: DRAFT and FINALIZED counts')

    const chipGroup = page.locator('[role="radiogroup"][aria-label="กรองห้องตามสถานะ"]')
    await chipGroup.waitFor({ state: 'visible', timeout: 5000 })

    const draftChip = chipGroup.locator('button[role="radio"]', { hasText: 'บิลร่าง' })
    await draftChip.waitFor({ state: 'visible', timeout: 5000 })
    const draftChipCount = await readChipCount(draftChip)
    if (draftChipCount < 2) {
      throw new Error(`DRAFT chip count = ${draftChipCount}, expected ≥ 2 (E101+E102)`)
    }
    console.log(`  "บิลร่าง" chip: ${draftChipCount} ✅`)

    const finalChip = chipGroup.locator('button[role="radio"]', { hasText: 'ออกบิลแล้ว' })
    await finalChip.waitFor({ state: 'visible', timeout: 5000 })
    const finalChipCount = await readChipCount(finalChip)
    if (finalChipCount < 5) {
      throw new Error(`FINALIZED chip count = ${finalChipCount}, expected ≥ 5 (A101–A107)`)
    }
    console.log(`  "ออกบิลแล้ว" chip: ${finalChipCount} ✅`)

    // ── F: Finalize CTA ───────────────────────────────────────────────
    console.log('\n🧪 F — Finalize CTA: visible, enabled, count matches DRAFT chip')

    const finalizeCta = page.getByRole('button', { name: /ยืนยันบิล \d+ ใบ/ }).first()
    await finalizeCta.waitFor({ state: 'visible', timeout: 5000 })
    const ctaLabel = (await finalizeCta.innerText()).trim()

    // Extract N from "ยืนยันบิล N ใบ"
    const ctaCountMatch = ctaLabel.match(/(\d+)/)
    if (!ctaCountMatch) throw new Error(`Cannot parse count from CTA label: "${ctaLabel}"`)
    const ctaCount = Number(ctaCountMatch[1])

    if (ctaCount !== draftChipCount) {
      throw new Error(`CTA count ${ctaCount} ≠ DRAFT chip count ${draftChipCount}`)
    }
    console.log(`  CTA: "${ctaLabel}" — count matches DRAFT chip ✅`)

    const ctaDisabled = await finalizeCta.isDisabled()
    if (ctaDisabled) throw new Error('"ยืนยันบิล N ใบ" CTA is disabled — expected enabled')
    console.log('  CTA enabled ✅')

    await page.screenshot({ path: '/tmp/workspace-smoke-F-finalize-cta.png', fullPage: true })

    console.log('\n✅ monthly-bills-workspace smoke PASS')
    console.log(`   billing_month=${BILLING_MONTH}`)
    console.log(`   draftCount=${draftChipCount}  finalizedCount=${finalChipCount}  ctaCount=${ctaCount}`)
    console.log('   screenshots: /tmp/workspace-smoke-*.png')
  } catch (err) {
    console.error('\n❌ monthly-bills-workspace smoke FAIL')
    console.error(err)
    await page.screenshot({ path: '/tmp/workspace-smoke-FAILURE.png', fullPage: true }).catch(() => {})
    process.exitCode = 1
  } finally {
    await browser.close()
  }
})()
