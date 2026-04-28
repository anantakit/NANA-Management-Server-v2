// Move-out Phase 2 — Step 4 Close-with-Unsettled Smoke Test (Scenarios A–E)
// ----------------------------------------------------------------------
// Validates the Phase-2 close-with-unsettled + post-close back-fill flow on
// the RoomWorkflowDrawer:
//
//   A  PENDING_PAYMENT + null → record settled → close → history
//      Drawer at Step 3 shows form + edit-back ("แก้มิเตอร์", "แก้ยอดสรุป").
//      After record, drawer auto-advances to Step 4. "ปิดสัญญา" closes the
//      contract. Card lands in history; backend = COMPLETED + outcome set.
//
//   B  PENDING_PAYMENT + null → skip → close-with-unsettled
//      User clicks "ยังไม่ชำระ" (SkipPayment → READY_TO_CLOSE+null), drawer
//      advances to Step 4, then "ปิดงาน (ยังไม่ชำระ)" fires close-with-
//      unsettled. Backend = COMPLETED + payment_outcome = null. Drawer
//      STAYS at Step 4 (no auto-dismiss) and re-renders into the post-
//      close unsettled view — operator can immediately back-fill or X to
//      dismiss. Card STAYS in the payment tab.
//
//   C  COMPLETED + nil re-entry from queue (chained from Scenario B)
//      Re-open the same card from the payment tab. Drawer routes to
//      Step 3 (statusToCurrentIdx returns 2 for COMPLETED+nil). PaymentStep
//      renders the form DIRECTLY (no SkippedRecap intermediate, no blank).
//      Edit-back buttons are hidden (status > PENDING_PAYMENT). Submit
//      records payment → notice becomes COMPLETED+settled → card moves to
//      history. Pins the bug "PaymentStep blank for COMPLETED+UNSETTLED".
//
//   D  ZERO_BALANCE back-fill — payment_method must stay nil
//      Set up a COMPLETED+nil notice with net_amount=0 via API (generate +
//      finalize + close-with-unsettled). Re-enter via UI, derived outcome
//      is ZERO_BALANCE (no method section). Submit. Backend forces
//      payment_method = null even if FE were to send a stale value.
//      Status stays COMPLETED.
//
//   E  Step 4 "ไปชำระเงิน" → Step 3 → record → Step 4 confirm
//      Tests the in-drawer back-fill path (vs Scenario C which re-opens
//      from queue). After close-with-unsettled drawer stays at Step 4,
//      operator clicks "ไปชำระเงิน" → drawer at Step 3, form renders
//      directly with banner, no edit-back. Submit → drawer advances to
//      Step 4 showing CompletedRecap. Pins:
//        - drawer NOT auto-dismissed after close-with-unsettled
//        - "ไปชำระเงิน" navigates to Step 3 (NOT Step 2)
//        - PaymentStep form for COMPLETED+nil is not blank
//        - back-fill submit advances to Step 4 with CompletedRecap
//        - status STAYS COMPLETED (no revert to PENDING_PAYMENT)
//
// Fixtures (all start at PENDING_SETTLEMENT, advanced via API):
//   - TC22 (D201, has draft + manual items)        → Scenario A
//   - TC23 (D202, has draft + FULL_MONTH)          → Scenarios B + C (chained)
//   - TC24 (D103, ZERO_BALANCE net_amount, no draft) → Scenario D
//   - TC4  (B202, has draft)                       → Scenario E
//
// Safe to run independently: cleanup+seed cycle at the top resets state.
// Run:  npm run smoke:moveout-step4   (from backend/devtools/smoke/)
//       make smoke-moveout-step4      (from backend/)

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
    await page.fill('input[name="new_password"]', ADMIN_PASS_POST)
    await page.fill('input[name="confirm_password"]', ADMIN_PASS_POST)
    await page.click('button[type="submit"]')
    await page.waitForLoadState('networkidle')
  }
  await page.waitForFunction(() => !window.location.pathname.includes('/login'), { timeout: 10000 })
}

async function loginApi() {
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

// ─── API helpers (state setup is faster via API than UI) ──────────────

async function apiPost(token, path, body) {
  const res = await fetch(`${BACKEND}${path}`, {
    method: 'POST',
    headers: {
      Authorization: `Bearer ${token}`,
      'Content-Type': 'application/json',
    },
    body: body ? JSON.stringify(body) : undefined,
  })
  if (!res.ok) {
    const text = await res.text()
    throw new Error(`POST ${path} failed: ${res.status} ${text}`)
  }
  return (await res.json()).data
}

async function getNotice(token, noticeId) {
  const res = await fetch(`${BACKEND}/api/v1/move-out-notices/${noticeId}`, {
    headers: { Authorization: `Bearer ${token}` },
  })
  if (!res.ok) throw new Error(`GET notice failed: ${res.status}`)
  return (await res.json()).data
}

// Advance PENDING_SETTLEMENT (with draft) → PENDING_PAYMENT in one API call.
async function bootstrapPendingPayment(token, noticeId) {
  await apiPost(token, `/api/v1/move-out-notices/${noticeId}/finalize-settlement`)
}

// Advance PENDING_SETTLEMENT (no draft) → COMPLETED+nil in three API calls.
// Used to set up Scenario D's fixture: generate the bill, finalize it,
// then close-with-unsettled — all without touching the UI so the test
// can focus on the back-fill flow.
async function bootstrapCompletedUnsettled(token, noticeId) {
  await apiPost(token, `/api/v1/move-out-notices/${noticeId}/generate-settlement`)
  await apiPost(token, `/api/v1/move-out-notices/${noticeId}/finalize-settlement`)
  await apiPost(token, `/api/v1/move-out-notices/${noticeId}/close-with-unsettled`)
}

// ─── Queue + drawer helpers ───────────────────────────────────────────

async function gotoQueue(page, tab /* 'meter'|'settlement'|'payment'|'close'|null */) {
  await page.goto(`${FRONTEND}/move-out`)
  await page.waitForLoadState('networkidle')
  await page.waitForSelector('text=คิวงานย้ายออก', { timeout: 10000 })
  await page.waitForTimeout(400)
  const tabLabel = {
    meter: 'มิเตอร์',
    settlement: 'สรุปยอด',
    payment: 'การเงิน',
    close: 'ปิดสัญญา',
  }[tab]
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
  await page.waitForTimeout(500)
}

async function isCardOnTab(page, roomNumber) {
  return page.locator(`button:has-text("${roomNumber}")`).first().isVisible().catch(() => false)
}

async function closeDrawer(page) {
  const closeBtn = page.locator(`${DRAWER} button[aria-label="ปิด"]`)
  if (await closeBtn.isVisible().catch(() => false)) {
    await closeBtn.click()
    await page.waitForTimeout(400)
  }
}

async function getSelectedStepIdx(page) {
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
  console.log('━━━ Move-out Phase 2 Step 4 (Close-with-Unsettled) Smoke — Scenarios A–E ━━━')

  // Reset fixtures: cleanup first then seed. Without this the TC fixtures
  // may already be advanced past PENDING_SETTLEMENT from prior runs and
  // the API bootstrap would 400.
  const cleanupRes = await fetch(`${BACKEND}/api/v1/dev/smoke/cleanup`, { method: 'POST' })
  if (!cleanupRes.ok) throw new Error(`Cleanup failed: ${cleanupRes.status}`)
  const seedRes = await fetch(`${BACKEND}/api/v1/dev/smoke/seed`, { method: 'POST' })
  if (!seedRes.ok) throw new Error(`Seed failed: ${seedRes.status}`)
  console.log('  ✅ Smoke fixtures cleaned + re-seeded')

  const fixtures = await fetchFixtures()
  const tc4 = fixtures.TC4   // PENDING_SETTLEMENT + draft (Scenario E)
  const tc22 = fixtures.TC22 // PENDING_SETTLEMENT + draft + manual
  const tc23 = fixtures.TC23 // PENDING_SETTLEMENT + draft + FULL_MONTH
  const tc24 = fixtures.TC24 // PENDING_SETTLEMENT, no draft, ZERO net
  for (const [name, f] of [['TC4', tc4], ['TC22', tc22], ['TC23', tc23], ['TC24', tc24]]) {
    if (!f) throw new Error(`${name} fixture missing`)
    console.log(`  ℹ️  ${name} = ${f.room_number} / status=${f.status} / has_draft=${f.has_draft}`)
  }

  const token = await loginApi()

  // Bootstrap fixture states via API (faster + isolates UI test from setup work):
  //   TC22 → PENDING_PAYMENT (Scenario A entry)
  //   TC23 → PENDING_PAYMENT (Scenario B entry, chained to C)
  //   TC24 → COMPLETED + nil + net_amount=0 (Scenario D entry)
  //   TC4  → PENDING_PAYMENT (Scenario E entry)
  await bootstrapPendingPayment(token, tc22.notice_id)
  await bootstrapPendingPayment(token, tc23.notice_id)
  await bootstrapCompletedUnsettled(token, tc24.notice_id)
  await bootstrapPendingPayment(token, tc4.notice_id)
  console.log('  ✅ Fixtures bootstrapped via API (TC22+TC23+TC4 → PENDING_PAYMENT, TC24 → COMPLETED+nil)')

  const browser = await chromium.launch({ headless: false, slowMo: 60 })
  const ctx = await browser.newContext({ viewport: { width: 1400, height: 900 } })
  const page = await ctx.newPage()

  try {
    await login(page)
    console.log('  ✅ UI login complete')

    // ─── Scenario A: PENDING_PAYMENT → record settled → close → history ───
    console.log('\n[A] Scenario 1: PENDING_PAYMENT + null → record → close → history')

    await gotoQueue(page, 'payment')
    check('TC22 visible on "การเงิน" tab (PENDING_PAYMENT)', await isCardOnTab(page, tc22.room_number))

    await clickQueueCard(page, tc22.room_number)
    check('Drawer opens at Step 3 (Payment) for PENDING_PAYMENT', (await getSelectedStepIdx(page)) === 2)

    const editMeterBtn = page.locator(`${DRAWER} button:has-text("แก้มิเตอร์")`)
    const editSettlementBtn = page.locator(`${DRAWER} button:has-text("แก้ยอดสรุป")`)
    check('"แก้มิเตอร์" visible on PENDING_PAYMENT initial entry', await editMeterBtn.isVisible().catch(() => false))
    check('"แก้ยอดสรุป" visible on PENDING_PAYMENT initial entry', await editSettlementBtn.isVisible().catch(() => false))

    // Pick CASH (TC22 has manual items → likely PAY_MORE outcome → method required).
    const cashBtn = page.locator(`${DRAWER} button:has-text("เงินสด")`).first()
    await cashBtn.waitFor({ state: 'visible', timeout: 3000 })
    await cashBtn.click()
    await page.waitForTimeout(200)

    const submitBtn = page.locator(`${DRAWER} button:has-text("บันทึกรับเงิน"), ${DRAWER} button:has-text("บันทึกคืนเงิน"), ${DRAWER} button:has-text("ยืนยันปิดรายการ")`).first()
    await submitBtn.waitFor({ state: 'visible', timeout: 3000 })
    const recordPromise = page.waitForResponse(
      (r) => r.url().includes(`/move-out-notices/${tc22.notice_id}/record-payment`) && r.status() < 400,
      { timeout: 10000 },
    )
    await submitBtn.click()
    let recordResp
    try { recordResp = await recordPromise } catch (_e) {}
    check('record-payment endpoint hit', !!recordResp, recordResp ? `${recordResp.status()}` : 'no call')

    await page.waitForTimeout(1500)
    check('Drawer auto-advances to Step 4 after record', (await getSelectedStepIdx(page)) === 3)

    const closeSettledBtn = page.locator(`${DRAWER} button:has-text("ปิดสัญญา")`).last()
    await closeSettledBtn.waitFor({ state: 'visible', timeout: 3000 })
    const closePromise = page.waitForResponse(
      (r) => r.url().includes(`/move-out-notices/${tc22.notice_id}/close`)
          && !r.url().includes('/close-with-unsettled')
          && r.status() < 400,
      { timeout: 10000 },
    )
    await closeSettledBtn.click()
    let closeResp
    try { closeResp = await closePromise } catch (_e) {}
    check('/close (settled) endpoint hit', !!closeResp, closeResp ? `${closeResp.status()}` : 'no call')
    await page.waitForTimeout(1200)

    const noticeA = await getNotice(token, tc22.notice_id)
    check('TC22 backend status = COMPLETED', noticeA.status === 'COMPLETED', `status=${noticeA.status}`)
    check('TC22 backend payment_outcome SET (settled close)', !!noticeA.payment_outcome, `outcome=${noticeA.payment_outcome}`)

    await gotoQueue(page, 'payment')
    check('TC22 NOT in payment tab anymore (settled → history)', !(await isCardOnTab(page, tc22.room_number)))

    // ─── Scenario B: PENDING_PAYMENT → skip → close-with-unsettled ───────
    console.log('\n[B] Scenario 2: PENDING_PAYMENT → skip → "ปิดงาน (ยังไม่ชำระ)" → COMPLETED+nil')

    check('TC23 visible on "การเงิน" tab (PENDING_PAYMENT)', await isCardOnTab(page, tc23.room_number))
    await clickQueueCard(page, tc23.room_number)
    check('Drawer opens at Step 3 for PENDING_PAYMENT', (await getSelectedStepIdx(page)) === 2)

    // Skip via "ยังไม่ชำระ" — fires SkipPayment → READY_TO_CLOSE+null,
    // drawer auto-advances to Step 4 via currentIdx sync.
    const skipBtn = page.locator(`${DRAWER} button:has-text("ยังไม่ชำระ")`).first()
    await skipBtn.waitFor({ state: 'visible', timeout: 3000 })
    const skipPromise = page.waitForResponse(
      (r) => r.url().includes(`/move-out-notices/${tc23.notice_id}/skip-payment`) && r.status() < 400,
      { timeout: 10000 },
    )
    await skipBtn.click()
    let skipResp
    try { skipResp = await skipPromise } catch (_e) {}
    check('skip-payment endpoint hit', !!skipResp, skipResp ? `${skipResp.status()}` : 'no call')

    await page.waitForTimeout(1200)
    check('Drawer at Step 4 after skip', (await getSelectedStepIdx(page)) === 3)

    const closeUnsettledBtn = page.locator(`${DRAWER} button:has-text("ปิดงาน (ยังไม่ชำระ)")`).first()
    await closeUnsettledBtn.waitFor({ state: 'visible', timeout: 3000 })
    const closeUnsettledPromise = page.waitForResponse(
      (r) => r.url().includes(`/move-out-notices/${tc23.notice_id}/close-with-unsettled`) && r.status() < 400,
      { timeout: 10000 },
    )
    await closeUnsettledBtn.click()
    let closeUnsettledResp
    try { closeUnsettledResp = await closeUnsettledPromise } catch (_e) {}
    check('/close-with-unsettled endpoint hit', !!closeUnsettledResp, closeUnsettledResp ? `${closeUnsettledResp.status()}` : 'no call')
    await page.waitForTimeout(1200)

    const noticeB = await getNotice(token, tc23.notice_id)
    check('TC23 backend status = COMPLETED', noticeB.status === 'COMPLETED', `status=${noticeB.status}`)
    check('TC23 backend payment_outcome STILL nil/undef (close-with-unsettled does not modify)',
      noticeB.payment_outcome == null, `outcome=${noticeB.payment_outcome}`)
    check('TC23 backend settlement_bill_id preserved (still attached)', !!noticeB.settlement_bill_id)

    // Drawer should NOT auto-dismiss after close-with-unsettled — operator
    // gets to choose: back-fill via "ไปชำระเงิน" or X to close. The post-
    // close view replaces the pre-close view in CloseStep.
    const drawerStillOpen = await page.locator(DRAWER).isVisible().catch(() => false)
    check('Drawer STILL OPEN after close-with-unsettled (no auto-dismiss)', drawerStillOpen)
    check('Drawer STILL at Step 4 (post-close view)', (await getSelectedStepIdx(page)) === 3)
    // Locked vocabulary: post-close interim title = "ปิดแล้ว" (green
    // success panel). Pins both the title text AND the "did my click
    // fire?" UX confusion fix — without the explicit confirmation panel
    // the drawer-stays-open behavior reads as ambiguous.
    const postCloseTitle = page.locator(`${DRAWER} :text("ปิดแล้ว")`).first()
    check('Step 4 shows "ปิดแล้ว" success confirmation', await postCloseTitle.isVisible().catch(() => false))
    const postCloseWarning = page.locator(`${DRAWER} :text("ยังไม่บันทึกการเงิน")`).first()
    check('Step 4 shows "ยังไม่บันทึกการเงิน" pending-work warning', await postCloseWarning.isVisible().catch(() => false))
    const postCloseBackBtn = page.locator(`${DRAWER} button:has-text("ไปชำระเงิน")`).first()
    check('Step 4 post-close has "ไปชำระเงิน" CTA', await postCloseBackBtn.isVisible().catch(() => false))

    // Close drawer manually before checking queue (post-close view doesn't
    // auto-dismiss).
    await closeDrawer(page)
    await gotoQueue(page, 'payment')
    check('TC23 STILL on "การเงิน" tab (COMPLETED+UNSETTLED routes here)', await isCardOnTab(page, tc23.room_number))

    // Section grouping: payment tab splits into two clearly-labeled groups
    // so operators can scan "new work" vs "post-close back-fill" at a
    // glance. At this checkpoint TC4 is in Section A (PENDING_PAYMENT,
    // bootstrapped earlier), TC23 + TC24 are in Section B (COMPLETED+nil).
    // Headers hide when their section is empty — the assertions below
    // confirm both sections currently populate. Use h3 selector to avoid
    // matching the same substring in card hints below the header.
    const sectionAHeader = page.locator('h3:has-text("ยังไม่ปิดสัญญา")').first()
    const sectionBHeader = page.locator('h3:has-text("ปิดแล้ว • ค้างชำระ")').first()
    check('Payment tab shows "ยังไม่ปิดสัญญา" header (Section A populated)',
      await sectionAHeader.isVisible().catch(() => false))
    check('Payment tab shows "ปิดแล้ว • ค้างชำระ" header (Section B populated)',
      await sectionBHeader.isVisible().catch(() => false))

    // Card-level signal: every COMPLETED+nil card in Section B must
    // visibly say it's closed AND carry the financial state, so the user
    // doesn't need to open the drawer to tell normal pending-payment
    // cards apart from post-close back-fill cards.
    //
    //   TC23 (FULL_MONTH settlement) — non-zero net → number-bearing variant
    //   TC24 (ZERO_BALANCE seed)     — zero net → no-amount variant
    //
    // Both must appear inline on the card. Pinned here to catch any
    // future regression of the showFinancial gating that caused the
    // original "card looks like normal pending-payment" bug.
    const tc23CardHint = page
      .locator(`button:has-text("${tc23.room_number}")`)
      .filter({ hasText: /ปิดแล้ว • ค้างชำระ ฿/ })
      .first()
    check('TC23 card shows "ปิดแล้ว • ค้างชำระ ฿X" (non-zero variant)',
      await tc23CardHint.isVisible().catch(() => false))
    const tc24CardHint = page
      .locator(`button:has-text("${tc24.room_number}"):has-text("ปิดแล้ว • ยังไม่บันทึกการเงิน")`)
      .first()
    check('TC24 card shows "ปิดแล้ว • ยังไม่บันทึกการเงิน" (zero-amount variant)',
      await tc24CardHint.isVisible().catch(() => false))
    // Negative-space guard: never render "ค้างชำระ ฿0.00" — confusing UX.
    const zeroDisplayHack = page.locator(':text-matches("ค้างชำระ\\\\s*฿0(?:[.,]00)?\\\\b")').first()
    check('No card shows "ค้างชำระ ฿0.00" (zero-amount renders text-only variant)',
      !(await zeroDisplayHack.isVisible().catch(() => false)))

    // ─── Scenario C: COMPLETED+nil re-entry → form direct → record → history ──
    console.log('\n[C] Scenario 3: COMPLETED+nil re-entry → form (no edit-back) → record → history')

    await clickQueueCard(page, tc23.room_number)
    check('Drawer opens at Step 3 for COMPLETED+nil (back-fill)', (await getSelectedStepIdx(page)) === 2)

    // Critical regression guard: PaymentStep must NOT render blank for
    // COMPLETED+nil. Form controls (method buttons OR submit CTA) must be
    // visible and interactive — that's the user-visible signal that the
    // back-fill flow is wired correctly.
    const submitBtnReentry = page.locator(`${DRAWER} button:has-text("บันทึกรับเงิน"), ${DRAWER} button:has-text("บันทึกคืนเงิน"), ${DRAWER} button:has-text("ยืนยันปิดรายการ")`).first()
    check('PaymentStep renders form (not blank) on re-entry', await submitBtnReentry.isVisible().catch(() => false))

    // Edit-back must be hidden (operational close already happened, backend
    // UpdateExitMeter would 400). Skip must be hidden too (SkipPayment
    // rejects past PENDING_PAYMENT).
    const editMeterRe = page.locator(`${DRAWER} button:has-text("แก้มิเตอร์")`)
    const editSettlementRe = page.locator(`${DRAWER} button:has-text("แก้ยอดสรุป")`)
    const skipBtnRe = page.locator(`${DRAWER} button:has-text("ยังไม่ชำระ")`)
    check('"แก้มิเตอร์" hidden on COMPLETED+nil', !(await editMeterRe.isVisible().catch(() => false)))
    check('"แก้ยอดสรุป" hidden on COMPLETED+nil', !(await editSettlementRe.isVisible().catch(() => false)))
    check('"ยังไม่ชำระ" hidden on COMPLETED+nil', !(await skipBtnRe.isVisible().catch(() => false)))

    // Pick method (TC23 likely PAY_MORE/REFUND non-zero) and submit. Sticking
    // to TRANSFER for variety vs Scenario A's CASH choice.
    const transferBtn = page.locator(`${DRAWER} button:has-text("โอนเงิน")`).first()
    if (await transferBtn.isVisible().catch(() => false)) {
      await transferBtn.click()
      await page.waitForTimeout(200)
    }

    const reentryRecordPromise = page.waitForResponse(
      (r) => r.url().includes(`/move-out-notices/${tc23.notice_id}/record-payment`) && r.status() < 400,
      { timeout: 10000 },
    )
    await submitBtnReentry.click()
    let reentryResp
    try { reentryResp = await reentryRecordPromise } catch (_e) {}
    check('record-payment endpoint hit on COMPLETED+nil back-fill', !!reentryResp, reentryResp ? `${reentryResp.status()}` : 'no call')
    await page.waitForTimeout(1200)

    const noticeC = await getNotice(token, tc23.notice_id)
    check('TC23 backend status STAYS COMPLETED (no contract reopen)',
      noticeC.status === 'COMPLETED', `status=${noticeC.status}`)
    check('TC23 backend payment_outcome NOW SET (back-fill recorded)',
      !!noticeC.payment_outcome, `outcome=${noticeC.payment_outcome}`)

    await closeDrawer(page)
    await gotoQueue(page, 'payment')
    check('TC23 NOT on payment tab anymore (COMPLETED+settled → history)', !(await isCardOnTab(page, tc23.room_number)))

    // ─── Scenario E: Step 4 → "ไปชำระเงิน" → Step 3 → record → Step 4 ─────
    console.log('\n[E] Scenario 5: Step 4 → "ไปชำระเงิน" → Step 3 (back-fill in-drawer) → Step 4 confirm')

    // Different fixture from B/C — TC4 lets us re-test the close-with-
    // unsettled → "ไปชำระเงิน" → record → confirm chain end-to-end without
    // depending on prior scenario state.
    check('TC4 visible on payment tab (PENDING_PAYMENT after API finalize)',
      await isCardOnTab(page, tc4.room_number))

    await clickQueueCard(page, tc4.room_number)
    check('Drawer opens at Step 3 for PENDING_PAYMENT', (await getSelectedStepIdx(page)) === 2)

    // Skip → READY_TO_CLOSE+nil → drawer auto-advances to Step 4
    const skipBtnE = page.locator(`${DRAWER} button:has-text("ยังไม่ชำระ")`).first()
    await skipBtnE.click()
    await page.waitForTimeout(1200)
    check('Drawer at Step 4 after skip (PRE-close, READY_TO_CLOSE+nil)',
      (await getSelectedStepIdx(page)) === 3)

    // Close-with-unsettled → COMPLETED+nil. Drawer must STAY at Step 4 and
    // re-render with the post-close view.
    const closeUnsettledBtnE = page.locator(`${DRAWER} button:has-text("ปิดงาน (ยังไม่ชำระ)")`).first()
    const closeUnsettledPromiseE = page.waitForResponse(
      (r) => r.url().includes(`/move-out-notices/${tc4.notice_id}/close-with-unsettled`) && r.status() < 400,
      { timeout: 10000 },
    )
    await closeUnsettledBtnE.click()
    let closeUnsettledRespE
    try { closeUnsettledRespE = await closeUnsettledPromiseE } catch (_e) {}
    check('TC4 /close-with-unsettled endpoint hit', !!closeUnsettledRespE)
    await page.waitForTimeout(1200)

    const noticeEAfterClose = await getNotice(token, tc4.notice_id)
    check('TC4 backend = COMPLETED + payment_outcome nil after close-with-unsettled',
      noticeEAfterClose.status === 'COMPLETED' && noticeEAfterClose.payment_outcome == null,
      `status=${noticeEAfterClose.status} outcome=${noticeEAfterClose.payment_outcome}`)

    // Drawer-stays-open invariant — the whole point of Scenario E.
    check('TC4 drawer STILL OPEN after close-with-unsettled', await page.locator(DRAWER).isVisible().catch(() => false))
    check('TC4 drawer STILL at Step 4 (post-close view, NOT Step 2)',
      (await getSelectedStepIdx(page)) === 3)

    // Click "ไปชำระเงิน" → must navigate to Step 3 (NOT Step 2 — that's the
    // "ผิด step" failure mode the user explicitly called out).
    const goToPaymentBtnE = page.locator(`${DRAWER} button:has-text("ไปชำระเงิน")`).first()
    await goToPaymentBtnE.waitFor({ state: 'visible', timeout: 3000 })
    await goToPaymentBtnE.click()
    await page.waitForTimeout(500)
    check('Drawer at Step 3 after "ไปชำระเงิน" (NOT Step 2)',
      (await getSelectedStepIdx(page)) === 2)

    // Step 3 form-renders for COMPLETED+UNSETTLED. Pin all "ต้องไม่เกิด"
    // failure modes from the spec:
    const submitBtnE = page.locator(`${DRAWER} button:has-text("บันทึกรับเงิน"), ${DRAWER} button:has-text("บันทึกคืนเงิน"), ${DRAWER} button:has-text("ยืนยันปิดรายการ")`).first()
    check('Step 3 form-renders (NOT blank) for COMPLETED+nil via in-drawer nav',
      await submitBtnE.isVisible().catch(() => false))
    const bannerE = page.locator(`${DRAWER} :text("ยังไม่บันทึกการเงิน")`).first()
    check('Step 3 shows "ยังไม่บันทึกการเงิน" banner', await bannerE.isVisible().catch(() => false))
    const editMeterE = page.locator(`${DRAWER} button:has-text("แก้มิเตอร์")`)
    const editSettlementE = page.locator(`${DRAWER} button:has-text("แก้ยอดสรุป")`)
    check('Step 3 NO "แก้มิเตอร์" (operational close already happened)',
      !(await editMeterE.isVisible().catch(() => false)))
    check('Step 3 NO "แก้ยอดสรุป" (operational close already happened)',
      !(await editSettlementE.isVisible().catch(() => false)))

    // Pick method + submit. The form is the same one PaymentStep would
    // render for any other isUnsettled back-fill — same code path as
    // Scenario C but reached via in-drawer nav instead of queue re-entry.
    const cashBtnE = page.locator(`${DRAWER} button:has-text("เงินสด")`).first()
    if (await cashBtnE.isVisible().catch(() => false)) {
      await cashBtnE.click()
      await page.waitForTimeout(200)
    }
    const recordPromiseE = page.waitForResponse(
      (r) => r.url().includes(`/move-out-notices/${tc4.notice_id}/record-payment`) && r.status() < 400,
      { timeout: 10000 },
    )
    await submitBtnE.click()
    let recordRespE
    try { recordRespE = await recordPromiseE } catch (_e) {}
    check('TC4 record-payment endpoint hit (back-fill on COMPLETED+nil)', !!recordRespE)
    await page.waitForTimeout(1500)

    // After back-fill: status STAYS COMPLETED, outcome set. Drawer
    // advances to Step 4 showing CompletedRecap.
    const noticeERecorded = await getNotice(token, tc4.notice_id)
    check('TC4 backend status STAYS COMPLETED (no revert to PENDING_PAYMENT)',
      noticeERecorded.status === 'COMPLETED', `status=${noticeERecorded.status}`)
    check('TC4 backend payment_outcome NOW SET',
      !!noticeERecorded.payment_outcome, `outcome=${noticeERecorded.payment_outcome}`)
    check('Drawer advanced to Step 4 after back-fill record',
      (await getSelectedStepIdx(page)) === 3)
    const completedRecap = page.locator(`${DRAWER} :text("ปิดสัญญาเรียบร้อย")`).first()
    check('Step 4 shows "ปิดสัญญาเรียบร้อย" CompletedRecap',
      await completedRecap.isVisible().catch(() => false))

    await closeDrawer(page)
    await gotoQueue(page, 'payment')
    check('TC4 NOT on payment tab anymore (COMPLETED+settled → history)',
      !(await isCardOnTab(page, tc4.room_number)))

    // ─── Scenario D: ZERO_BALANCE back-fill — method must stay nil ────────
    console.log('\n[D] Scenario 4: ZERO_BALANCE back-fill — payment_method must be nil')

    // Confirm TC24 was bootstrapped to COMPLETED+nil with net_amount=0.
    const tc24Pre = await getNotice(token, tc24.notice_id)
    check('TC24 pre-state: COMPLETED+nil + net_amount=0',
      tc24Pre.status === 'COMPLETED' && tc24Pre.payment_outcome == null && (tc24Pre.net_amount ?? 0) === 0,
      `status=${tc24Pre.status} outcome=${tc24Pre.payment_outcome} net=${tc24Pre.net_amount}`)

    check('TC24 visible on payment tab (COMPLETED+UNSETTLED + ZERO net)', await isCardOnTab(page, tc24.room_number))

    await clickQueueCard(page, tc24.room_number)
    check('Drawer opens at Step 3 for COMPLETED+nil (ZERO case)', (await getSelectedStepIdx(page)) === 2)

    // ZERO_BALANCE flow: outcome derived from net_amount=0 → no method
    // section. Submit button label = "ยืนยันปิดรายการ".
    const zeroSubmitBtn = page.locator(`${DRAWER} button:has-text("ยืนยันปิดรายการ")`).first()
    check('"ยืนยันปิดรายการ" submit visible (ZERO_BALANCE outcome derived)',
      await zeroSubmitBtn.isVisible().catch(() => false))

    // Method buttons MUST be hidden — invariant: ZERO_BALANCE has no method.
    const cashBtnZero = page.locator(`${DRAWER} button:has-text("เงินสด")`)
    const transferBtnZero = page.locator(`${DRAWER} button:has-text("โอนเงิน")`)
    check('"เงินสด" hidden for ZERO_BALANCE', !(await cashBtnZero.isVisible().catch(() => false)))
    check('"โอนเงิน" hidden for ZERO_BALANCE', !(await transferBtnZero.isVisible().catch(() => false)))

    const zeroRecordPromise = page.waitForResponse(
      (r) => r.url().includes(`/move-out-notices/${tc24.notice_id}/record-payment`) && r.status() < 400,
      { timeout: 10000 },
    )
    await zeroSubmitBtn.click()
    let zeroResp
    try { zeroResp = await zeroRecordPromise } catch (_e) {}
    check('record-payment ZERO_BALANCE endpoint hit', !!zeroResp, zeroResp ? `${zeroResp.status()}` : 'no call')
    await page.waitForTimeout(1200)

    const noticeD = await getNotice(token, tc24.notice_id)
    check('TC24 backend status STAYS COMPLETED', noticeD.status === 'COMPLETED', `status=${noticeD.status}`)
    check('TC24 backend payment_outcome = ZERO_BALANCE',
      noticeD.payment_outcome === 'ZERO_BALANCE', `outcome=${noticeD.payment_outcome}`)
    // INVARIANT: ZERO_BALANCE → method must be nil regardless of FE state.
    // Service forces this at entry — strict assertion locks it in smoke.
    check('TC24 backend payment_method = null/undef (ZERO_BALANCE invariant)',
      noticeD.payment_method == null, `method=${noticeD.payment_method}`)

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
