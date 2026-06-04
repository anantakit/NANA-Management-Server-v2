// Bill Batch Replan — Behavior Smoke Test
// ----------------------------------------------------------------------
// Pins the in-context "บันทึกมิเตอร์ห้อง XXX → row flip" flow added on top
// of BillBatchReview (BatchItemDrawer → MonthlyMeterDrawer REPLACE), and
// the matching backend single-item re-plan endpoint.
//
// Three assertions:
//   A. Row flip — TC28 (A103) starts as SKIPPED(MISSING_METER_READING),
//      operator records the meter via the in-context drawer, the row
//      moves into section-ready with the snapshot populated.
//   B. Chip counts refresh — `ต้องตรวจสอบ` and `พร้อมสร้างบิลร่าง` chip
//      counts shift by ±1 after the flip (page derives stats from items[],
//      so invalidating batchItems is the only refresh needed).
//   C. Replan-fail UX — TC29 (A104) saves the meter successfully but the
//      replan POST is intercepted to return 500. The drawer must surface
//      a non-error warning toast ("บันทึกมิเตอร์ห้อง A104 แล้ว แต่ยัง
//      อัปเดตรายการตรวจสอบไม่สำเร็จ") so the operator can disambiguate
//      "save failed" from "save worked, view stale". Row stays in
//      ต้องตรวจสอบ because the BE snapshot is still frozen.
//
// Pre-state required:
//   - dev seed includes TC28 (A103) + TC29 (A104) in seed_dev_smoke.go:
//     ACTIVE contract on the room, no MONTHLY meter for current month,
//     no move-out notice.
//   - cleanup + seed run at the top so the test is self-contained.
//
// Selector contract:
//   - Drawers scoped via [role="dialog"]
//   - CTAs by exact name (avoid sidebar/list text collisions —
//     "บันทึก" / "บันทึกมิเตอร์" exist in multiple places)
//   - Sections by id `#section-attention` / `#section-ready`
//
// Screenshots saved to /tmp/batch-replan-*.png. exit(1) on failure.

const { chromium } = require('playwright')

const BACKEND = 'http://localhost:8080'
const FRONTEND = 'http://localhost:3001'
const ADMIN_USER = 'admin'
const ADMIN_PASS_FRESH = 'admin123'
const ADMIN_PASS_POST = 'admin1234'

// Fixed by seed_dev_smoke.go (createMissingMeterScenario for A103/A104).
const APARTMENT_NAME = 'นานาคอร์ท'
const ROOM_HAPPY = 'D105' // TC28 — happy-path replan
const ROOM_FAIL = 'D106'  // TC29 — replan returns 500

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
  await page.waitForLoadState('networkidle')
  if (page.url().includes('/change-password')) {
    await page.fill('input[name="new_password"]', ADMIN_PASS_POST)
    await page.fill('input[name="confirm_password"]', ADMIN_PASS_POST)
    await page.click('button[type="submit"]')
    await page.waitForLoadState('networkidle')
  }
  await page.waitForFunction(() => !window.location.pathname.includes('/login'), { timeout: 10000 })
}

// Sidebar global apartment selector — pinned via data-test attributes
// (same hooks used by playwright-test-meter-scope-smoke.js).
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

// Trigger BatchCreateMonthlyBills via the UI: navigate to /bills/generate,
// confirm the current month is selected, click the toolbar CTA. The page
// redirects to /bills/batches/:id on success — wait for the URL change.
async function generateBatchForApartment(page) {
  await page.goto(`${FRONTEND}/bills/generate`, { waitUntil: 'networkidle' })
  await page.waitForTimeout(500)
  await page
    .getByRole('button', { name: 'คำนวณรอบบิล', exact: true })
    .click({ timeout: 8000 })
  await page.waitForURL(/\/bills\/batches\/[0-9a-f-]+$/i, { timeout: 15000 })
  await page.waitForLoadState('networkidle')
  await page.waitForTimeout(500)
}

// Read the count from a SegmentChip whose label matches `labelText`.
// Chip renders as a single <button role="radio"> with a label <span> +
// count <span>. The full button text concatenates both, e.g.
// "ต้องตรวจสอบ 2". Parse the trailing integer.
async function readChipCount(page, labelText) {
  const chip = page
    .locator('[role="radio"]')
    .filter({ hasText: labelText })
    .first()
  const text = (await chip.innerText()).trim()
  const m = text.match(/(\d+)\s*$/)
  if (!m) {
    throw new Error(`chip "${labelText}" — could not parse count from "${text}"`)
  }
  return Number(m[1])
}

// Find a row in a specific section by room_number. Rows are <button>s
// inside the section container. Filter by hasText to defeat strict mode
// when multiple sections contain similar identifiers.
function rowInSection(page, sectionId, roomNumber) {
  return page
    .locator(`#${sectionId} button`)
    .filter({ hasText: roomNumber })
    .first()
}

async function openBatchItemDrawer(page, sectionId, roomNumber) {
  const row = rowInSection(page, sectionId, roomNumber)
  await row.scrollIntoViewIfNeeded()
  await row.click({ timeout: 5000 })
  const dialog = page.locator('[role="dialog"]')
  await dialog.waitFor({ state: 'visible', timeout: 5000 })
  // Confirm we got the BatchItemDrawer for the right room — title is
  // "ห้อง <number>" from BatchItemDrawer.tsx.
  await dialog
    .locator('h2', { hasText: `ห้อง ${roomNumber}` })
    .waitFor({ timeout: 3000 })
  return dialog
}

async function closeAnyDialog(page) {
  const open = page.locator('[role="dialog"]')
  if (await open.count()) {
    await page.keyboard.press('Escape')
    await page.waitForTimeout(350)
  }
}

// Drive the MonthlyMeterDrawer: type water + elec, click บันทึก, await
// the success beat. Caller controls assertions after the drawer closes.
async function recordMeterInDrawer(page, water, electricity) {
  const dialog = page.locator('[role="dialog"]')
  await dialog.locator('input#monthly_water').fill(String(water))
  await dialog.locator('input#monthly_electricity').fill(String(electricity))
  await dialog
    .getByRole('button', { name: 'บันทึก', exact: true })
    .click({ timeout: 5000 })
}

;(async () => {
  console.log('🧪 batch-replan smoke')

  await postDev('/smoke/cleanup')
  await postDev('/smoke/seed')

  const browser = await chromium.launch({
    headless: process.env.SMOKE_HEADLESS === '1',
    slowMo: process.env.SMOKE_HEADLESS === '1' ? 0 : 120,
  })
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  const page = await ctx.newPage()

  try {
    await login(page)
    await selectApartmentViaSidebar(page, APARTMENT_NAME)
    await generateBatchForApartment(page)

    // ── Scenario A: happy path ────────────────────────────────────────
    console.log(`\n🧪 SCENARIO A — บันทึกมิเตอร์ห้อง ${ROOM_HAPPY} → row flip → CREATED`)

    const attentionBefore = await readChipCount(page, 'ต้องตรวจสอบ')
    const readyBefore = await readChipCount(page, 'พร้อมสร้างบิลร่าง')
    console.log(`  pre:  ต้องตรวจสอบ=${attentionBefore} • พร้อมสร้างบิลร่าง=${readyBefore}`)
    if (attentionBefore < 1) {
      throw new Error(`${ROOM_HAPPY} should land in ต้องตรวจสอบ — chip count is 0`)
    }

    await openBatchItemDrawer(page, 'section-attention', ROOM_HAPPY)
    // Confirm blocked state copy before clicking through.
    await page
      .locator('[role="dialog"]')
      .getByText('ยังไม่จดมิเตอร์')
      .first()
      .waitFor({ timeout: 3000 })

    await page
      .getByRole('button', { name: `บันทึกมิเตอร์ห้อง ${ROOM_HAPPY}`, exact: true })
      .click({ timeout: 5000 })
    // MonthlyMeterDrawer mounts in the same [role="dialog"] slot. Confirm
    // the title flipped before typing into stale inputs.
    await page
      .locator('[role="dialog"] h2', { hasText: `บันทึกมิเตอร์ห้อง ${ROOM_HAPPY}` })
      .waitFor({ timeout: 5000 })

    await recordMeterInDrawer(page, /* water */ 110, /* elec */ 1120)

    // Replan + invalidate + 320ms dwell + drawer reopen takes ~700-900ms.
    // Wait for BatchItemDrawer to reappear (title flips back to "ห้อง A103").
    await page
      .locator('[role="dialog"] h2', { hasText: `ห้อง ${ROOM_HAPPY}` })
      .waitFor({ timeout: 8000 })
    // The blocked-state copy must be gone — snapshot replaces it.
    const blockedStill = await page
      .locator('[role="dialog"]')
      .getByText('ยังไม่จดมิเตอร์')
      .count()
    if (blockedStill > 0) {
      throw new Error(`${ROOM_HAPPY} BatchItemDrawer still shows "ยังไม่จดมิเตอร์" after replan`)
    }
    // Snapshot rendered — proves replan flipped result_type to CREATED.
    await page
      .locator('[role="dialog"]')
      .getByText('ค่าใช้จ่าย')
      .first()
      .waitFor({ timeout: 3000 })
    // Status badge text should flip to "พร้อมสร้างบิลร่าง".
    await page
      .locator('[role="dialog"]')
      .getByText('พร้อมสร้างบิลร่าง')
      .first()
      .waitFor({ timeout: 3000 })

    await closeAnyDialog(page)

    // Chip counts shift by exactly ±1 (the single row that flipped).
    const attentionAfter = await readChipCount(page, 'ต้องตรวจสอบ')
    const readyAfter = await readChipCount(page, 'พร้อมสร้างบิลร่าง')
    console.log(`  post: ต้องตรวจสอบ=${attentionAfter} • พร้อมสร้างบิลร่าง=${readyAfter}`)
    if (attentionAfter !== attentionBefore - 1) {
      throw new Error(`ต้องตรวจสอบ should be ${attentionBefore - 1}, got ${attentionAfter}`)
    }
    if (readyAfter !== readyBefore + 1) {
      throw new Error(`พร้อมสร้างบิลร่าง should be ${readyBefore + 1}, got ${readyAfter}`)
    }
    console.log('  ✅ row flip + chip counts both refreshed')

    await page.screenshot({ path: '/tmp/batch-replan-A-flipped.png', fullPage: true })

    // ── Scenario B: replan failure → warning toast ────────────────────
    console.log(`\n🧪 SCENARIO B — บันทึกมิเตอร์ห้อง ${ROOM_FAIL} → replan 500 → warning toast`)

    // Intercept the next replan POST and return 500. Keep the route
    // active only for the duration of this scenario.
    let replanIntercepted = false
    await page.route(
      /\/api\/v1\/bills\/batches\/[^/]+\/items\/[^/]+\/replan$/,
      async (route) => {
        replanIntercepted = true
        await route.fulfill({
          status: 500,
          contentType: 'application/json',
          body: JSON.stringify({ status: 'error', message: 'simulated replan failure' }),
        })
      },
    )

    await openBatchItemDrawer(page, 'section-attention', ROOM_FAIL)
    await page
      .getByRole('button', { name: `บันทึกมิเตอร์ห้อง ${ROOM_FAIL}`, exact: true })
      .click({ timeout: 5000 })
    await page
      .locator('[role="dialog"] h2', { hasText: `บันทึกมิเตอร์ห้อง ${ROOM_FAIL}` })
      .waitFor({ timeout: 5000 })

    await recordMeterInDrawer(page, /* water */ 112, /* elec */ 1130)

    // The meter save returns success → drawer enters success beat → calls
    // onSaveSuccess → page awaits replan → replan rejects (intercept) →
    // page fires the warning toast → 320ms dwell → drawer closes.
    // react-hot-toast renders toasts in a portal at the document root.
    // Wait for the disambiguation copy explicitly.
    await page
      .locator(
        `text=บันทึกมิเตอร์ห้อง ${ROOM_FAIL} แล้ว แต่ยังอัปเดตรายการตรวจสอบไม่สำเร็จ`,
      )
      .first()
      .waitFor({ timeout: 10000 })
    console.log('  ✅ warning toast surfaced')

    if (!replanIntercepted) {
      throw new Error('replan endpoint was never hit — intercept did not match')
    }

    // BE snapshot was never updated → row must stay in ต้องตรวจสอบ.
    await page.waitForTimeout(500)
    const failRowInAttention = await rowInSection(
      page,
      'section-attention',
      ROOM_FAIL,
    ).count()
    if (failRowInAttention < 1) {
      throw new Error(`${ROOM_FAIL} should remain in #section-attention after replan failure`)
    }
    const failRowInReady = await rowInSection(
      page,
      'section-ready',
      ROOM_FAIL,
    ).count()
    if (failRowInReady > 0) {
      throw new Error(`${ROOM_FAIL} should NOT appear in #section-ready when replan failed`)
    }
    console.log(`  ✅ ${ROOM_FAIL} stayed in ต้องตรวจสอบ (snapshot still frozen)`)

    await page.screenshot({ path: '/tmp/batch-replan-B-warning.png', fullPage: true })

    console.log('\n✅ batch-replan smoke PASS')
  } catch (err) {
    console.error('\n❌ batch-replan smoke FAIL')
    console.error(err)
    await page.screenshot({ path: '/tmp/batch-replan-FAILURE.png', fullPage: true })
    process.exitCode = 1
  } finally {
    await browser.close()
  }
})()
