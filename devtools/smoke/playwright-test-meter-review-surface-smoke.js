// Review Surface (B1-b) — capability smoke
// ----------------------------------------------------------------------
// Locks what Review is allowed to be. The vocabulary was settled by
// REVIEW_STATUS_SELECTOR_AUDIT.md and the row grammar by
// METER_WORKFLOW_CONVERGENCE_AUDIT.md §7 Lock A; this smoke is the executable
// half of both.
//
//   TC1  identity + the one-line test — Review shows state and routes; NO FORM
//   TC2  vocabulary — only the six admitted names ship; the reject list is absent
//   TC3  worst-first — actionable rows precede silent ones; silence has no CTA
//   TC4  routing — one destination per state, and the operator comes back
//   TC5  the flush — a draft Focus never submitted is committable FROM Review
//   TC6  the Building Workspace owns coverage — ADR-0001 items 1 + 3 (B1-c S1)
//   TC7  EXIT lineage parity — FE `previous` IS the backend's (D3 gate)
//   TC8  Slice 2 — two inventories · stable workspace · composed row truth
//
// ⏳ TRANSITIONAL: Review lives at /meter-readings/review while the Building
// Workspace owns /meter-readings. ADR-0001 reversed OD-2 — Review is being
// retired, not promoted — so TC1-TC5 are the surface's obituary, not its spec.
// TC6 is the first case written against the workspace instead; B1-c Slice 3
// deletes TC1-TC5 outright and grows TC6 into the full 9-item conformance
// table (migration plan §4).
//
// Requires: backend :8080 + FE dev :3001 + `make smoke-install`.
// Run:  cd devtools/smoke && node playwright-test-meter-review-surface-smoke.js
//       (SMOKE_HEADED=1 to watch)
const { chromium } = require('playwright')

const FRONTEND = process.env.FRONTEND || 'http://localhost:3001'
const ADMIN_USER = 'admin'
const ADMIN_PASS_FRESH = 'admin123'
const ADMIN_PASS_POST = 'admin1234'

const results = { pass: 0, fail: 0, total: 0, failedCases: [] }
const check = (name, ok, detail = '') => {
  const msg = detail ? ` — ${detail}` : ''
  console.log(`  ${ok ? '✅' : '❌'} ${name}${msg}`)
  results.total++
  if (ok) results.pass++
  else {
    results.fail++
    results.failedCases.push(`${name}${msg}`)
  }
}

async function login(page) {
  await page.goto(`${FRONTEND}/login`, { waitUntil: 'domcontentloaded' })
  for (const pw of [ADMIN_PASS_POST, ADMIN_PASS_FRESH]) {
    await page.fill('input[name="username"]', ADMIN_USER)
    await page.fill('input[name="password"]', pw)
    await page.click('button[type="submit"]')
    try {
      await page.waitForFunction(
        () => !window.location.pathname.includes('/login') || document.querySelector('[role="alert"]'),
        { timeout: 4000 },
      )
    } catch (_) {}
    if (!page.url().includes('/login')) break
    await page.fill('input[name="username"]', '')
    await page.fill('input[name="password"]', '')
  }
  if (page.url().includes('/change-password')) {
    await page.fill('input[name="new_password"]', ADMIN_PASS_POST)
    await page.fill('input[name="confirm_password"]', ADMIN_PASS_POST)
    await page.click('button[type="submit"]')
    await page.waitForFunction(() => !window.location.pathname.includes('/change-password'), { timeout: 8000 })
  }
  await page.waitForFunction(() => !window.location.pathname.includes('/login'), { timeout: 10000 })
}

function currentMonth() {
  const d = new Date()
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}`
}

async function gotoReview(page, month) {
  await page.goto(`${FRONTEND}/meter-readings/review?month=${month}`, { waitUntil: 'networkidle' })
  await page.locator('[data-test="review-row"]').first().waitFor({ timeout: 15000 })
  await page.waitForTimeout(250)
}

async function readRows(page) {
  return page.$$eval('[data-test="review-row"]', (nodes) =>
    nodes.map((n) => ({
      room: n.getAttribute('data-room'),
      state: n.querySelector('[data-test="review-state"]')?.textContent?.trim() ?? '',
      cta: n.querySelector('button[aria-label^="จดมิเตอร์ห้อง"]')
        ? 'จด'
        : n.querySelector('button[aria-label^="ตรวจห้อง"]')
          ? 'ตรวจ'
          : null,
    })),
  )
}

/* ─── TC1 — identity, and the one-line test ─── */

async function tc1_identityAndNoForm(page, month) {
  console.log('\n🧪 TC1 — Review renders as a judgment workspace, not an entry surface')
  await gotoReview(page, month)

  const heading = await page.locator('h1', { hasText: 'ตรวจมิเตอร์' }).first().isVisible().catch(() => false)
  check('TC1.1 Review has its own identity chrome', heading)

  const monthNav = await page.locator('[aria-label="ตัวเลือกเดือน"], [role="group"]').first().isVisible().catch(() => false)
  check('TC1.2 month scope control is present', monthNav)

  // ⛔ THE TEST: ถ้า Review ต้องมี "ฟอร์ม" แปลว่าวางผิดที่.
  const inputs = await page.locator('main input, main textarea, main select').count()
  check('TC1.3 no input / form anywhere on Review — it shows state and routes', inputs === 0,
    inputs === 0 ? '' : `found ${inputs} field(s)`)

  const rows = await readRows(page)
  check('TC1.4 the month renders as rows', rows.length > 0, `${rows.length} row(s)`)
}

/* ─── TC2 — the vocabulary, and the reject list ─── */

const ADMITTED = [
  /^ยังไม่มีค่าการใช้งานของเดือนนี้$/,
  /^เปลี่ยนมิเตอร์ .+ · ยังไม่มีค่าการใช้งานหลังเปลี่ยน$/,
  /^ยังไม่มีค่าการใช้งานหลังเปลี่ยนมิเตอร์$/,
  /^รอบนี้ถูกทำเครื่องหมายว่าใช้สูงผิดปกติ$/,
  /^มีการแก้ค่ามิเตอร์ที่จดผิดในเดือนนี้$/,
  /^เปลี่ยนมิเตอร์ .+$/,
  /^เปลี่ยนมิเตอร์แล้ว$/,
  // silence: the row shows the month's usage instead of a verdict, or nothing.
  /^ไฟ [\d,]+ · น้ำ [\d,]+$/,
  /^$/,
]

// §5, the explicit reject list — a clock, money, a billing phase, or a ปกติ badge.
const REJECTED = [
  'รอค่าปลายเดือน', 'ค้างจด', 'เกินกำหนด', 'เลยรอบ', 'ค้างมา', 'รอตรวจ',
  'รอปรับยอด', 'ปรับยอดแล้ว', 'คืนเงิน', 'บาท', '฿',
  'พร้อมสร้างบิล', 'สร้างบิลได้', 'ออกบิล', 'ACTION_REQUIRED', 'READY',
  'ต้องจัดการเอง', 'ชนกับ Replace',
]

async function tc2_vocabulary(page, month) {
  console.log('\n🧪 TC2 — only the six admitted names ship')
  await gotoReview(page, month)
  const rows = await readRows(page)

  const unknown = rows.filter((r) => !ADMITTED.some((re) => re.test(r.state)))
  check('TC2.1 every state line is one of the admitted six (or silence)', unknown.length === 0,
    unknown.length === 0 ? `${rows.length} row(s) checked` : `e.g. ${unknown[0].room}: "${unknown[0].state}"`)

  const body = await page.locator('main').innerText()
  const leaked = REJECTED.filter((w) => body.includes(w))
  check('TC2.2 no clock, money, or billing word on the surface', leaked.length === 0,
    leaked.length === 0 ? '' : `leaked: ${leaked.join(', ')}`)

  // "ปกติ" may only ever appear inside ผิดปกติ — never as a healthy-row badge.
  const badBadge = (body.match(/ปกติ/g) || []).length !== (body.match(/ผิดปกติ/g) || []).length
  check('TC2.3 no ปกติ badge on healthy rows — silence = done', !badBadge)
}

/* ─── TC3 — worst-first, and silence has no CTA ─── */

async function tc3_worstFirst(page, month) {
  console.log('\n🧪 TC3 — worst-first ordering, silence carries no action')
  await gotoReview(page, month)
  const rows = await readRows(page)

  const lastActionable = rows.map((r) => !!r.cta).lastIndexOf(true)
  const firstSilent = rows.findIndex((r) => !r.cta)
  const ordered = lastActionable === -1 || firstSilent === -1 || lastActionable < firstSilent
  check('TC3.1 rows needing attention sort above silent rows', ordered,
    ordered ? '' : `actionable row at index ${lastActionable} after silent row at ${firstSilent}`)

  const silentWithCta = rows.filter((r) => !r.state && r.cta)
  check('TC3.2 a row with nothing to say has no CTA', silentWithCta.length === 0)

  const entryRows = rows.filter((r) => r.state.startsWith('ยังไม่มีค่าการใช้งาน'))
  check('TC3.3 every "no reading" row offers exactly one action — จด',
    entryRows.every((r) => r.cta === 'จด'), `${entryRows.length} row(s)`)
}

/* ─── TC4 — one destination per state, and the operator comes back ─── */

async function tc4_routing(page, month) {
  console.log('\n🧪 TC4 — routing out, and the return contract')
  await gotoReview(page, month)
  const rows = await readRows(page)

  const entryRoom = rows.find((r) => r.cta === 'จด')
  if (!entryRoom) {
    check('TC4.1 SKIPPED — no room needs a reading in this month', true, 'nothing to route')
  } else {
    await page.locator(`button[aria-label="จดมิเตอร์ห้อง ${entryRoom.room}"]`).first().click()
    await page.waitForTimeout(900)
    const onFocus = page.url().includes('/meter-readings') && !page.url().includes('/review')
    check(`TC4.1 "จด" hands room ${entryRoom.room} to Focus`, onFocus, page.url())

    const focusRoom = await page.locator('main').innerText().catch(() => '')
    check('TC4.2 Focus opens on the room Review handed it', focusRoom.includes(entryRoom.room))

    // Leaving Focus must return the operator to the surface they were judging on.
    await page.keyboard.press('Escape')
    await page.waitForTimeout(900)
    check('TC4.3 leaving Focus returns to Review, month intact',
      page.url().includes('/meter-readings/review') && page.url().includes(month), page.url())
  }

  await gotoReview(page, month)
  const rows2 = await readRows(page)
  const inspectRoom = rows2.find((r) => r.cta === 'ตรวจ')
  if (!inspectRoom) {
    check('TC4.4 SKIPPED — no committed event to inspect this month', true, 'nothing to route')
    return
  }

  await page.locator(`button[aria-label="ตรวจห้อง ${inspectRoom.room}"]`).first().click()
  await page.waitForTimeout(400)
  const openLink = page.locator('button', { hasText: 'เปิดห้อง' }).first()
  check('TC4.4 "ตรวจ" expands in place and offers ONE link out', await openLink.isVisible().catch(() => false))

  const expandInputs = await page.locator('main input, main textarea').count()
  check('TC4.5 the expand is facts + a link, never a form', expandInputs === 0)

  await openLink.click()
  await page.waitForTimeout(1200)
  check('TC4.6 the link lands on Meter Continuity for that room',
    /\/rooms\/[0-9a-f-]+\/meter/.test(page.url()), page.url())

  await page.locator('a,button', { hasText: 'กลับ' }).first().click()
  await page.waitForTimeout(900)
  check('TC4.7 Continuity returns to Review with its month intact',
    page.url().includes('/meter-readings/review') && page.url().includes(month), page.url())
}

/* ─── TC5 — the flush: what Focus deferred, Review commits ─── */

async function tc5_draftFlush(page, month) {
  console.log('\n🧪 TC5 — a draft Focus never submitted is committable FROM Review')
  await gotoReview(page, month)
  const rows = await readRows(page)
  const target = rows.find((r) => r.cta === 'จด')
  if (!target) {
    check('TC5 SKIPPED — every room already has a reading this month', true, 'nothing to flush')
    return
  }

  // Produce the draft the way an operator does: Review hands the room to Focus,
  // Focus saves LOCALLY and defers the submit. Seeding localStorage directly
  // would prove less — the point is that the real local-first path lands here.
  await page.locator(`button[aria-label="จดมิเตอร์ห้อง ${target.room}"]`).first().click()
  await page.waitForTimeout(1000)

  const elec = page.locator('input[aria-label="มิเตอร์ไฟฟ้า"]').first()
  const water = page.locator('input[aria-label="มิเตอร์น้ำ"]').first()
  await elec.waitFor({ timeout: 8000 })

  // Read each meter's own baseline off the Focus row and step just above it:
  // the values this smoke commits land in the dev database and become next
  // month's `previous`, so a plausible reading matters.
  // Walk UP from the input until the row's baseline number is found. The
  // previous scraper only looked at input.parentElement, where Focus does not
  // put it — so it always fell through to its `|| '0'` default and silently
  // submitted a value below the real baseline. That made TC5.5/5.6 fail for a
  // reason that had nothing to do with the flush being tested.
  const previous = await page.$$eval('input[aria-label^="มิเตอร์"]', (inputs) =>
    Object.fromEntries(inputs.map((i) => {
      let node = i.parentElement
      for (let depth = 0; node && depth < 5; depth++) {
        const shown = Array.from(node.querySelectorAll('span'))
          .map((s) => s.textContent.trim())
          .find((t) => /^[\d,]+$/.test(t))
        if (shown) return [i.getAttribute('aria-label'), Number(shown.replace(/,/g, ''))]
        node = node.parentElement
      }
      // A genuinely new meter shows an em-dash, not a number — baseline 0.
      return [i.getAttribute('aria-label'), 0]
    })),
  )
  await elec.fill(String((previous['มิเตอร์ไฟฟ้า'] ?? 0) + 7))
  await page.keyboard.press('Enter')
  await water.fill(String((previous['มิเตอร์น้ำ'] ?? 0) + 3))
  await page.keyboard.press('Enter')
  await page.waitForTimeout(1400)

  // Focus was entered in single-room mode, so the save exits and the return
  // contract brings the operator back to Review.
  check('TC5.1 Focus returns to Review after the save', page.url().includes('/meter-readings/review'), page.url())
  await page.waitForTimeout(800)

  const band = page.locator('[data-test="review-draft-band"]')
  check('TC5.2 Review surfaces the unflushed draft as its own band',
    await band.isVisible().catch(() => false))

  const rowStateBeforeFlush = await page.$eval(
    `[data-test="review-row"][data-room="${target.room}"] [data-test="review-state"]`,
    (n) => n.textContent.trim(),
  ).catch(() => '')
  check('TC5.3 the ROW still reads committed state — draft presence is not a verdict',
    rowStateBeforeFlush.startsWith('ยังไม่มีค่าการใช้งาน'), `"${rowStateBeforeFlush}"`)

  const commitBtn = band.locator('button', { hasText: 'บันทึกค่ามิเตอร์' }).first()
  const commitLabel = await commitBtn.innerText().catch(() => '')
  check('TC5.4 the terminal CTA commits METER READINGS, never bills',
    commitLabel.includes('บันทึกค่ามิเตอร์') && !commitLabel.includes('บิล'), `"${commitLabel}"`)

  await commitBtn.click()
  await page.waitForTimeout(3000)

  check('TC5.5 the band clears once the draft is committed',
    !(await band.isVisible().catch(() => false)))

  const rowStateAfter = await page.$eval(
    `[data-test="review-row"][data-room="${target.room}"] [data-test="review-state"]`,
    (n) => n.textContent.trim(),
  ).catch(() => '')
  check('TC5.6 the row re-derives from the server — no hand-patching',
    !rowStateAfter.startsWith('ยังไม่มีค่าการใช้งาน'), `now "${rowStateAfter}"`)
}

/* ─── TC6 — the Building Workspace owns coverage (ADR-0001 items 1 + 3) ─── */

async function gotoWorkspace(page, month) {
  await page.goto(`${FRONTEND}/meter-readings?month=${month}`, { waitUntil: 'networkidle' })
  await page.locator('[data-room-number]').first().waitFor({ timeout: 15000 })
  await page.waitForTimeout(250)
}

/** The workspace's progress chips, as `{ 'ทั้งหมด': n, 'ยังไม่บันทึก': n, ... }`. */
async function readChips(page) {
  return page.$$eval(
    '[role="radiogroup"][aria-label="กรองการแสดงห้อง"] button[role="radio"]',
    (nodes) =>
      Object.fromEntries(
        nodes.map((n) => {
          const spans = n.querySelectorAll('span')
          const label = spans[0] ? spans[0].textContent.trim() : ''
          const count = spans[1] ? Number(spans[1].textContent.trim()) : null
          return [label, count]
        }),
      ),
  )
}

async function tc6_workspaceDenominator(page, month) {
  console.log('\n🧪 TC6 — the workspace answers progress from its own denominator')
  await gotoWorkspace(page, month)

  // ── ADR item 1 — from a COLD LOAD, with ZERO navigation. Nothing below this
  //    line may click through to another surface before the question is answered.
  const chips = await readChips(page)
  const remaining = chips['ยังไม่บันทึก']
  const all = chips['ทั้งหมด']
  const done = chips['บันทึกแล้ว']

  check('TC6.1 "เหลือกี่ห้อง" is answerable on the workspace itself, zero navigation',
    Number.isInteger(remaining), `ยังไม่บันทึก = ${remaining}`)
  check('TC6.2 the workspace states its own counts',
    Number.isInteger(all) && Number.isInteger(done), `ทั้งหมด=${all} · บันทึกแล้ว=${done}`)

  // ── ADR item 3 — the whole EXPECTED inventory.
  // The inventory view is asserted explicitly rather than relied on as the
  // landing view: the workspace auto-switches to "ยังไม่บันทึก" once past the
  // halfway mark, so a cold load is not guaranteed to BE the inventory view.
  await page.locator('button[role="radio"]', { hasText: 'ทั้งหมด' }).first().click()
  await page.waitForTimeout(400)
  const rendered = await page.locator('[data-room-number]').count()

  check('TC6.3 the inventory view lists every expected room — not only the remaining ones',
    rendered >= done + remaining, `${rendered} row(s) rendered ⊇ ${done + remaining} expected`)

  // ⚠️ Re-anchored for owner decision D1 (2026-07-28). Slice 1 briefly made the
  // "ทั้งหมด" CHIP the denominator, which put the chip's count and its own row
  // list into disagreement. D1 separated them: "ทั้งหมด" is a NAVIGATION count
  // over the workspace inventory, and the progress denominator is the EXPECTED
  // inventory. Both are correct simultaneously — 75 rooms, 18 readings expected
  // — so the denominator is asserted where it actually lives (the progress
  // readout, TC8.2) rather than on the chip.
  const token = await apiToken()
  const apts = (await (await fetch(`${BACKEND}/api/v1/apartments?limit=50`,
    { headers: { Authorization: `Bearer ${token}` } })).json()).data
  const name = await page.locator('h1, [class*="breadcrumb"]').first().innerText().catch(() => '')
  const apt = apts.find((a) => name.includes(a.name)) || apts[0]
  const rooms = (await (await fetch(`${BACKEND}/api/v1/apartments/${apt.id}/rooms?limit=500`,
    { headers: { Authorization: `Bearer ${token}` } })).json()).data
  const expectedCount = rooms.filter((r) =>
    r.status === 'OCCUPIED' && r.active_contract && r.active_contract.move_out_status !== 'PENDING').length

  check('TC6.4 the expected inventory is narrower than the rendered one — the suppressor is applied',
    expectedCount < rendered,
    expectedCount < rendered
      ? `${rendered - expectedCount} room(s) suppressed from progress`
      : `expected=${expectedCount} equals rendered=${rendered} — no suppression`)

  // Completability: every expected room is either done or still to do, so
  // done === expected is reachable. Catches a half-applied fix (progress moved
  // to `expected` while done/remaining still count the raw inventory).
  check('TC6.5 progress is completable — done + remaining = the expected inventory',
    done + remaining === expectedCount,
    `${done} + ${remaining} = ${done + remaining}, expected ${expectedCount}`)
}

/* ─── TC7 — EXIT parity: the grid's `previous` IS the backend's lineage ─── */

const BACKEND = process.env.BACKEND || 'http://localhost:8080'

/** Log in against the API directly, to use the backend as the oracle. */
async function apiToken() {
  for (const password of [ADMIN_PASS_POST, ADMIN_PASS_FRESH]) {
    const res = await fetch(`${BACKEND}/api/v1/auth/login`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username: ADMIN_USER, password }),
    })
    if (res.ok) return (await res.json()).data.access_token
  }
  throw new Error('api login failed')
}

// D3 (owner decision, 2026-07-28): the frontend's previous-value resolution must
// agree with backend lineage for every room — the defect was a room whose latest
// reading is an EXIT row (billing_month NULL), invisible to the month-scoped
// queries the grid used to derive `previous` from. The operator saw 0, entered
// against it, and the save was rejected by the baseline the backend actually
// holds. Comparing the rendered row against the backend's own projection is the
// only assertion that cannot drift back.
async function tc7_exitLineageParity(page, month) {
  console.log('\n🧪 TC7 — the grid renders the backend\'s lineage baseline, not "last month"')
  const token = await apiToken()
  const auth = { headers: { Authorization: `Bearer ${token}` } }

  const apartments = (await (await fetch(`${BACKEND}/api/v1/apartments?limit=50`, auth)).json()).data
  await gotoWorkspace(page, month)
  const apartmentName = await page.locator('h1, [class*="breadcrumb"]').first().innerText().catch(() => '')
  const apartment = apartments.find((a) => apartmentName.includes(a.name)) || apartments[0]

  const latest = (await (await fetch(
    `${BACKEND}/api/v1/apartments/${apartment.id}/meter-readings/latest`, auth)).json()).data || []
  const exitLatest = latest.filter((r) => r.reading_type === 'EXIT')

  check('TC7.1 the lineage projection is reachable and non-empty',
    latest.length > 0, `${latest.length} room(s), ${exitLatest.length} with an EXIT as latest`)

  if (exitLatest.length === 0) {
    check('TC7.2 SKIPPED — no room in this apartment has an EXIT as its latest row', true,
      'seed has no re-let room to prove parity against')
    return
  }

  // Read what the operator actually sees for those rooms.
  const rendered = await page.$$eval('[data-room-number]', (nodes) =>
    Object.fromEntries(nodes.map((n) => [
      n.getAttribute('data-room-number'),
      (n.innerText.match(/[\d,]+/g) || []).map((t) => Number(t.replace(/,/g, ''))),
    ])),
  )

  const mismatches = []
  for (const row of exitLatest) {
    const numbers = rendered[row.room_number]
    if (!numbers) continue // not rendered under the active filter — not this case's concern
    // The row prints each utility's baseline; an unread room shows no other
    // figures, so containment is the honest assertion at this granularity.
    for (const [utility, expected] of [['ไฟฟ้า', row.electricity_current], ['น้ำ', row.water_current]]) {
      if (expected > 0 && !numbers.includes(expected)) {
        mismatches.push(`${row.room_number} ${utility}: backend lineage ${expected}, row shows [${numbers}]`)
      }
    }
  }

  check('TC7.2 every EXIT-latest room shows the backend\'s baseline, never 0',
    mismatches.length === 0,
    mismatches.length === 0
      ? `${exitLatest.length} re-let room(s) verified`
      : mismatches.slice(0, 3).join(' · '))
}

/* ─── TC8 — Slice 2: two inventories, a stable workspace, composed row truth ─── */

/** The chip whose label starts with `label`, as a locator. */
const chip = (page, label) => page.locator('button[role="radio"]', { hasText: label }).first()

async function tc8_sliceTwoContract(page, month) {
  console.log('\n🧪 TC8 — D1 two inventories · D2 no autonomous narrowing · P-1 composed row truth')
  const token = await apiToken()
  const auth = { headers: { Authorization: `Bearer ${token}` } }
  const apartments = (await (await fetch(`${BACKEND}/api/v1/apartments?limit=50`, auth)).json()).data

  await gotoWorkspace(page, month)
  const apartmentName = await page.locator('h1, [class*="breadcrumb"]').first().innerText().catch(() => '')
  const apartment = apartments.find((a) => apartmentName.includes(a.name)) || apartments[0]
  const rooms = (await (await fetch(
    `${BACKEND}/api/v1/apartments/${apartment.id}/rooms?limit=500`, auth)).json()).data
  const expected = rooms.filter((r) =>
    r.status === 'OCCUPIED' && r.active_contract && r.active_contract.move_out_status !== 'PENDING')
  const nonExpected = rooms.filter((r) => !expected.includes(r))

  const chips = await readChips(page)
  const renderedUnderAll = await page.locator('[data-room-number]').count()

  // ── 5 + 6 — the workspace the operator opens is the one they left it as ──
  const cold = await page.evaluate(() => {
    const group = document.querySelector('[role="radiogroup"][aria-label="กรองการแสดงห้อง"]')
    const active = group.querySelector('button[aria-checked="true"]')
    return {
      scrollY: Math.round(window.scrollY),
      chipBarTop: Math.round(group.getBoundingClientRect().top),
      activeChip: active ? active.textContent.trim() : '(none)',
    }
  })
  check('TC8.5 cold load stays at the top of the workspace — nothing scrolls itself',
    cold.scrollY === 0 && cold.chipBarTop >= 0,
    `scrollY=${cold.scrollY}, chip bar top=${cold.chipBarTop}px`)
  check('TC8.6 cold load keeps the whole inventory selected — no auto-narrowing',
    cold.activeChip.startsWith('ทั้งหมด'), `active chip = "${cold.activeChip}"`)

  // ── 1 — "ทั้งหมด" counts what it renders ──
  check('TC8.1 "ทั้งหมด" equals the rendered whole inventory',
    chips['ทั้งหมด'] === renderedUnderAll && renderedUnderAll === rooms.length,
    `chip=${chips['ทั้งหมด']} rendered=${renderedUnderAll} rooms=${rooms.length}`)

  // ── 2 — progress runs on the EXPECTED inventory, which is a different number ──
  await page.setViewportSize({ width: 375, height: 780 })
  await page.waitForTimeout(500)
  const footer = await page.locator('.fixed.bottom-0').innerText().catch(() => '')
  const progress = footer.match(/(\d+)\s*\/\s*(\d+)/)
  check('TC8.2 the progress denominator is the expected inventory, not the room count',
    !!progress && Number(progress[2]) === expected.length && expected.length !== rooms.length,
    progress ? `${progress[1]} / ${progress[2]}, expected=${expected.length}, rooms=${rooms.length}` : 'no progress readout')

  // ── 8 — the attention chip is reachable at 375 px ──
  // Not a demand that it fit: the group is a scroll container, so "reachable"
  // means scrollable to and readable, with the scroll affordance actually
  // working. Making it fit is a re-presentation decision, not this slice.
  const attention = await page.evaluate(() => {
    const group = document.querySelector('[role="radiogroup"][aria-label="กรองการแสดงห้อง"]')
    const target = Array.from(group.querySelectorAll('button[role="radio"]'))
      .find((b) => b.textContent.includes('มีปัญหา'))
    if (!target) return { found: false }
    group.scrollLeft = group.scrollWidth
    const gr = group.getBoundingClientRect(), tr = target.getBoundingClientRect()
    return { found: true, visibleAfterScroll: tr.left >= gr.left - 0.5 && tr.right <= gr.right + 0.5 }
  })
  check('TC8.8 the attention chip is reachable at 375 px',
    attention.found && attention.visibleAfterScroll,
    attention.found ? '' : 'chip not present at all')
  await page.setViewportSize({ width: 1280, height: 900 })
  await page.waitForTimeout(400)

  // ── 3 + 4 — a non-expected room is reachable, but is not queued as work ──
  if (nonExpected.length === 0) {
    check('TC8.3/8.4 SKIPPED — every room in this apartment is expected', true, '')
  } else {
    const sample = nonExpected[0].number
    const underAll = await page.locator(`[data-room-number="${sample}"]`).count()
    check(`TC8.3 non-expected room ${sample} stays reachable under "ทั้งหมด" (incidental entry)`,
      underAll === 1, `${underAll} row(s)`)

    await chip(page, 'ยังไม่บันทึก').click()
    await page.waitForTimeout(400)
    const underRemaining = await page.locator(`[data-room-number="${sample}"]`).count()
    const remainingRendered = await page.locator('[data-room-number]').count()
    check(`TC8.4 non-expected room ${sample} is absent from "ยังไม่บันทึก"`,
      underRemaining === 0, `${underRemaining} row(s)`)
    check('TC8.4b …and that chip counts exactly the rows it renders',
      remainingRendered === chips['ยังไม่บันทึก'],
      `chip=${chips['ยังไม่บันทึก']} rendered=${remainingRendered}`)
  }

  // ── 10 + 9 — composed row truth ──
  await chip(page, 'มีปัญหา').click()
  await page.waitForTimeout(400)
  const attentionRendered = await page.locator('[data-room-number]').count()
  check('TC8.4c "มีปัญหา" counts exactly the rows it renders',
    attentionRendered === chips['มีปัญหา'], `chip=${chips['มีปัญหา']} rendered=${attentionRendered}`)

  const flagged = await page.$$eval('[data-room-number]', (nodes) =>
    nodes.map((n) => n.getAttribute('data-room-number')))
  if (flagged.length === 0) {
    check('TC8.9/8.10 SKIPPED — no committed attention row this month', true, '')
    return
  }

  await chip(page, 'ทั้งหมด').click()
  await page.waitForTimeout(400)
  const target = page.locator(`[data-room-number="${flagged[0]}"]`)
  const committedNote = (await target.innerText()).replace(/\s+/g, ' ')

  check(`TC8.10 saved room ${flagged[0]} carries committed truth AND stays read-only`,
    (await target.locator('input').count()) === 0 && /ผิดปกติ|เปลี่ยนมิเตอร์|แก้ค่ามิเตอร์/.test(committedNote),
    `inputs=${await target.locator('input').count()} · "${committedNote.slice(-46)}"`)

  // Re-open it: the row must switch to draft truth WHOLESALE, not show both.
  await target.click()
  await page.waitForTimeout(500)
  const afterReEdit = (await target.innerText()).replace(/\s+/g, ' ')
  check('TC8.9 a row being re-edited speaks draft truth only — never both at once',
    !/ผิดปกติ|เปลี่ยนมิเตอร์ \d|แก้ค่ามิเตอร์/.test(afterReEdit) && (await target.locator('input').count()) > 0,
    `inputs=${await target.locator('input').count()} · "${afterReEdit.slice(-46)}"`)

  // ── 7 — an EXPLICIT room-scoped return may still land on its room ──
  await page.goto(`${FRONTEND}/meter-readings?month=${month}&focus=${
    (rooms.find((r) => r.number === flagged[0]) || rooms[0]).id}`, { waitUntil: 'networkidle' })
  await page.waitForTimeout(1500)
  const focusText = await page.locator('main').innerText().catch(() => '')
  check('TC8.7 an explicit room-scoped entry still lands on its room',
    focusText.includes(flagged[0]), `looking for ${flagged[0]}`)
}

/* ─── Runner ─── */

;(async () => {
  const browser = await chromium.launch({ headless: !process.env.SMOKE_HEADED })
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 } })
  const month = currentMonth()
  try {
    console.log(`🔗 FRONTEND = ${FRONTEND}`)
    console.log(`📅 month = ${month}`)
    await login(page)
    await tc1_identityAndNoForm(page, month)
    await tc2_vocabulary(page, month)
    await tc3_worstFirst(page, month)
    await tc4_routing(page, month)
    await tc5_draftFlush(page, month)
    await tc6_workspaceDenominator(page, month)
    await tc7_exitLineageParity(page, month)
    await tc8_sliceTwoContract(page, month)
  } catch (err) {
    check('FATAL', false, err.message)
  } finally {
    console.log('\n──────── Summary ────────')
    console.log(`Total: ${results.total}  Pass: ${results.pass}  Fail: ${results.fail}`)
    if (results.failedCases.length) {
      console.log('\nFailed:')
      results.failedCases.forEach((c) => console.log(`  - ${c}`))
    }
    await browser.close()
    process.exit(results.fail ? 1 : 0)
  }
})()
