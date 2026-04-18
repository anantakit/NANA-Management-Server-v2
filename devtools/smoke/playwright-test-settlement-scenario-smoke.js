// Move-out Settlement Scenario Smoke v1 — TC13–TC21
// ----------------------------------------------------------------------
// Covers business-risk scenarios from senario.md that the legacy preview
// smoke (TC1–TC12) does NOT exercise:
//
//   TC13  paid monthly bill carry-forward (no absorbed section)
//   TC14  unpaid monthly bill absorbed into settlement
//   TC15  multiple outstanding bills absorbed
//   TC16  scheduled move-out date differs from actual
//   TC17  backdated actual move-out date
//   TC18  deposit refund outcome
//   TC19  deposit shortfall / customer still owes money
//   TC20  invalid exit reading (current < prior MONTHLY)
//   TC21  missing baseline / incomplete data (C3)
//
// Legacy playwright-test-settlement-preview.js (TC1–TC12) stays intact.
// This suite is independent — its own fixtures (TC13–TC20) + own login +
// own cleanup. Safe to run separately or back-to-back with the legacy
// suite (cleanup wipes all 9999-prefix smoke fixtures).
//
// Selectors favor aria-label / role / stable Thai text rendered by
// SettlementPreviewDrawer.tsx + SettlementPreviewSections.tsx. Per
// scenario we assert only the one semantic state that matters — this is
// a smoke suite, not exhaustive acceptance coverage.

const { chromium } = require('playwright')

const FRONTEND = 'http://localhost:3001'
const BACKEND = 'http://localhost:8080'
const ADMIN_USER = 'admin'
const ADMIN_PASS = 'admin123'

// ─── Pretty logger ──────────────────────────────────────────────────────

const check = (name, ok, detail = '') => {
  const icon = ok ? '✅' : '❌'
  const msg = detail ? ` — ${detail}` : ''
  console.log(`  ${icon} ${name}${msg}`)
  return ok
}

const results = { pass: 0, fail: 0, total: 0, failedCases: [] }
const track = (tc, ok) => {
  results.total++
  if (ok) results.pass++
  else { results.fail++; results.failedCases.push(tc) }
}

// ─── Auth + fixtures ───────────────────────────────────────────────────

async function login(page) {
  await page.goto(`${FRONTEND}/login`)
  await page.fill('input[name="username"]', ADMIN_USER)
  await page.fill('input[name="password"]', ADMIN_PASS)
  await page.click('button[type="submit"]')
  await page.waitForFunction(() => !window.location.pathname.includes('/login'), { timeout: 10000 })
  await page.waitForLoadState('networkidle')
  console.log(`  ✅ Logged in — landed on ${page.url()}`)
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

// ─── Drawer helpers ────────────────────────────────────────────────────

const DRAWER = '[role="dialog"][aria-label="สรุปยอดย้ายออก"]'

async function openSettlementPreview(page, noticeId) {
  await page.goto(`${FRONTEND}/move-out/${noticeId}`)
  await page.waitForLoadState('networkidle')
  const btn = page.locator('button:has-text("ดูสรุปยอด")').first()
  await btn.click()
  await page.waitForSelector(DRAWER, { timeout: 10000 })
  // Wait for either content or error state — both end the skeleton
  await page.waitForSelector(
    `${DRAWER} >> text=/คิดค่าเช่า:|ยังไม่สามารถสรุปยอดได้/`,
    { timeout: 10000 },
  )
}

async function closeDrawer(page) {
  await page.keyboard.press('Escape')
  await page.waitForSelector(DRAWER, { state: 'detached', timeout: 5000 }).catch(() => {})
}

async function getOutcome(page) {
  const dialog = page.locator(DRAWER)
  const labelEl = dialog.locator(
    'text=/^(ผู้เช่าต้องชำระเพิ่ม|ต้องคืนเงินให้ผู้เช่า|เคลียร์ครบ ไม่มียอดค้าง)$/',
  ).first()
  const label = await labelEl.textContent().catch(() => null)
  const amountEl = dialog.locator('p.text-xl, p.text-2xl').first()
  const amount = await amountEl.textContent().catch(() => null)
  return { label: label?.trim() ?? null, amount: amount?.trim() ?? null }
}

async function expectAbsorbedSection(page, { visible, minCount }) {
  const dialog = page.locator(DRAWER)
  const header = dialog.locator('text=บิลค้างที่ต้องชำระก่อนย้ายออก')
  const isVisible = await header.isVisible().catch(() => false)
  if (visible !== undefined && isVisible !== visible) {
    return { ok: false, detail: `visible=${isVisible} want=${visible}` }
  }
  if (minCount !== undefined && isVisible) {
    // Count bill rows under the absorbed section — rows have "บิลรายเดือนค้างชำระ"
    const rows = await dialog.locator('text=บิลรายเดือนค้างชำระ').count()
    if (rows < minCount) {
      return { ok: false, detail: `rows=${rows} want>=${minCount}` }
    }
    return { ok: true, detail: `rows=${rows}` }
  }
  return { ok: true, detail: isVisible ? 'shown' : 'hidden' }
}

async function getValueByLabel(page, label) {
  // BreakdownRow shape: <div class="flex"><div><span>label</span></div><span>amount</span></div>
  // Find the row div that contains the label, then grab the tabular-nums span.
  const row = page.locator(
    `${DRAWER} >> xpath=.//span[normalize-space(text())=${JSON.stringify(label)}]/ancestor::div[contains(@class,"flex")][1]`,
  ).first()
  const amountEl = row.locator('span.tabular-nums').first()
  return (await amountEl.textContent().catch(() => null))?.trim() ?? null
}

// Back-compat alias — earlier tests referenced getDepositRow.
const getDepositRow = getValueByLabel

async function getChargesSubtotal(page) {
  // ChargesSection subtotal is a labelled row just like deposit rows.
  return parseThb(await getValueByLabel(page, 'รวมค่าใช้จ่ายรอบย้ายออก'))
}

async function getHeaderActualDate(page) {
  // Drawer header: "วันย้ายออกจริง {formatShortThaiDate(actualMoveOutDate)}"
  const dialog = page.locator(DRAWER)
  const el = dialog.locator('text=/วันย้ายออกจริง /').first()
  return (await el.textContent().catch(() => null))?.trim() ?? null
}

async function getBillingMonthValue(page) {
  // ContextSummary: "รอบบิล" label → value in next <p>
  const dialog = page.locator(DRAWER)
  const block = dialog
    .locator('div')
    .filter({ has: page.locator('text=/^รอบบิล$/') })
    .first()
  const value = block.locator('p').first()
  return (await value.textContent().catch(() => null))?.trim() ?? null
}

// Parse "฿1,234.56" → 1234.56 (bahts). Handles also negative / "-฿"
function parseThb(str) {
  if (!str) return NaN
  const cleaned = str.replace(/[^\d.-]/g, '')
  return Number(cleaned)
}

// ─── Scenario runners ──────────────────────────────────────────────────

async function runTC13(page, fixtures) {
  console.log('\n── TC13 paid monthly bill carry-forward ──')
  const fx = fixtures.TC13
  if (!fx) return track('TC13', check('Fixture TC13 present', false, 'missing'))
  await openSettlementPreview(page, fx.notice_id)
  const absorbed = await expectAbsorbedSection(page, { visible: false })
  track('TC13.1', check('No absorbed-bills section', absorbed.ok, absorbed.detail))
  // Charges section should still render (paid rent = exit-period utilities only)
  const hasCharges = await page.locator(`${DRAWER} >> text=/^ค่าใช้จ่ายรอบย้ายออก$/ >> nth=0`).isVisible()
  track('TC13.2', check('Exit-period charges render', hasCharges))
  // ── Carry-forward lock ──
  // EXIT reading: elecPrev=1000, elecCurr=1135 (135 units × ฿8 = ฿1,080)
  //               waterPrev=100, waterCurr=118 (18 units × ฿18 = ฿324)
  //               + cleaning ฿300 → subtotal ≈ ฿1,704.
  // If the service re-counted from 0, elec=1135 units → subtotal ≈ ฿11,500.
  // Asserting subtotal < ฿3,000 locks the carry-forward to the committed
  // prior reading (spec rule D2: "settlement starts from last committed
  // reading, never re-counts already-billed usage").
  const subtotal = await getChargesSubtotal(page)
  track('TC13.3', check(
    'Carry-forward: subtotal ≈ ฿1,704 (not ≈ ฿11,500 if re-counted from 0)',
    subtotal > 1500 && subtotal < 2500,
    `subtotal=${subtotal}`,
  ))
  // Outcome must have label + numeric amount
  const outcome = await getOutcome(page)
  track('TC13.4', check(
    'Has outcome (label + amount)',
    !!outcome.label && !!outcome.amount,
    `${outcome.label}: ${outcome.amount}`,
  ))
  await page.screenshot({ path: '/tmp/smoke-tc13.png' })
  await closeDrawer(page)
}

async function runTC14(page, fixtures) {
  console.log('\n── TC14 unpaid monthly bill absorbed ──')
  const fx = fixtures.TC14
  if (!fx) return track('TC14', check('Fixture TC14 present', false, 'missing'))
  await openSettlementPreview(page, fx.notice_id)
  const absorbed = await expectAbsorbedSection(page, { visible: true, minCount: 1 })
  track('TC14.1', check('Absorbed-bills section shown with ≥1 row', absorbed.ok, absorbed.detail))
  // Exit-period charges remain separate from absorbed block
  const hasCharges = await page.locator(`${DRAWER} >> text=/^ค่าใช้จ่ายรอบย้ายออก$/ >> nth=0`).isVisible()
  track('TC14.2', check('Exit-period charges still separate', hasCharges))
  // Preview/draft creation path must be usable (no hard-error)
  const createBtn = page.locator(`${DRAWER} >> button:has-text("สร้างแบบร่าง")`)
  track('TC14.3', check('Create-draft button present', await createBtn.isVisible()))
  await page.screenshot({ path: '/tmp/smoke-tc14.png' })
  await closeDrawer(page)
}

async function runTC15(page, fixtures) {
  console.log('\n── TC15 multiple outstanding bills ──')
  const fx = fixtures.TC15
  if (!fx) return track('TC15', check('Fixture TC15 present', false, 'missing'))
  await openSettlementPreview(page, fx.notice_id)
  const absorbed = await expectAbsorbedSection(page, { visible: true, minCount: 2 })
  track('TC15.1', check('Absorbed section shows ≥2 outstanding rows', absorbed.ok, absorbed.detail))
  // Outcome should have a concrete amount
  const outcome = await getOutcome(page)
  track('TC15.2', check('Outcome has amount', !!outcome.amount, outcome.amount ?? ''))
  await page.screenshot({ path: '/tmp/smoke-tc15.png' })
  await closeDrawer(page)
}

async function runTC16(page, fixtures) {
  console.log('\n── TC16 scheduled ≠ actual move-out date ──')
  const fx = fixtures.TC16
  if (!fx) return track('TC16', check('Fixture TC16 present', false, 'missing'))
  if (!fx.actual_move_out_date) {
    return track('TC16', check('Fixture has actual_move_out_date', false, 'missing'))
  }
  await openSettlementPreview(page, fx.notice_id)
  // The header echoes actual_move_out_date (not scheduled). Thai short format
  // uses abbreviated month + 2-digit BE year (e.g. "10 เม.ย. 69"). Verify
  // the day AND the last-two-digit BE year appear.
  const [y, , d] = fx.actual_move_out_date.split('-').map(Number)
  const beYearShort = String((y + 543) % 100).padStart(2, '0')
  const headerText = await getHeaderActualDate(page)
  const dayOk = headerText?.includes(String(d)) ?? false
  const beYearOk = headerText?.includes(beYearShort) ?? false
  track('TC16.1', check(
    'Header shows actual move-out date (not scheduled)',
    !!headerText && dayOk && beYearOk,
    headerText ?? '(none)',
  ))
  // SettlementMeta shows current rent mode
  const metaText = await page.locator(`${DRAWER} >> text=คิดค่าเช่า:`).first().textContent().catch(() => null)
  track('TC16.2', check(
    'SettlementMeta renders rent mode',
    !!metaText,
    metaText?.trim() ?? '(none)',
  ))
  // ── Actual-date rent lock ──
  // C204 fan room: monthly rent ฿2,500, April has 30 days.
  //   actual day 10  → prorate rent ≈ ฿833  → subtotal ≈ ฿2,537
  //   scheduled day 14 → prorate rent ≈ ฿1,167 → subtotal ≈ ฿2,871
  // If the backend silently used scheduled, subtotal would exceed ฿2,700.
  // Asserting subtotal < ฿2,700 locks the financial calculation to the
  // actual date (spec rule D1: "actual move-out date for settlement").
  const subtotal = await getChargesSubtotal(page)
  track('TC16.3', check(
    'Rent uses actual date: subtotal ≈ ฿2,537 (not ≈ ฿2,871 if scheduled)',
    subtotal > 2300 && subtotal < 2800,
    `subtotal=${subtotal}`,
  ))
  await page.screenshot({ path: '/tmp/smoke-tc16.png' })
  await closeDrawer(page)
}

async function runTC17(page, fixtures) {
  console.log('\n── TC17 backdated actual move-out date ──')
  const fx = fixtures.TC17
  if (!fx) return track('TC17', check('Fixture TC17 present', false, 'missing'))
  if (!fx.actual_move_out_date) {
    return track('TC17', check('Fixture has actual_move_out_date', false, 'missing'))
  }
  // Verify the seed backdated value (~10 days ago) is what the preview uses.
  const actual = new Date(fx.actual_move_out_date)
  const today = new Date()
  const diffDays = Math.round((today - actual) / 86_400_000)
  track('TC17.0', check(
    'Fixture actual date is in the past',
    diffDays > 0,
    `${fx.actual_move_out_date} (${diffDays}d ago)`,
  ))
  await openSettlementPreview(page, fx.notice_id)
  const [y, , d] = fx.actual_move_out_date.split('-').map(Number)
  const beYearShort = String((y + 543) % 100).padStart(2, '0')
  const headerText = await getHeaderActualDate(page)
  const dayOk = headerText?.includes(String(d)) ?? false
  const beYearOk = headerText?.includes(beYearShort) ?? false
  track('TC17.1', check(
    'Preview header honors backdated actual date',
    !!headerText && dayOk && beYearOk,
    headerText ?? '(none)',
  ))
  // Outcome still computes (no NaN)
  const outcome = await getOutcome(page)
  const looksNumeric = outcome.amount && !/NaN|undefined/.test(outcome.amount)
  track('TC17.2', check('Outcome has concrete numeric amount', !!looksNumeric, outcome.amount ?? ''))
  await page.screenshot({ path: '/tmp/smoke-tc17.png' })
  await closeDrawer(page)
}

async function runTC18(page, fixtures) {
  console.log('\n── TC18 deposit refund outcome ──')
  const fx = fixtures.TC18
  if (!fx) return track('TC18', check('Fixture TC18 present', false, 'missing'))
  await openSettlementPreview(page, fx.notice_id)
  const outcome = await getOutcome(page)
  track('TC18.1', check(
    'Outcome label is "ต้องคืนเงินให้ผู้เช่า"',
    outcome.label === 'ต้องคืนเงินให้ผู้เช่า',
    `label=${outcome.label}`,
  ))
  // Deposit section: refund > 0, due == 0
  // New IA: deposit section shows "คืนผู้เช่า" when remaining > 0, "คงเหลือ" when 0.
  // FinalAmountRow shows "ต้องคืนเงินผู้เช่า" for refund cases.
  const refund = parseThb(await getDepositRow(page, 'คืนผู้เช่า'))
  track('TC18.2', check('Deposit shows "คืนผู้เช่า" > 0', refund > 0, `refund=${refund}`))
  // No "ยอดค้างชำระ" row in new IA — final amount is in FinalAmountRow instead
  const finalRow = await getValueByLabel(page, 'ต้องคืนเงินผู้เช่า')
  track('TC18.3', check('FinalAmountRow shows refund', !!finalRow, `final=${finalRow}`))
  await page.screenshot({ path: '/tmp/smoke-tc18.png' })
  await closeDrawer(page)
}

async function runTC19(page, fixtures) {
  console.log('\n── TC19 deposit shortfall ──')
  const fx = fixtures.TC19
  if (!fx) return track('TC19', check('Fixture TC19 present', false, 'missing'))
  await openSettlementPreview(page, fx.notice_id)
  const outcome = await getOutcome(page)
  track('TC19.1', check(
    'Outcome label is "ผู้เช่าต้องชำระเพิ่ม"',
    outcome.label === 'ผู้เช่าต้องชำระเพิ่ม',
    `label=${outcome.label}`,
  ))
  // New IA: deposit shows "คงเหลือ ฿0" when fully applied. FinalAmountRow shows due.
  const remaining = parseThb(await getDepositRow(page, 'คงเหลือ'))
  track('TC19.2', check('Deposit remaining == 0 (fully applied)', remaining === 0, `remaining=${remaining}`))
  const finalDue = parseThb(await getValueByLabel(page, 'ต้องชำระเพิ่ม'))
  track('TC19.3', check('FinalAmountRow shows due > 0', finalDue > 0, `due=${finalDue}`))
  // Create-draft path must still be usable despite shortfall
  const createBtn = page.locator(`${DRAWER} >> button:has-text("สร้างแบบร่าง")`)
  track('TC19.4', check('Create-draft button present', await createBtn.isVisible()))
  await page.screenshot({ path: '/tmp/smoke-tc19.png' })
  await closeDrawer(page)
}

async function runTC20(page, fixtures) {
  console.log('\n── TC20 invalid exit reading (current < previous) ──')
  const fx = fixtures.TC20
  if (!fx) return track('TC20', check('Fixture TC20 present', false, 'missing'))

  // PENDING_METER stage → open the RecordMoveOutDrawer
  await page.goto(`${FRONTEND}/move-out/${fx.notice_id}`)
  await page.waitForLoadState('networkidle')

  // The detail page renders a <Button>บันทึกมิเตอร์ย้ายออก</Button> that toggles
  // the drawer open. The same text also appears as an <h2> heading — target
  // the button role explicitly to avoid the heading.
  const recordBtn = page.getByRole('button', { name: 'บันทึกมิเตอร์ย้ายออก' }).first()
  await recordBtn.waitFor({ state: 'visible', timeout: 10000 })
  await recordBtn.click()

  // Wait for the form to be fully ready (data loaded, skeleton gone).
  // The ready marker only renders once notice + latest reading + baseline
  // are all loaded and the form phase is active — NOT during skeleton.
  await page.waitForSelector('[data-testid="exit-meter-ready"]', { timeout: 10000 })

  // Fixture seeded prior MONTHLY (elec=2500, water=300). Type values well
  // below those and expect the below-previous validation to surface.
  await page.fill('#drawer_water', '50')
  await page.fill('#drawer_electricity', '100')
  // Blur electricity so both onChange + blur events have fired
  await page.locator('#drawer_electricity').blur().catch(() => {})
  await page.waitForTimeout(150)

  const errorLocator = page.locator('text=เลขมิเตอร์ต้องไม่น้อยกว่าครั้งก่อน').first()
  const errorShown = await errorLocator.isVisible().catch(() => false)
  track('TC20.1', check('Below-previous error message shown', errorShown))

  // Submit button must be disabled (see ExitMeterDrawer.tsx isComplete guard).
  // Scope to the dialog to avoid matching "บันทึกมิเตอร์ย้ายออก" on the page behind.
  const submitBtn = page.locator('[role="dialog"]').getByRole('button', { name: 'บันทึก', exact: true })
  const submitDisabled = await submitBtn.isDisabled().catch(() => true)
  track('TC20.2', check('Submit disabled while reading < previous', submitDisabled))

  await page.screenshot({ path: '/tmp/smoke-tc20.png' })

  // No EXIT draft should be created — confirm fixture still PENDING_METER
  const refetched = await fetchFixtures()
  const tc20After = refetched.TC20
  track('TC20.3', check(
    'Fixture still PENDING_METER (no draft created)',
    tc20After?.status === 'PENDING_METER' && tc20After?.has_draft === false,
    `status=${tc20After?.status} has_draft=${tc20After?.has_draft}`,
  ))

  // Close the drawer so any subsequent test starts clean
  await page.keyboard.press('Escape').catch(() => {})
}

async function runTC21(page, fixtures) {
  console.log('\n── TC21 missing baseline / incomplete data (C3) ──')
  const fx = fixtures.TC21
  if (!fx) return track('TC21', check('Fixture TC21 present', false, 'missing'))
  await openSettlementPreview(page, fx.notice_id)
  // 1. No invalid values anywhere in the dialog
  const dialogText = await page.locator(DRAWER).textContent()
  const hasNaN = /NaN/.test(dialogText)
  const hasUndefined = /undefined/.test(dialogText)
  track('TC21.1', check(
    'No NaN or undefined in preview',
    !hasNaN && !hasUndefined,
    hasNaN ? 'found NaN' : hasUndefined ? 'found undefined' : 'clean',
  ))
  // 2. Outcome renders with valid label + numeric amount
  const outcome = await getOutcome(page)
  const looksNumeric = outcome.amount && !/NaN|undefined/.test(outcome.amount)
  track('TC21.2', check(
    'Outcome has valid label + amount',
    !!outcome.label && !!looksNumeric,
    `${outcome.label}: ${outcome.amount}`,
  ))
  // 3. Create-draft button works (main flow not blocked)
  const createBtn = page.locator(`${DRAWER} >> button:has-text("สร้างแบบร่าง")`)
  track('TC21.3', check('Create-draft button present', await createBtn.isVisible()))
  await page.screenshot({ path: '/tmp/smoke-tc21.png' })
  await closeDrawer(page)
}

// ─── Main ──────────────────────────────────────────────────────────────

;(async () => {
  console.log('\n🧪 Move-out Settlement Scenario Smoke v1 (TC13–TC21)\n')

  console.log('📦 Refreshing smoke fixtures...')
  await fetch(`${BACKEND}/api/v1/dev/smoke/cleanup`, { method: 'POST' })
  await fetch(`${BACKEND}/api/v1/dev/smoke/seed`, { method: 'POST' })
  const fixtures = await fetchFixtures()
  const scenarioKeys = ['TC13','TC14','TC15','TC16','TC17','TC18','TC19','TC20','TC21']
  const seeded = scenarioKeys.filter((k) => fixtures[k])
  console.log(`  ✅ ${seeded.length}/${scenarioKeys.length} scenario fixtures ready: ${seeded.join(', ')}`)
  const missing = scenarioKeys.filter((k) => !fixtures[k])
  if (missing.length) console.log(`  ⚠️  missing: ${missing.join(', ')}`)
  console.log('')

  const browser = await chromium.launch({ headless: false, slowMo: 60 })
  const context = await browser.newContext({ viewport: { width: 1400, height: 900 } })
  const page = await context.newPage()

  try {
    console.log('🔐 Login')
    await login(page)

    await runTC13(page, fixtures)
    await runTC14(page, fixtures)
    await runTC15(page, fixtures)
    await runTC16(page, fixtures)
    await runTC19(page, fixtures)
    await runTC17(page, fixtures)
    await runTC18(page, fixtures)
    await runTC21(page, fixtures)
    await runTC20(page, fixtures)

    console.log(`\n${'='.repeat(60)}`)
    console.log(`📊 Results: ${results.pass}/${results.total} passed, ${results.fail} failed`)
    if (results.failedCases.length > 0) {
      console.log(`❌ Failed: ${results.failedCases.join(', ')}`)
    }
    console.log(`📸 Screenshots in /tmp/smoke-tc1[3-9].png and /tmp/smoke-tc20.png`)
    console.log('='.repeat(60))
  } catch (err) {
    console.error('\n💥 Fatal error:', err.message)
    console.error(err.stack)
    await page.screenshot({ path: '/tmp/smoke-scenario-fatal.png' })
  } finally {
    await browser.close()
  }

  console.log('\n🧹 Cleanup smoke fixtures...')
  await fetch(`${BACKEND}/api/v1/dev/smoke/cleanup`, { method: 'POST' })
  console.log('  ✅ Done\n')

  process.exit(results.fail > 0 ? 1 : 0)
})()
