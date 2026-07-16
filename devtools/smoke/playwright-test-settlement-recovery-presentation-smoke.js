// Epic B — Settlement Recovery PRESENTATION smoke (browser, self-seeding).
// ----------------------------------------------------------------------
// Locks the FRONTEND presentation + wiring of a settlement recovery credit
// through the CANONICAL entry (a real `/bills/:id` load + move-out detail),
// re-runnably and with no hardcoded ids / no dependency on prior DB state.
//
// The bill is seeded via the OWNER-DECIDED Model B flow (observe-once-at-exit):
// record-exit-meter with the over-record flag re-anchors from the single move-out
// observation, so the electricity usage line is 0 and the refund is recorded −
// observed (จริง 1240). The FE presentation is model-agnostic — it renders
// whatever line items the backend emits — so this smoke's assertions (substrings,
// not amounts) hold across Model A → Model B unchanged.
//
// This is the reachability half of Epic B closure. The GROUPING logic itself
// (DQ4a: N same-utility recoveries → one line, provenance preserved) is fully
// covered by unit tests:
//   - frontend/src/features/bills/domain/breakdownProjection.test.ts
//   - frontend/src/features/bills/components/adjustmentNoteRender.test.tsx
//   - frontend/src/features/move-out/components/BreakdownPanel.test.tsx
// The dev fixture produces a SINGLE recovery, so this browser smoke proves the
// render + provenance wiring that the grouped path reuses identically (same
// `projectBreakdownItems`, same `HistorySourceLink`, same
// `onOpenHistoryAtRecovery`), not the N>1 collapse.
//
// Coverage:
//   1. Bill Detail renders the recovery as "คืนค่าไฟฟ้า" (not a raw negative)
//   2. Over-record evidence ("จดไว้ … → จริง …") is shown
//   3. Provenance affordance ("ดูในประวัติมิเตอร์") appears (room resolved)
//   4. Clicking it opens RoomHistoryDrawer at the CORRECT room's history
//   5. Move-out BreakdownPanel uses the SAME presentation (DQ0 parity)
//   6. Mobile (375px) still renders the recovery line
//
// Self-seed: POST /dev/smoke/settlement-recovery-setup (re-creates a clean
// SR-SETTLE fixture in PENDING_METER, wiping the room's prior bills/notices/
// readings), then drives record-exit-meter(over-record) → generate → finalize
// via HTTP to reach a FINALIZED settlement carrying the recovery. Teardown: re-run setup, which
// removes the settlement bill + recovery this run created (the shared
// find-or-create room/contract/tenant scaffolding stays, by design).
//
// Requires: `make dev` (postgres + backend :8080 + frontend :3001, ENV=development)
//           + `make smoke-install` once.
// Run:      npm run smoke:settlement-recovery-presentation
//           (SMOKE_HEADED=1 to watch the browser)

const { chromium } = require('playwright')

const FRONTEND = process.env.FRONTEND || 'http://localhost:3001'
const BACKEND = process.env.BACKEND || 'http://localhost:8080'
const V1 = `${BACKEND}/api/v1`
const ADMIN_USER = 'admin'
const ADMIN_PASS = 'admin1234'
const ADMIN_PASS_FRESH = 'admin123'

let failed = 0
function check(name, ok, detail = '') {
  console.log(`  ${ok ? '✅' : '❌'} ${name}${detail ? ' — ' + detail : ''}`)
  if (!ok) failed++
}

// ─── HTTP helpers (setup / drive / teardown) ────────────────────────────

async function api(method, path, { token, body } = {}) {
  const headers = { 'Content-Type': 'application/json' }
  if (token) headers.Authorization = `Bearer ${token}`
  const r = await fetch(`${V1}${path}`, {
    method,
    headers,
    body: body ? JSON.stringify(body) : undefined,
  })
  let json = null
  try {
    json = await r.json()
  } catch {
    /* empty body */
  }
  return { status: r.status, json }
}

// Normalizes admin creds to ADMIN_PASS (handles a fresh DB where admin starts
// on ADMIN_PASS_FRESH + must_change_password) so the browser login below can
// always use ADMIN_PASS.
async function loginHttp() {
  for (const pw of [ADMIN_PASS, ADMIN_PASS_FRESH]) {
    const r = await api('POST', '/auth/login', { body: { username: ADMIN_USER, password: pw } })
    if (r.status !== 200 || !r.json?.data?.access_token) continue
    let token = r.json.data.access_token
    if (r.json.data.must_change_password) {
      await api('POST', '/auth/change-password', {
        token,
        body: { current_password: pw, new_password: ADMIN_PASS },
      })
      const r2 = await api('POST', '/auth/login', { body: { username: ADMIN_USER, password: ADMIN_PASS } })
      token = r2.json.data.access_token
    }
    return token
  }
  throw new Error('HTTP login failed (tried admin1234 / admin123)')
}

// Seeds a FINALIZED settlement bill carrying one electricity recovery and
// returns { billId, noticeId }. No ids are hardcoded — everything is read back
// from the fixture + the bills list.
async function seedSettlementWithRecovery(token) {
  const setup = await api('POST', '/dev/smoke/settlement-recovery-setup', { token })
  if (setup.status !== 200) throw new Error(`fixture setup ${setup.status}`)
  const fx = setup.json.data
  const notice = fx.notice_id

  // Model B: the operator observes the meter ONCE at move-out and flags the
  // over-record. record-exit-meter re-anchors from that single observation
  // (READING_RECOVERY event created before the exit reading) → 0-usage exit +
  // settlement refund (recorded − now). No separate baseline-correction / no
  // regenerate dance.
  const moveOutDate = new Date(Date.now() - 3 * 86400000).toISOString().slice(0, 10)
  const rec = await api('POST', `/move-out-notices/${notice}/record-exit-meter`, {
    token,
    body: {
      actual_move_out_date: moveOutDate,
      electricity_current: fx.exit_observation_electricity,
      water_current: fx.exit_observation_water,
      is_electricity_over_record: true,
    },
  })
  if (rec.status !== 200) throw new Error(`record-exit-meter ${rec.status}`)
  await api('POST', `/move-out-notices/${notice}/generate-settlement`, {
    token,
    body: { rent_mode: 'FULL_MONTH_KEEP_DEPOSIT' },
  })
  const fin = await api('POST', `/move-out-notices/${notice}/finalize-settlement`, { token })
  if (fin.status !== 200 && fin.status !== 201) throw new Error(`finalize-settlement ${fin.status}`)

  const list = await api(
    'GET',
    `/bills?contract_id=${fx.contract_id}&bill_type=SETTLEMENT&limit=50`,
    { token },
  )
  const live = (list.json?.data || []).filter((b) => b.status !== 'VOID')
  if (live.length !== 1) throw new Error(`expected 1 live settlement bill, got ${live.length}`)
  const detail = await api('GET', `/bills/${live[0].id}`, { token })
  const bill = detail.json.data
  const recovery = (bill.line_items || []).find(
    (li) => li.line_type === 'ADJUSTMENT' && li.adjustment_recovery_reading_id,
  )
  if (!recovery) throw new Error('seeded settlement bill has no recovery ADJUSTMENT line')
  return { billId: bill.id, noticeId: notice, roomNumber: bill.room_number }
}

// Teardown: re-running setup wipes the room's bills/notices/readings — i.e. the
// settlement bill + recovery reading this run created.
async function teardown(token) {
  try {
    await api('POST', '/dev/smoke/settlement-recovery-setup', { token })
  } catch {
    /* best-effort */
  }
}

// ─── Browser helpers ────────────────────────────────────────────────────

// Optional screenshot capture (SMOKE_SHOTS=<dir>) — for eyeballing the run
// without watching headed. No-op when unset, so the committed smoke stays clean.
async function shot(page, name, opts = {}) {
  if (!process.env.SMOKE_SHOTS) return
  await page
    .screenshot({ path: `${process.env.SMOKE_SHOTS}/${name}.png`, ...opts })
    .catch(() => {})
}

async function loginUi(page) {
  for (let attempt = 0; attempt < 3; attempt++) {
    await page.goto(`${FRONTEND}/login`, { waitUntil: 'networkidle' })
    await page.fill('input[placeholder="กรอกชื่อผู้ใช้"]', ADMIN_USER)
    await page.fill('input[placeholder="กรอกรหัสผ่าน"]', ADMIN_PASS)
    await page.click('button[type="submit"]')
    try {
      await page.waitForURL((u) => !u.toString().includes('/login'), { timeout: 12000 })
      return true
    } catch {
      /* retry once */
    }
  }
  return false
}

// ─── Main ───────────────────────────────────────────────────────────────

async function main() {
  console.log('Epic B — Settlement Recovery PRESENTATION smoke (browser)\n')
  const token = await loginHttp()
  console.log('  seeding fixture …')
  const { billId, noticeId, roomNumber } = await seedSettlementWithRecovery(token)
  console.log(`  seeded settlement bill ${billId} (room ${roomNumber})\n`)

  const browser = await chromium.launch({ headless: !process.env.SMOKE_HEADED })
  const page = await browser.newPage()
  page.on('pageerror', (e) => console.log('  ⚠ pageerror:', e.message))

  try {
    check('UI login', await loginUi(page))

    // ── Bill Detail (canonical entry) ──
    console.log('\nBill Detail — /bills/:id')
    await page.goto(`${FRONTEND}/bills/${billId}`, { waitUntil: 'networkidle' })
    await page
      .getByText('คืนค่าไฟฟ้า')
      .first()
      .waitFor({ state: 'visible', timeout: 15000 })
      .catch(() => {})
    const body = await page.textContent('body')
    check('renders "คืนค่าไฟฟ้า" refund title (not a raw negative)', body.includes('คืนค่าไฟฟ้า'))
    check('renders over-record evidence "จดไว้ … → จริง …"', body.includes('จดไว้') && body.includes('จริง'))
    await shot(page, '1-bill-detail', { fullPage: true })

    // ── Provenance link → RoomHistoryDrawer ──
    console.log('\nProvenance deep-link → RoomHistoryDrawer')
    const link = page.getByRole('button', { name: 'ดูการแก้ค่าในประวัติมิเตอร์' })
    let linkOk = true
    await link.first().waitFor({ state: 'visible', timeout: 15000 }).catch(() => (linkOk = false))
    check('provenance affordance appears (room resolved via useRooms)', linkOk)
    if (linkOk) {
      // The drawer is a two-phase-mount Sheet; click and wait for its dialog,
      // retrying the click once (the button can re-render as room state settles).
      const dialog = page.getByRole('dialog').filter({ hasText: roomNumber })
      let dialogOk = false
      for (let attempt = 0; attempt < 3 && !dialogOk; attempt++) {
        await link.first().scrollIntoViewIfNeeded().catch(() => {})
        await link.first().click().catch(() => {})
        dialogOk = await dialog
          .first()
          .waitFor({ state: 'visible', timeout: 8000 })
          .then(() => true)
          .catch(() => false)
      }
      // The filtered waitFor above is the wiring proof: the recovery credit's
      // provenance link opened RoomHistoryDrawer AT THE CORRECT ROOM (the dialog
      // is required to contain this bill's room number). The drawer's own
      // content rendering is the meter domain's surface — covered by
      // `smoke:reading-recovery`, not re-proven here.
      check('RoomHistoryDrawer opens on click (at the correct room)', dialogOk, `room=${roomNumber}`)
      // The drawer must actually render its content — not crash into the error
      // boundary. A moved-out room carries an EXIT reading (null billing_month);
      // the month-grouped history view must tolerate it.
      await page.waitForTimeout(800)
      const crashed = await page.getByText('เกิดข้อผิดพลาด').count()
      check('drawer renders without crashing (no error boundary)', dialogOk && crashed === 0)
      if (dialogOk) await shot(page, '2-provenance-drawer') // viewport (frames the right-side sheet)
      // Close the drawer before the next step (Escape).
      await page.keyboard.press('Escape').catch(() => {})
      await page.waitForTimeout(400)
    }

    // ── Mobile render ──
    console.log('\nMobile (375px)')
    await page.setViewportSize({ width: 375, height: 800 })
    await page.goto(`${FRONTEND}/bills/${billId}`, { waitUntil: 'networkidle' })
    await page
      .getByText('คืนค่าไฟฟ้า')
      .first()
      .waitFor({ state: 'visible', timeout: 15000 })
      .catch(() => {})
    const mbody = await page.textContent('body')
    check('mobile still renders the recovery refund line', mbody.includes('คืนค่าไฟฟ้า'))
    await shot(page, '3-mobile', { fullPage: true })

    // ── Move-out BreakdownPanel parity (DQ0 — one settlement document) ──
    console.log('\nMove-out detail — BreakdownPanel parity')
    await page.setViewportSize({ width: 1280, height: 1000 })
    await page.goto(`${FRONTEND}/move-out/${noticeId}`, { waitUntil: 'networkidle' })
    const trigger = page.getByRole('button', { name: /รายละเอียดค่าใช้จ่าย/ })
    let accordionOk = true
    await trigger.first().waitFor({ state: 'visible', timeout: 15000 }).catch(() => (accordionOk = false))
    check('move-out detail shows the settlement breakdown accordion', accordionOk)
    if (accordionOk) {
      await trigger.first().click()
      await page
        .getByText('คืนค่าไฟฟ้า')
        .first()
        .waitFor({ state: 'visible', timeout: 8000 })
        .catch(() => {})
      const mo = await page.textContent('body')
      check('BreakdownPanel renders "คืนค่าไฟฟ้า" (same presentation, not raw negative)', mo.includes('คืนค่าไฟฟ้า'))
      check('BreakdownPanel renders over-record evidence', mo.includes('จดไว้') && mo.includes('จริง'))
      await shot(page, '4-moveout-breakdown', { fullPage: true })
    }
  } finally {
    await browser.close()
    await teardown(token)
    console.log('\n  fixture reset (teardown)')
  }

  if (failed === 0) {
    console.log('\n✅ PASS — settlement recovery presentation smoke')
  } else {
    console.log(`\n❌ FAIL — ${failed} assertion(s) failed`)
    process.exit(1)
  }
}

main().catch((e) => {
  console.error(`\n❌ ${e.message}`)
  process.exit(1)
})
