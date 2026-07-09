// Reading Recovery — Q1.6 E2E Smoke (source-derived over-record → auto-refund)
// ----------------------------------------------------------------------------
// Drives the REAL operator chain through the UI, from the meter-entry origin.
// Three legs, each a fully-reachable real path (no seeded terminal artifacts):
//
//   LEG A — fresh generation → refund
//     forward breakage in Focus (new reading < last recorded) → per-utility
//       inline affordance "แก้ค่าไฟที่จดผิด" ON the meter row → inline Review
//       (suspect source AUTO-BOUND from history, meter-facts only, ZERO money)
//       → confirm → backend derives over-record → workspace generate → bill
//       carries the auto refund ADJUSTMENT, priced at the SOURCE bill's rate.
//
//   LEG B — stale DRAFT → Monthly Draft Refresh (regenerate) → refund
//     a DRAFT that predates the recovery is stale; the reconciliation workspace
//       flags it ("ล้าสมัย" indicator + per-row regenerate signal) and the
//       per-row "อัปเดตร่าง" action atomically voids it + regenerates a fresh
//       draft that now carries the refund. Stale signal clears afterward.
//
//   LEG C — void FINALIZED + regenerate → refund
//     finalize B BEFORE the recovery (succeeds) → record the recovery → the
//     FINALIZED bill no longer reflects reality → operator voids it
//     ("ยกเลิกแล้วออกใหม่") → regenerates → fresh DRAFT carries the refund.
//
// SETTLED SHAPE (ontology lock 2026-07-08): the forward credit (refund) fires
// ONLY when the over-record's SOURCE month carries a FINALIZED/PAID bill (S0
// gate), priced at that bill's electricity line unit_price. The fixture seeds a
// FINALIZED source-month bill (300 units @ 800) so every leg's refund is
// reachable and deterministic (300 × 800 = ฿2,400).
//
// Fixture (seed_dev_recovery.go): room A211 (นานาคอร์ท), active contract, meter
// history whose last electricity reading is a wrong-high 1500 (true ~1200) with
// a FINALIZED source-month bill; water is clean (220) so the smoke also proves
// the per-utility derivation.
//   reset:       POST /api/v1/dev/smoke/reset-recovery       (+ FINALIZED source bill)
//   stale draft: POST /api/v1/dev/smoke/recovery-stale-setup (+ FINALIZED source bill)
//
// Negative assertions (dead Q1.6 decision flow must NOT resurface):
//   - no ACCEPT / WAIVE / "ไม่คืนค่า" / decision UI in the drawer or bill
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
async function dev(path) {
  const r = await fetch(`${BACKEND}/api/v1/dev/smoke/${path}`, { method: 'POST' }).catch(() => null)
  if (!r || !r.ok) fatal(`${path} failed — is the dev backend up on :8080?`)
}

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

// Record a Reading Recovery for A211 through the real Focus meter-entry flow
// (inline recovery, UX shape B — ontology lock 2026-07-08): enter a forward-
// breakage electricity reading (below the wrong-high 1500) so the per-utility
// "แก้ค่าไฟที่จดผิด" affordance appears ON the meter row, tap it to open the inline
// Review (suspect source auto-binds from room history, meter-facts only — ZERO
// money), confirm. No drawer, no correction_* inputs. When opts.assertReview is
// set (LEG A only), also assert the affordance/auto-bind/no-money invariants.
async function doCorrection(page, opts = {}) {
  await page.goto(`${FRONTEND}/meter-readings`, { waitUntil: 'networkidle' })
  await page.waitForTimeout(700)
  await selectApartment(page, 'นานาคอร์ท')

  await page.locator('button', { hasText: 'จดมิเตอร์เร็ว' }).first().click()
  await page.locator('input[aria-label="มิเตอร์ไฟฟ้า"]').first().waitFor({ state: 'visible', timeout: 6000 })
  await page.keyboard.press('Meta+p'); await page.waitForTimeout(500)
  const palInput = page.locator('[role="dialog"] input, input[placeholder]').last()
  if (await palInput.count()) { await palInput.fill(ROOM); await page.waitForTimeout(400) }
  const item = page.locator('[role="dialog"]', { hasText: ROOM }).locator('button, [role="option"]', { hasText: ROOM }).first()
  if (await item.count()) await item.click(); else await page.keyboard.press('Enter')
  await page.waitForTimeout(600)

  if (opts.assertReview) {
    const roomShown = await page.locator('p.text-5xl').first().innerText().catch(() => '?')
    check(`Focus on room ${ROOM}`, roomShown.includes(ROOM), `showed ${roomShown}`)
  }

  await page.locator('input[aria-label="มิเตอร์ไฟฟ้า"]').first().fill('1200') // below recorded 1500 → elec breakage
  await page.locator('input[aria-label="มิเตอร์น้ำ"]').first().fill('225')     // ≥ prev 220 → NO water breakage
  await page.waitForTimeout(300)

  // Per-utility affordance: electricity only (water is clean). The short label
  // makes it "แก้ค่าไฟที่จดผิด"; a water version must NOT appear.
  const elecCta = page.locator('button', { hasText: 'แก้ค่าไฟที่จดผิด' })
  if (opts.assertReview) {
    check('electricity recovery affordance visible on forward breakage', (await elecCta.count()) > 0)
    check('no water recovery affordance (water is clean, per-utility)',
      (await page.locator('button', { hasText: 'แก้ค่าน้ำที่จดผิด' }).count()) === 0)
  }
  await elecCta.first().click()

  // Inline Review (not a drawer) — meter facts only, source read-only.
  await page.getByText('คุณกำลังแก้ค่าไฟฟ้าที่จดผิด').first().waitFor({ state: 'visible', timeout: 5000 })
  if (opts.assertReview) {
    const reviewText = await page.locator('div', { hasText: 'คุณกำลังแก้ค่าไฟฟ้าที่จดผิด' }).last().innerText()
    check('Review shows auto-bound source ("อ้างอิง")', /อ้างอิง/.test(reviewText))
    check('Review shows recorded→actual (จดไว้ 1,500 → ค่าจริง 1,200)',
      /จดไว้/.test(reviewText) && /1,500/.test(reviewText) && /1,200/.test(reviewText))
    check('Review is money-free (Recovery speaks meter, not money)',
      !/฿|บาท|คืนค่า|เก็บเพิ่ม|คืนบางส่วน|ไม่คืนค่า|ACCEPT|WAIVE/.test(reviewText))
  }

  await page.getByRole('button', { name: 'ยืนยัน', exact: true }).first().click()

  const okToast = await page.getByText('บันทึกค่ามิเตอร์ที่แก้แล้ว').waitFor({ timeout: 4000 }).then(() => true).catch(() => false)
  if (opts.assertReview) check('recovery committed (success toast)', okToast)
  await page.waitForTimeout(500)
}

async function gotoWorkspace(page, month) {
  await page.goto(`${FRONTEND}/monthly-bills/${month}`, { waitUntil: 'networkidle' })
  await page.waitForTimeout(1200)
}

function workspaceRow(page) {
  return page.locator(`[data-test="reconciliation-row"][data-room-number="${ROOM}"]`)
}
async function rowAction(page) {
  return page.evaluate((room) => {
    const r = document.querySelector(`[data-test="reconciliation-row"][data-room-number="${room}"]`)
    return r ? r.getAttribute('data-action') : 'NO_ROW'
  }, ROOM)
}

;(async () => {
  const month = new Date().toISOString().slice(0, 7)
  console.log('🧪 reading-recovery Q1.6 smoke — room', ROOM)

  const browser = await chromium.launch({ headless: process.env.SMOKE_HEADLESS === '1' })
  const page = await browser.newPage()

  // Network guard: the FE must never hit the deleted pending endpoint nor send
  // applied_corrections. Recorded across all legs for a hard assertion at the end.
  let hitPendingEndpoint = false
  let sentAppliedCorrections = false
  page.on('request', (req) => {
    if (req.url().includes('pending-baseline-corrections')) hitPendingEndpoint = true
    const body = req.method() === 'POST' || req.method() === 'PATCH' ? (req.postData() || '') : ''
    if (body.includes('applied_corrections')) sentAppliedCorrections = true
  })

  try {
    await login(page)

    // ══ LEG A — fresh generation → refund ═══════════════════════════════════
    console.log('\n── LEG A: fresh correction → generate → refund ──')
    await dev('reset-recovery')
    await doCorrection(page, { assertReview: true })

    // Recovery row IS this cycle's meter anchor (Lock E): bill-ready WITHOUT a
    // separate normal reading (product invariant).
    await gotoWorkspace(page, month)
    check('recovery-only room is bill-ready (not missing-meter)', (await rowAction(page)) !== 'open-meter',
      `action=${await rowAction(page)}`)

    const genA = page.locator('button', { hasText: /ออกบิล \d+ ห้อง/ })
    if ((await genA.count()) && !(await genA.first().isDisabled())) { await genA.first().click(); await page.waitForTimeout(2500) }

    await workspaceRow(page).first().click()
    const drawerA = page.locator('[role="dialog"]')
    await drawerA.first().waitFor({ state: 'visible', timeout: 6000 })
    await page.waitForTimeout(800)
    const billText = await drawerA.first().innerText()
    check('bill shows auto refund line "คืนค่าไฟฟ้า"', /คืนค่าไฟฟ้า/.test(billText))
    check('bill shows over-record evidence (เกิน 300 หน่วย)', /เกิน\s*300\s*หน่วย/.test(billText))
    // S0 + rate basis: refund = over-record (300) × the SOURCE bill's elec unit
    // price (800) = ฿2,400 — priced at the source month, not re-derived here.
    check('refund priced at source-bill rate (฿2,400 = 300 × 800)', /2,400/.test(billText))
    check('electricity refund only (no water refund line)', !/คืนค่าน้ำ/.test(billText))
    check('no decision/waive text on the bill', !/ไม่คืนค่า|เก็บเพิ่ม|คืนบางส่วน|ACCEPT|WAIVE/.test(billText))
    await page.keyboard.press('Escape'); await page.waitForTimeout(500)

    // ══ LEG B — stale DRAFT → Monthly Draft Refresh (regenerate) ════════════
    // The capability the old smoke logged as an uncovered dead-end. A DRAFT that
    // predates the recovery is STALE; the reconciliation workspace flags it
    // ("ล้าสมัย" indicator + per-row badge, data-action=regenerate-draft) and the
    // per-row "อัปเดตร่าง" action atomically voids it + regenerates a fresh draft
    // that now carries the refund. Monthly-only (Settlement out of scope).
    console.log('\n── LEG B: stale DRAFT → อัปเดตร่าง (regenerate) → refund ──')
    await dev('reset-recovery')
    await dev('recovery-stale-setup') // DRAFT bill B for A211 (no refund) + FINALIZED source bill
    await doCorrection(page)          // recovery R → B is now stale

    await gotoWorkspace(page, month)
    check('workspace flags a stale draft ("ล้าสมัย" indicator)',
      (await page.locator('[data-test="stale-draft-indicator"]').count()) > 0)
    check('stale draft row signals regenerate (not plain edit)',
      (await rowAction(page)) === 'regenerate-draft', `action=${await rowAction(page)}`)

    // Per-row click on a stale draft opens the confirm-regenerate modal (takes
    // precedence over edit). Confirm → void old + regenerate from source-of-truth.
    await workspaceRow(page).first().click()
    const regenModal = page.locator('[role="dialog"]', { hasText: 'อัปเดตร่างบิลนี้?' })
    await regenModal.first().waitFor({ state: 'visible', timeout: 5000 })
    check('confirm modal is money-neutral (ร่าง, no refund/เงินคืน wording)',
      !/คืนค่า|เงินคืน|฿|บาท/.test(await regenModal.first().innerText()))
    await regenModal.getByRole('button', { name: 'อัปเดตร่าง', exact: true }).first().click()

    const regenToast = await page.getByText('อัปเดตร่างแล้ว').waitFor({ timeout: 5000 })
      .then(() => true).catch(() => false)
    check('regenerate success toast', regenToast)
    await page.waitForTimeout(1500)

    // Stale signal must clear: indicator gone, row back to a plain editable draft.
    check('stale indicator cleared after regenerate',
      (await page.locator('[data-test="stale-draft-indicator"]').count()) === 0)
    check('row no longer stale (back to edit-draft)',
      (await rowAction(page)) === 'edit-draft', `action=${await rowAction(page)}`)

    // The refreshed draft carries the refund the stale one lacked.
    await workspaceRow(page).first().click()
    const drawerB = page.locator('[role="dialog"]')
    await drawerB.first().waitFor({ state: 'visible', timeout: 6000 })
    await page.waitForTimeout(700)
    const billTextB = await drawerB.first().innerText()
    check('regenerated draft carries refund "คืนค่าไฟฟ้า"', /คืนค่าไฟฟ้า/.test(billTextB))
    check('regenerated draft still electricity-only (no water refund)', !/คืนค่าน้ำ/.test(billTextB))
    await page.keyboard.press('Escape'); await page.waitForTimeout(500)

    // ══ LEG C — void FINALIZED + regenerate → refund ════════════════════════
    console.log('\n── LEG C: finalize→recovery→void+recreate→refund ──')
    await dev('reset-recovery')
    await dev('recovery-stale-setup') // DRAFT bill B

    // Finalize B BEFORE any recovery via the bulk confirm CTA (only A211 carries
    // a draft after reset, so this finalizes exactly one bill).
    await gotoWorkspace(page, month)
    const finalizeAll = page.locator('button', { hasText: /ยืนยันบิล \d+ ใบ/ })
    check('bulk finalize CTA present for the fresh draft', (await finalizeAll.count()) > 0)
    await finalizeAll.first().click(); await page.waitForTimeout(600)
    await page.locator('[role="dialog"]').getByRole('button', { name: 'ออกบิล', exact: true }).first().click()
    await page.waitForTimeout(1800)

    await doCorrection(page) // recovery R AFTER finalize → FINALIZED B is now out of date

    // Void + regenerate in ONE authentic operator action: the FINALIZED bill now
    // appears in /bills (Collection Workspace lists FINALIZED, excludes DRAFT — so
    // its mere presence proves B finalized before the recovery). Row body →
    // collection drawer → "ยกเลิกแล้วออกใหม่" (void+recreate). The new DRAFT is
    // regenerated from meter source-of-truth — now the recovery anchor — so it
    // must carry the refund ADJUSTMENT.
    await page.goto(`${FRONTEND}/bills`, { waitUntil: 'networkidle' })
    await page.waitForTimeout(900)
    await selectApartment(page, 'นานาคอร์ท')
    await page.waitForTimeout(500)
    const searchC = page.locator('input[placeholder*="ค้นหา"]')
    if (await searchC.count()) { await searchC.first().fill(ROOM); await page.waitForTimeout(800) }
    const rowC = page.getByText(ROOM, { exact: true }).first()
    check('draft B finalized before recovery (FINALIZED bill listed in /bills)', (await rowC.count()) > 0)
    await rowC.click()
    const colDrawer = page.locator('[role="dialog"]')
    await colDrawer.first().waitFor({ state: 'visible', timeout: 6000 })
    await page.waitForTimeout(700)
    await colDrawer.locator('button', { hasText: 'ยกเลิกแล้วออกใหม่' }).first().click()
    const vcModal = page.locator('[role="dialog"]', { hasText: 'ยกเลิกบิลและออกใบใหม่' })
    await vcModal.first().waitFor({ state: 'visible', timeout: 5000 })
    await page.locator('textarea#correction-reason').fill('ออกบิลก่อนแก้ค่ามิเตอร์ ต้องออกใบใหม่ให้มียอดคืน')
    await page.getByRole('button', { name: 'ยกเลิกและออกใบใหม่' }).first().click()

    // On success the page chains into BillEditDrawer on the new DRAFT.
    await page.waitForTimeout(2800)
    const editDrawer = page.locator('[role="dialog"]').last()
    await editDrawer.waitFor({ state: 'visible', timeout: 6000 })
    await page.waitForTimeout(600)
    const regenText = await editDrawer.innerText()
    check('void+recreate produced a fresh draft with refund "คืนค่าไฟฟ้า"', /คืนค่าไฟฟ้า/.test(regenText))
    check('regenerated draft still electricity-only (no water refund)', !/คืนค่าน้ำ/.test(regenText))

    // ══ Negative network assertions (all legs) ══════════════════════════════
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
