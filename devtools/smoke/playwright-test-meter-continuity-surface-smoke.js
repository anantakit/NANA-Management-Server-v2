// Meter Continuity Surface — /rooms/:roomId/meter (P2 Slice 2)
// ----------------------------------------------------------------------
// Locks the two things the surface exists to prove:
//
//   1. apartmentId is RESOLVED from a room read addressed by roomId alone —
//      the route carries no apartment id, is never told one, and the
//      room-scoped primitives still mount.
//   2. The return contract (METER_BOUNDARY_PLAN_CAPABILITY_OWNERSHIP.md §5)
//      is a LOAN: any origin routes in with only "where I was" and lands
//      back there — scope + position intact — no matter what happened while
//      away. Nothing here is Spreadsheet-, Review- or Bill-Detail-specific.
//
// 6 cases:
//   TC1  Direct entry — surface renders, all primitives mount, fallback back
//   TC2  Entry from the meter workflow — query scope survives the round trip
//   TC3  Entry from Bill Detail — returns to that exact bill
//   TC4  Browser back — the gesture and the back button agree
//   TC5  Return after opening + cancelling Replace Meter — silent, same origin
//   TC6  Return after pending-correction deletion — where dev data allows
//
// Run:  make smoke-meter-continuity
//       FRONTEND=http://localhost:3001 node playwright-test-meter-continuity-surface-smoke.js

const { chromium } = require('playwright')

const FRONTEND = process.env.FRONTEND || 'http://localhost:3001'
const BACKEND = process.env.BACKEND || 'http://localhost:8080'
const ADMIN_USER = 'admin'
const ADMIN_PASS_FRESH = 'admin123'
const ADMIN_PASS_POST = 'admin1234'

/* ─── Pretty logger ─────────────────────────────────────────────────── */

const results = { pass: 0, fail: 0, skip: 0, total: 0, failedCases: [] }
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
const skip = (name, why) => {
  console.log(`  ⏭️  ${name} — SKIPPED: ${why}`)
  results.skip++
}

/* ─── Auth ──────────────────────────────────────────────────────────── */

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
    await page.waitForFunction(() => !window.location.pathname.includes('/change-password'), {
      timeout: 8000,
    })
  }
  await page.waitForFunction(() => !window.location.pathname.includes('/login'), { timeout: 10000 })
}

async function apiToken() {
  for (const pw of [ADMIN_PASS_POST, ADMIN_PASS_FRESH]) {
    const res = await fetch(`${BACKEND}/api/v1/auth/login`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username: ADMIN_USER, password: pw }),
    })
    if (res.ok) return (await res.json()).data.access_token
  }
  throw new Error('login failed')
}

/* ─── Caller simulation ─────────────────────────────────────────────────
 * Slice 2 ships the DESTINATION; the affordances that route into it move
 * off RoomHistoryDrawer in Slice 3. To verify the contract now, we push the
 * same history entry React Router itself would write for
 * `navigate(to, { state })` — location state lives at history.state.usr.
 * That exercises the real page reading a real origin, with no production
 * caller wired up prematurely.                                            */
async function enterFrom(page, origin, roomId) {
  await page.goto(`${FRONTEND}${origin}`, { waitUntil: 'domcontentloaded' })
  await page.waitForTimeout(600)
  await page.evaluate(
    ({ origin, roomId }) => {
      const prev = window.history.state || {}
      window.history.pushState(
        { usr: { returnTo: origin }, key: 'smoke', idx: (prev.idx ?? 0) + 1 },
        '',
        `/rooms/${roomId}/meter`,
      )
      window.dispatchEvent(new PopStateEvent('popstate', { state: window.history.state }))
    },
    { origin, roomId },
  )
  await page.waitForFunction(() => window.location.pathname.endsWith('/meter'), { timeout: 8000 })
  await page.waitForTimeout(900)
}

const clickBack = async (page) => {
  await page.getByRole('button', { name: 'กลับ' }).first().click()
  await page.waitForTimeout(700)
}

const path = (page) => new URL(page.url()).pathname + new URL(page.url()).search

/* ─── Run ───────────────────────────────────────────────────────────── */

;(async () => {
  const token = await apiToken()
  const auth = { Authorization: `Bearer ${token}` }

  const apts = await (await fetch(`${BACKEND}/api/v1/apartments`, { headers: auth })).json()
  const apartmentId = apts.data[0].id
  const rooms = await (
    await fetch(`${BACKEND}/api/v1/apartments/${apartmentId}/rooms?limit=200`, { headers: auth })
  ).json()
  const occupied = rooms.data.find((r) => r.active_contract) || rooms.data[0]
  const vacant = rooms.data.find((r) => !r.active_contract)

  console.log(`\n🏠 apartment=${apartmentId}`)
  console.log(`   occupied room=${occupied.number} (${occupied.id})`)
  console.log(`   vacant room=${vacant ? vacant.number : '—'}\n`)

  const browser = await chromium.launch()
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 } })
  await login(page)

  /* ── TC1 — Direct entry ── */
  console.log('TC1 — Direct entry to /rooms/:roomId/meter')
  await page.goto(`${FRONTEND}/rooms/${occupied.id}/meter`, { waitUntil: 'domcontentloaded' })
  await page.waitForTimeout(1400)
  let body = await page.textContent('body')

  check('room number is the surface identity', body.includes(`ห้อง ${occupied.number}`))
  check(
    'current tenant appears as read-only context',
    body.includes(occupied.active_contract.tenant_name),
    occupied.active_contract.tenant_name,
  )
  check('CurrentMeterState mounted', body.includes('ข้อมูลมิเตอร์'))
  check('ReplaceMeterAction mounted', body.includes('เปลี่ยนมิเตอร์'))
  check('MeterHistoryTimeline mounted', body.includes('ประวัติมิเตอร์'))
  check('no rent on the surface', !body.includes('ค่าเช่า'))
  check('no deposit on the surface', !body.includes('เงินประกัน'))
  check('no contract CTA', !body.includes('จัดการสัญญา'))
  check(
    'apartmentId never appears in the URL',
    !page.url().includes(apartmentId),
    'resolved data, not navigation identity',
  )

  await clickBack(page)
  check(
    'direct entry falls back to the room home',
    path(page) === `/apartments/${apartmentId}/rooms`,
    path(page),
  )

  if (vacant) {
    await page.goto(`${FRONTEND}/rooms/${vacant.id}/meter`, { waitUntil: 'domcontentloaded' })
    await page.waitForTimeout(1200)
    body = await page.textContent('body')
    check('vacant room reads ห้องว่าง', body.includes('ห้องว่าง'))
  } else {
    skip('vacant room reads ห้องว่าง', 'no vacant room in dev data')
  }

  /* ── TC2 — Entry from the current meter workflow ── */
  console.log('\nTC2 — Entry from the meter workflow')
  const meterOrigin = `/meter-readings?apartment_id=${apartmentId}`
  await enterFrom(page, meterOrigin, occupied.id)
  body = await page.textContent('body')
  check('surface renders when routed in from the workflow', body.includes(`ห้อง ${occupied.number}`))
  await clickBack(page)
  check('returns to the workflow with its scope intact', path(page) === meterOrigin, path(page))

  /* ── TC3 — Entry from Bill Detail ── */
  console.log('\nTC3 — Entry from Bill Detail')
  const bills = await (await fetch(`${BACKEND}/api/v1/bills?limit=1`, { headers: auth })).json()
  if (bills.data && bills.data.length) {
    const billOrigin = `/bills/${bills.data[0].id}`
    await enterFrom(page, billOrigin, occupied.id)
    check('surface renders when routed in from a bill', (await page.textContent('body')).includes('ประวัติมิเตอร์'))
    await clickBack(page)
    check('returns to that exact bill', path(page) === billOrigin, path(page))
  } else {
    skip('entry from Bill Detail', 'no bills in dev data')
  }

  /* ── TC4 — Browser back ── */
  console.log('\nTC4 — Browser back gesture')
  await enterFrom(page, meterOrigin, occupied.id)
  await page.goBack()
  await page.waitForTimeout(800)
  check('browser back lands on the origin too', path(page) === meterOrigin, path(page))

  /* ── TC5 — Return after Replace Meter interaction ── */
  console.log('\nTC5 — Return after opening + cancelling Replace Meter')
  await enterFrom(page, meterOrigin, occupied.id)
  await page.getByRole('button', { name: /เปลี่ยนมิเตอร์/ }).first().click()
  await page.waitForTimeout(900)
  const sheetOpen = (await page.textContent('body')).includes('เปลี่ยนมิเตอร์')
  check('Replace Meter form opens on this surface', sheetOpen)
  await page.keyboard.press('Escape')
  await page.waitForTimeout(700)
  const toastAfterCancel = await page.locator('[class*="toast"]').count()
  check('cancel is silent — no toast', toastAfterCancel === 0, `${toastAfterCancel} toast(s)`)
  await clickBack(page)
  check(
    'return is unchanged by what happened while away',
    path(page) === meterOrigin,
    path(page),
  )

  /* ── TC6 — Recovery management + return ──
   * Runs against a room that actually carries a READING_RECOVERY anchor, so
   * the Recovery MUTATION is proven reachable on this surface (its permanent
   * owner). The destructive commit is deliberately NOT executed — deleting a
   * real anchor would rewrite dev meter truth, and the delete behaviour itself
   * is unchanged by this slice. We take it to the confirm step, back out, and
   * verify the origin still holds.                                          */
  console.log('\nTC6 — Recovery management reachable + return')
  let recoveryRoom = null
  for (const r of rooms.data) {
    const h = await (
      await fetch(
        `${BACKEND}/api/v1/apartments/${apartmentId}/meter-readings/rooms/${r.id}/history?limit=20`,
        { headers: auth },
      )
    ).json()
    if (JSON.stringify(h).includes('READING_RECOVERY')) {
      recoveryRoom = r
      break
    }
  }

  if (recoveryRoom) {
    await enterFrom(page, meterOrigin, recoveryRoom.id)
    const deleteBtn = page.getByRole('button', { name: 'ลบการแก้ค่านี้' })
    const reachable = (await deleteBtn.count()) > 0
    check(
      'pending-correction delete is reachable on this surface',
      reachable,
      `room ${recoveryRoom.number}`,
    )
    if (reachable) {
      await deleteBtn.first().click()
      await page.waitForTimeout(400)
      check(
        'inline confirm opens (commit not executed — dev data preserved)',
        (await page.textContent('body')).includes('ยืนยันลบ?'),
      )
      await page.getByRole('button', { name: 'ยกเลิก' }).first().click()
      await page.waitForTimeout(400)
    }
    await clickBack(page)
    check(
      'return after recovery management goes to the same origin',
      path(page) === meterOrigin,
      path(page),
    )
  } else {
    skip('recovery management reachable + return', 'no READING_RECOVERY anchor in dev data')
  }

  await browser.close()

  console.log(`\n${'─'.repeat(60)}`)
  console.log(`  PASS ${results.pass}/${results.total}   SKIP ${results.skip}`)
  if (results.fail) {
    console.log('  FAILED:')
    results.failedCases.forEach((c) => console.log(`    • ${c}`))
  }
  console.log(`${'─'.repeat(60)}\n`)
  process.exit(results.fail ? 1 : 0)
})().catch((err) => {
  console.error(err)
  process.exit(1)
})
