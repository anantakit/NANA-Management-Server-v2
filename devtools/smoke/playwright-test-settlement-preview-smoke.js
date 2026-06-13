// Settlement Preview — Smoke Test (under 2026-05-02 drawer workflow)
// ----------------------------------------------------------------------
// Replaces playwright-test-settlement-preview-legacy.js, which tested the
// removed standalone "ดูสรุปยอด" SettlementPreviewDrawer that lived on
// MoveOutDetailPage. The settlement-preview surface is now Step 2
// (SettlementStep) inside RoomWorkflowDrawer; its aria-label is
// "ดำเนินการย้ายออก", not "สรุปยอดย้ายออก".
//
// Slim port: keeps only the cases that aren't already pinned by
// smoke:moveout-step23 (confirm chain → bucket transition / edit-back /
// rent-mode warning) or smoke:moveout-detail (per-state CTA + drawer
// step routing). Drops TC4 / TC5 / TC8 / TC9 / TC12 (all redundant).
//
//   TC1   Baseline preview surface renders inside SettlementStep
//   TC2   Rent-mode toggle updates outcome amount + mode label
//   TC3   MinMonths threshold flip — outcome amount differs across modes
//         (PRORATED fails minMonths / FULL_MONTH passes → distinct settlement)
//   TC6   rent_paid=true: ChargesSection has NO rent line item
//         (pinned because the rent-already-paid path skips the rent line
//         in line_items rather than rendering it as ฿0)
//   TC7   AbsorbedBillsSection visible when notice has unpaid prior bills
//   TC10  PENDING_METER → primary CTA opens drawer at Step 1 (Meter),
//         NOT Step 2 (Settlement). Pins statusToCurrentIdx routing for
//         the PENDING_METER state, which moveout-detail T2/T3/T1 don't
//         cover (they pin PENDING_SETTLEMENT/READY_TO_CLOSE/PENDING_PAYMENT).
//   TC11  Mode-switch loading UX — charges card stays visible during the
//         refetch (no full-page skeleton flash). Pins the
//         `isModeSwitching && opacity-50` overlay pattern in SettlementStep.
//
// Fixtures (all from seed_dev_smoke.go):
//   TC1  (B105) PENDING_SETTLEMENT, no draft        → TC1, TC2, TC11
//   TC3  (B201) PENDING_SETTLEMENT, minMonths flip  → TC3
//   TC6  (B203) PENDING_SETTLEMENT, M-1 PAID rent   → TC6
//   TC7  (B204) PENDING_SETTLEMENT, 2 unpaid bills  → TC7
//   TC10 (B205) PENDING_METER                       → TC10
//
// Run:  npm run smoke:settlement-preview   (from backend/devtools/smoke/)
//       make smoke-settlement-preview      (from backend/)

const { chromium } = require('playwright')

const FRONTEND = 'http://localhost:3001'
const BACKEND = 'http://localhost:8080'
const ADMIN_USER = 'admin'
const ADMIN_PASS_FRESH = 'admin123'
const ADMIN_PASS_POST = 'admin1234'

const DRAWER = '[role="dialog"][aria-label="ดำเนินการย้ายออก"]'

// Mode-label text comes from MODE_LABEL in SettlementPreviewSections.tsx.
// Stable identifier for assertions on the SettlementMeta line.
const MODE_PRORATED_LABEL = 'คิดตามวันย้ายออกจริง'
const MODE_FULL_MONTH_LABEL = 'คิดเต็มเดือนเพื่อรักษาเงินประกัน'

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
  // Retry with post-change password if fresh password was rejected (DB not reset between runs)
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

// ─── Drawer + step helpers ─────────────────────────────────────────────

async function gotoDetail(page, noticeId) {
  await page.goto(`${FRONTEND}/move-out/${noticeId}`)
  await page.waitForLoadState('networkidle')
  // h1 selector avoids matching the "ย้ายออก" sidebar entry.
  await page.locator('h1:has-text("ย้ายออก ห้อง")').first().waitFor({ timeout: 8000 })
  await page.waitForTimeout(300)
}

async function openDrawerFromDetail(page, expectedStepIdx) {
  // PENDING_METER / PENDING_SETTLEMENT use "ดำเนินการต่อ" — scope to the
  // page header / sticky CTA (the sidebar nav has no such button).
  const cta = page.locator('main button:has-text("ดำเนินการต่อ"), button:has-text("ดำเนินการต่อ")').first()
  await cta.waitFor({ state: 'visible', timeout: 5000 })
  await cta.click()
  await page.waitForSelector(DRAWER, { timeout: 5000 })
  // Step routing happens via statusToCurrentIdx; settle the sync effect
  // before reading aria-current.
  await page.waitForTimeout(400)
  if (typeof expectedStepIdx === 'number') {
    const idx = await getSelectedStepIdx(page)
    if (idx !== expectedStepIdx) {
      throw new Error(`Drawer opened at Step ${idx + 1}, expected Step ${expectedStepIdx + 1}`)
    }
  }
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

// ─── SettlementStep-specific helpers ──────────────────────────────────

async function getOutcomeAmount(page) {
  // Stable testid added on OutcomeCard's amount <p>. Returns the visible
  // formatted value (e.g. "฿1,234.56") or '' if not yet present.
  const el = page.locator(`${DRAWER} [data-testid="settlement-outcome-amount"]`).first()
  return (await el.textContent().catch(() => '') || '').trim()
}

async function getModeLine(page) {
  // SettlementMeta renders "คิดค่าเช่า: <mode-label>" as a single <p>.
  const txt = await page.locator(`${DRAWER} >> text=คิดค่าเช่า:`).first().textContent().catch(() => '')
  return (txt || '').trim()
}

async function toggleRentMode(page) {
  // aria-label pinned in SettlementMeta to survive future copy edits to
  // the visible "เปลี่ยน" text (see step23 smoke for the same rationale).
  const btn = page.locator(`${DRAWER} button[aria-label="เปลี่ยนวิธีคิดค่าเช่า"]`).first()
  await btn.waitFor({ state: 'visible', timeout: 5000 })
  await btn.click()
}

// Wait for the settlement preview to render at least one outcome amount.
// Initial fetch on Step 2 may take a beat; the wait keeps subsequent reads
// deterministic.
async function waitForPreviewReady(page) {
  await page.locator(`${DRAWER} [data-testid="settlement-outcome-amount"]`).first().waitFor({ state: 'visible', timeout: 10000 })
}

// ─── Main ─────────────────────────────────────────────────────────────

async function main() {
  console.log('━━━ Settlement Preview Smoke (drawer Step 2) ━━━')

  // Reset fixtures: cleanup before seed so prior state (e.g. an advanced
  // TC1 from a previous run) doesn't keep this run from PENDING_SETTLEMENT.
  const cleanupRes = await fetch(`${BACKEND}/api/v1/dev/smoke/cleanup`, { method: 'POST' })
  if (!cleanupRes.ok) throw new Error(`Cleanup failed: ${cleanupRes.status}`)
  const seedRes = await fetch(`${BACKEND}/api/v1/dev/smoke/seed`, { method: 'POST' })
  if (!seedRes.ok) throw new Error(`Seed failed: ${seedRes.status}`)
  console.log('  ✅ Smoke fixtures cleaned + re-seeded')

  const fixtures = await fetchFixtures()
  const requireFixture = (key) => {
    if (!fixtures[key]) throw new Error(`${key} fixture missing`)
    return fixtures[key]
  }
  const tc1 = requireFixture('TC1')
  const tc3 = requireFixture('TC3')
  const tc6 = requireFixture('TC6')
  const tc7 = requireFixture('TC7')
  const tc10 = requireFixture('TC10')

  const browser = await chromium.launch({ headless: false, slowMo: 60 })
  const ctx = await browser.newContext({ viewport: { width: 1400, height: 900 } })
  const page = await ctx.newPage()

  let fatal = false
  try {
    await login(page)
    console.log('  ✅ UI login complete')

    // ─── TC1: Baseline preview renders in SettlementStep ───────────────
    console.log('\n[TC1] Baseline preview renders in SettlementStep')
    await gotoDetail(page, tc1.notice_id)
    await openDrawerFromDetail(page, /* Step 2 */ 1)
    await waitForPreviewReady(page)

    const tc1Mode = await getModeLine(page)
    const tc1Amount = await getOutcomeAmount(page)
    const tc1HasCharges = await page.locator(`${DRAWER} >> text=ค่าใช้จ่ายรอบย้ายออก`).first().isVisible().catch(() => false)
    const tc1HasDeposit = await page.locator(`${DRAWER} >> text=เงินประกัน`).first().isVisible().catch(() => false)
    // No-draft path: primary CTA reads "ยืนยันยอด → ไปชำระเงิน".
    const tc1HasConfirmCta = await page.locator(`${DRAWER} button:has-text("ยืนยันยอด")`).first().isVisible().catch(() => false)
    // Secondary "แก้ไขรายละเอียด" must NOT render in NO_DRAFT mode.
    const tc1NoEditDetailsCta = !(await page.locator(`${DRAWER} button:has-text("แก้ไขรายละเอียด")`).first().isVisible().catch(() => false))

    check('Drawer at Step 2 (PENDING_SETTLEMENT routing)', (await getSelectedStepIdx(page)) === 1)
    check('SettlementMeta visible (rent-mode line)', tc1Mode.includes('คิดค่าเช่า:'))
    check('OutcomeCard amount visible', /\d/.test(tc1Amount), tc1Amount)
    check('ChargesSection visible', tc1HasCharges)
    check('SettlementSection visible (เงินประกัน row)', tc1HasDeposit)
    check('Primary CTA "ยืนยันยอด" visible (NO_DRAFT path)', tc1HasConfirmCta)
    check('No "แก้ไขรายละเอียด" secondary CTA in NO_DRAFT mode', tc1NoEditDetailsCta)

    // Stay on TC1 drawer for TC2 and TC11.

    // ─── TC2: Rent-mode toggle updates outcome amount + mode label ─────
    console.log('\n[TC2] Toggle rent mode → mode label + outcome amount update')
    const tc2BeforeMode = (await getModeLine(page)).includes(MODE_PRORATED_LABEL) ? 'PRORATED' : 'FULL_MONTH'
    const tc2BeforeAmount = await getOutcomeAmount(page)

    await toggleRentMode(page)
    // Preview refetch debounce; mode-switch keeps content visible (see TC11).
    await page.waitForTimeout(700)

    const tc2AfterMode = (await getModeLine(page)).includes(MODE_PRORATED_LABEL) ? 'PRORATED' : 'FULL_MONTH'
    const tc2AfterAmount = await getOutcomeAmount(page)
    check(
      'Mode label flipped (PRORATED ↔ FULL_MONTH)',
      tc2BeforeMode !== tc2AfterMode,
      `${tc2BeforeMode} → ${tc2AfterMode}`,
    )
    check(
      'Outcome amount updated after toggle',
      tc2BeforeAmount !== tc2AfterAmount && /\d/.test(tc2AfterAmount),
      `${tc2BeforeAmount} → ${tc2AfterAmount}`,
    )

    // ─── TC11: Loading UX — charges card stays visible during refetch ──
    console.log('\n[TC11] Mode switch keeps content visible (no full skeleton flash)')
    // Toggle again; immediately probe charges card visibility before the
    // refetch settles. The skeleton has no "ค่าใช้จ่ายรอบย้ายออก" text —
    // if visible, we know the preview content stayed mounted.
    await toggleRentMode(page)
    // Probe quickly before the refetch settles. Don't waitForTimeout; we
    // want the in-flight window.
    const tc11ChargesVisibleDuring = await page
      .locator(`${DRAWER} >> text=ค่าใช้จ่ายรอบย้ายออก`)
      .first()
      .isVisible()
      .catch(() => false)
    await page.waitForTimeout(700) // settle for clean state on next TC
    const tc11ChargesVisibleAfter = await page
      .locator(`${DRAWER} >> text=ค่าใช้จ่ายรอบย้ายออก`)
      .first()
      .isVisible()
      .catch(() => false)
    check('Charges card visible DURING mode-switch refetch', tc11ChargesVisibleDuring)
    check('Charges card still visible AFTER refetch settles', tc11ChargesVisibleAfter)

    await closeDrawer(page)

    // ─── TC3: MinMonths threshold flip → outcome differs per mode ──────
    console.log('\n[TC3] MinMonths threshold flip — outcome differs across modes')
    await gotoDetail(page, tc3.notice_id)
    await openDrawerFromDetail(page, /* Step 2 */ 1)
    await waitForPreviewReady(page)

    const tc3FirstMode = (await getModeLine(page)).includes(MODE_PRORATED_LABEL) ? 'PRORATED' : 'FULL_MONTH'
    const tc3FirstAmount = await getOutcomeAmount(page)

    await toggleRentMode(page)
    await page.waitForTimeout(700)

    const tc3SecondMode = (await getModeLine(page)).includes(MODE_PRORATED_LABEL) ? 'PRORATED' : 'FULL_MONTH'
    const tc3SecondAmount = await getOutcomeAmount(page)

    check(
      'TC3 has outcome in both modes',
      /\d/.test(tc3FirstAmount) && /\d/.test(tc3SecondAmount),
      `${tc3FirstMode}=${tc3FirstAmount} / ${tc3SecondMode}=${tc3SecondAmount}`,
    )
    // Mode-dependent deposit handling: PRORATED fails minMonths → forfeits
    // deposit → owed = (dailyRate × actualMoveOut.day) + utilities + fees.
    // FULL_MONTH passes minMonths → returns deposit → owed = monthly_rent
    // + utilities + fees − deposit. Utilities + fees cancel in the diff,
    // so the delta is (dailyRate × day) − (monthly_rent − deposit).
    //
    // TC3 fixture: room B201 (rent ฿2,500 / deposit ฿2,000), actualOffset = 0
    // (today), PRORATE_DAILY_RATE = ฿100. The delta CAN equal 0 on the
    // calendar day where 100 × day == 2,500 − 2,000 = 500 (i.e. day 5).
    // The original assertion "amounts differ" tripped exactly then; mode
    // semantics still flip — they just happen to produce the same owed
    // total. Assert the formula, not the inequality.
    const tc3Today = new Date()
    const tc3Day = tc3Today.getUTCDate()
    const TC3_DAILY_RATE = 100
    const TC3_MONTHLY_RENT = 2500
    const TC3_DEPOSIT = 2000
    const expectedDelta = TC3_DAILY_RATE * tc3Day - (TC3_MONTHLY_RENT - TC3_DEPOSIT)
    const parse = (s) => Number(String(s).replace(/[^\d.-]/g, ''))
    const tc3Prorated = tc3FirstMode === 'PRORATED' ? parse(tc3FirstAmount) : parse(tc3SecondAmount)
    const tc3FullMonth = tc3FirstMode === 'PRORATED' ? parse(tc3SecondAmount) : parse(tc3FirstAmount)
    const observedDelta = tc3Prorated - tc3FullMonth
    check(
      'Mode delta matches formula (dailyRate × day − (monthly_rent − deposit))',
      Math.abs(observedDelta - expectedDelta) < 0.01,
      `expected=${expectedDelta} observed=${observedDelta} day=${tc3Day} PRORATED=${tc3Prorated} FULL=${tc3FullMonth}`,
    )

    await closeDrawer(page)

    // ─── TC6: rent_paid=true skips rent line in ChargesSection ─────────
    console.log('\n[TC6] rent_paid=true: ChargesSection has NO rent line')
    await gotoDetail(page, tc6.notice_id)
    await openDrawerFromDetail(page, /* Step 2 */ 1)
    await waitForPreviewReady(page)

    // ChargesSection BreakdownCard is the only block with the
    // "ค่าใช้จ่ายรอบย้ายออก" title. Use its outer wrapper as a scope so
    // the assertion can't be confused by future absorbed-bills copy.
    const chargesScope = page.locator(`${DRAWER}`).locator('div', {
      has: page.locator('p', { hasText: /^ค่าใช้จ่ายรอบย้ายออก$/ }),
    }).first()
    const tc6ChargesVisible = await chargesScope.isVisible().catch(() => false)
    // BreakdownRow renders item.description as the row label. In TC6, the
    // M-1 PAID monthly bill carries forward the rent, so backend omits the
    // rent line entirely — there is no "ค่าเช่า..." description in the
    // settlement charges block.
    const tc6RentRowCount = await chargesScope.locator('text=/^ค่าเช่า/').count()

    check('ChargesSection visible for rent-paid case', tc6ChargesVisible)
    check('No rent line in ChargesSection (rent_paid path)', tc6RentRowCount === 0, `rent rows=${tc6RentRowCount}`)

    await closeDrawer(page)

    // ─── TC7: Absorbed bills section visible ───────────────────────────
    console.log('\n[TC7] AbsorbedBillsSection visible when notice has unpaid prior bills')
    await gotoDetail(page, tc7.notice_id)
    await openDrawerFromDetail(page, /* Step 2 */ 1)
    await waitForPreviewReady(page)

    const tc7Absorbed = await page
      .locator(`${DRAWER} >> text=บิลค้างที่ต้องชำระก่อนย้ายออก`)
      .first()
      .isVisible()
      .catch(() => false)
    check('"บิลค้างที่ต้องชำระก่อนย้ายออก" header visible', tc7Absorbed)

    await closeDrawer(page)

    // ─── TC10: PENDING_METER → drawer opens at Step 1, NOT Step 2 ─────
    console.log('\n[TC10] PENDING_METER opens drawer at Step 1 (Meter), not Step 2')
    await gotoDetail(page, tc10.notice_id)
    // Don't pre-assert a step here — open and inspect actual idx.
    const ctaTc10 = page.locator('main button:has-text("ดำเนินการต่อ"), button:has-text("ดำเนินการต่อ")').first()
    await ctaTc10.waitFor({ state: 'visible', timeout: 5000 })
    await ctaTc10.click()
    await page.waitForSelector(DRAWER, { timeout: 5000 })
    await page.waitForTimeout(400)
    const tc10Step = await getSelectedStepIdx(page)
    check('Drawer opens at Step 1 (Meter) for PENDING_METER', tc10Step === 0, `idx=${tc10Step}`)
    // Negative guard: SettlementStep markers must NOT be on screen.
    const tc10NoSettlementMode = !(await page.locator(`${DRAWER} >> text=คิดค่าเช่า:`).first().isVisible().catch(() => false))
    const tc10NoCharges = !(await page.locator(`${DRAWER} >> text=ค่าใช้จ่ายรอบย้ายออก`).first().isVisible().catch(() => false))
    check('SettlementMeta NOT rendered (we are on Step 1, not Step 2)', tc10NoSettlementMode)
    check('ChargesSection NOT rendered (Step 1 active)', tc10NoCharges)

    await closeDrawer(page)

    console.log(`\n━━━ Result: ${results.pass}/${results.total} pass, ${results.fail} fail ━━━`)
    if (results.failedCases.length) {
      console.log('Failed checks:')
      for (const name of results.failedCases) console.log(`  • ${name}`)
    }
  } catch (err) {
    fatal = true
    console.error('\n💥 Fatal:', err.message)
    console.error(err.stack)
    await page.screenshot({ path: '/tmp/smoke-settlement-preview-fatal.png' }).catch(() => {})
  } finally {
    await browser.close()
  }

  process.exit(fatal || results.fail > 0 ? 1 : 0)
}

main().catch((err) => {
  console.error('FATAL:', err)
  process.exit(2)
})
