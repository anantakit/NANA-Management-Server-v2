// MoveOutDetailPage — Behavior Smoke Test
// ----------------------------------------------------------------------
// Pins the user-visible *behavior* of the move-out detail page (not just
// its HTML). Every assertion that checks a UI element ALSO verifies the
// *consequence* of acting on it — drawer step idx, /api/v1/... response,
// or backend status re-fetch. The previous round of smokes passed while
// behavior regressed; the rule here is "no false pass".
//
// Critical assertions (T1, T2, T3, T4, T5, T5b, T6a, T6b, T7, T8):
//   T1   PENDING_PAYMENT     — primary CTA opens drawer at Step 3 (Payment).
//                              Cancel/reopen text links MUST NOT exist
//                              (backend CanCancel disallows; reopen only
//                              valid in READY_TO_CLOSE).
//   T2   PENDING_SETTLEMENT  — both with-draft and no-draft must show
//                              "ยังไม่มีสรุปยอด" and MUST NOT render the
//                              ResultBlock's "ต้องคืนเงินประกัน / ต้อง
//                              ชำระเพิ่ม / ยอดสุทธิ" amount labels (the
//                              "฿0 hero" regression fixed by the
//                              PendingSettlementBlock split). CTA opens
//                              drawer at Step 2 (Settlement).
//   T3   READY_TO_CLOSE      — primary CTA label = "ปิดการย้ายออก", opens
//                              drawer at Step 4. The subtle bottom link
//                              "กลับไปบันทึกการชำระ" → confirm prompt
//                              "ยืนยันกลับไปบันทึกการชำระ" → confirm
//                              button "กลับไปบันทึกการชำระ" hits /reopen
//                              and backend transitions
//                              READY_TO_CLOSE → PENDING_PAYMENT (clears
//                              payment_outcome). Copy now matches the
//                              actual backend behavior.
//   T4   CANCEL flow         — link visible only in PENDING_METER /
//                              PENDING_SETTLEMENT. Confirm button copy
//                              "ยืนยันยกเลิก" (verb-differentiated from
//                              trigger "ยกเลิกแจ้งย้ายออก"). /cancel →
//                              backend status = CANCELLED. PENDING_PAYMENT
//                              and READY_TO_CLOSE MUST NOT render the
//                              link (CanCancel rule).
//   T5   COMPLETED+settled   — no primary CTA, no cancel link, no reopen
//                              link, no restart CTA, no back-fill CTA.
//                              Green "ปิดสัญญาเรียบร้อย" OutcomeCard visible.
//   T5b  COMPLETED+nil       — closed-with-unsettled. OutcomeCard renders
//                              the warning-tone unsettled variant
//                              ("ปิดสัญญาแล้ว" / "ยังไม่บันทึกการเงิน")
//                              with a "บันทึกการชำระ" CTA that opens the
//                              drawer at Step 3 (Payment) for back-fill.
//                              Green success title MUST NOT appear.
//   T6a  CANCELLED+had-draft — backend voids the bill but leaves
//                              settlement_bill_id set. The page must
//                              self-gate: NO "รายละเอียดค่าใช้จ่าย"
//                              accordion, NO "รวมทั้งสิ้น" total row.
//   T6b  CANCELLED+no-draft  — primary CTA label = "เริ่มแจ้งย้ายออกใหม่".
//                              Click opens MoveOutNoticeModal prefilled
//                              with the same room number + tenant name.
//   T7   QUEUE REGRESSION    — clicking a queue card opens drawer
//                              IN-PLACE (URL stays at /move-out). Pins
//                              the moveout-queue → drawer pattern in
//                              case anyone re-introduces the
//                              navigate(`/move-out/${id}`) regression.
//   T8   DIRECT URL          — page.goto(/move-out/:id) MUST NOT
//                              auto-open the drawer. Drawer only opens
//                              when the user clicks the primary CTA.
//
// Fixtures:
//   TC1  (B105) PENDING_SETTLEMENT, no draft   → T2 no-draft, T6b cancelled
//   TC3  (B201) PENDING_SETTLEMENT, no draft   → T5b (bootstrap COMPLETED+nil)
//   TC4  (B202) PENDING_SETTLEMENT, with draft → T1 (bootstrap PENDING_PAYMENT)
//   TC10 (B205) PENDING_METER                  → T4 cancel, T8 direct URL
//   TC22 (D201) PENDING_SETTLEMENT, draft+manual → T2 with-draft, T7 queue, T6a voided-breakdown
//   TC23 (D202) PENDING_SETTLEMENT, draft FULL_MONTH → T3 READY_TO_CLOSE (bootstrap)
//   TC24 (D103) PENDING_SETTLEMENT, no draft, ZERO net → T5 COMPLETED+settled (bootstrap)
//
// Run:  npm run smoke:moveout-detail  (from backend/devtools/smoke/)
//       make smoke-moveout-detail     (from backend/)

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

// ─── API helpers ───────────────────────────────────────────────────────

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

// ─── Drawer step helper (matches RoomWorkflowDrawer stepper) ───────────

async function getSelectedStepIdx(page) {
  const labels = ['1 มิเตอร์', '2 สรุปยอด', '3 การเงิน', '4 ปิดสัญญา']
  for (let i = 0; i < labels.length; i++) {
    const btn = page.locator(`[aria-label="ขั้นตอนที่ ${labels[i]}"]`)
    const aria = await btn.getAttribute('aria-current').catch(() => null)
    if (aria === 'step') return i
  }
  return -1
}

async function closeDrawer(page) {
  const closeBtn = page.locator(`${DRAWER} button[aria-label="ปิด"]`)
  if (await closeBtn.isVisible().catch(() => false)) {
    await closeBtn.click()
    await page.waitForTimeout(400)
  }
}

async function gotoDetail(page, noticeId) {
  await page.goto(`${FRONTEND}/move-out/${noticeId}`)
  await page.waitForLoadState('networkidle')
  // Wait for the page-level title — selects the H1 to avoid matching the
  // "ย้ายออก" sidebar entry.
  await page.locator('h1:has-text("ย้ายออก ห้อง")').first().waitFor({ timeout: 8000 })
  await page.waitForTimeout(300)
}

// ─── Main ─────────────────────────────────────────────────────────────

async function main() {
  console.log('━━━ MoveOutDetailPage Behavior Smoke (T1–T8) ━━━')

  // Reset fixtures: cleanup first then seed.
  const cleanupRes = await fetch(`${BACKEND}/api/v1/dev/smoke/cleanup`, { method: 'POST' })
  if (!cleanupRes.ok) throw new Error(`Cleanup failed: ${cleanupRes.status}`)
  const seedRes = await fetch(`${BACKEND}/api/v1/dev/smoke/seed`, { method: 'POST' })
  if (!seedRes.ok) throw new Error(`Seed failed: ${seedRes.status}`)
  console.log('  ✅ Smoke fixtures cleaned + re-seeded')

  const fx = await fetchFixtures()
  for (const k of ['TC1', 'TC3', 'TC4', 'TC10', 'TC22', 'TC23', 'TC24']) {
    if (!fx[k]) throw new Error(`${k} fixture missing`)
    console.log(`  ℹ️  ${k} = ${fx[k].room_number} / status=${fx[k].status} / has_draft=${fx[k].has_draft}`)
  }

  const token = await loginApi()

  // Bootstrap states via API:
  //   TC4  → PENDING_PAYMENT       (T1)
  //   TC23 → READY_TO_CLOSE+nil    (T3)  via finalize → skip-payment
  //   TC24 → COMPLETED+ZERO        (T5)  via generate → finalize → record-payment(ZERO) → close
  await apiPost(token, `/api/v1/move-out-notices/${fx.TC4.notice_id}/finalize-settlement`)

  await apiPost(token, `/api/v1/move-out-notices/${fx.TC23.notice_id}/finalize-settlement`)
  await apiPost(token, `/api/v1/move-out-notices/${fx.TC23.notice_id}/skip-payment`)

  // TC24: seeded ZERO net → ZERO_BALANCE outcome → READY_TO_CLOSE → close
  await apiPost(token, `/api/v1/move-out-notices/${fx.TC24.notice_id}/generate-settlement`)
  await apiPost(token, `/api/v1/move-out-notices/${fx.TC24.notice_id}/finalize-settlement`)
  await apiPost(token, `/api/v1/move-out-notices/${fx.TC24.notice_id}/record-payment`, {
    payment_outcome: 'ZERO_BALANCE',
  })
  await apiPost(token, `/api/v1/move-out-notices/${fx.TC24.notice_id}/close`)

  // TC3 → COMPLETED + nil (closed-with-unsettled). Used by T5b to verify
  // the unsettled OutcomeCard variant on the detail page.
  await apiPost(token, `/api/v1/move-out-notices/${fx.TC3.notice_id}/generate-settlement`)
  await apiPost(token, `/api/v1/move-out-notices/${fx.TC3.notice_id}/finalize-settlement`)
  await apiPost(token, `/api/v1/move-out-notices/${fx.TC3.notice_id}/skip-payment`)
  await apiPost(token, `/api/v1/move-out-notices/${fx.TC3.notice_id}/close-with-unsettled`)
  console.log('  ✅ Fixtures bootstrapped (TC4=PENDING_PAYMENT, TC23=READY_TO_CLOSE, TC24=COMPLETED+settled, TC3=COMPLETED+nil)')

  const tc4 = await getNotice(token, fx.TC4.notice_id)
  const tc23 = await getNotice(token, fx.TC23.notice_id)
  const tc24 = await getNotice(token, fx.TC24.notice_id)
  const tc3 = await getNotice(token, fx.TC3.notice_id)
  check('Pre-state: TC4 = PENDING_PAYMENT', tc4.status === 'PENDING_PAYMENT', `status=${tc4.status}`)
  check('Pre-state: TC23 = READY_TO_CLOSE', tc23.status === 'READY_TO_CLOSE', `status=${tc23.status}`)
  check('Pre-state: TC24 = COMPLETED+settled', tc24.status === 'COMPLETED' && tc24.payment_outcome === 'ZERO_BALANCE',
    `status=${tc24.status} outcome=${tc24.payment_outcome}`)
  check('Pre-state: TC3 = COMPLETED+nil (unsettled)',
    tc3.status === 'COMPLETED' && tc3.payment_outcome == null,
    `status=${tc3.status} outcome=${tc3.payment_outcome}`)

  const browser = await chromium.launch({ headless: false, slowMo: 60 })
  const ctx = await browser.newContext({ viewport: { width: 1400, height: 900 } })
  const page = await ctx.newPage()

  try {
    await login(page)
    console.log('  ✅ UI login complete')

    // ───────────────────────────────────────────────────────────────────
    // T8 — DIRECT URL: drawer must NOT auto-open
    // ───────────────────────────────────────────────────────────────────
    console.log('\n[T8] Direct URL → drawer NOT auto-opened')

    await gotoDetail(page, fx.TC10.notice_id) // PENDING_METER
    const t8DrawerVisible = await page.locator(DRAWER).isVisible().catch(() => false)
    check('Direct URL navigation does NOT auto-open drawer', !t8DrawerVisible)

    const t8Cta = page.locator('button:has-text("ดำเนินการต่อ")').first()
    check('Primary CTA "ดำเนินการต่อ" visible (PENDING_METER, active)',
      await t8Cta.isVisible().catch(() => false))

    await t8Cta.click()
    await page.waitForSelector(DRAWER, { timeout: 5000 })
    check('Clicking primary CTA opens drawer', await page.locator(DRAWER).isVisible().catch(() => false))
    check('Drawer opens at Step 1 (Meter) for PENDING_METER',
      (await getSelectedStepIdx(page)) === 0)
    await closeDrawer(page)

    // ───────────────────────────────────────────────────────────────────
    // T7 — QUEUE REGRESSION: card click opens drawer in-place (URL stays)
    // ───────────────────────────────────────────────────────────────────
    console.log('\n[T7] Queue card click → drawer in-place, URL stays at /move-out')

    await page.goto(`${FRONTEND}/move-out`)
    await page.waitForLoadState('networkidle')
    await page.waitForSelector('text=คิวงานย้ายออก', { timeout: 10000 })
    // TC22 is PENDING_SETTLEMENT → "สรุปยอด" tab.
    const settlementTab = page.locator('button:has-text("สรุปยอด")').first()
    if (await settlementTab.isVisible().catch(() => false)) {
      await settlementTab.click()
      await page.waitForTimeout(300)
    }
    const tc22Card = page.locator(`button:has-text("${fx.TC22.room_number}")`).first()
    await tc22Card.waitFor({ state: 'visible', timeout: 5000 })
    const beforeClickUrl = page.url()
    await tc22Card.click()
    await page.waitForSelector(DRAWER, { timeout: 5000 })
    const afterClickUrl = page.url()
    check('Queue card click opens drawer (in-place)', await page.locator(DRAWER).isVisible().catch(() => false))
    check('Queue URL UNCHANGED after card click (stays at /move-out, no nav to /move-out/:id)',
      afterClickUrl === beforeClickUrl,
      `before=${new URL(beforeClickUrl).pathname} after=${new URL(afterClickUrl).pathname}`)
    await closeDrawer(page)

    // ───────────────────────────────────────────────────────────────────
    // T2 — PENDING_SETTLEMENT: pending block, no "฿0" ResultBlock, drawer at idx 1
    // ───────────────────────────────────────────────────────────────────
    console.log('\n[T2] PENDING_SETTLEMENT — no-draft (TC1) + with-draft (TC22)')

    for (const [name, notice] of [['TC1 (no draft)', fx.TC1], ['TC22 (with draft)', fx.TC22]]) {
      await gotoDetail(page, notice.notice_id)
      const pendingText = page.locator(':text("ยังไม่มีสรุปยอด")').first()
      check(`${name}: PendingSettlementBlock shows "ยังไม่มีสรุปยอด"`,
        await pendingText.isVisible().catch(() => false))

      // Negative-space guard: ResultBlock copy MUST NOT render. The page's
      // hero swap is the whole point of the PendingSettlementBlock split —
      // if any of these labels show up the "฿0 hero regression" is back.
      for (const label of ['ต้องคืนเงินประกัน', 'ต้องชำระเพิ่ม', 'ยอดสุทธิ']) {
        const visible = await page.locator(`:text("${label}")`).first().isVisible().catch(() => false)
        check(`${name}: ResultBlock label "${label}" NOT rendered (no ฿0 hero)`, !visible)
      }

      const cancelLink = page.locator('button:has-text("ยกเลิกแจ้งย้ายออก")').first()
      check(`${name}: cancel link visible (PENDING_SETTLEMENT is in CanCancel set)`,
        await cancelLink.isVisible().catch(() => false))

      const cta = page.locator('button:has-text("ดำเนินการต่อ")').first()
      await cta.click()
      await page.waitForSelector(DRAWER, { timeout: 5000 })
      check(`${name}: CTA opens drawer at Step 2 (Settlement)`,
        (await getSelectedStepIdx(page)) === 1)
      await closeDrawer(page)
    }

    // ───────────────────────────────────────────────────────────────────
    // T1 — PENDING_PAYMENT: CTA → drawer at Step 3, NO cancel/reopen links
    // ───────────────────────────────────────────────────────────────────
    console.log('\n[T1] PENDING_PAYMENT (TC4) — CTA opens drawer at Step 3, no cancel/reopen')

    await gotoDetail(page, fx.TC4.notice_id)
    const t1Cancel = await page.locator('button:has-text("ยกเลิกแจ้งย้ายออก")').first().isVisible().catch(() => false)
    check('PENDING_PAYMENT: cancel link ABSENT (backend CanCancel disallows)', !t1Cancel)
    const t1Reopen = await page.locator('button:has-text("กลับไปบันทึกการชำระ")').first().isVisible().catch(() => false)
    check('PENDING_PAYMENT: reopen link ABSENT (only valid in READY_TO_CLOSE)', !t1Reopen)
    const t1Restart = await page.locator('button:has-text("เริ่มแจ้งย้ายออกใหม่")').first().isVisible().catch(() => false)
    check('PENDING_PAYMENT: restart CTA ABSENT (only valid in CANCELLED)', !t1Restart)

    const t1Cta = page.locator('button:has-text("ดำเนินการต่อ")').first()
    check('PENDING_PAYMENT: primary CTA = "ดำเนินการต่อ"', await t1Cta.isVisible().catch(() => false))
    await t1Cta.click()
    await page.waitForSelector(DRAWER, { timeout: 5000 })
    check('PENDING_PAYMENT: drawer opens at Step 3 (Payment)',
      (await getSelectedStepIdx(page)) === 2)
    // Drawer at Step 3 must show interactive payment controls — either the
    // method picker (เงินสด/โอนเงิน) for non-zero outcomes, or the
    // "ยังไม่ชำระ" skip path. Pins "blank Step 3" failure mode.
    const t1Method = await page.locator(`${DRAWER} button:has-text("เงินสด"), ${DRAWER} button:has-text("โอนเงิน"), ${DRAWER} button:has-text("ยังไม่ชำระ")`).first().isVisible().catch(() => false)
    check('PENDING_PAYMENT: drawer Step 3 renders interactive payment controls', t1Method)
    await closeDrawer(page)

    // ───────────────────────────────────────────────────────────────────
    // T3 — READY_TO_CLOSE: CTA="ปิดการย้ายออก", reopen flow → PENDING_PAYMENT
    // ───────────────────────────────────────────────────────────────────
    console.log('\n[T3] READY_TO_CLOSE (TC23) — CTA "ปิดการย้ายออก", reopen → PENDING_PAYMENT')

    await gotoDetail(page, fx.TC23.notice_id)
    const t3Cta = page.locator('button:has-text("ปิดการย้ายออก")').first()
    check('READY_TO_CLOSE: primary CTA label = "ปิดการย้ายออก"',
      await t3Cta.isVisible().catch(() => false))
    const t3CancelLink = await page.locator('button:has-text("ยกเลิกแจ้งย้ายออก")').first().isVisible().catch(() => false)
    check('READY_TO_CLOSE: cancel link ABSENT (CanCancel disallows past PENDING_SETTLEMENT)', !t3CancelLink)
    // After P0 fix: confirm verb differentiated from trigger verb. Trigger
    // says "ยกเลิกแจ้งย้ายออก", confirm now says "ยืนยันยกเลิก" (different
    // root verb per wording-tone-guide).
    const _t3StaleConfirm = await page.locator('button:has-text("ยกเลิกการย้ายออก")').first().isVisible().catch(() => false)
    check('READY_TO_CLOSE: stale "ยกเลิกการย้ายออก" confirm copy NOT present (was renamed to "ยืนยันยกเลิก")',
      !_t3StaleConfirm)

    await t3Cta.click()
    await page.waitForSelector(DRAWER, { timeout: 5000 })
    check('READY_TO_CLOSE: CTA opens drawer at Step 4 (Close)',
      (await getSelectedStepIdx(page)) === 3)
    await closeDrawer(page)

    // Reopen flow: subtle bottom link → confirm UI → "กลับไปบันทึกการชำระ" → /reopen
    // Copy was changed in P0 fix: button now matches actual backend behavior
    // (rewinds to PENDING_PAYMENT, not PENDING_SETTLEMENT) so the verb is
    // "บันทึกการชำระ" (record payment), not "ยอดสรุป" (settlement).
    const t3ReopenLink = page.locator('button:has-text("กลับไปบันทึกการชำระ")').first()
    check('READY_TO_CLOSE: reopen text link visible',
      await t3ReopenLink.isVisible().catch(() => false))
    await t3ReopenLink.click()
    await page.waitForTimeout(200)
    // After click, the trigger button is replaced by the confirm UI: a
    // <span> prompt ("ยืนยันกลับไปบันทึกการชำระ") + a <button> confirm
    // ("กลับไปบันทึกการชำระ") + a <button> "ไม่". The `button:has-text(...)`
    // selector matches the active confirm button only — the prompt is a
    // span and `:has-text` doesn't match it.
    const t3ConfirmPrompt = await page.locator(':text("ยืนยันกลับไปบันทึกการชำระ")').first().isVisible().catch(() => false)
    check('READY_TO_CLOSE: reopen confirm prompt "ยืนยันกลับไปบันทึกการชำระ" visible', t3ConfirmPrompt)
    const t3ConfirmReopen = page.locator('button:has-text("กลับไปบันทึกการชำระ")').first()
    check('READY_TO_CLOSE: reopen confirm UI shows "กลับไปบันทึกการชำระ" button',
      await t3ConfirmReopen.isVisible().catch(() => false))

    const reopenPromise = page.waitForResponse(
      (r) => r.url().includes(`/move-out-notices/${fx.TC23.notice_id}/reopen`) && r.status() < 400,
      { timeout: 10000 },
    )
    await t3ConfirmReopen.click()
    let reopenResp
    try { reopenResp = await reopenPromise } catch (_e) {}
    check('READY_TO_CLOSE: /reopen endpoint hit', !!reopenResp,
      reopenResp ? `${reopenResp.status()}` : 'no call')
    await page.waitForTimeout(1500)

    // Reopen rewinds READY_TO_CLOSE → PENDING_PAYMENT (service.go
    // ReopenForCorrection clears payment_outcome so the operator can
    // re-record it). Button copy "กลับไปบันทึกการชำระ" matches this.
    // Drawer lands at Step 3 (Payment) on next entry. To edit the
    // settlement bill instead, the operator clicks "กลับไปแก้ยอดสรุป" inside
    // Step 3 (the drawer's own edit-back affordance).
    const tc23After = await getNotice(token, fx.TC23.notice_id)
    check('READY_TO_CLOSE → reopen: backend status now PENDING_PAYMENT',
      tc23After.status === 'PENDING_PAYMENT', `status=${tc23After.status}`)
    check('READY_TO_CLOSE → reopen: backend payment_outcome cleared',
      tc23After.payment_outcome == null, `outcome=${tc23After.payment_outcome}`)

    // After reopen + TQ refetch, the visible status badge should reflect
    // the new state. Label "รอชำระ/คืนเงิน" comes from MOVE_OUT_STATUS_CONFIG
    // for PENDING_PAYMENT.
    const newBadge = page.locator(':text("รอชำระ/คืนเงิน")').first()
    check('READY_TO_CLOSE → reopen: status badge updated to "รอชำระ/คืนเงิน"',
      await newBadge.isVisible().catch(() => false))

    // ───────────────────────────────────────────────────────────────────
    // T4 — CANCEL flow: PENDING_METER, link visible, confirm → CANCELLED
    // ───────────────────────────────────────────────────────────────────
    console.log('\n[T4] Cancel flow (TC10 PENDING_METER) — link visible, confirm → CANCELLED')

    await gotoDetail(page, fx.TC10.notice_id)
    const t4Link = page.locator('button:has-text("ยกเลิกแจ้งย้ายออก")').first()
    check('PENDING_METER: cancel link visible', await t4Link.isVisible().catch(() => false))
    await t4Link.click()
    await page.waitForTimeout(200)
    // P0 fix: confirm button is "ยืนยันยกเลิก" (verb-differentiated from
    // trigger "ยกเลิกแจ้งย้ายออก"). Pin both the new copy AND the absence
    // of the old "ยกเลิกการย้ายออก" copy so a regression here surfaces.
    const t4Confirm = page.locator('button:has-text("ยืนยันยกเลิก")').first()
    check('PENDING_METER: cancel confirm UI shows "ยืนยันยกเลิก" (new verb-differentiated copy)',
      await t4Confirm.isVisible().catch(() => false))
    const _t4StaleConfirm = await page.locator('button:has-text("ยกเลิกการย้ายออก")').first().isVisible().catch(() => false)
    check('PENDING_METER: stale "ยกเลิกการย้ายออก" copy gone',
      !_t4StaleConfirm)

    const cancelPromise = page.waitForResponse(
      (r) => r.url().includes(`/move-out-notices/${fx.TC10.notice_id}/cancel`) && r.status() < 400,
      { timeout: 10000 },
    )
    await t4Confirm.click()
    let cancelResp
    try { cancelResp = await cancelPromise } catch (_e) {}
    check('PENDING_METER → cancel: /cancel endpoint hit', !!cancelResp,
      cancelResp ? `${cancelResp.status()}` : 'no call')
    await page.waitForTimeout(1200)

    const tc10After = await getNotice(token, fx.TC10.notice_id)
    check('PENDING_METER → cancel: backend status = CANCELLED',
      tc10After.status === 'CANCELLED', `status=${tc10After.status}`)
    check('PENDING_METER → cancel: cancelled_at stamped',
      !!tc10After.cancelled_at, `cancelled_at=${tc10After.cancelled_at}`)

    // ───────────────────────────────────────────────────────────────────
    // T5 — COMPLETED: no CTA, no cancel/reopen/restart, OutcomeCard visible
    // ───────────────────────────────────────────────────────────────────
    console.log('\n[T5] COMPLETED (TC24) — terminal read-only')

    await gotoDetail(page, fx.TC24.notice_id)
    const t5Outcome = page.locator(':text("ปิดสัญญาเรียบร้อย")').first()
    check('COMPLETED: OutcomeCard "ปิดสัญญาเรียบร้อย" visible',
      await t5Outcome.isVisible().catch(() => false))
    const t5CtaContinue = await page.locator('button:has-text("ดำเนินการต่อ")').first().isVisible().catch(() => false)
    const t5CtaClose = await page.locator('button:has-text("ปิดการย้ายออก")').first().isVisible().catch(() => false)
    const t5Cancel = await page.locator('button:has-text("ยกเลิกแจ้งย้ายออก")').first().isVisible().catch(() => false)
    const t5Reopen = await page.locator('button:has-text("กลับไปบันทึกการชำระ")').first().isVisible().catch(() => false)
    const t5Restart = await page.locator('button:has-text("เริ่มแจ้งย้ายออกใหม่")').first().isVisible().catch(() => false)
    check('COMPLETED: NO "ดำเนินการต่อ" CTA', !t5CtaContinue)
    check('COMPLETED: NO "ปิดการย้ายออก" CTA', !t5CtaClose)
    check('COMPLETED: NO cancel link', !t5Cancel)
    check('COMPLETED: NO reopen link', !t5Reopen)
    check('COMPLETED: NO restart CTA', !t5Restart)
    // P0 fix: settled COMPLETED must NOT show the unsettled CTA.
    const t5BackFill = await page.locator('button:has-text("บันทึกการชำระ")').first().isVisible().catch(() => false)
    check('COMPLETED+settled: NO "บันทึกการชำระ" back-fill CTA (only on COMPLETED+nil)', !t5BackFill)

    // ───────────────────────────────────────────────────────────────────
    // T5b — COMPLETED + nil outcome: warning-tone OutcomeCard + back-fill CTA
    // ───────────────────────────────────────────────────────────────────
    console.log('\n[T5b] COMPLETED+nil (TC3) — unsettled variant + back-fill CTA opens drawer')

    await gotoDetail(page, fx.TC3.notice_id)
    // Must NOT show the green success title.
    const t5bSuccessTitle = await page.locator(':text("ปิดสัญญาเรียบร้อย")').first().isVisible().catch(() => false)
    check('COMPLETED+nil: NO green "ปิดสัญญาเรียบร้อย" title (regression guard)', !t5bSuccessTitle)
    // Must show the unsettled variant copy.
    const t5bClosedTitle = await page.locator(':text("ปิดสัญญาแล้ว")').first().isVisible().catch(() => false)
    check('COMPLETED+nil: shows "ปิดสัญญาแล้ว" title', t5bClosedTitle)
    const t5bUnsettledSub = await page.locator(':text("ยังไม่บันทึกการเงิน")').first().isVisible().catch(() => false)
    check('COMPLETED+nil: shows "ยังไม่บันทึกการเงิน" subtitle', t5bUnsettledSub)
    // Back-fill CTA must be present.
    const t5bBackFill = page.locator('button:has-text("บันทึกการชำระ")').first()
    check('COMPLETED+nil: "บันทึกการชำระ" CTA visible',
      await t5bBackFill.isVisible().catch(() => false))
    // Click → drawer opens. Drawer's statusToCurrentIdx routes COMPLETED+nil → idx 2 (Payment).
    await t5bBackFill.click()
    await page.waitForSelector(DRAWER, { timeout: 5000 })
    check('COMPLETED+nil: back-fill CTA opens drawer', await page.locator(DRAWER).isVisible().catch(() => false))
    check('COMPLETED+nil: drawer routes to Step 3 (Payment) for back-fill',
      (await getSelectedStepIdx(page)) === 2)
    await closeDrawer(page)

    // ───────────────────────────────────────────────────────────────────
    // T6a — CANCELLED + voided draft: breakdown accordion MUST NOT render
    // ───────────────────────────────────────────────────────────────────
    // Pins the P0 fix: backend Cancel voids the bill but leaves
    // notice.settlement_bill_id set; the page must not surface the voided
    // line items via the "รายละเอียดค่าใช้จ่าย" accordion.
    //
    // ⚠️ FIXTURE-STATE DEPENDENCY: TC22 must be PENDING_SETTLEMENT here.
    // T7 (queue regression) and T2 (PENDING_SETTLEMENT with-draft case)
    // both visit TC22 but neither triggers a state mutation. If a future
    // test inserted between bootstrap and T6a touches TC22, this assertion
    // will start failing for an unrelated reason — re-bootstrap TC22 to
    // PENDING_SETTLEMENT before T6a in that case.
    console.log('\n[T6a] CANCELLED + voided draft (TC22) — breakdown hidden')

    // TC22 stayed at PENDING_SETTLEMENT through T7+T2 (only renders, no
    // state change). Cancel it now — it had a draft, so backend voids the
    // bill and we can verify the FE no longer surfaces it.
    await apiPost(token, `/api/v1/move-out-notices/${fx.TC22.notice_id}/cancel`)
    const tc22After = await getNotice(token, fx.TC22.notice_id)
    check('TC22 pre-T6a: backend status now CANCELLED', tc22After.status === 'CANCELLED', `status=${tc22After.status}`)
    check('TC22 pre-T6a: settlement_bill_id still set (FE must self-gate)',
      tc22After.settlement_bill_id != null, `bill_id=${tc22After.settlement_bill_id}`)
    // Pragmatic note: the FE banner copy ("ยกเลิกแจ้งย้ายออกแล้ว") and the
    // banner's timestamp formatting both render `cancelled_at` from the
    // notice. We do NOT separately differentiate `cancelled_at` from
    // `updated_at` in the FE — in smoke runs the two are within ms of each
    // other so a UI-side check would be flaky for no real coverage gain.
    // Backend-side, the assertion below pins that `cancelled_at` IS stamped
    // (which is the regression we actually care about).
    // cancelled_at must be stamped by the service (mirrors closed_at for
    // COMPLETED). FE's CancelledBanner relies on this for the precise
    // "ยกเลิกเมื่อ" timestamp instead of the drift-prone updated_at proxy.
    check('TC22 pre-T6a: cancelled_at stamped by service',
      !!tc22After.cancelled_at, `cancelled_at=${tc22After.cancelled_at}`)

    await gotoDetail(page, fx.TC22.notice_id)
    const t6aBanner = await page.locator(':text("ยกเลิกแจ้งย้ายออกแล้ว")').first().isVisible().catch(() => false)
    check('CANCELLED+had-draft: CancelledBanner visible', t6aBanner)
    const t6aBreakdownTrigger = await page.locator('button:has-text("รายละเอียดค่าใช้จ่าย")').first().isVisible().catch(() => false)
    check('CANCELLED+had-draft: NO "รายละเอียดค่าใช้จ่าย" accordion (voided bill must not surface)',
      !t6aBreakdownTrigger)
    // Negative-space: the row total label "รวมทั้งสิ้น" comes from the
    // breakdown body. If somehow the body rendered without the trigger
    // (regression to prior `{settlementBill && ...}` gate), this catches it.
    const t6aGrandTotal = await page.locator(':text("รวมทั้งสิ้น")').first().isVisible().catch(() => false)
    check('CANCELLED+had-draft: NO "รวมทั้งสิ้น" total row from voided bill', !t6aGrandTotal)

    // ───────────────────────────────────────────────────────────────────
    // T6b — CANCELLED: restart CTA opens prefilled MoveOutNoticeModal
    // ───────────────────────────────────────────────────────────────────
    console.log('\n[T6b] CANCELLED (cancel TC1 via API) — restart CTA opens prefilled modal')

    // Cancel TC1 (PENDING_SETTLEMENT, no draft) via API.
    await apiPost(token, `/api/v1/move-out-notices/${fx.TC1.notice_id}/cancel`)
    const tc1After = await getNotice(token, fx.TC1.notice_id)
    check('TC1 pre-T6b: backend status now CANCELLED',
      tc1After.status === 'CANCELLED', `status=${tc1After.status}`)

    await gotoDetail(page, fx.TC1.notice_id)
    const t6Banner = page.locator(':text("ยกเลิกแจ้งย้ายออกแล้ว")').first()
    check('CANCELLED: CancelledBanner visible', await t6Banner.isVisible().catch(() => false))
    const t6Restart = page.locator('button:has-text("เริ่มแจ้งย้ายออกใหม่")').first()
    check('CANCELLED: primary CTA = "เริ่มแจ้งย้ายออกใหม่"',
      await t6Restart.isVisible().catch(() => false))
    const t6Continue = await page.locator('button:has-text("ดำเนินการต่อ")').first().isVisible().catch(() => false)
    check('CANCELLED: NO "ดำเนินการต่อ" CTA (terminal branch)', !t6Continue)

    await t6Restart.click()
    await page.waitForTimeout(400)
    const restartModal = page.locator('div[role="dialog"][aria-modal="true"]:has-text("แจ้งย้ายออก")').first()
    check('CANCELLED: restart click opens MoveOutNoticeModal',
      await restartModal.isVisible().catch(() => false))
    // Modal must be prefilled with the same room + tenant context (sourced
    // from the cancelled notice). Pins the contract-context preservation
    // path — without it the user would have to re-enter / re-search the
    // contract, defeating the "ใช้ข้อมูลสัญญาเดิม" caption.
    const t6Room = await restartModal.locator(`:text("ห้อง ${fx.TC1.room_number}")`).first().isVisible().catch(() => false)
    const t6Tenant = await restartModal.locator(`:text-matches("TC1_SMOKE")`).first().isVisible().catch(() => false)
    check(`CANCELLED: restart modal shows room ${fx.TC1.room_number}`, t6Room)
    check('CANCELLED: restart modal shows tenant name (TC1_SMOKE...)', t6Tenant)

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
