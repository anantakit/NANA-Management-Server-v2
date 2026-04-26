// Move-out RoomWorkflowDrawer Step 2/3 — Smoke Test (Scenarios A–C)
// ----------------------------------------------------------------------
// Validates the Step 2 commit boundary fix + Step 3 edit-back affordances
// on the move-out queue's drawer.
//
//   A  Step 2 confirm chains generateSettlement → finalizeSettlement →
//      advance, transitioning notice.status PENDING_SETTLEMENT →
//      PENDING_PAYMENT so the queue card moves bucket.
//   B  Step 3 edit-back navigation ("แก้ยอดสรุป") moves the drawer to
//      Step 2 despite RoomWorkflowDrawer's forward-only Math.max status
//      sync, and re-advance from Step 2 (status=PENDING_PAYMENT) is a
//      pure UI navigation (no API mutation).
//   C  Step 3 "แก้มิเตอร์" → meter edit triggers backend void+regenerate,
//      reverts notice.status to PENDING_SETTLEMENT, drawer auto-advances
//      to Step 2 with the recomputed banner; re-confirm finalizes again.
//
// Fixtures: TC3 (PENDING_SETTLEMENT, no draft, room B201) — chosen so we
// exercise the NO_DRAFT generate+finalize path on Scenario A and the
// HAS_DRAFT finalize-only path on Scenario C.
//
// Safe to run independently: cleanup+seed cycle at the top resets state.
// Run:  npm run smoke:moveout-step23   (from backend/devtools/smoke/)
//       make smoke-moveout-step23      (from backend/)

const { chromium } = require('playwright')

const FRONTEND = 'http://localhost:3001'
const BACKEND = 'http://localhost:8080'
const ADMIN_USER = 'admin'
const ADMIN_PASS_FRESH = 'admin123'
const ADMIN_PASS_POST = 'admin1234'

const DRAWER = '[role="dialog"][aria-label="ดำเนินการย้ายออก"]'

// ─── Pretty logger ──────────────────────────────────────────────────────

const results = { pass: 0, fail: 0, total: 0, failedCases: [] }
const check = (name, ok, detail = '') => {
  const icon = ok ? '✅' : '❌'
  const msg = detail ? ` — ${detail}` : ''
  console.log(`  ${icon} ${name}${msg}`)
  results.total++
  if (ok) results.pass++
  else { results.fail++; results.failedCases.push(name) }
  return ok
}

// ─── Auth + fixtures ───────────────────────────────────────────────────

async function login(page) {
  await page.goto(`${FRONTEND}/login`)
  await page.fill('input[name="username"]', ADMIN_USER)
  await page.fill('input[name="password"]', ADMIN_PASS_FRESH)
  await page.click('button[type="submit"]')
  await page.waitForLoadState('networkidle')
  if (page.url().includes('/change-password')) {
    // Forced password change after fresh seed — pick the post-change pass.
    await page.fill('input[name="new_password"]', ADMIN_PASS_POST)
    await page.fill('input[name="confirm_password"]', ADMIN_PASS_POST)
    await page.click('button[type="submit"]')
    await page.waitForLoadState('networkidle')
  }
  await page.waitForFunction(() => !window.location.pathname.includes('/login'), { timeout: 10000 })
}

async function loginApi() {
  // Try post-change pass first; fall back to fresh-seed pass.
  for (const password of [ADMIN_PASS_POST, ADMIN_PASS_FRESH]) {
    const res = await fetch(`${BACKEND}/api/v1/auth/login`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username: ADMIN_USER, password }),
    })
    const json = await res.json()
    if (json.status === 'success') return json.data.access_token
  }
  throw new Error('API login failed for both fresh + post-change passwords')
}

async function fetchFixtures() {
  const res = await fetch(`${BACKEND}/api/v1/dev/smoke/fixtures`)
  const json = await res.json()
  const map = {}
  for (const f of json.data) {
    const tc = f.tenant_name.match(/^TC(\d+)_SMOKE/)?.[1]
    if (tc) map[`TC${tc}`] = f
  }
  return map
}

async function getNotice(token, noticeId) {
  const res = await fetch(`${BACKEND}/api/v1/move-out-notices/${noticeId}`, {
    headers: { Authorization: `Bearer ${token}` },
  })
  return (await res.json()).data
}

// ─── Queue + drawer helpers ───────────────────────────────────────────

async function gotoQueue(page, tab) {
  // tab: 'settlement' | 'payment' | null
  await page.goto(`${FRONTEND}/move-out`)
  await page.waitForLoadState('networkidle')
  await page.waitForSelector('text=คิวงานย้ายออก', { timeout: 10000 })
  await page.waitForTimeout(400)
  const tabLabel = tab === 'settlement' ? 'สรุปยอด' : tab === 'payment' ? 'การเงิน' : null
  if (tabLabel) {
    const tabBtn = page.locator(`button:has-text("${tabLabel}")`).first()
    if (await tabBtn.isVisible().catch(() => false)) {
      await tabBtn.click()
      await page.waitForTimeout(300)
    }
  }
}

async function clickQueueCard(page, roomNumber) {
  const card = page.locator(`button:has-text("${roomNumber}")`).first()
  await card.waitFor({ state: 'visible', timeout: 5000 })
  await card.click()
  await page.waitForSelector(DRAWER, { timeout: 5000 })
  await page.waitForTimeout(400)
}

async function closeDrawer(page) {
  const closeBtn = page.locator(`${DRAWER} button[aria-label="ปิด"]`)
  if (await closeBtn.isVisible().catch(() => false)) {
    await closeBtn.click()
    await page.waitForTimeout(400)
  }
}

async function getSelectedStepIdx(page) {
  // The Stepper marks the selected step with aria-current="step".
  const labels = ['1 มิเตอร์', '2 สรุปยอด', '3 การเงิน', '4 ปิดสัญญา']
  for (let i = 0; i < labels.length; i++) {
    const btn = page.locator(`[aria-label="ขั้นตอนที่ ${labels[i]}"]`)
    const aria = await btn.getAttribute('aria-current').catch(() => null)
    if (aria === 'step') return i
  }
  return -1
}

// ─── Main ─────────────────────────────────────────────────────────────

async function main() {
  console.log('━━━ Move-out RoomWorkflowDrawer Step 2/3 Smoke ━━━')

  // Reset fixtures: cleanup first (mutated state from prior runs persists),
  // then seed. Without this the TC3 row may already be PENDING_PAYMENT and
  // Scenario A's preconditions don't hold.
  const cleanupRes = await fetch(`${BACKEND}/api/v1/dev/smoke/cleanup`, { method: 'POST' })
  if (!cleanupRes.ok) throw new Error(`Cleanup failed: ${cleanupRes.status}`)
  const seedRes = await fetch(`${BACKEND}/api/v1/dev/smoke/seed`, { method: 'POST' })
  if (!seedRes.ok) throw new Error(`Seed failed: ${seedRes.status}`)
  console.log('  ✅ Smoke fixtures cleaned + re-seeded')

  const fixtures = await fetchFixtures()
  const tc3 = fixtures.TC3
  if (!tc3) throw new Error('TC3 fixture missing')
  console.log(`  ℹ️  TC3 = ${tc3.room_number} / ${tc3.tenant_name} / status=${tc3.status} / has_draft=${tc3.has_draft}`)

  const token = await loginApi()
  const browser = await chromium.launch({ headless: false, slowMo: 60 })
  const ctx = await browser.newContext({ viewport: { width: 1400, height: 900 } })
  const page = await ctx.newPage()

  try {
    await login(page)
    console.log('  ✅ UI login complete')

    // ─── Scenario A: Step 2 confirm chain → bucket transition ───────────
    console.log('\n[A] Step 2 confirm: generate → finalize → advance, queue card re-buckets')

    await gotoQueue(page, 'settlement')
    check(
      'TC3 card visible on "สรุปยอด" tab before confirm',
      await page.locator(`button:has-text("${tc3.room_number}")`).first().isVisible().catch(() => false),
    )

    await clickQueueCard(page, tc3.room_number)
    check('Drawer opens at Step 2', (await getSelectedStepIdx(page)) === 1)

    const confirmCta = page.locator(`${DRAWER} button:has-text("ยืนยันยอด"), ${DRAWER} button:has-text("ไปชำระเงิน")`).first()
    await confirmCta.waitFor({ state: 'visible', timeout: 5000 })

    const finalizePromise = page.waitForResponse(
      (r) => r.url().includes(`/move-out-notices/${tc3.notice_id}/finalize-settlement`) && r.status() < 400,
      { timeout: 15000 },
    )
    await confirmCta.click()
    let finalizeResp
    try { finalizeResp = await finalizePromise } catch (_e) {}
    check('finalize-settlement endpoint hit', !!finalizeResp, finalizeResp ? `${finalizeResp.status()}` : 'no call')

    await page.waitForTimeout(1500)
    check('Drawer at Step 3 after confirm chain', (await getSelectedStepIdx(page)) === 2)

    const noticeA = await getNotice(token, tc3.notice_id)
    check('Backend status = PENDING_PAYMENT', noticeA.status === 'PENDING_PAYMENT', `status=${noticeA.status}`)
    check('Backend has finalized settlement_bill_id', !!noticeA.settlement_bill_id)

    await closeDrawer(page)
    await gotoQueue(page, 'settlement')
    check(
      'TC3 NO LONGER on "สรุปยอด" tab',
      !(await page.locator(`button:has-text("${tc3.room_number}")`).first().isVisible().catch(() => false)),
    )
    await gotoQueue(page, 'payment')
    check(
      'TC3 visible on "การเงิน" tab',
      await page.locator(`button:has-text("${tc3.room_number}")`).first().isVisible().catch(() => false),
    )

    // ─── Scenario B: edit-back nav vs. forward-only sync ────────────────
    console.log('\n[B] Step 3 → "แก้ยอดสรุป" → Step 2 → "ไปชำระเงิน" → Step 3 (advance only, no API)')

    await clickQueueCard(page, tc3.room_number)
    check('Drawer reopens at Step 3 (status synced from PENDING_PAYMENT)', (await getSelectedStepIdx(page)) === 2)

    const editSettlementBtn = page.locator(`${DRAWER} button:has-text("แก้ยอดสรุป")`)
    const editMeterBtn = page.locator(`${DRAWER} button:has-text("แก้มิเตอร์")`)
    check('"แก้ยอดสรุป" button visible', await editSettlementBtn.isVisible().catch(() => false))
    check('"แก้มิเตอร์" button visible', await editMeterBtn.isVisible().catch(() => false))

    await editSettlementBtn.click()
    await page.waitForTimeout(300)
    check('Click "แก้ยอดสรุป" navigates drawer to Step 2', (await getSelectedStepIdx(page)) === 1)

    const noticeBmid = await getNotice(token, tc3.notice_id)
    check('Backend status unchanged (still PENDING_PAYMENT)', noticeBmid.status === 'PENDING_PAYMENT')

    const advanceBtn = page.locator(`${DRAWER} button:has-text("ไปชำระเงิน")`).first()
    await advanceBtn.waitFor({ state: 'visible', timeout: 3000 })

    let unexpectedMutation = false
    const sniffer = (resp) => {
      const url = resp.url()
      if (
        (url.includes('/generate-settlement') || url.includes('/finalize-settlement') || url.includes('/regenerate-settlement')) &&
        url.includes(tc3.notice_id)
      ) unexpectedMutation = true
    }
    page.on('response', sniffer)
    await advanceBtn.click()
    await page.waitForTimeout(1500)
    page.off('response', sniffer)

    check('Drawer back at Step 3 after "ไปชำระเงิน"', (await getSelectedStepIdx(page)) === 2)
    check('No generate/finalize/regenerate fired (advance-only path)', !unexpectedMutation)

    // ─── Scenario C: meter edit revert + re-finalize ────────────────────
    console.log('\n[C] Step 3 → "แก้มิเตอร์" → save → Step 2 (recomputed banner) → confirm → Step 3')

    const editMeterBtn2 = page.locator(`${DRAWER} button:has-text("แก้มิเตอร์")`)
    await editMeterBtn2.click()
    await page.waitForTimeout(300)
    check('Click "แก้มิเตอร์" navigates drawer to Step 1', (await getSelectedStepIdx(page)) === 0)

    const elecInput = page.locator(`${DRAWER} #rwd_electricity`)
    await elecInput.waitFor({ state: 'visible', timeout: 5000 })
    const currentElec = await elecInput.inputValue()
    const numericElec = parseInt((currentElec || '').replace(/,/g, ''), 10)
    const newElec = (Number.isFinite(numericElec) ? numericElec : 1000) + 1
    await elecInput.fill(String(newElec))
    await page.waitForTimeout(200)

    const updateMeterPromise = page.waitForResponse(
      (r) => r.url().includes(`/move-out-notices/${tc3.notice_id}/update-exit-meter`) && r.status() < 400,
      { timeout: 10000 },
    )
    const meterSubmitBtn = page.locator(`${DRAWER} button:has-text("บันทึกการแก้ไข")`).first()
    await meterSubmitBtn.click()
    let updateMeterResp
    try { updateMeterResp = await updateMeterPromise } catch (_e) {}
    check('update-exit-meter endpoint hit', !!updateMeterResp, updateMeterResp ? `${updateMeterResp.status()}` : 'no call')

    const noticeAfterMeter = await getNotice(token, tc3.notice_id)
    check('Backend reverted to PENDING_SETTLEMENT', noticeAfterMeter.status === 'PENDING_SETTLEMENT')
    check('Backend has new DRAFT settlement_bill_id', !!noticeAfterMeter.settlement_bill_id)
    // Confirm new bill ID differs from the finalized one (void+regenerate, not no-op).
    check('New bill ID differs from prior finalized one', noticeAfterMeter.settlement_bill_id !== noticeA.settlement_bill_id)

    await page.waitForTimeout(1200)
    check('Drawer auto-advances to Step 2', (await getSelectedStepIdx(page)) === 1)

    const recomputedBanner = page.locator(`${DRAWER} :text("ยอดสรุปถูกคำนวณใหม่ตามมิเตอร์ล่าสุด")`)
    check('"ยอดสรุปถูกคำนวณใหม่" banner visible', await recomputedBanner.isVisible().catch(() => false))

    const confirmCta2 = page.locator(`${DRAWER} button:has-text("ยืนยันยอด"), ${DRAWER} button:has-text("ไปชำระเงิน")`).first()
    await confirmCta2.waitFor({ state: 'visible', timeout: 5000 })
    const finalize2Promise = page.waitForResponse(
      (r) => r.url().includes(`/move-out-notices/${tc3.notice_id}/finalize-settlement`) && r.status() < 400,
      { timeout: 10000 },
    )
    await confirmCta2.click()
    let finalize2Resp
    try { finalize2Resp = await finalize2Promise } catch (_e) {}
    check('finalize-settlement fires on Step 2 reconfirm (HAS_DRAFT path)', !!finalize2Resp)

    await page.waitForTimeout(1500)
    check('Drawer back at Step 3 after re-finalize', (await getSelectedStepIdx(page)) === 2)

    const noticeC = await getNotice(token, tc3.notice_id)
    check('Backend status = PENDING_PAYMENT after re-finalize', noticeC.status === 'PENDING_PAYMENT')

    await closeDrawer(page)

    console.log(`\n━━━ Result: ${results.pass}/${results.total} pass, ${results.fail} fail ━━━`)
    if (results.failedCases.length) {
      console.log('Failed checks:')
      for (const name of results.failedCases) console.log(`  • ${name}`)
    }
  } finally {
    await browser.close()
  }

  process.exit(results.fail === 0 ? 0 : 1)
}

main().catch((err) => {
  console.error('FATAL:', err)
  process.exit(2)
})
