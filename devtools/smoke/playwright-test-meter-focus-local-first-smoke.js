// Meter Focus Mode — Local-first Architecture Smoke Test
// ----------------------------------------------------------------------
// Locks the Phase 1 architecture (commits 27bd1de..acc49c4):
//
//   - Focus Mode writes drafts to localStorage on Enter, never the API
//   - Focus surface has no submit affordance (Spreadsheet owns commit)
//   - Drafts cross the Focus ↔ Spreadsheet boundary via form remount
//   - Re-entering Focus excludes rooms that already have a local draft
//   - Drawer escalation opens single-room Focus and returns to drawer
//
// 5 cases — the two REGRESSION GUARDS that must hold or Phase 1 is broken:
//   TC1  REGRESSION GUARD #1 — zero API call per Focus Enter
//   TC2  REGRESSION GUARD #2 — Focus surface has no submit
//   TC3  Drafts visible in Spreadsheet after Focus exit
//   TC4  Re-entering Focus excludes drafted rooms from queue
//   TC5  Drawer escalation → Focus single-room → return → drawer reopens
//
// Run:  make smoke-meter-focus-local-first
//       FRONTEND=http://localhost:3001 node playwright-test-meter-focus-local-first-smoke.js

const { chromium } = require('playwright')

const FRONTEND = process.env.FRONTEND || 'http://localhost:3001'
const BACKEND = process.env.BACKEND || 'http://localhost:8080'
const ADMIN_USER = 'admin'
const ADMIN_PASS_FRESH = 'admin123'
const ADMIN_PASS_POST = 'admin1234'

// ─── Pretty logger ──────────────────────────────────────────────────────

const results = { pass: 0, fail: 0, total: 0, failedCases: [] }
const check = (name, ok, detail = '') => {
  const icon = ok ? '✅' : '❌'
  const msg = detail ? ` — ${detail}` : ''
  console.log(`  ${icon} ${name}${msg}`)
  results.total++
  if (ok) results.pass++
  else {
    results.fail++
    results.failedCases.push(`${name}${msg}`)
  }
}

// ─── Login ──────────────────────────────────────────────────────────────

async function login(page) {
  await page.goto(`${FRONTEND}/login`, { waitUntil: 'domcontentloaded' })
  for (const pw of [ADMIN_PASS_POST, ADMIN_PASS_FRESH]) {
    await page.fill('input[name="username"]', ADMIN_USER)
    await page.fill('input[name="password"]', pw)
    await page.click('button[type="submit"]')
    try {
      await page.waitForFunction(
        () => !window.location.pathname.includes('/login') ||
              document.querySelector('[role="alert"]'),
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
    await page.waitForFunction(() => !window.location.pathname.includes('/change-password'), {
      timeout: 8000,
    })
  }
  await page.waitForFunction(() => !window.location.pathname.includes('/login'), { timeout: 10000 })
}

async function loginApi() {
  for (const pw of [ADMIN_PASS_POST, ADMIN_PASS_FRESH]) {
    const res = await fetch(`${BACKEND}/api/v1/auth/login`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username: ADMIN_USER, password: pw }),
    })
    const json = await res.json().catch(() => null)
    if (json?.status === 'success') return json.data.access_token
  }
  throw new Error('API login failed')
}

async function fetchApartments(token) {
  const res = await fetch(`${BACKEND}/api/v1/apartments`, {
    headers: { Authorization: `Bearer ${token}` },
  })
  if (!res.ok) throw new Error(`fetchApartments: ${res.status}`)
  return (await res.json()).data || []
}

// ─── Page helpers ───────────────────────────────────────────────────────

async function gotoMeterReadings(page) {
  await page.goto(`${FRONTEND}/meter-readings`, { waitUntil: 'networkidle' })
  await page.waitForTimeout(700)
}

async function enterFocusMode(page) {
  await page.locator('button', { hasText: 'จดมิเตอร์เร็ว' }).first().click()
  // Focus uses singular aria-labels; spreadsheet uses "...ห้อง {roomNumber}"
  await page.locator('input[aria-label="มิเตอร์ไฟฟ้า"]').first().waitFor({ state: 'visible', timeout: 5000 })
}

async function exitFocusMode(page) {
  // Click the explicit "ออก" button in the Focus footer. Filter by the Esc
  // kbd hint to disambiguate from the sidebar's "ออกจากระบบ" logout button —
  // both would otherwise match a bare "ออก" text query.
  await page.locator('button:has(kbd):has-text("ออก")').first().click()
  // Focus surface uses "มิเตอร์ไฟฟ้า" / "มิเตอร์น้ำ" (full Thai words);
  // Spreadsheet's MeterCell uses "มิเตอร์ไฟห้อง" / "มิเตอร์น้ำห้อง" (short
  // form + room number suffix). The divergence is intentional and makes
  // the two surfaces grep-distinct.
  await page.locator('input[aria-label="มิเตอร์ไฟฟ้า"]').waitFor({ state: 'detached', timeout: 5000 })
  await page.locator('input[aria-label^="มิเตอร์ไฟห้อง"]').first().waitFor({ state: 'attached', timeout: 5000 })
}

async function readCurrentFocusRoom(page) {
  return (await page.locator('p.text-5xl').first().textContent().catch(() => '') || '').trim()
}

/** Type elec+water then Enter on water → save+advance. Returns the room number that was saved. */
async function saveOneInFocus(page, elec, water) {
  const room = await readCurrentFocusRoom(page)
  await page.locator('input[aria-label="มิเตอร์ไฟฟ้า"]').first().fill(String(elec))
  await page.keyboard.press('Enter') // Enter on elec focuses water
  await page.locator('input[aria-label="มิเตอร์น้ำ"]').first().fill(String(water))
  await page.keyboard.press('Enter') // Enter on water saves + advances
  // Wait for the next room to render (room hero text changes) OR completion/exit
  await page.waitForTimeout(300)
  return room
}

function readDraft(page, aptId, month) {
  return page.evaluate(({ aptId, month }) => {
    const key = `nana-meter-draft:${aptId}:${month}`
    const raw = localStorage.getItem(key)
    return raw ? JSON.parse(raw) : null
  }, { aptId, month })
}

function clearDraft(page, aptId) {
  return page.evaluate(({ aptId }) => {
    Object.keys(localStorage)
      .filter((k) => k.startsWith(`nana-meter-draft:${aptId}:`))
      .forEach((k) => localStorage.removeItem(k))
  }, { aptId })
}

function currentMonth() {
  const d = new Date()
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}`
}

// ─── Cases ──────────────────────────────────────────────────────────────

async function tc1_zeroApiPerEnter(page, apartmentId) {
  console.log('\n🧪 TC1 — REGRESSION GUARD #1: zero API call per Focus Enter')
  await clearDraft(page, apartmentId)
  await gotoMeterReadings(page)
  await enterFocusMode(page)

  // Spy AFTER entering Focus — initial page loads (rooms, readings, baselines)
  // are allowed; the guard is specifically that Enter inside Focus fires nothing.
  const writeRequests = []
  const onRequest = (req) => {
    const url = req.url()
    if (!url.includes('/api/v1/') || !url.includes('meter-readings')) return
    const method = req.method()
    if (method === 'GET' || method === 'HEAD') return
    writeRequests.push(`${method} ${url}`)
  }
  page.on('request', onRequest)

  const saved = []
  saved.push(await saveOneInFocus(page, 1001, 501))
  saved.push(await saveOneInFocus(page, 1002, 502))
  saved.push(await saveOneInFocus(page, 1003, 503))

  // Give any latent in-flight write a chance to surface (none should exist).
  await page.waitForTimeout(800)
  page.off('request', onRequest)

  check(
    `TC1.1 zero meter-readings write requests across 3 Focus Enter saves (got ${writeRequests.length})`,
    writeRequests.length === 0,
    writeRequests.length > 0 ? writeRequests.join(' · ') : '',
  )
  check(
    `TC1.2 saved 3 distinct rooms via Enter (got ${saved.filter(Boolean).length})`,
    saved.filter(Boolean).length === 3 && new Set(saved).size === 3,
    saved.join(','),
  )

  // Cleanup so the next case starts from a known state
  await exitFocusMode(page)
  await clearDraft(page, apartmentId)
}

async function tc2_focusHasNoSubmit(page, apartmentId) {
  console.log('\n🧪 TC2 — REGRESSION GUARD #2: Focus surface has no submit')
  await clearDraft(page, apartmentId)
  await gotoMeterReadings(page)
  await enterFocusMode(page)

  // Focus's "บันทึก" button is local-write only. The Spreadsheet's submit is
  // distinguishable by either the count parens — "บันทึก (N)" — or the long
  // form "บันทึกลงระบบ". Neither should appear in the Focus surface.
  const submitLikeCount = await page.evaluate(() => {
    const buttons = Array.from(document.querySelectorAll('button'))
    return buttons.filter((b) => {
      const t = (b.textContent || '').trim()
      return t.includes('บันทึกลงระบบ') || /บันทึก\s*\(\d+\)/.test(t)
    }).length
  })
  check('TC2.1 no "บันทึกลงระบบ" or "บันทึก (N)" buttons in Focus surface', submitLikeCount === 0,
    submitLikeCount > 0 ? `found ${submitLikeCount}` : '')

  // The Focus save button itself should be the plain "บันทึก" + Enter hint
  const focusSaveExists = await page.locator('button', { hasText: 'บันทึก' }).first().isVisible().catch(() => false)
  check('TC2.2 Focus save button "บันทึก" still present (sanity)', focusSaveExists)

  await exitFocusMode(page)
}

async function tc3_draftsVisibleInSpreadsheet(page, apartmentId, month) {
  console.log('\n🧪 TC3 — drafts visible in Spreadsheet after Focus exit')
  await clearDraft(page, apartmentId)
  await gotoMeterReadings(page)
  await enterFocusMode(page)

  const saved = []
  saved.push(await saveOneInFocus(page, 2001, 1001))
  saved.push(await saveOneInFocus(page, 2002, 1002))
  saved.push(await saveOneInFocus(page, 2003, 1003))

  // localStorage proof first (architecture invariant — independent of UX gating)
  const draft = await readDraft(page, apartmentId, month)
  check('TC3.1 draft slot exists for current scope', !!draft)
  check(
    `TC3.2 draft contains 3 rooms (got ${Object.keys(draft?.rooms || {}).length})`,
    !!draft && Object.keys(draft.rooms).length === 3,
  )

  // Exit Focus → form remounts → useMeterDraft loadDraft on mount → DraftBanner
  // appears. The banner is the existing user-gated restore pattern (same shape
  // used by mid-session Spreadsheet drafts). Operator confirms via "กู้คืน".
  await exitFocusMode(page)

  const draftBanner = page.locator('text=พบร่างที่ยังไม่ได้บันทึก').first()
  const bannerShown = await draftBanner.isVisible().catch(() => false)
  check('TC3.3 Spreadsheet surfaces DraftBanner immediately on Focus exit', bannerShown)

  if (bannerShown) {
    await page.locator('button', { hasText: 'กู้คืน' }).first().click()
    await page.waitForTimeout(400)
    let hydrated = 0
    for (const roomNumber of saved) {
      if (!roomNumber) continue
      const input = page.locator(`input[aria-label="มิเตอร์ไฟห้อง ${roomNumber}"]`).first()
      try {
        await input.waitFor({ state: 'attached', timeout: 3000 })
        const val = await input.inputValue()
        if (val && val !== '') hydrated++
      } catch (_) {}
    }
    check(`TC3.4 inputs hydrate after "กู้คืน" (${hydrated}/${saved.length})`,
      hydrated === saved.length, `expected ${saved.length}, got ${hydrated}`)
  }

  await clearDraft(page, apartmentId)
}

async function tc4_focusQueueExcludesDrafts(page, apartmentId) {
  console.log('\n🧪 TC4 — re-entering Focus excludes drafted rooms from queue')
  await clearDraft(page, apartmentId)
  await gotoMeterReadings(page)
  await enterFocusMode(page)

  const firstSaved = await saveOneInFocus(page, 3001, 1500)
  const secondSaved = await saveOneInFocus(page, 3002, 1501)
  await exitFocusMode(page)

  // Re-enter Focus — the queue filter in handleEnterFocusMode reads the fresh
  // draft and excludes rooms with entries. Walk the next 5 rooms via Skip and
  // assert neither saved room appears.
  await enterFocusMode(page)
  const seen = new Set()
  for (let i = 0; i < 5; i++) {
    const r = await readCurrentFocusRoom(page)
    if (r) seen.add(r)
    // Skip button advances without saving
    await page.locator('button', { hasText: /^ข้าม$/ }).first().click().catch(() => {})
    await page.waitForTimeout(150)
  }

  check(`TC4.1 previously-drafted room ${firstSaved} NOT re-shown in re-entered queue`,
    !!firstSaved && !seen.has(firstSaved),
    seen.has(firstSaved) ? `appeared` : '')
  check(`TC4.2 previously-drafted room ${secondSaved} NOT re-shown in re-entered queue`,
    !!secondSaved && !seen.has(secondSaved),
    seen.has(secondSaved) ? `appeared` : '')

  await exitFocusMode(page)
  await clearDraft(page, apartmentId)
}

async function tc5_drawerEscalationRoundtrip(page, apartmentId) {
  console.log('\n🧪 TC5 — drawer escalation → Focus single-room → return → drawer reopens')
  await clearDraft(page, apartmentId)
  await gotoMeterReadings(page)

  // Click the first room-number button in the spreadsheet to open RoomHistoryDrawer.
  // Room numbers render as <button> on each row (see MeterReadingRow onOpenHistory).
  const firstRoomNumber = await page.locator('input[aria-label^="มิเตอร์ไฟห้อง"]')
    .first()
    .evaluate((el) => {
      const label = el.getAttribute('aria-label') || ''
      return label.replace('มิเตอร์ไฟห้อง ', '').trim()
    })
  if (!firstRoomNumber) {
    check('TC5.0 could read first room number from spreadsheet', false)
    return
  }
  // Room-number buttons inside row scope — find by exact text match
  await page.locator(`button:has-text("${firstRoomNumber}")`).first().click()
  // Drawer appears as a Sheet; title contains the room number
  await page.waitForTimeout(400)
  const drawerVisible = await page.locator('text=เปิดในโหมดจดเร็ว').isVisible().catch(() => false)
  check(`TC5.1 RoomHistoryDrawer opens for room ${firstRoomNumber}`, drawerVisible)
  if (!drawerVisible) return

  // Click the escalation button
  await page.locator('button', { hasText: 'เปิดในโหมดจดเร็ว' }).first().click()
  await page.locator('input[aria-label="มิเตอร์ไฟฟ้า"]').first().waitFor({ state: 'visible', timeout: 5000 })
  const focusedRoom = await readCurrentFocusRoom(page)
  check(`TC5.2 Focus opens in single-room mode for room ${firstRoomNumber}`,
    focusedRoom === firstRoomNumber, `got "${focusedRoom}"`)

  // Save in Focus — single-room mode should exit synchronously (no completion screen)
  await page.locator('input[aria-label="มิเตอร์ไฟฟ้า"]').first().fill('9999')
  await page.keyboard.press('Enter')
  await page.locator('input[aria-label="มิเตอร์น้ำ"]').first().fill('5555')
  await page.keyboard.press('Enter')

  // Wait for the form to remount + drawer to reopen
  await page.waitForTimeout(800)

  // Spreadsheet should be back AND drawer should still be open for the same room.
  // attached (not visible) — drawer overlay may sit on top of some inputs but
  // the spreadsheet itself is mounted again.
  const backInSpreadsheet = await page.locator('input[aria-label^="มิเตอร์ไฟห้อง"]').first().count() > 0
  check('TC5.3 returned to Spreadsheet after single-room save', backInSpreadsheet)

  const drawerStillOpen = await page.locator('text=เปิดในโหมดจดเร็ว').isVisible().catch(() => false)
  check('TC5.4 RoomHistoryDrawer stays open on return (judgment context preserved)', drawerStillOpen)

  // Draft was written
  const month = currentMonth()
  const draft = await readDraft(page, apartmentId, month)
  const draftRoomIds = draft ? Object.keys(draft.rooms) : []
  check(`TC5.5 single-room save wrote draft (draft has ${draftRoomIds.length} room)`,
    draftRoomIds.length === 1)

  // Close drawer + cleanup
  await page.keyboard.press('Escape')
  await page.waitForTimeout(200)
  await clearDraft(page, apartmentId)
}

// ─── Runner ─────────────────────────────────────────────────────────────

;(async () => {
  console.log(`🔗 FRONTEND = ${FRONTEND}`)
  const token = await loginApi()
  const apartments = await fetchApartments(token)
  if (apartments.length === 0) {
    console.error('❌ no apartments in seed')
    process.exit(1)
  }
  const apt = apartments[0]
  const month = currentMonth()
  console.log(`📋 using apartment: ${apt.name} (${apt.id}) · month=${month}`)

  const browser = await chromium.launch({
    headless: process.env.SMOKE_HEADLESS === '1',
    slowMo: process.env.SMOKE_HEADLESS === '1' ? 0 : 60,
  })
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  const page = await ctx.newPage()
  await login(page)

  try {
    await tc1_zeroApiPerEnter(page, apt.id)
    await tc2_focusHasNoSubmit(page, apt.id)
    await tc3_draftsVisibleInSpreadsheet(page, apt.id, month)
    await tc4_focusQueueExcludesDrafts(page, apt.id)
    await tc5_drawerEscalationRoundtrip(page, apt.id)
  } finally {
    await browser.close()
  }

  console.log(`\n──────── Summary ────────`)
  console.log(`Total: ${results.total}  Pass: ${results.pass}  Fail: ${results.fail}`)
  if (results.failedCases.length > 0) {
    console.log('\nFailed:')
    results.failedCases.forEach((c) => console.log(`  - ${c}`))
  }
  process.exit(results.fail > 0 ? 1 : 0)
})().catch((err) => {
  console.error('💥 smoke failed:', err)
  process.exit(1)
})
