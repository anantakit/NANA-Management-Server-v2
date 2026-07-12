// Move-out FULL-LIFECYCLE e2e with rollover + replacement — browser smoke.
// ----------------------------------------------------------------------
// The gap the existing smokes leave: no single BROWSER run walks the whole
// move-out workflow (Queue → record exit meter WITH a hardware flag →
// settlement → finalize → payment → close) end-to-end. Coverage today is
// piecemeal:
//   - smoke-exit-meter-flags (HTTP)      → record(+flag) → generate → finalize, no browser
//   - smoke-exit-meter-flags-ui (browser)→ record(+flag) then STOPS at Step 2
//   - smoke-settlement-exit-meter-edit   → edit(+flag) on settlement, no lifecycle
//   - moveout-step23 / step4 (browser)   → full lifecycle but NORMAL meters (no flag)
//
// This smoke joins the two: it drives BOTH flag cases through the ENTIRE
// RoomWorkflowDrawer (Step 1 มิเตอร์ → 2 สรุปยอด → 3 การเงิน → 4 ปิดสัญญา) in the
// browser, and cross-checks the persisted settlement bill (via API) so the
// flagged usage is proven to flow all the way into the real bill line.
//
//   ROLLOVER  (XM-ROLL): prior elec 99000 → enter 500 + "มิเตอร์ครบรอบ" →
//                        usage wraps to 1,499 → bill ELECTRICITY qty 1499.
//   REPLACEMENT (XM-REPL): enter 45 + "เปลี่ยนมิเตอร์ใหม่" → previous 0 →
//                        usage 45 → bill ELECTRICITY qty 45, meter_previous 0.
//
// Fixtures: POST /dev/smoke/exit-meter-flags-setup — the SAME re-runnable dev
// endpoint the create-path UI smoke uses. It wipes each room's
// notices/bills/deliveries/audit/readings first (no `payments` table exists —
// move-out record-payment only sets payment_outcome + marks the bill PAID), so
// the full lifecycle is cleanly re-seedable with NO extra cleanup wiring.
//
// Run:  npm run smoke:moveout-exit-meter-e2e   (from backend/devtools/smoke/)
//       make smoke-moveout-exit-meter-e2e      (from backend/)
//       HEADED=1 make smoke-moveout-exit-meter-e2e   (watch it)

const { chromium } = require('playwright')

const FRONTEND = 'http://localhost:3001'
const BACKEND = 'http://localhost:8080'
const V1 = `${BACKEND}/api/v1`
const ADMIN_USER = 'admin'
const ADMIN_PASS_FRESH = 'admin123'
const ADMIN_PASS_POST = 'admin1234'

const DRAWER = '[role="dialog"][aria-label="ดำเนินการย้ายออก"]'
const SAVE_LABEL = 'บันทึกและไปสรุปยอด'
const BELOW_PREV = 'เลขมิเตอร์ต้องไม่น้อยกว่าครั้งก่อน'
const ROLLOVER_ROOM = 'XM-ROLL'
const REPLACE_ROOM = 'XM-REPL'
// Drawer step aria-labels (mirror moveout-step4 smoke).
const STEP_LABELS = ['1 มิเตอร์', '2 สรุปยอด', '3 การเงิน', '4 ปิดสัญญา']

const results = { pass: 0, fail: 0, total: 0, failedCases: [] }
const check = (name, ok, detail = '') => {
  console.log(`  ${ok ? '✅' : '❌'} ${name}${detail ? ` — ${detail}` : ''}`)
  results.total++
  if (ok) results.pass++
  else { results.fail++; results.failedCases.push(name) }
  return ok
}

// ─── API helpers (verify the persisted truth the browser drove) ──────────

async function api(method, path, { token, body } = {}) {
  const headers = { 'content-type': 'application/json' }
  if (token) headers.authorization = `Bearer ${token}`
  const r = await fetch(`${V1}${path}`, { method, headers, body: body ? JSON.stringify(body) : undefined })
  let json = null
  try { json = await r.json() } catch { /* empty body */ }
  return { status: r.status, json }
}

async function apiLogin() {
  for (const pw of [ADMIN_PASS_POST, ADMIN_PASS_FRESH]) {
    const r = await api('POST', '/auth/login', { body: { username: ADMIN_USER, password: pw } })
    if (r.status !== 200 || !r.json?.data?.access_token) continue
    let token = r.json.data.access_token
    if (r.json.data.must_change_password) {
      await api('POST', '/auth/change-password', { token, body: { current_password: pw, new_password: ADMIN_PASS_POST } })
      const r2 = await api('POST', '/auth/login', { body: { username: ADMIN_USER, password: ADMIN_PASS_POST } })
      token = r2.json.data.access_token
    }
    return token
  }
  throw new Error('API login failed')
}

const getNotice = (token, id) => api('GET', `/move-out-notices/${id}`, { token }).then((r) => r.json?.data)
const getBill = (token, id) => api('GET', `/bills/${id}`, { token }).then((r) => r.json?.data)
const lineByType = (bill, type) => (bill?.line_items || []).find((li) => li.line_type === type)

// ─── Browser helpers ─────────────────────────────────────────────────────

async function login(page) {
  await page.goto(`${FRONTEND}/login`)
  await page.fill('input[name="username"]', ADMIN_USER)
  await page.fill('input[name="password"]', ADMIN_PASS_FRESH)
  await page.click('button[type="submit"]')
  await page.waitForLoadState('networkidle')
  if (page.url().includes('/login')) {
    await page.fill('input[name="password"]', ADMIN_PASS_POST)
    await page.click('button[type="submit"]')
    await page.waitForLoadState('networkidle')
  }
  if (page.url().includes('/change-password')) {
    await page.fill('input[name="new_password"]', ADMIN_PASS_POST)
    await page.fill('input[name="confirm_password"]', ADMIN_PASS_POST)
    await page.click('button[type="submit"]')
    await page.waitForLoadState('networkidle')
  }
  await page.waitForFunction(() => !window.location.pathname.includes('/login'), { timeout: 10000 })
}

async function getSelectedStepIdx(page) {
  for (let i = 0; i < STEP_LABELS.length; i++) {
    const aria = await page.locator(`[aria-label="ขั้นตอนที่ ${STEP_LABELS[i]}"]`).getAttribute('aria-current').catch(() => null)
    if (aria === 'step') return i
  }
  return -1
}

// Open the queue on the PENDING_METER stage and launch the room's workflow drawer.
async function openMeterDrawerFromQueue(page, roomNumber) {
  await page.goto(`${FRONTEND}/move-out`)
  await page.waitForLoadState('networkidle')
  await page.waitForSelector('text=คิวงานย้ายออก', { timeout: 10000 })
  const meterTab = page.locator('button:has-text("รอจดมิเตอร์")').first()
  if (await meterTab.isVisible().catch(() => false)) {
    await meterTab.click()
    await page.waitForTimeout(300)
  }
  const card = page.locator(`button:has-text("${roomNumber}")`).first()
  await card.waitFor({ state: 'visible', timeout: 8000 })
  await card.click()
  await page.waitForSelector(DRAWER, { timeout: 8000 })
  await page.waitForSelector('#rwd_electricity', { timeout: 8000 })
  await page.waitForTimeout(400)
}

// ─── Full lifecycle for one flag case ──────────────────────────────────────

async function runLifecycle(page, token, c, meta) {
  console.log(`\n${meta.label} — full lifecycle via RoomWorkflowDrawer (${meta.room})`)

  // ── STEP 1 — มิเตอร์: record the exit meter WITH the hardware flag ──
  await openMeterDrawerFromQueue(page, meta.room)
  check('drawer opens at Step 1 (มิเตอร์)', (await getSelectedStepIdx(page)) === 0)

  const flagChip = page.locator(`[aria-label="${meta.flagAria}"]`)
  await page.fill('#rwd_electricity', String(c.exit_electricity_current))
  await page.waitForTimeout(400)
  check('below-previous blocks the flagged reading before the flag',
    await page.getByText(BELOW_PREV).first().isVisible().catch(() => false))
  await flagChip.click()
  await page.fill('#rwd_water', String(c.exit_water_current))
  await page.waitForTimeout(400)
  check(`${meta.flagWord} chip aria-pressed=true`, (await flagChip.getAttribute('aria-pressed')) === 'true')
  check('below-previous CLEARED after the flag',
    !(await page.getByText(BELOW_PREV).first().isVisible().catch(() => false)))
  check(`electricity usage = ${c.expected_electricity_usage.toLocaleString()} (flag calc)`,
    await page.getByText(`ใช้ไป ${c.expected_electricity_usage.toLocaleString()} หน่วย`, { exact: true }).first().isVisible().catch(() => false))

  const saveBtn = page.getByRole('button', { name: SAVE_LABEL })
  const recPromise = page.waitForResponse((r) => r.url().includes(`/move-out-notices/${c.notice_id}/record-exit-meter`), { timeout: 10000 })
  await saveBtn.click()
  const recResp = await recPromise
  check('record-exit-meter fired from the UI', recResp.status() < 400, `HTTP ${recResp.status()}`)
  await page.waitForTimeout(1200)
  check('drawer auto-advanced to Step 2 (สรุปยอด)', (await getSelectedStepIdx(page)) === 1)

  // ── STEP 2 — สรุปยอด: confirm chains generate → finalize → advance ──
  const confirmCta = page.locator(`${DRAWER} button:has-text("ยืนยันยอด"), ${DRAWER} button:has-text("ไปชำระเงิน")`).first()
  await confirmCta.waitFor({ state: 'visible', timeout: 8000 })
  const finPromise = page.waitForResponse((r) => r.url().includes(`/move-out-notices/${c.notice_id}/finalize-settlement`) && r.status() < 400, { timeout: 15000 })
  await confirmCta.click()
  let finResp
  try { finResp = await finPromise } catch { /* surfaced below */ }
  check('finalize-settlement fired', !!finResp, finResp ? `HTTP ${finResp.status()}` : 'no call')
  await page.waitForTimeout(1500)
  check('drawer advanced to Step 3 (การเงิน)', (await getSelectedStepIdx(page)) === 2)

  // API cross-check: status flipped + the FLAGGED usage reached the real bill.
  const notice2 = await getNotice(token, c.notice_id)
  check('backend status = PENDING_PAYMENT after finalize', notice2.status === 'PENDING_PAYMENT', `status=${notice2.status}`)
  check('notice has a finalized settlement_bill_id', !!notice2.settlement_bill_id)
  const bill = await getBill(token, notice2.settlement_bill_id)
  const elec = lineByType(bill, 'ELECTRICITY')
  const water = lineByType(bill, 'WATER')
  check('settlement bill ELECTRICITY quantity = flagged usage',
    !!elec && elec.quantity === c.expected_electricity_usage, elec ? `qty=${elec.quantity} want=${c.expected_electricity_usage}` : 'no elec line')
  check('settlement bill ELECTRICITY meter_previous matches flag semantics',
    !!elec && elec.meter_previous === c.expected_electricity_previous, elec ? `prev=${elec.meter_previous} want=${c.expected_electricity_previous}` : 'no elec line')
  check('settlement bill ELECTRICITY meter_current = entered reading',
    !!elec && elec.meter_current === c.exit_electricity_current, elec ? `cur=${elec.meter_current} want=${c.exit_electricity_current}` : 'no elec line')
  check('settlement bill WATER quantity = clean-control usage',
    !!water && water.quantity === c.expected_water_usage, water ? `qty=${water.quantity} want=${c.expected_water_usage}` : 'no water line')

  // ── STEP 3 — การเงิน: record the payment outcome ──
  const cashBtn = page.locator(`${DRAWER} button:has-text("เงินสด")`).first()
  if (await cashBtn.isVisible().catch(() => false)) await cashBtn.click()
  await page.waitForTimeout(300)
  const paySubmit = page.locator(`${DRAWER} button:has-text("บันทึกรับเงิน"), ${DRAWER} button:has-text("บันทึกคืนเงิน"), ${DRAWER} button:has-text("ยืนยันปิดรายการ")`).first()
  const payPromise = page.waitForResponse((r) => r.url().includes(`/move-out-notices/${c.notice_id}/record-payment`) && r.status() < 400, { timeout: 12000 })
  await paySubmit.click()
  let payResp
  try { payResp = await payPromise } catch { /* surfaced below */ }
  check('record-payment fired', !!payResp, payResp ? `HTTP ${payResp.status()}` : 'no call')
  await page.waitForTimeout(1500)
  check('drawer advanced to Step 4 (ปิดสัญญา)', (await getSelectedStepIdx(page)) === 3)

  // ── STEP 4 — ปิดสัญญา: close the move-out ──
  const closeBtn = page.locator(`${DRAWER} button:has-text("ปิดสัญญา")`).last()
  await closeBtn.waitFor({ state: 'visible', timeout: 6000 })
  const closePromise = page.waitForResponse(
    (r) => r.url().includes(`/move-out-notices/${c.notice_id}/close`) && !r.url().includes('/close-with-unsettled') && r.status() < 400,
    { timeout: 12000 },
  )
  await closeBtn.click()
  let closeResp
  try { closeResp = await closePromise } catch { /* surfaced below */ }
  check('/close fired (settled path)', !!closeResp, closeResp ? `HTTP ${closeResp.status()}` : 'no call')
  await page.waitForTimeout(1200)

  const notice4 = await getNotice(token, c.notice_id)
  check('backend status = COMPLETED — full lifecycle closed', notice4.status === 'COMPLETED', `status=${notice4.status}`)

  await page.screenshot({ path: `/tmp/smoke-moveout-e2e-${meta.room}.png` })
}

// ─── Main ──────────────────────────────────────────────────────────────

async function main() {
  console.log('━━━ Move-out FULL-LIFECYCLE e2e (rollover + replacement) browser smoke ━━━')
  const token = await apiLogin()
  const setup = await api('POST', '/dev/smoke/exit-meter-flags-setup', { token })
  if (setup.status !== 200 || !setup.json?.data) throw new Error(`fixture setup failed: ${setup.status}`)
  const fx = setup.json.data
  console.log('  ✅ fixtures re-seeded (XM-ROLL / XM-REPL → PENDING_METER)')

  const headed = !!process.env.HEADED
  const browser = await chromium.launch({ headless: !headed, slowMo: headed ? 100 : 0 })
  try {
    const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } })
    const page = await ctx.newPage()
    await login(page)
    console.log('  ✅ UI login complete')

    await runLifecycle(page, token, fx.rollover, {
      room: ROLLOVER_ROOM, label: 'ROLLOVER', flagWord: 'rollover',
      flagAria: 'มิเตอร์ไฟฟ้า — มิเตอร์ครบรอบ',
    })
    await runLifecycle(page, token, fx.replacement, {
      room: REPLACE_ROOM, label: 'REPLACEMENT', flagWord: 'replacement',
      flagAria: 'มิเตอร์ไฟฟ้า — เปลี่ยนมิเตอร์ใหม่',
    })

    await ctx.close()
  } finally {
    await browser.close()
  }

  console.log(`\n━━━ ${results.pass}/${results.total} passed ━━━`)
  if (results.fail > 0) {
    console.log('  Failed:', results.failedCases.join(', '))
    process.exit(1)
  }
  console.log('✅ PASS — rollover + replacement each driven through the full move-out lifecycle (meter → settlement → payment → close), flagged usage verified in the persisted bill')
}

main().catch((e) => { console.error(`\n❌ ${e.message}\n${e.stack ?? ''}`); process.exit(1) })
