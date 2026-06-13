// Draft Settlement — Numeric Anchor Tests (TC-D13 – TC-D17)
// ----------------------------------------------------------------------
// Verifies the UI displays **exact expected numbers** for critical
// settlement calculations. Complements D01–D12 which test structure,
// flow, and idempotency.
//
// TC-D13  Initial draft totals match backend formula
// TC-D14  Preset charge adds exact ฿250
// TC-D15  Custom deposit ฿500 subtracts exact amount
// TC-D16  Rent mode switch: PRORATED ↔ FULL_MONTH exact delta
// TC-D17  Regenerate preserves exact numbers (no drift)
//
// Fixtures: TC4 (D13–D16), TC22 (D17). Fresh seed before this pack.
//
// Run:  npm run smoke:numeric       (from backend/devtools/smoke/)

const { chromium } = require('playwright')

const FRONTEND = 'http://localhost:3001'
const BACKEND = 'http://localhost:8080'
const ADMIN_USER = 'admin'
const ADMIN_PASS_FRESH = 'admin123'
const ADMIN_PASS_POST = 'admin1234'

// ─── Seed constants (must mirror canonical planner output) ────────────
//
// These constants describe what `prepareSettlementPlan` + `addConfigFees`
// would produce for a smoke-seeded contract. See
// `smoke_formula_contract.md` — any future refactor of settlement billing
// must update both planner + this file together.

const MONTHLY_RENT  = 250000  // satang (฿2,500)
const DEPOSIT       = 200000  // satang (฿2,000)
const ELEC_RATE     = 800     // satang/unit (฿8)
const WATER_RATE    = 1800    // satang/unit (฿18)
const ELEC_UNITS    = 135
const WATER_UNITS   = 18
const CLEANING_FEE  = 30000   // satang (฿300) — CLEANING_FEE config default
const KEY_SERVICE   = 5000    // satang (฿50)  — KEY_SERVICE config default
const PRORATE_RATE  = 10000   // satang/day (฿100) — PRORATE_DAILY_RATE default
const MANUAL_1      = 50000   // satang (฿500) — ค่าซ่อมแซมห้อง
const MANUAL_2      = 30000   // satang (฿300) — ค่าเคลื่อนย้ายของ
const TC4_OFFSET    = -4      // actualMoveOutDate = today - 4
const TC22_OFFSET   = -3      // actualMoveOutDate = today - 3
const PRESET_AMOUNT = 25000   // satang (฿250) — ค่ากุญแจและคีการ์ด

// ─── Derived expected values ─────────────────────────────────────────

function daysInMonth(date) {
  return new Date(date.getFullYear(), date.getMonth() + 1, 0).getDate()
}

function computeExpected(offset, { withManual = false } = {}) {
  const today = new Date()
  const actual = new Date(today)
  actual.setDate(today.getDate() + offset)
  const day = actual.getDate()
  const dim = daysInMonth(actual)

  // Settlement planner uses flat per-day rate × days (PRORATE_DAILY_RATE
  // config), NOT MONTHLY_RENT × day / dim — refactor 2026-05-10.
  const proRateRent = PRORATE_RATE * day
  const waterAmount = WATER_UNITS * WATER_RATE
  const elecAmount  = ELEC_UNITS * ELEC_RATE
  const autoTotal   = proRateRent + waterAmount + elecAmount + CLEANING_FEE + KEY_SERVICE
  const manualTotal = withManual ? MANUAL_1 + MANUAL_2 : 0
  const total       = autoTotal + manualTotal
  const net         = total - DEPOSIT

  const fullMonthAuto = MONTHLY_RENT + waterAmount + elecAmount + CLEANING_FEE + KEY_SERVICE

  return {
    day, dim, proRateRent,
    proRateRentBaht: proRateRent / 100,
    waterBaht:       waterAmount / 100,
    elecBaht:        elecAmount / 100,
    cleaningBaht:    CLEANING_FEE / 100,
    keyServiceBaht:  KEY_SERVICE / 100,
    manual1Baht:     MANUAL_1 / 100,
    manual2Baht:     MANUAL_2 / 100,
    totalBaht:       total / 100,
    depositBaht:     DEPOSIT / 100,
    netBaht:         net / 100,
    fullMonthRentBaht: MONTHLY_RENT / 100,
    fullMonthTotalBaht: (fullMonthAuto + manualTotal) / 100,
    fullMonthNetBaht: (fullMonthAuto + manualTotal - DEPOSIT) / 100,
  }
}

// ─── Pretty logger ───────────────────────────────────────────────────

const check = (name, ok, detail = '') => {
  const msg = detail ? ` — ${detail}` : ''
  console.log(`  ${ok ? '✅' : '❌'} ${name}${msg}`)
  return ok
}

const results = { pass: 0, fail: 0, total: 0, failedCases: [] }
const track = (tc, ok) => {
  results.total++
  if (ok) results.pass++
  else { results.fail++; results.failedCases.push(tc) }
}

// ─── Page helpers ────────────────────────────────────────────────────

function parseThb(str) {
  if (!str) return NaN
  return Number(str.replace(/[^\d.-]/g, ''))
}

// Get the large net amount from the action bar.
async function getNetAmount(page) {
  const el = page.locator('.tabular-nums.text-\\[24px\\], .tabular-nums.text-xl').first()
  return parseThb(await el.textContent().catch(() => null))
}

// Get "รวมค่าใช้จ่าย" row value.
async function getChargesSubtotal(page) {
  const row = page.locator('text=รวมค่าใช้จ่าย').first()
  const parent = row.locator('xpath=ancestor::div[contains(@class,"flex")][1]')
  return parseThb(await parent.locator('.tabular-nums').first().textContent().catch(() => null))
}

// Get the amount from a specific line item row by its description prefix.
async function getLineAmount(page, descPrefix) {
  const row = page.locator(`text=${descPrefix}`).first()
  const container = row.locator('xpath=ancestor::div[contains(@class,"flex") and contains(@class,"justify-between")][1]')
  const amountEl = container.locator('.tabular-nums').first()
  return parseThb(await amountEl.textContent().catch(() => null))
}

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

async function goToSettlement(page, noticeId) {
  await page.goto(`${FRONTEND}/move-out/${noticeId}/settlement`)
  await page.waitForLoadState('networkidle')
  await page.waitForSelector('text=รายการค่าใช้จ่าย', { timeout: 15000 })
  await page.waitForTimeout(500)
}

// ─── TC-D13: Initial draft totals ────────────────────────────────────

async function runTCD13(page, fixtures) {
  console.log('\n── TC-D13: Numeric anchor — initial draft totals ──')
  const fx = fixtures.TC4
  if (!fx) return track('D13', check('Fixture TC4', false, 'missing'))

  const exp = computeExpected(TC4_OFFSET)
  console.log(`  📐 Expected: rent=${exp.proRateRentBaht} (${exp.day}/${exp.dim}d), water=${exp.waterBaht}, elec=${exp.elecBaht}, clean=${exp.cleaningBaht}`)
  console.log(`  📐 Total=${exp.totalBaht}, deposit=${exp.depositBaht}, net=${exp.netBaht}`)

  await goToSettlement(page, fx.notice_id)

  // Rent line
  // Draft rent row: SettlementChargesCard renders BE description as label
  // (not LINE_TYPE_LABEL). For PRORATE_RENT, description = "N วัน × ฿100/วัน"
  // so we match on the unique fragment "วัน × ฿". Other line descriptions
  // use "หน่วย × N บาท" pattern, no collision.
  const rentAmount = await getLineAmount(page, 'วัน × ฿')
  track('D13.1', check(`Rent = ฿${exp.proRateRentBaht}`,
    Math.abs(rentAmount - exp.proRateRentBaht) < 0.01,
    `got ${rentAmount}`,
  ))

  // Water line
  const waterAmount = await getLineAmount(page, 'ค่าน้ำ')
  track('D13.2', check(`Water = ฿${exp.waterBaht}`,
    waterAmount === exp.waterBaht,
    `got ${waterAmount}`,
  ))

  // Electricity line
  const elecAmount = await getLineAmount(page, 'ค่าไฟฟ้า')
  track('D13.3', check(`Electricity = ฿${exp.elecBaht}`,
    elecAmount === exp.elecBaht,
    `got ${elecAmount}`,
  ))

  // Cleaning fee
  const cleanAmount = await getLineAmount(page, 'ค่าทำความสะอาด')
  track('D13.4', check(`Cleaning = ฿${exp.cleaningBaht}`,
    cleanAmount === exp.cleaningBaht,
    `got ${cleanAmount}`,
  ))

  // Total charges
  const total = await getChargesSubtotal(page)
  track('D13.5', check(`Total charges = ฿${exp.totalBaht}`,
    Math.abs(total - exp.totalBaht) < 0.01,
    `got ${total}`,
  ))

  // Net amount — the UI's "ยอดสุทธิ" line shows the amount the tenant
  // OWES, clamped at 0 (refund is rendered on a separate "คืนประกัน"
  // breakdown line, not folded into the net). Calendar-sensitive: TC4
  // actualMoveOut = today - 4 days; when that lands early in the month
  // (small day-of-month), proRateRent × ฿100/day is below the gap
  // between deposit and other charges and the outcome flips to refund —
  // expected net becomes 0, not a negative number.
  const net = await getNetAmount(page)
  const expectedNetOwedBaht = Math.max(0, exp.totalBaht - exp.depositBaht)
  track('D13.6', check(`Net owed = ฿${expectedNetOwedBaht}`,
    Math.abs(net - expectedNetOwedBaht) < 0.01,
    `got ${net}`,
  ))

  await page.screenshot({ path: '/tmp/smoke-draft-d13.png' })
}

// ─── TC-D14: Preset charge adds exact ฿250 ──────────────────────────

async function runTCD14(page, fixtures) {
  console.log('\n── TC-D14: Numeric anchor — preset charge +฿250 ──')
  const fx = fixtures.TC4
  if (!fx) return track('D14', check('Fixture TC4', false, 'missing'))

  await goToSettlement(page, fx.notice_id)

  const totalBefore = await getChargesSubtotal(page)
  track('D14.1', check('Baseline total is numeric', !isNaN(totalBefore), `${totalBefore}`))

  // Add preset: ค่ากุญแจและคีการ์ด ฿250
  await page.locator('text=เพิ่มรายการ').first().click()
  await page.waitForTimeout(400)

  const chip = page.locator('button:has-text("คีการ์ด"), [class*="cursor-pointer"]:has-text("คีการ์ด")').first()
  if (await chip.isVisible().catch(() => false)) {
    await chip.click()
    await page.waitForTimeout(300)
    await page.locator('button[aria-label="เพิ่ม"], button:has-text("เพิ่ม")').first().click()
    await page.waitForTimeout(500)
  } else {
    track('D14.2', check('Preset chip visible', false))
    return
  }

  // Verify exact amounts
  const totalAfter = await getChargesSubtotal(page)
  const netAfter = await getNetAmount(page)
  const expectedTotal = totalBefore + 250
  // "Net" on the UI is max(0, charges − deposit) — adding 250 to charges
  // does NOT linearly add 250 to net when the baseline sits inside the
  // refund zone. expectedNet must mirror the formula, not extrapolate
  // from netBefore. TC4 fixture has DEPOSIT = ฿2,000 (B202 fan base).
  const DEPOSIT_BAHT_D14 = 2000
  const expectedNet = Math.max(0, totalBefore + 250 - DEPOSIT_BAHT_D14)

  track('D14.2', check(`Total: ${totalBefore} + 250 = ${expectedTotal}`,
    Math.abs(totalAfter - expectedTotal) < 0.01,
    `got ${totalAfter}`,
  ))
  track('D14.3', check(`Net owed = max(0, ${totalBefore + 250} − ${DEPOSIT_BAHT_D14}) = ${expectedNet}`,
    Math.abs(netAfter - expectedNet) < 0.01,
    `got ${netAfter}`,
  ))

  // Verify the row itself shows ฿250
  const rowAmount = await getLineAmount(page, 'คีการ์ด')
  track('D14.4', check('Preset row shows ฿250',
    rowAmount === 250,
    `got ${rowAmount}`,
  ))

  await page.screenshot({ path: '/tmp/smoke-draft-d14.png' })
}

// ─── TC-D15: Custom deposit subtracts exact ฿500 ─────────────────────

async function runTCD15(page, fixtures) {
  console.log('\n── TC-D15: Numeric anchor — custom deposit ฿500 ──')
  const fx = fixtures.TC4
  if (!fx) return track('D15', check('Fixture TC4', false, 'missing'))

  await goToSettlement(page, fx.notice_id)

  const totalCharges = await getChargesSubtotal(page)
  track('D15.1', check('Charges total is numeric', !isNaN(totalCharges), `${totalCharges}`))

  // Switch to CUSTOM deposit
  const customRadio = page.locator('text=กำหนดเอง').first()
  if (await customRadio.isVisible().catch(() => false)) {
    await customRadio.click()
    await page.waitForTimeout(400)
    const input = page.locator('input[aria-label="จำนวนที่ใช้"]')
    await input.fill('500')
    await input.blur()
    await page.waitForTimeout(300)
  } else {
    track('D15.2', check('CUSTOM radio visible', false))
    return
  }

  // Net should be exactly totalCharges - 500
  const net = await getNetAmount(page)
  const expectedNet = totalCharges - 500

  track('D15.2', check(`Net = ${totalCharges} - 500 = ${expectedNet}`,
    Math.abs(net - expectedNet) < 0.01,
    `got ${net}`,
  ))

  await page.screenshot({ path: '/tmp/smoke-draft-d15.png' })
}

// ─── TC-D16: Rent mode switch exact delta ────────────────────────────

async function runTCD16(page, fixtures) {
  console.log('\n── TC-D16: Numeric anchor — rent mode switch ──')
  const fx = fixtures.TC4
  if (!fx) return track('D16', check('Fixture TC4', false, 'missing'))

  await goToSettlement(page, fx.notice_id)

  const exp = computeExpected(TC4_OFFSET)

  // Capture PRORATED values
  const prorateRent = await getLineAmount(page, 'วัน × ฿')
  const prorateTotal = await getChargesSubtotal(page)
  const prorateNet = await getNetAmount(page)

  track('D16.1', check(`PRORATED rent = ฿${exp.proRateRentBaht}`,
    Math.abs(prorateRent - exp.proRateRentBaht) < 0.01,
    `got ${prorateRent}`,
  ))

  // Switch to FULL_MONTH
  await page.locator('text=เปลี่ยน').first().click()
  await page.waitForSelector('text=เปลี่ยนวิธีคิดค่าเช่า', { timeout: 5000 })
  await page.waitForTimeout(300)
  const dialog = page.locator('[role="dialog"]')
  await dialog.locator('text=คิดเต็มเดือนเพื่อรักษาเงินประกัน').first().click()
  await page.waitForTimeout(300)
  await dialog.locator('button:has-text("คำนวณใหม่")').first().click()

  // Wait for regeneration
  await page.waitForSelector('[role="dialog"] >> text=คำนวณใหม่', { state: 'detached', timeout: 10000 }).catch(() => {})
  await page.waitForSelector('text=รายการค่าใช้จ่าย', { timeout: 15000 })
  await page.waitForLoadState('networkidle')
  await page.waitForTimeout(500)

  // Verify FULL_MONTH values
  const fullMonthTotal = await getChargesSubtotal(page)
  const fullMonthNet = await getNetAmount(page)
  const rentDelta = exp.fullMonthRentBaht - exp.proRateRentBaht

  track('D16.2', check(`FULL_MONTH total = ฿${exp.fullMonthTotalBaht}`,
    Math.abs(fullMonthTotal - exp.fullMonthTotalBaht) < 0.01,
    `got ${fullMonthTotal}`,
  ))
  const actualDelta = fullMonthTotal - prorateTotal
  track('D16.3', check(`Total delta = +฿${rentDelta.toFixed(2)}`,
    Math.abs(actualDelta - rentDelta) < 0.02,
    `was ${prorateTotal} now ${fullMonthTotal} delta=${actualDelta.toFixed(2)}`,
  ))
  track('D16.4', check(`FULL_MONTH net = ฿${exp.fullMonthNetBaht}`,
    Math.abs(fullMonthNet - exp.fullMonthNetBaht) < 0.01,
    `got ${fullMonthNet}`,
  ))

  await page.screenshot({ path: '/tmp/smoke-draft-d16.png' })
}

// ─── TC-D17: Regenerate preserves exact numbers ──────────────────────

async function runTCD17(page, fixtures) {
  console.log('\n── TC-D17: Numeric anchor — regenerate preserves exact totals ──')
  const fx = fixtures.TC22
  if (!fx) return track('D17', check('Fixture TC22', false, 'missing'))

  const exp = computeExpected(TC22_OFFSET, { withManual: true })
  console.log(`  📐 TC22 expected: total=${exp.totalBaht}, net=${exp.netBaht}`)

  await goToSettlement(page, fx.notice_id)

  // Record baseline
  const baselineTotal = await getChargesSubtotal(page)
  const baselineNet = await getNetAmount(page)
  const manual1 = await getLineAmount(page, 'ค่าซ่อมแซมห้อง')
  const manual2 = await getLineAmount(page, 'ค่าเคลื่อนย้ายของ')

  track('D17.1', check(`Baseline total = ฿${exp.totalBaht}`,
    Math.abs(baselineTotal - exp.totalBaht) < 0.01,
    `got ${baselineTotal}`,
  ))
  track('D17.2', check('Manual items: ฿500 + ฿300',
    manual1 === 500 && manual2 === 300,
    `got ${manual1} + ${manual2}`,
  ))

  // Regenerate 1
  console.log('    ↻ Regeneration 1...')
  await triggerRegenerate(page)
  const total1 = await getChargesSubtotal(page)
  const net1 = await getNetAmount(page)

  // Regenerate 2
  console.log('    ↻ Regeneration 2...')
  await triggerRegenerate(page)
  const total2 = await getChargesSubtotal(page)
  const net2 = await getNetAmount(page)

  track('D17.3', check(`After regen 1: total still ${baselineTotal}`,
    Math.abs(total1 - baselineTotal) < 0.01,
    `got ${total1}`,
  ))
  track('D17.4', check(`After regen 2: total still ${baselineTotal}`,
    Math.abs(total2 - baselineTotal) < 0.01,
    `got ${total2}`,
  ))
  track('D17.5', check(`Net stable: ${baselineNet} → ${net1} → ${net2}`,
    Math.abs(net1 - baselineNet) < 0.01 && Math.abs(net2 - baselineNet) < 0.01,
    `drift=${Math.abs(net2 - baselineNet)}`,
  ))

  // Manual items still exact
  const m1after = await getLineAmount(page, 'ค่าซ่อมแซมห้อง')
  const m2after = await getLineAmount(page, 'ค่าเคลื่อนย้ายของ')
  track('D17.6', check('Manual items exact after 2 regens',
    m1after === 500 && m2after === 300,
    `got ${m1after} + ${m2after}`,
  ))

  await page.screenshot({ path: '/tmp/smoke-draft-d17.png' })
}

// Trigger regenerate via button + confirm modal.
async function triggerRegenerate(page) {
  const regenBtn = page.locator('button:has-text("คำนวณใหม่"), [class*="cursor-pointer"]:has-text("คำนวณใหม่")').first()
  await regenBtn.click()

  const modalConfirm = page.locator('[role="dialog"] button:has-text("คำนวณใหม่")').first()
  await modalConfirm.waitFor({ state: 'visible', timeout: 3000 }).catch(() => {})
  if (await modalConfirm.isVisible().catch(() => false)) {
    await modalConfirm.click()
  }

  const successToast = page.locator('text=คำนวณยอดใหม่แล้ว')
  await successToast.waitFor({ state: 'visible', timeout: 15000 }).catch(() => {})
  await page.waitForLoadState('networkidle')
  await page.waitForSelector('text=รายการค่าใช้จ่าย', { timeout: 15000 })
  await page.waitForTimeout(600)
}

// ─── Main ────────────────────────────────────────────────────────────

;(async () => {
  console.log('\n🧪 Draft Settlement — Numeric Anchor Tests (TC-D13 – TC-D17)\n')

  console.log('📦 Refreshing smoke fixtures...')
  await fetch(`${BACKEND}/api/v1/dev/smoke/cleanup`, { method: 'POST' })
  await fetch(`${BACKEND}/api/v1/dev/smoke/seed`, { method: 'POST' })
  const fixtures = await fetchFixtures()
  const needed = ['TC4', 'TC22']
  const seeded = needed.filter((k) => fixtures[k])
  console.log(`  ✅ ${seeded.length}/${needed.length} fixtures ready: ${seeded.join(', ')}`)
  const missing = needed.filter((k) => !fixtures[k])
  if (missing.length) {
    console.log(`  ❌ missing: ${missing.join(', ')} — cannot continue`)
    process.exit(1)
  }

  const browser = await chromium.launch({ headless: false, slowMo: 60 })
  const context = await browser.newContext({ viewport: { width: 1400, height: 900 } })
  const page = await context.newPage()

  try {
    console.log('\n🔐 Login')
    await login(page)

    // D13–D15 are read-only / client-side only — safe order
    // D16 triggers server-side regenerate — run last for TC4
    await runTCD13(page, fixtures)
    await runTCD14(page, fixtures)
    await runTCD15(page, fixtures)
    await runTCD16(page, fixtures)
    await runTCD17(page, fixtures)

    console.log(`\n${'='.repeat(60)}`)
    console.log(`📊 Results: ${results.pass}/${results.total} passed, ${results.fail} failed`)
    if (results.failedCases.length > 0) {
      console.log(`❌ Failed: ${results.failedCases.join(', ')}`)
    }
    console.log(`📸 Screenshots in /tmp/smoke-draft-d{13-17}.png`)
    console.log('='.repeat(60))
  } catch (err) {
    console.error('\n💥 Fatal error:', err.message)
    console.error(err.stack)
    await page.screenshot({ path: '/tmp/smoke-numeric-fatal.png' })
  } finally {
    await browser.close()
  }

  console.log('\n🧹 Cleanup smoke fixtures...')
  await fetch(`${BACKEND}/api/v1/dev/smoke/cleanup`, { method: 'POST' })
  console.log('  ✅ Done\n')

  process.exit(results.fail > 0 ? 1 : 0)
})()

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
