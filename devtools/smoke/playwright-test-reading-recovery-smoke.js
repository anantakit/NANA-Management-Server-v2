// Reading Recovery — Q1.6 E2E Smoke (source-derived over-record → auto-refund)
// ----------------------------------------------------------------------------
// Drives the REAL operator chain through the UI, from the meter-entry origin:
//
//   forward breakage in Focus (new reading < last recorded)
//     → "แก้ค่าที่จดผิด" CTA (visible while the row is red)
//     → BaselineCorrectionDrawer with the suspect source AUTO-BOUND
//     → enter the true physical value + note → submit
//     → backend derives the over-record (recorded = source.current) per-utility
//
// Fixture (seed_dev_recovery.go): room A211 (นานาคอร์ท), active contract, meter
// history whose last electricity reading is a wrong-high 1500 (true ~1200);
// water is clean (220) so the smoke also proves the per-utility derivation.
// Reset each run:  POST /api/v1/dev/smoke/reset-recovery
//
// Negative assertions (dead Q1.6 decision flow must NOT resurface):
//   - no ACCEPT / WAIVE / "ไม่คืนค่า" / decision UI in the drawer
//   - no call to the deleted /bills/:id/pending-baseline-corrections endpoint
//   - no applied_corrections payload sent from the FE
//
// Settlement over-record refund is intentionally OUT OF SCOPE (unreachable — see
// backlog_settlement_overrecord_refund_unreachable): this smoke is MONTHLY only.
//
// Run:  npm run smoke:reading-recovery
//       SMOKE_HEADLESS=1 FRONTEND=http://localhost:3001 node playwright-test-reading-recovery-smoke.js
const { chromium } = require('playwright')

const FRONTEND = process.env.FRONTEND || 'http://localhost:3001'
const BACKEND = process.env.BACKEND || 'http://localhost:8080'
const ROOM = 'A211'
const results = { pass: 0, fail: 0 }
function check(name, ok, detail = '') {
  if (ok) { results.pass++; console.log(`  ✅ ${name}`) }
  else { results.fail++; console.log(`  ❌ ${name}${detail ? ' — ' + detail : ''}`) }
}
function fatal(msg) { console.error('❌ FATAL:', msg); process.exit(1) }

async function login(page) {
  await page.goto(`${FRONTEND}/login`, { waitUntil: 'domcontentloaded' })
  for (const pw of ['admin123', 'admin1234']) {
    if (!page.url().includes('/login')) break
    await page.fill('input[name="username"]', 'admin')
    await page.fill('input[name="password"]', pw)
    await page.click('button[type="submit"]'); await page.waitForTimeout(1200)
  }
  if (page.url().includes('/change-password')) {
    await page.fill('input[name="new_password"]', 'admin1234')
    await page.fill('input[name="confirm_password"]', 'admin1234')
    await page.click('button[type="submit"]'); await page.waitForTimeout(1200)
  }
  await page.waitForFunction(() => !location.pathname.includes('/login'), { timeout: 10000 })
}

async function selectApartment(page, name) {
  const trig = page.locator('[data-test="apartment-selector-trigger"]')
  if (await trig.count()) {
    await trig.first().click()
    const opt = page.locator('[data-test="apartment-selector-option"]', { hasText: name })
    if (await opt.count()) { await opt.first().click(); await page.waitForTimeout(700) }
  }
}

;(async () => {
  // Reset the fixture to the clean Q1.6 starting state (unauthenticated dev route).
  const resp = await fetch(`${BACKEND}/api/v1/dev/smoke/reset-recovery`, { method: 'POST' }).catch(() => null)
  if (!resp || !resp.ok) fatal('reset-recovery failed — is the dev backend up on :8080?')
  console.log('🧪 reading-recovery Q1.6 smoke — room', ROOM)

  const browser = await chromium.launch({ headless: process.env.SMOKE_HEADLESS === '1' })
  const page = await browser.newPage()

  // Network guard: the FE must never hit the deleted pending endpoint nor send
  // applied_corrections. Recorded per request for a hard assertion at the end.
  let hitPendingEndpoint = false
  let sentAppliedCorrections = false
  page.on('request', (req) => {
    if (req.url().includes('pending-baseline-corrections')) hitPendingEndpoint = true
    const body = req.method() === 'POST' || req.method() === 'PATCH' ? (req.postData() || '') : ''
    if (body.includes('applied_corrections')) sentAppliedCorrections = true
  })

  try {
    await login(page)
    await page.goto(`${FRONTEND}/meter-readings`, { waitUntil: 'networkidle' })
    await page.waitForTimeout(700)
    await selectApartment(page, 'นานาคอร์ท')

    // ── Enter Focus + jump to the recovery room ──
    await page.locator('button', { hasText: 'จดมิเตอร์เร็ว' }).first().click()
    await page.locator('input[aria-label="มิเตอร์ไฟฟ้า"]').first().waitFor({ state: 'visible', timeout: 6000 })
    await page.keyboard.press('Meta+p'); await page.waitForTimeout(500)
    const palInput = page.locator('[role="dialog"] input, input[placeholder]').last()
    if (await palInput.count()) { await palInput.fill(ROOM); await page.waitForTimeout(400) }
    const item = page.locator('[role="dialog"]', { hasText: ROOM }).locator('button, [role="option"]', { hasText: ROOM }).first()
    if (await item.count()) await item.click(); else await page.keyboard.press('Enter')
    await page.waitForTimeout(600)

    const roomShown = await page.locator('p.text-5xl').first().innerText().catch(() => '?')
    check(`Focus on room ${ROOM}`, roomShown.includes(ROOM), `showed ${roomShown}`)

    // ── Enter a forward-breakage electricity reading (below the wrong-high 1500);
    //    water stays valid (>= 220) so only electricity breaks. ──
    const elecInput = page.locator('input[aria-label="มิเตอร์ไฟฟ้า"]').first()
    const waterInput = page.locator('input[aria-label="มิเตอร์น้ำ"]').first()
    await elecInput.fill('1200')   // physical truth, below recorded 1500
    await waterInput.fill('225')   // normal, above previous 220 → no water breakage
    await page.waitForTimeout(300)

    // CTA must be visible on the LIVE breakage (P5A.5 fix: gated on the red row).
    const ctaBtn = page.locator('button', { hasText: 'แก้ค่าที่จดผิด' })
    check('recovery CTA visible on forward breakage', (await ctaBtn.count()) > 0)

    // ── Open the correction drawer; the suspect source auto-binds ──
    await ctaBtn.first().click()
    const drawer = page.locator('[role="dialog"]', { hasText: 'แก้ค่ามิเตอร์ที่จดผิด' })
    await drawer.first().waitFor({ state: 'visible', timeout: 5000 })
    await page.waitForTimeout(600)

    // Auto-bound source shows "อ้างอิงจากเดือน ..." (not the empty picker prompt).
    const boundSource = await drawer.getByText(/อ้างอิงจากเดือน/).count()
    check('suspect source auto-bound in drawer', boundSource > 0)

    // Negative: no ACCEPT / WAIVE / ไม่คืนค่า decision controls in the drawer.
    const drawerText = await drawer.first().innerText()
    check('no decision UI (ACCEPT/WAIVE/ไม่คืนค่า) in drawer',
      !/เก็บเพิ่ม|คืนบางส่วน|ไม่คืนค่า|ACCEPT|WAIVE/.test(drawerText))

    // ── Fill the physical values + note, submit ──
    await drawer.locator('input#correction_elec').fill('1200')
    await drawer.locator('input#correction_water').fill('225')
    await drawer.locator('textarea#correction_anchor_note').fill('จดไฟฟ้าเกินจริงเดือนก่อน — บันทึกค่าที่ถูกต้อง')
    await drawer.locator('button[type="submit"], button', { hasText: 'บันทึกค่ามิเตอร์ที่ถูกต้อง' }).first().click()

    // Success toast confirms the recovery committed.
    const okToast = await page.getByText('บันทึกแล้ว — ระบุยอดตอนออกบิล').waitFor({ timeout: 4000 }).then(() => true).catch(() => false)
    check('correction committed (success toast)', okToast)
    await page.waitForTimeout(500)

    // ── Reconciliation readiness + generation ──
    // The recovery row IS this cycle's meter anchor (Lock E). The room must be
    // bill-ready WITHOUT a separate normal reading (product invariant).
    const month = new Date().toISOString().slice(0, 7)
    await page.goto(`${FRONTEND}/monthly-bills/${month}`, { waitUntil: 'networkidle' })
    await page.waitForTimeout(1200)
    const rowAction = await page.evaluate(() => {
      const r = document.querySelector('[data-test="reconciliation-row"][data-room-number="A211"]')
      return r ? r.getAttribute('data-action') : 'NO_ROW'
    })
    // Recovery-only room must NOT be flagged missing-meter.
    check('recovery-only room is bill-ready (not missing-meter)', rowAction !== 'open-meter', `action=${rowAction}`)

    // Generate via the workspace CTA (ออกบิล N ห้อง). After reset only A211 is
    // freshly ready, so this generates its bill from the recovery anchor.
    const genCTA = page.locator('button', { hasText: /ออกบิล \d+ ห้อง/ })
    if ((await genCTA.count()) && !(await genCTA.first().isDisabled())) {
      await genCTA.first().click()
      await page.waitForTimeout(2500)
    }

    // Open A211's bill (row now view-bill) and inspect the breakdown.
    await page.locator('[data-test="reconciliation-row"][data-room-number="A211"]').first().click()
    const billDrawer = page.locator('[role="dialog"]')
    await billDrawer.first().waitFor({ state: 'visible', timeout: 6000 })
    await page.waitForTimeout(800)
    const billText = await billDrawer.first().innerText()

    check('bill shows auto refund line "คืนค่าไฟฟ้า"', /คืนค่าไฟฟ้า/.test(billText))
    check('bill shows over-record evidence (เกิน N หน่วย × ฿rate)', /เกิน\s*300\s*หน่วย/.test(billText))
    check('electricity refund only (no water refund line)', !/คืนค่าน้ำ/.test(billText))
    check('no decision/waive text on the bill', !/ไม่คืนค่า|เก็บเพิ่ม|คืนบางส่วน|ACCEPT|WAIVE/.test(billText))

    // ── Negative network assertions ──
    check('no call to deleted pending-baseline-corrections endpoint', !hitPendingEndpoint)
    check('no applied_corrections payload sent', !sentAppliedCorrections)

  } catch (e) {
    console.error('SMOKE ERROR:', e.message)
    await page.screenshot({ path: '/tmp/recovery-smoke-err.png' }).catch(() => {})
    results.fail++
  } finally {
    await browser.close()
  }

  console.log(`\n${results.fail === 0 ? '✅ PASS' : '❌ FAIL'} — ${results.pass} passed, ${results.fail} failed`)
  process.exit(results.fail === 0 ? 0 : 1)
})()
