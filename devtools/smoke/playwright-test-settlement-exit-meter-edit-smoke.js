// F2 Slice 2A — Settlement exit-meter EDIT path: browser regression gate.
// ----------------------------------------------------------------------
// The named blocker in the F2 audit (EPIC_B_F1_EXIT_METER_SURFACE_AUDIT.md,
// §F2.7) is the ABSENCE of a browser smoke on the live Settlement edit path.
// The MeterStep smoke (playwright-test-exit-meter-flags-smoke.js) covers the
// CREATE path via the Move-out Queue → RoomWorkflowDrawer. NOTHING covered the
// second live surface: SettlementPage → "แก้ไข" pencil → ExitMeterDrawer (edit).
//
// This smoke closes that gap so Slice 2B (shared form-body extraction) can
// refactor the surface with a regression net underneath it. It is ADDITIVE —
// a new test + a make target, NO production code change.
//
// Covered path:
//   /move-out/:id/settlement (PENDING_SETTLEMENT + draft bill + exit meter)
//     → meter strip "แก้ไข" pencil → ExitMeterDrawer (edit mode)
//     → hydrate → below-prev blocks → rollover unblocks → replacement from zero
//     → save → updateExitMeter (NOT recordExitMeter) → drawer closes
//     → settlement bill regenerated (new settlement_bill_id) → form re-hydrates
//
// Coverage ↔ F2.7 Slice 2A checklist:
//   1. existing exit meter HYDRATED (prev + current + rollover flags)      — item 1
//   2. below-prev BLOCKS before any flag (save disabled + error)          — item 2
//   3. rollover UNBLOCKS + digit-wrap usage                               — item 3
//   4. replacement uses previous = 0, usage counts from zero              — item 4
//   5. save calls updateExitMeter (edit), NOT recordExitMeter (create)    — item 5
//   6. drawer closes / returns to settlement (current behavior)           — item 6
//   7. settlement form/preview REFRESHED (new settlement_bill_id)         — item 7
//   8. mobile path — ONLY asserted if the pencil renders at 375px         — item 8
//
// Fixtures: /dev/smoke/seed → TC1 (B105, desktop) + TC13 (C201, mobile). Both
// land at PENDING_SETTLEMENT with a recorded EXIT reading (elec 1000→1135,
// water 100→118) and NO pre-attached bill, so the settlement page cleanly
// auto-generates the draft on mount → isEditable → the "แก้ไข" pencil renders.
// (The TC4/TC22/D202 fixtures pre-seed an *unlinked* draft settlement bill, so
// the page's auto-generate collides on idx_bills_unique_settlement — not usable
// as a clean edit surface.) Reuses the sibling settlement/draft smoke seed
// helpers (cleanup → seed → fixtures) — no hand-rolled seeding path.
//
// Run:  npm run smoke:settlement-exit-meter-edit   (from backend/devtools/smoke/)
//       make smoke-settlement-exit-meter-edit      (from backend/)

const { chromium } = require('playwright')

const FRONTEND = 'http://localhost:3001'
const BACKEND = 'http://localhost:8080'
const ADMIN_USER = 'admin'
const ADMIN_PASS_FRESH = 'admin123'
const ADMIN_PASS_POST = 'admin1234'

const DRAWER = '[role="dialog"][aria-label="แก้ไขมิเตอร์"]'
const READY = '[data-testid="exit-meter-ready"]'
const BELOW_PREV = 'เลขมิเตอร์ต้องไม่น้อยกว่าครั้งก่อน'

// Seeded EXIT reading (createSmokeScenarioReturn default, seed_dev_smoke.go):
//   electricity 1000 → 1135 (135 units) · water 100 → 118 (18 units)
const ELEC_PREV = 1000
const ELEC_CUR = 1135
const WATER_PREV = 100
const WATER_CUR = 118
// Below-prev electricity value + its digit-wrap rollover usage:
//   rolloverUsage(1000, 500) = (digitMax(1000) - 1000) + 500 = (9999-1000)+500 = 9499
const ELEC_BELOW = 500
const ELEC_ROLLOVER_USAGE = 9499

const results = { pass: 0, fail: 0, total: 0, failedCases: [] }
const check = (name, ok, detail = '') => {
  console.log(`  ${ok ? '✅' : '❌'} ${name}${detail ? ` — ${detail}` : ''}`)
  results.total++
  if (ok) results.pass++
  else { results.fail++; results.failedCases.push(name) }
  return ok
}

// ─── Auth + fixtures (mirror sibling settlement smokes) ──────────────────

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

async function seed() {
  await fetch(`${BACKEND}/api/v1/dev/smoke/cleanup`, { method: 'POST' })
  await fetch(`${BACKEND}/api/v1/dev/smoke/seed`, { method: 'POST' })
  const res = await fetch(`${BACKEND}/api/v1/dev/smoke/fixtures`)
  const json = await res.json()
  const map = {}
  for (const f of json.data) {
    const tc = f.tenant_name.match(/^TC(\d+)_SMOKE/)?.[1]
    if (tc) map[`TC${tc}`] = f
  }
  return map
}

// ─── Page helpers ────────────────────────────────────────────────────────

// Navigate to the settlement page; the page auto-generates the draft bill on
// mount, so the charges section only appears once settlementBill exists —
// which is exactly the condition (PENDING_SETTLEMENT + bill) that makes
// isEditable true and renders the meter "แก้ไข" pencil.
async function goToSettlement(page, noticeId) {
  await page.goto(`${FRONTEND}/move-out/${noticeId}/settlement`)
  await page.waitForLoadState('networkidle')
  await page.waitForSelector('text=รายการค่าใช้จ่าย', { timeout: 15000 })
  await page.waitForTimeout(500)
}

// The meter-strip "แก้ไข" pencil is the only control labelled "แก้ไข" on the
// settlement page (back="กลับ", rent="เปลี่ยน", charges="เพิ่มรายการ"/"คำนวณใหม่").
function meterEditPencil(page) {
  return page.getByRole('button', { name: 'แก้ไข', exact: true })
}

async function openMeterDrawer(page) {
  await meterEditPencil(page).first().click()
  await page.waitForSelector(DRAWER, { timeout: 8000 })
  await page.waitForSelector(READY, { timeout: 8000 })
  await page.waitForTimeout(300)
}

// Click save; if the anomaly-review phase intervenes (rollover usage well above
// baseline), confirm it. Returns the /update-exit-meter response.
async function saveAndConfirm(page, drawer) {
  const updatePromise = page.waitForResponse((r) => r.url().includes('/update-exit-meter'), { timeout: 12000 })
  await drawer.getByRole('button', { name: 'บันทึก', exact: true }).click()
  const confirmBtn = drawer.getByRole('button', { name: 'ยืนยัน', exact: true })
  await confirmBtn.waitFor({ state: 'visible', timeout: 1800 })
    .then(() => confirmBtn.click())
    .catch(() => {}) // no anomaly → mutation already fired on the บันทึก click
  return updatePromise
}

// ─── Desktop — full edit path on TC4 ──────────────────────────────────────

async function runDesktop(page, noticeId) {
  console.log(`\nDESKTOP 1440px — Settlement edit path (TC1 / B105, notice ${noticeId.slice(0, 8)})`)

  // Track the settlement_bill_id seen on GET detail + which meter endpoints fire.
  let seenBillId = null
  let recordHit = false
  let updateHit = false
  const detailRe = new RegExp(`/move-out-notices/${noticeId}(\\?|$)`)
  page.on('response', async (r) => {
    try {
      if (r.request().method() === 'GET' && detailRe.test(r.url())) {
        const j = await r.json()
        if (j?.data?.settlement_bill_id) seenBillId = j.data.settlement_bill_id
      }
    } catch { /* non-JSON / detached — ignore */ }
  })
  page.on('request', (req) => {
    if (req.url().includes('/record-exit-meter')) recordHit = true
    if (req.url().includes('/update-exit-meter')) updateHit = true
  })

  await goToSettlement(page, noticeId)

  // Strip shows the seeded reading before any edit.
  check('settlement meter strip shows seeded electricity (→ 1,135) before edit',
    await page.getByText(`${ELEC_PREV.toLocaleString()} → ${ELEC_CUR.toLocaleString()}`).first().isVisible().catch(() => false))
  check('meter "แก้ไข" pencil renders (isEditable: PENDING_SETTLEMENT + bill)',
    await meterEditPencil(page).first().isVisible())

  await openMeterDrawer(page)
  const drawer = page.locator(DRAWER)

  // ── item 1: hydration ──
  const elecVal = await page.locator('#drawer_electricity').inputValue()
  const waterVal = await page.locator('#drawer_water').inputValue()
  check('[1] hydrates electricity current from stored EXIT reading (1135)', elecVal === String(ELEC_CUR), elecVal)
  check('[1] hydrates water current from stored EXIT reading (118)', waterVal === String(WATER_CUR), waterVal)
  check('[1] previous hints show stored previous (1,000 elec + 100 water)',
    (await drawer.getByText(`ครั้งก่อน: ${ELEC_PREV.toLocaleString()}`).isVisible().catch(() => false)) &&
    (await drawer.getByText(`ครั้งก่อน: ${WATER_PREV.toLocaleString()}`).isVisible().catch(() => false)))
  const elecRoll = page.locator('[aria-label="มิเตอร์ไฟฟ้า — มิเตอร์ครบรอบ"]')
  const waterRoll = page.locator('[aria-label="มิเตอร์น้ำ — มิเตอร์ครบรอบ"]')
  check('[1] rollover flags default OFF (no stored rollover on seeded reading)',
    (await elecRoll.getAttribute('aria-pressed')) === 'false' &&
    (await waterRoll.getAttribute('aria-pressed')) === 'false')

  // ── item 2: below-prev blocks before any flag ──
  await page.fill('#drawer_electricity', String(ELEC_BELOW))
  await page.waitForTimeout(400)
  const saveBtn = drawer.getByRole('button', { name: 'บันทึก', exact: true })
  check('[2] below-previous error shown for 500 < 1000 (no flag yet)',
    await drawer.getByText(BELOW_PREV).first().isVisible().catch(() => false))
  check('[2] save DISABLED while below-previous and no flag', await saveBtn.isDisabled())

  // ── item 3: rollover unblocks + digit-wrap usage ──
  await elecRoll.click()
  await page.waitForTimeout(400)
  check('[3] below-previous error CLEARED after rollover toggle',
    !(await drawer.getByText(BELOW_PREV).first().isVisible().catch(() => false)))
  check('[3] rollover chip aria-pressed=true', (await elecRoll.getAttribute('aria-pressed')) === 'true')
  check('[3] digit-wrap usage shows 9,499 หน่วย',
    await drawer.getByText(`ใช้ไป ${ELEC_ROLLOVER_USAGE.toLocaleString()} หน่วย`).first().isVisible().catch(() => false))
  check('[3] save ENABLED after rollover unblocks the reading', await saveBtn.isEnabled())

  // ── item 4: replacement uses previous = 0, usage counts from zero ──
  // Water is 118 vs prev 100 → normal usage 18. Replacement re-bases to 0 → 118.
  const waterRepl = page.locator('[aria-label="มิเตอร์น้ำ — เปลี่ยนมิเตอร์ใหม่"]')
  check('[4] water usage is 18 (118 - 100) before replacement',
    await drawer.getByText('ใช้ไป 18 หน่วย', { exact: true }).first().isVisible().catch(() => false))
  await waterRepl.click()
  await page.waitForTimeout(400)
  check('[4] replacement chip aria-pressed=true', (await waterRepl.getAttribute('aria-pressed')) === 'true')
  check('[4] usage counts from zero after replacement (118 หน่วย)',
    await drawer.getByText(`ใช้ไป ${WATER_CUR.toLocaleString()} หน่วย`, { exact: true }).first().isVisible().catch(() => false))
  // Reset water to normal so the saved reading is minimal (elec rollover only) —
  // keeps the post-save strip assertion clean.
  await waterRepl.click()
  await page.waitForTimeout(300)

  // ── items 5-7: save → updateExitMeter → drawer closes → settlement re-init ──
  const billBefore = seenBillId
  const resp = await saveAndConfirm(page, drawer)
  check('[5] save fired /update-exit-meter (edit path, status < 400)', resp.status() < 400, `HTTP ${resp.status()}`)
  check('[5] /record-exit-meter was NEVER called (not the create path)', !recordHit)
  check('[5] /update-exit-meter observed on the wire', updateHit)

  const respBody = await resp.json().catch(() => null)
  const billAfter = respBody?.data?.settlement_bill_id ?? null

  // item 6: drawer closes back to the settlement page.
  await page.locator(DRAWER).waitFor({ state: 'detached', timeout: 8000 }).catch(() => {})
  check('[6] drawer closed after save (returned to settlement)',
    !(await page.locator(READY).isVisible().catch(() => false)) && page.url().includes('/settlement'))

  // item 7: settlement bill regenerated (new id) + FE strip re-hydrated to 500.
  check('[7] settlement_bill_id changed after edit (draft void+regenerated)',
    !!billBefore && !!billAfter && billBefore !== billAfter,
    `before=${billBefore?.slice(0, 8)} after=${billAfter?.slice(0, 8)}`)
  await page.waitForTimeout(1200)
  check('[7] settlement meter strip re-hydrated to new reading (→ 500)',
    await page.getByText(`${ELEC_PREV.toLocaleString()} → ${ELEC_BELOW.toLocaleString()}`).first().isVisible().catch(() => false))

  await page.screenshot({ path: '/tmp/smoke-settlement-exit-meter-edit-desktop.png' })
}

// ─── Mobile — item 8, gated on the pencil rendering at 375px ───────────────

async function runMobile(page, noticeId) {
  console.log(`\nMOBILE 375px — Settlement edit path (TC13 / C201, notice ${noticeId.slice(0, 8)})`)
  await goToSettlement(page, noticeId)

  // Verify the render condition FIRST — do not assert a control that isn't there.
  const pencilVisible = await meterEditPencil(page).first().isVisible().catch(() => false)
  if (!pencilVisible) {
    check('[8] meter "แก้ไข" pencil does NOT render at 375px — mobile edit path unavailable (recorded, not asserted)', true)
    await page.screenshot({ path: '/tmp/smoke-settlement-exit-meter-edit-mobile.png' })
    return
  }
  check('[8] meter "แก้ไข" pencil renders at 375px — mobile edit path available', true)

  await openMeterDrawer(page)
  const drawer = page.locator(DRAWER)

  // Hydration holds on mobile.
  check('[8] mobile hydrates electricity current (1135)',
    (await page.locator('#drawer_electricity').inputValue()) === String(ELEC_CUR))

  // below-prev blocks, then rollover unblocks — the gate works on mobile too.
  await page.fill('#drawer_electricity', String(ELEC_BELOW))
  await page.waitForTimeout(400)
  const saveBtn = drawer.getByRole('button', { name: 'บันทึก', exact: true })
  check('[8] mobile below-previous blocks save (500 < 1000, no flag)',
    (await drawer.getByText(BELOW_PREV).first().isVisible().catch(() => false)) && (await saveBtn.isDisabled()))

  const elecRoll = page.locator('[aria-label="มิเตอร์ไฟฟ้า — มิเตอร์ครบรอบ"]')
  const box = await elecRoll.boundingBox()
  check('[8] mobile flag chip touch target ≥ 40px', !!box && box.height >= 40, box ? `${Math.round(box.height)}px` : 'no box')
  await elecRoll.click()
  await page.waitForTimeout(400)
  check('[8] mobile rollover unblocks save', await saveBtn.isEnabled())

  // Read-only mobile pass — cancel out (desktop already proved save → refresh).
  await drawer.getByRole('button', { name: 'ยกเลิก', exact: true }).click().catch(() => {})
  await page.screenshot({ path: '/tmp/smoke-settlement-exit-meter-edit-mobile.png' })
}

// ─── Main ──────────────────────────────────────────────────────────────

async function main() {
  console.log('━━━ F2 Slice 2A — Settlement exit-meter EDIT path browser smoke ━━━')
  const fixtures = await seed()
  const DESKTOP_FX = 'TC1'  // B105 — happy path, no pre-attached bill
  const MOBILE_FX = 'TC13'  // C201 — paid carry-forward, no pre-attached bill
  const missing = [DESKTOP_FX, MOBILE_FX].filter((k) => !fixtures[k])
  if (missing.length) {
    console.error(`❌ missing fixtures: ${missing.join(', ')} — cannot continue`)
    process.exit(1)
  }
  console.log(`  ✅ seeded — ${DESKTOP_FX} (${fixtures[DESKTOP_FX].room_number}) + ${MOBILE_FX} (${fixtures[MOBILE_FX].room_number}) @ PENDING_SETTLEMENT`)

  // HEADED=1 opens a visible browser (with a small slowMo) for watching a run;
  // default stays headless so `make smoke-all` / CI need no display.
  const headed = !!process.env.HEADED
  const browser = await chromium.launch({ headless: !headed, slowMo: headed ? 120 : 0 })
  try {
    const desktop = await browser.newContext({ viewport: { width: 1440, height: 900 } })
    const dp = await desktop.newPage()
    await login(dp)
    console.log('  ✅ UI login complete')
    await runDesktop(dp, fixtures[DESKTOP_FX].notice_id)
    await desktop.close()

    const mobile = await browser.newContext({ viewport: { width: 375, height: 812 } })
    const mp = await mobile.newPage()
    await login(mp)
    await runMobile(mp, fixtures[MOBILE_FX].notice_id)
    await mobile.close()
  } finally {
    await browser.close()
  }

  console.log('\n🧹 Cleanup smoke fixtures...')
  await fetch(`${BACKEND}/api/v1/dev/smoke/cleanup`, { method: 'POST' }).catch(() => {})

  console.log(`\n━━━ ${results.pass}/${results.total} passed ━━━`)
  if (results.fail > 0) {
    console.log('  Failed:', results.failedCases.join(', '))
    process.exit(1)
  }
  console.log('✅ PASS — Settlement edit path (pencil → ExitMeterDrawer → updateExitMeter → re-init) has a persisted regression gate')
}

main().catch((e) => { console.error(`\n❌ ${e.message}\n${e.stack ?? ''}`); process.exit(1) })
