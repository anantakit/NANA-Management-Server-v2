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
//
// ⏳ TRANSITIONAL: Review lives at /meter-readings/review while the Spreadsheet
// still owns /meter-readings. At B1-c the route moves and TC1's URL changes.
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
  const previous = await page.$$eval('input[aria-label^="มิเตอร์"]', (inputs) =>
    Object.fromEntries(inputs.map((i) => {
      const spans = i.parentElement ? Array.from(i.parentElement.querySelectorAll('span')) : []
      const shown = spans.map((s) => s.textContent.trim()).find((t) => /^[\d,]+$/.test(t)) || '0'
      return [i.getAttribute('aria-label'), Number(shown.replace(/,/g, ''))]
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
