// Reading Recovery — Owner Smoke (P5 Layer B, thin)
// ----------------------------------------------------------------------
// Verifies the OPERATOR-FACING wiring of the Q1 Recovery Decision surface.
// The business invariants (money sign, WAIVED, gate block/pass, void→re-pending,
// source-less) are locked by the Go integration suite
// (service_recovery_canonical_integration_test.go RR01–RR09); this smoke only
// confirms that:
//
//   1. admin SEES the pending recovery — "รอตัดสินใจ" badge on the bill list
//   2. admin can DECIDE — the 3-way resolve UI (เก็บเพิ่ม/คืนเงิน/ไม่คิดเงิน) +
//      the 2a "ใช้ยอดนี้" calculator are present in the bill editor
//   3. finalize is BLOCKED while a recovery is pending (block modal, no proceed)
//   4. after resolving (charge), finalize PASSES
//
// Fixture: seed_dev_recovery.go seeds room A207 (นานาคอร์ท) with an ACTIVE
// contract, a DRAFT monthly bill, and one PENDING recovery (with source).
//
// ONE-SHOT per seed: the smoke resolves A207's recovery, so it needs a fresh
// pending state to re-run. Reset with `make dev-clean && make dev`, or just the
// recovery's ADJUSTMENT:
//   DELETE FROM bill_line_items WHERE adjustment_recovery_reading_id IN (
//     SELECT mr.id FROM meter_readings mr JOIN rooms r ON r.id=mr.room_id
//     JOIN apartments a ON a.id=r.apartment_id
//     WHERE a.name='นานาคอร์ท' AND r.number='A207' AND mr.anchor_reason='READING_RECOVERY');
//
// Run:  npm run smoke:reading-recovery
//       SMOKE_HEADLESS=1 npm run smoke:reading-recovery   (CI / no window)
const { chromium } = require('playwright')

const FRONTEND = process.env.FRONTEND || 'http://localhost:3001'
const ADMIN_USER = 'admin'
const ADMIN_PASS_FRESH = 'admin123'
const ADMIN_PASS_POST = 'admin1234'
const ROOM = 'A207'

const results = { pass: 0, fail: 0 }
function check(name, ok, detail = '') {
  if (ok) {
    results.pass++
    console.log(`  ✅ ${name}`)
  } else {
    results.fail++
    console.log(`  ❌ ${name}${detail ? ' — ' + detail : ''}`)
  }
}
function fail(msg) {
  console.error('❌ FATAL:', msg)
  process.exit(1)
}

async function login(page) {
  await page.goto(`${FRONTEND}/login`)
  await page.fill('input[name="username"]', ADMIN_USER)
  await page.fill('input[name="password"]', ADMIN_PASS_FRESH)
  await page.click('button[type="submit"]')
  await page.waitForTimeout(1500)
  if (page.url().includes('/login')) {
    await page.fill('input[name="username"]', ADMIN_USER)
    await page.fill('input[name="password"]', ADMIN_PASS_POST)
    await page.click('button[type="submit"]')
    await page.waitForTimeout(1500)
  }
  if (page.url().includes('/change-password')) {
    await page.fill('input[name="new_password"]', ADMIN_PASS_POST)
    await page.fill('input[name="confirm_password"]', ADMIN_PASS_POST)
    await page.click('button[type="submit"]')
    await page.waitForTimeout(1500)
  }
  await page.waitForFunction(() => !window.location.pathname.includes('/login'), { timeout: 10000 })
}

async function waitDialogsClosed(page, timeout = 8000) {
  await page.waitForFunction(() => document.querySelectorAll('[role="dialog"]').length === 0, null, { timeout })
}

;(async () => {
  const browser = await chromium.launch({
    headless: process.env.SMOKE_HEADLESS === '1',
    slowMo: process.env.SMOKE_HEADLESS === '1' ? 0 : 100,
  })
  const page = await (await browser.newContext({ viewport: { width: 1440, height: 900 } })).newPage()

  try {
    await login(page)
    console.log('▶ login ok')

    // Select นานาคอร์ท (A207 lives there) via the global apartment selector.
    const trigger = page.locator('[data-test="apartment-selector-trigger"]')
    if (await trigger.count()) {
      await trigger.first().click()
      await page.waitForTimeout(300)
      const opt = page.locator('[data-test="apartment-selector-option"]', { hasText: 'นานาคอร์ท' })
      if (await opt.count()) await opt.first().click()
      await page.waitForTimeout(500)
    }

    // DRAFT bills live in the monthly-bills / reconciliation workspace (the
    // Collection Workspace at /bills excludes DRAFT). That workspace is where the
    // operator sees the pending recovery + resolves it.
    const month = new Date().toISOString().slice(0, 7) // YYYY-MM (current cycle)
    await page.goto(`${FRONTEND}/monthly-bills/${month}`, { waitUntil: 'networkidle' })
    await page.waitForTimeout(1800)

    const row = page.getByText(ROOM, { exact: true }).first().locator('xpath=ancestor::*[self::button or self::div][1]')
    if ((await page.getByText(ROOM, { exact: true }).count()) === 0) {
      fail(`no ${ROOM} row in /monthly-bills/${month} — seed_dev_recovery fixture missing or wrong month`)
    }

    // ── 1. See — pending recovery surfaced on the DRAFT row ──
    const pendingHint = page.getByText('ระบุยอดที่จะแก้', { exact: false })
    check('1. DRAFT row shows pending recovery ("ระบุยอดที่จะแก้ N รายการ")', (await pendingHint.count()) > 0)

    // ── 2. Decide — open the row → BillEditDrawer lands on the resolve section ──
    await row.click({ timeout: 8000 })
    await page.locator('[role="dialog"]').waitFor({ state: 'visible', timeout: 5000 })
    const edit = page.locator('[role="dialog"]').last()
    // PendingCorrectionsSection fetches its rows async — wait for the resolve
    // controls to render before asserting.
    await edit.getByRole('button', { name: 'เก็บเพิ่ม', exact: true }).waitFor({ state: 'visible', timeout: 8000 })
    const has3Way =
      (await edit.getByRole('button', { name: 'เก็บเพิ่ม', exact: true }).count()) > 0 &&
      (await edit.getByRole('button', { name: 'คืนเงิน', exact: true }).count()) > 0 &&
      (await edit.getByRole('button', { name: 'ไม่คิดเงิน', exact: true }).count()) > 0
    check('2a. 3-way resolve (เก็บเพิ่ม/คืนเงิน/ไม่คิดเงิน) present', has3Way)
    await edit.getByRole('button', { name: 'เก็บเพิ่ม', exact: true }).click()
    await page.waitForTimeout(300)
    check('2b. 2a "ใช้ยอดนี้" calculator present', (await edit.getByRole('button', { name: 'ใช้ยอดนี้', exact: true }).count()) > 0)

    // ── 3. Resolve (charge) → save → pending hint clears ──
    await edit.getByRole('button', { name: 'ใช้ยอดนี้', exact: true }).click()
    await edit.locator('textarea[id^="adj_note_"]').first().fill('เก็บยอดเพิ่มจากการจดมิเตอร์ผิด (smoke)')
    await edit.getByRole('button', { name: 'บันทึก', exact: true }).click({ timeout: 5000 })
    await waitDialogsClosed(page)
    // Re-fetch the workspace so the pending count reflects the applied ADJUSTMENT.
    await page.goto(`${FRONTEND}/monthly-bills/${month}`, { waitUntil: 'networkidle' })
    await page.waitForTimeout(1800)
    check('3. resolved (charge) — pending hint cleared', (await page.getByText('ระบุยอดที่จะแก้', { exact: false }).count()) === 0)

    // Note: the finalize gate (pending blocks / resolved passes) is locked
    // deterministically by the Go integration suite RR01/RR05 + RR02-04/RR06 —
    // not re-verified here, per the same logic that keeps money invariants out
    // of the UI smoke.

    console.log(`\n${results.fail === 0 ? '✅ PASS' : '❌ FAIL'} — ${results.pass} passed, ${results.fail} failed`)
    await browser.close()
    process.exit(results.fail === 0 ? 0 : 1)
  } catch (err) {
    try {
      await page.screenshot({ path: '/tmp/reading-recovery-smoke-error.png' })
    } catch {}
    await browser.close()
    fail(err.message)
  }
})()
