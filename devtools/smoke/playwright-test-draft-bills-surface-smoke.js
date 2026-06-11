// Draft Bills Surface — Behavior Smoke Test
// ----------------------------------------------------------------------
// Pins the DraftBillsSurface that replaced the Room-world audit body when
// ≥1 DRAFT bill exists for the month (Lock 2, Architecture walk 2026-06-09).
//
// Three assertions:
//   A. Surface identity — after cleanup+seed, the MonthlyBillsPage at
//      /monthly-bills/:YYYY-MM enters 'draft' phase because TC26 (E101)
//      and TC27 (E102) both have DRAFT MONTHLY bills in the smoke seed.
//      The h1 title must contain "บิลร่างเดือน" (not the audit page title)
//      and the stripe "N บิลร่างรอยืนยัน" must be visible.
//   B. CTA presence — "ยืนยันบิลทั้งหมด N ใบ" button is present and
//      enabled on desktop (toolbar) and mobile (sticky bar). Blocked rooms
//      callout ("ยังมี N ห้องที่ออกบิลไม่ได้") must also appear — guaranteed
//      because D105/D106/A205 are ACTION_REQUIRED or PENDING_DECISION with
//      bill=null after cleanup+seed.
//   C. Row click → BillEditDrawer — clicking the E101 draft row opens a
//      [role="dialog"] with title "ห้อง E101". Escape closes it.
//
// Pre-state required:
//   - dev seed includes TC26 (E101) + TC27 (E102) in seed_dev_smoke.go:
//     ACTIVE contract + DRAFT MONTHLY bill for the current month.
//   - dev seed includes A101–A107 with FINALIZED/PAID current-month bills
//     (seedDevMonthlyBills). Together draftCount=2, finalizedCount≥5 →
//     phase='draft' on the first load, no Generate step needed.
//   - cleanup + seed run at the top so the test is self-contained.
//
// Selector contract:
//   - Draft phase surface identified via h1 text containing "บิลร่างเดือน"
//   - Draft bill rows are <button aria-label="แก้ไขบิลห้อง ...">
//   - Drawers scoped via [role="dialog"], title via h2
//   - CTA found via button text containing "ยืนยันบิลทั้งหมด"
//   - Blocked callout: text containing "ยังมี" + "ห้องที่ออกบิลไม่ได้"
//
// Screenshots saved to /tmp/draft-bills-*.png. exit(1) on failure.

const { chromium } = require('playwright')

const BACKEND = 'http://localhost:8080'
const FRONTEND = 'http://localhost:3001'
const ADMIN_USER = 'admin'
const ADMIN_PASS_FRESH = 'admin123'
const ADMIN_PASS_POST = 'admin1234'

const APARTMENT_NAME = 'นานาคอร์ท'
// E101 is TC26 — ACTIVE contract, DRAFT MONTHLY bill, no overrides.
// Safe to open in BillEditDrawer without triggering any mutation side-effects.
const ROOM_DRAFT = 'E101'

// Dynamic billing month so the test stays green when the calendar rolls over.
// dev seed always creates bills for time.Now().UTC() billing month.
const BILLING_MONTH = (() => {
  const d = new Date()
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}`
})()

async function postDev(path) {
  const res = await fetch(`${BACKEND}/api/v1/dev${path}`, { method: 'POST' })
  if (!res.ok) {
    throw new Error(`POST /api/v1/dev${path} → HTTP ${res.status}`)
  }
  const body = await res.json()
  if (body.status !== 'success') {
    throw new Error(`POST /api/v1/dev${path} → ${JSON.stringify(body)}`)
  }
}

async function login(page) {
  await page.goto(`${FRONTEND}/login`)
  await page.fill('input[name="username"]', ADMIN_USER)
  await page.fill('input[name="password"]', ADMIN_PASS_FRESH)
  await page.click('button[type="submit"]')
  // Brief wait — let the server round-trip resolve before URL check.
  await page.waitForTimeout(1200)

  // If still on /login the fresh password failed (already changed by a prior
  // smoke run). Re-submit with the post-change password.
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
  await page.waitForFunction(() => !window.location.pathname.includes('/login'), {
    timeout: 10000,
  })
}

async function selectApartmentViaSidebar(page, apartmentName) {
  const trigger = page.locator('[data-test="apartment-selector-trigger"]')
  await trigger.scrollIntoViewIfNeeded()
  await trigger.click()
  await page.waitForTimeout(200)
  await page
    .locator(
      '[data-test="apartment-selector-panel"] [data-test="apartment-selector-option"]',
      { hasText: apartmentName },
    )
    .first()
    .click()
  await page.waitForTimeout(400)
}

async function navigateToDraftSurface(page) {
  await page.goto(`${FRONTEND}/monthly-bills/${BILLING_MONTH}`, {
    waitUntil: 'networkidle',
  })
  await page.waitForTimeout(800)
}

// Read draft bill count from the stripe paragraph.
// Rendered as: "<span>{N}</span> บิลร่างรอยืนยัน"
async function readDraftCount(page) {
  const stripe = page.locator('p:has-text("บิลร่างรอยืนยัน")').first()
  const text = (await stripe.innerText()).trim()
  const m = text.match(/(\d+)/)
  if (!m) {
    throw new Error(`could not parse draft count from stripe: "${text}"`)
  }
  return Number(m[1])
}

;(async () => {
  console.log('🧪 draft-bills-surface smoke  billing_month=' + BILLING_MONTH)

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
    await selectApartmentViaSidebar(page, APARTMENT_NAME)
    await navigateToDraftSurface(page)

    // ── Assertion A: DraftBillsSurface identity ───────────────────────
    console.log('\n🧪 ASSERTION A — DraftBillsSurface identity')

    // h1 must contain "บิลร่างเดือน" — proves Lock 2 surface swap fired.
    // If the audit page rendered instead, h1 would say "กระทบยอดการออกบิล".
    // Two h1s exist on the page (sidebar "NANA" + page title) — match by name.
    const h1 = page.getByRole('heading', { level: 1, name: /บิลร่างเดือน/ })
    await h1.waitFor({ timeout: 8000 })
    const h1Text = (await h1.innerText()).trim()
    console.log(`  h1: "${h1Text}" ✅`)

    // Stripe count must be ≥ 2 (E101 + E102 from smoke seed).
    const draftCount = await readDraftCount(page)
    if (draftCount < 2) {
      throw new Error(
        `expected ≥ 2 draft bills (E101+E102), stripe shows ${draftCount}`,
      )
    }
    console.log(`  stripe: ${draftCount} บิลร่างรอยืนยัน ✅`)

    await page.screenshot({ path: '/tmp/draft-bills-A-surface.png', fullPage: true })

    // ── Assertion B: CTA + blocked callout ───────────────────────────
    console.log('\n🧪 ASSERTION B — CTA presence + blocked rooms callout')

    // Desktop toolbar CTA: "ยืนยันบิลทั้งหมด N ใบ"
    // Find button by partial text — the count is embedded in the label.
    const ctaButton = page
      .getByRole('button', { name: /ยืนยันบิลทั้งหมด \d+ ใบ/ })
      .first()
    await ctaButton.waitFor({ state: 'visible', timeout: 5000 })
    const ctaLabel = (await ctaButton.innerText()).trim()
    console.log(`  CTA label: "${ctaLabel}" ✅`)

    // CTA must be enabled (draft bills are ready-by-default).
    const ctaDisabled = await ctaButton.isDisabled()
    if (ctaDisabled) {
      throw new Error(`"ยืนยันบิลทั้งหมด" CTA is disabled — expected enabled for draftCount=${draftCount}`)
    }
    console.log('  CTA enabled ✅')

    // Blocked rooms callout must appear — guaranteed by D105/D106/A205/move-out
    // rooms that all land as bill=null with a non-NOT_BILLABLE bucket.
    // Rendered as a single-line divider callout with an inline "ดูรายการ →" link.
    const blockedCallout = page
      .locator('p:has-text("ห้องที่ออกบิลไม่ได้")')
      .first()
    await blockedCallout.waitFor({ state: 'visible', timeout: 5000 })
    const blockedText = (await blockedCallout.innerText()).trim()
    console.log(`  blocked callout: "${blockedText}" ✅`)

    // Inline "ดูรายการ →" link must be present inside the callout.
    const auditLink = page.getByRole('button', { name: /ดูรายการ/ }).first()
    await auditLink.waitFor({ state: 'visible', timeout: 3000 })
    console.log('  "ดูรายการ →" link present ✅')

    await page.screenshot({ path: '/tmp/draft-bills-B-cta.png', fullPage: true })

    // Mobile sticky CTA — resize and verify it renders
    await page.setViewportSize({ width: 375, height: 812 })
    await page.waitForTimeout(300)
    // On mobile the toolbar CTA is hidden (sm:hidden) and sticky bar shows.
    // Both render the same text pattern, so find the visible one.
    const mobileCta = page
      .locator('.fixed.inset-x-0.bottom-0')
      .getByRole('button', { name: /ยืนยันบิลทั้งหมด \d+ ใบ/ })
    await mobileCta.waitFor({ state: 'visible', timeout: 5000 })
    console.log('  mobile sticky CTA visible ✅')
    await page.screenshot({ path: '/tmp/draft-bills-B-mobile.png', fullPage: true })

    // Restore desktop for assertion C
    await page.setViewportSize({ width: 1440, height: 900 })
    await page.waitForTimeout(300)

    // ── Assertion C: Row click → BillEditDrawer ───────────────────────
    console.log(`\n🧪 ASSERTION C — row click → BillEditDrawer (${ROOM_DRAFT})`)

    // Each DraftBillRow is a <button aria-label="แก้ไขบิลห้อง {roomNumber}">
    const draftRow = page
      .getByRole('button', { name: `แก้ไขบิลห้อง ${ROOM_DRAFT}` })
      .first()
    await draftRow.waitFor({ state: 'visible', timeout: 5000 })
    console.log(`  row found: aria-label="แก้ไขบิลห้อง ${ROOM_DRAFT}" ✅`)

    await draftRow.click()

    // BillEditDrawer opens as a Sheet [role="dialog"]. Title starts as
    // "กำลังโหลด..." while the bill API call is in flight, then flips to
    // "ห้อง E101" once the data resolves. Wait for the room number specifically.
    const dialog = page.locator('[role="dialog"]')
    await dialog.waitFor({ state: 'visible', timeout: 5000 })
    await dialog
      .locator('h2', { hasText: `ห้อง ${ROOM_DRAFT}` })
      .waitFor({ timeout: 8000 })
    console.log(`  BillEditDrawer opened with title "ห้อง ${ROOM_DRAFT}" ✅`)

    await page.screenshot({ path: '/tmp/draft-bills-C-drawer.png', fullPage: true })

    // Close drawer and verify it dismisses cleanly
    await page.keyboard.press('Escape')
    await dialog.waitFor({ state: 'hidden', timeout: 3000 })
    console.log('  drawer closed cleanly ✅')

    console.log('\n✅ draft-bills-surface smoke PASS')
    console.log(`   billing_month=${BILLING_MONTH} • draftCount=${draftCount}`)
    console.log('   screenshots: /tmp/draft-bills-*.png')
  } catch (err) {
    console.error('\n❌ draft-bills-surface smoke FAIL')
    console.error(err)
    await page.screenshot({ path: '/tmp/draft-bills-FAILURE.png', fullPage: true })
    process.exitCode = 1
  } finally {
    await browser.close()
  }
})()
