// Building Workspace — ADR-0001 conformance smoke
// ----------------------------------------------------------------------
// The executable half of ADR-0001's 9-item conformance table. Every case is
// settled by an OBSERVATION on `/meter-readings`, never by reading intent, so a
// future re-presentation (card grid, floor sections, anything) can be checked
// against the ADR without re-reading the audit.
//
//   TC6  coverage is answerable without leaving the workspace — items 1 + 3
//   TC7  EXIT lineage parity — FE `previous` IS the backend's (D3 gate)
//   TC8  two inventories · stable workspace · composed row truth — items 2, 4, 8
//   TC9  Focus consumes the queue, it never defines one — item 5
//   TC10 a room-scoped detour restores the ORIGINATING Focus mode (S4 return contract)
//   TC11 Focus lost the Replace mutation, kept rollover + the route-out (S4.3)
//
// Items 6, 7 and 9 land in Slice 4 (migration plan §4). Numbering is kept from
// the Review-era file on purpose: these cases were written slice by slice and
// renumbering them would break the trail back to the slice that added each one.
//
// 🪦 This file was `playwright-test-meter-review-surface-smoke.js`, and its
// TC1-TC5 asserted the Review surface. ADR-0001 reversed OD-2 — Review was
// retired, not promoted — so B1-c Slice 3 deleted those five cases with the page
// they described, and the file was renamed onto the host that survived.
//
// Requires: backend :8080 + FE dev :3001 + `make smoke-install`.
// Run:  cd devtools/smoke && node playwright-test-building-workspace-smoke.js
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

/* ─── TC9 — Focus consumes the queue; it never defines one (ADR item 5) ─── */

/** True while the Focus screen is mounted (its two inputs are unlabelled by room). */
const focusIsActive = (page) =>
  page.locator('input[aria-label="มิเตอร์ไฟฟ้า"]').first().isVisible().catch(() => false)

/** `ยังไม่ได้จด N ห้อง` → N. The session's own remaining count. */
async function readSessionRemaining(page) {
  const text = await page.locator('main').innerText().catch(() => '')
  const m = text.match(/ยังไม่ได้จด\s+(\d+)\s+ห้อง/)
  return m ? Number(m[1]) : null
}

async function tc9_focusConsumesTheQueue(page, month) {
  console.log('\n🧪 TC9 — the workspace defines the queue, Focus only consumes it')
  const token = await apiToken()
  const auth = { headers: { Authorization: `Bearer ${token}` } }
  const apartments = (await (await fetch(`${BACKEND}/api/v1/apartments?limit=50`, auth)).json()).data

  // A local draft narrows the queue at click time (local-first), so clear it —
  // this case is about WHO computes the set, not about draft exclusion (which
  // `smoke-meter-focus-local-first` TC4 already owns).
  await gotoWorkspace(page, month)
  await page.evaluate(() => {
    for (const k of Object.keys(localStorage)) {
      if (k.startsWith('nana-meter-draft:')) localStorage.removeItem(k)
    }
  })
  await gotoWorkspace(page, month)
  await chip(page, 'ทั้งหมด').click()
  await page.waitForTimeout(400)

  // Every row still carrying an editable input is a room the workspace considers
  // unread. This is the whole WORKSPACE inventory, deliberately wider than the
  // expected-only "ยังไม่บันทึก" chip — Focus's queue is the same wide set, which
  // is what keeps incidental entry reachable (ADR item 9).
  //
  // Read the SET here, while the grid is mounted: once Focus takes over, these
  // per-room inputs are gone and a later re-read would silently see zero.
  const editable = new Set(await page.$$eval('input[aria-label^="มิเตอร์ไฟห้อง"]',
    (nodes) => nodes.map((n) => (n.getAttribute('aria-label') || '').replace('มิเตอร์ไฟห้อง ', '').trim())))
  const unread = editable.size
  const entry = page.locator('button', { hasText: 'จดมิเตอร์เร็ว' }).first()
  const entryDisabled = await entry.isDisabled()

  check('TC9.1 entry into Focus is gated by the workspace\'s own room list',
    entryDisabled === (unread === 0),
    `${unread} unread row(s), entry ${entryDisabled ? 'disabled' : 'enabled'}`)

  if (entryDisabled) {
    check('TC9.2 SKIPPED — no unread room this month to size a queue against', true,
      'every expected room is already read')
  } else {
    await entry.click()
    await page.locator('input[aria-label="มิเตอร์ไฟฟ้า"]').first().waitFor({ timeout: 8000 })
    const remaining = await readSessionRemaining(page)
    check('TC9.2 the session is exactly the set the workspace computed',
      remaining === unread, `session=${remaining}, workspace unread=${unread}`)
    await page.keyboard.press('Escape')
    await page.waitForTimeout(400)
  }

  // ── A room-scoped entry stays one room. `?focus=` is a resume primitive, not a
  //    queue builder — if it ever grew into one, Focus would be defining scope.
  const apartmentName = await page.locator('h1, [class*="breadcrumb"]').first().innerText().catch(() => '')
  const apartment = apartments.find((a) => apartmentName.includes(a.name)) || apartments[0]
  const rooms = (await (await fetch(
    `${BACKEND}/api/v1/apartments/${apartment.id}/rooms?limit=500`, auth)).json()).data
  const anyRoom = rooms[0]
  await page.goto(`${FRONTEND}/meter-readings?month=${month}&focus=${anyRoom.id}`,
    { waitUntil: 'networkidle' })
  await page.locator('input[aria-label="มิเตอร์ไฟฟ้า"]').first().waitFor({ timeout: 8000 })
  const single = await readSessionRemaining(page)
  check('TC9.3 a room-scoped entry opens one room, never a queue',
    single === 1, `session=${single} room(s)`)

  // ── Focus carries no filter of its own: the chip group is the workspace's, and
  //    it is not mounted while Focus is.
  const filterWhileFocused = await page
    .locator('[role="radiogroup"][aria-label="กรองการแสดงห้อง"]').count()
  check('TC9.4 Focus renders no filter control of its own',
    filterWhileFocused === 0, `${filterWhileFocused} filter group(s) visible`)

  // ── The snapshot gate (B1-c Slice 3). `enterFocusMode` hands the room OBJECT to
  //    `session.enter()`, which stores it; Focus never re-reads the projection. A
  //    `?focus=` consumed before the lineage query resolved therefore FROZE
  //    `previous = 0` for the whole session, and the operator could not save
  //    because the backend validates against the real baseline. Before the fix
  //    this was masked by whichever caller happened to warm the cache; the effect
  //    now waits for the projection to resolve. Focus prints `—` for a zero
  //    baseline, so the regression is directly visible.
  //
  //    ⚠️ The race is NOT reproducible by simply visiting the URL — a cold visit
  //    starts every query together and lineage usually wins (verified 3/3 both
  //    before and after the fix). It only appeared when a CALLER had warmed the
  //    rooms cache but not the lineage cache, and the caller that did that (the
  //    Review surface) no longer exists. So the condition is created here on
  //    purpose: hold `/meter-readings/latest` open while rooms resolve normally.
  //    That is precisely the state any future entry point can produce, and it
  //    makes this case fail against the old room-count readiness proxy instead of
  //    passing under both.
  const latest = (await (await fetch(
    `${BACKEND}/api/v1/apartments/${apartment.id}/meter-readings/latest`, auth)).json()).data || []
  const probe = latest.find((r) => r.electricity_current > 0 && editable.has(r.room_number))

  if (!probe) {
    check('TC9.5 SKIPPED — no unread room with a non-zero lineage baseline to probe', true,
      `${latest.length} lineage row(s), ${editable.size} unread room(s), no overlap`)
  } else {
    const probeRoom = rooms.find((r) => r.number === probe.room_number)
    await page.route('**/meter-readings/latest', async (route) => {
      await new Promise((resolve) => setTimeout(resolve, 2500))
      await route.continue()
    })
    try {
      await page.goto(`${FRONTEND}/meter-readings?month=${month}&focus=${probeRoom.id}`,
        { waitUntil: 'domcontentloaded' })
      await page.locator('input[aria-label="มิเตอร์ไฟฟ้า"]').first().waitFor({ timeout: 15000 })
      const shown = (await page.locator('main').innerText().catch(() => '')).replace(/\s+/g, ' ')
      const expected = probe.electricity_current.toLocaleString('th-TH')
      check(`TC9.5 Focus is entered only from a RESOLVED projection — baseline never freezes at 0 (${probe.room_number} ไฟฟ้า ${expected})`,
        shown.includes(expected), shown.includes(expected) ? '' : `Focus shows "${shown.slice(0, 90)}"`)
    } finally {
      await page.unroute('**/meter-readings/latest')
    }
  }

  // ── Scope belongs to the workspace, and moving it EVICTS Focus. This is the
  //    positive form of "Focus has no month or apartment of its own": not that
  //    the control is hidden, but that Focus does not survive a scope change.
  if (await focusIsActive(page)) {
    await page.locator('button[aria-label="เดือนก่อนหน้า"]').first().click()
    await page.waitForTimeout(1200)
    check('TC9.6 changing the workspace\'s month evicts Focus — scope is not Focus\'s',
      (await focusIsActive(page)) === false,
      (await focusIsActive(page)) ? 'Focus survived a scope change' : 'returned to the workspace')
  } else {
    check('TC9.6 SKIPPED — Focus is not active, nothing to evict', true, '')
  }
}

/* ─── TC10 — a room-scoped detour restores the ORIGINATING mode ─── */

// Owner ruling 2026-07-29: the return primitive carries an explicit Focus mode,
// and a queue sweep must come back a queue sweep.
//
//   Focus(queue, A207) → Continuity(A207) → Focus(queue, resume=A207)   ✅
//   Focus(queue)       → Continuity       → Focus(single-room)          ❌
//
// The second destroys the sweep the operator only meant to pause. "Leave to do
// room-scoped work, then return immediately" is not satisfied by landing on the
// right URL in the wrong workflow — which is why these cases assert the MODE and
// the surviving queue, not just the room.
async function tc10_detourRestoresMode(page, month) {
  console.log('\n🧪 TC10 — Focus → Continuity → Focus restores the mode, not just the room')

  await gotoWorkspace(page, month)
  await page.evaluate(() => {
    for (const k of Object.keys(localStorage)) {
      if (k.startsWith('nana-meter-draft:')) localStorage.removeItem(k)
    }
  })
  await gotoWorkspace(page, month)
  await chip(page, 'ทั้งหมด').click()
  await page.waitForTimeout(400)

  const entry = page.locator('button', { hasText: 'จดมิเตอร์เร็ว' }).first()
  if (await entry.isDisabled()) {
    check('TC10 SKIPPED — no unread room this month, cannot start a queue sweep', true,
      'reseed or pick a month with unread rows to exercise this')
    return
  }

  // ── 1. start a QUEUE sweep ──
  await entry.click()
  await page.locator('input[aria-label="มิเตอร์ไฟฟ้า"]').first().waitFor({ timeout: 8000 })
  const queueSizeBefore = await readSessionRemaining(page)
  const roomBefore = await page.locator('p.text-5xl').first().innerText().catch(() => '')
  check('TC10.1 a queue sweep is running',
    queueSizeBefore !== null && queueSizeBefore > 1 && !!roomBefore,
    `${queueSizeBefore} room(s), standing on ${roomBefore}`)

  // ── 2. route out to Continuity for the room we are standing on ──
  const routeOut = page.locator('button', { hasText: 'จัดการมิเตอร์ห้องนี้' }).first()
  check('TC10.2 Focus offers a route out to the room\'s meter surface',
    await routeOut.isVisible().catch(() => false))
  await routeOut.click()
  await page.waitForTimeout(1200)
  check('TC10.3 it lands on Meter Continuity for that room',
    /\/rooms\/[0-9a-f-]+\/meter/.test(page.url()), page.url())

  // ── 3. come back the way the operator would: the surface's own back ──
  await page.getByRole('button', { name: 'กลับ' }).first().click()
  await page.locator('input[aria-label="มิเตอร์ไฟฟ้า"]').first().waitFor({ timeout: 12000 })

  // ── 4/5. the mode survived, and we are standing where we left ──
  const queueSizeAfter = await readSessionRemaining(page)
  const roomAfter = await page.locator('p.text-5xl').first().innerText().catch(() => '')
  check('TC10.4 the restored session is still a QUEUE, not one borrowed room',
    queueSizeAfter !== null && queueSizeAfter > 1,
    `session=${queueSizeAfter} room(s) (was ${queueSizeBefore})`)
  check('TC10.5 it resumes on the room that was lent',
    roomAfter === roomBefore, `left ${roomBefore}, returned to ${roomAfter}`)
  check('TC10.6 the month came back with it — the detour did not reset scope',
    page.url().includes(`month=${month}`), page.url())

  // ── 6/7. save one room: a queue does NOT auto-exit, and the rest survives ──
  await page.locator('input[aria-label="มิเตอร์ไฟฟ้า"]').first().fill('91234')
  await page.keyboard.press('Enter')
  await page.locator('input[aria-label="มิเตอร์น้ำ"]').first().fill('9123')
  await page.keyboard.press('Enter')
  await page.waitForTimeout(700)

  const stillInFocus = await focusIsActive(page)
  check('TC10.7 saving one room does not auto-exit — that is single-room behaviour',
    stillInFocus === true, stillInFocus ? '' : 'Focus exited after one save')
  const roomAfterSave = await page.locator('p.text-5xl').first().innerText().catch(() => '')
  check('TC10.8 the rest of the queue is still there, advanced to the next room',
    stillInFocus && !!roomAfterSave && roomAfterSave !== roomAfter,
    `${roomAfter} → ${roomAfterSave}`)

  await page.keyboard.press('Escape')
  await page.waitForTimeout(600)
  await page.evaluate(() => {
    for (const k of Object.keys(localStorage)) {
      if (k.startsWith('nana-meter-draft:')) localStorage.removeItem(k)
    }
  })

  // ── 8. the fallback: a bare `?focus=` with NO hint stays single-room ──
  // This is the compatibility half of the contract. Widening the default would
  // silently turn every one-room detour into a sweep.
  const token = await apiToken()
  const auth = { headers: { Authorization: `Bearer ${token}` } }
  const apartments = (await (await fetch(`${BACKEND}/api/v1/apartments?limit=50`, auth)).json()).data
  await gotoWorkspace(page, month)
  const apartmentName = await page.locator('h1, [class*="breadcrumb"]').first().innerText().catch(() => '')
  const apartment = apartments.find((a) => apartmentName.includes(a.name)) || apartments[0]
  const rooms = (await (await fetch(
    `${BACKEND}/api/v1/apartments/${apartment.id}/rooms?limit=500`, auth)).json()).data

  await page.goto(`${FRONTEND}/meter-readings?month=${month}&focus=${rooms[0].id}`,
    { waitUntil: 'networkidle' })
  await page.locator('input[aria-label="มิเตอร์ไฟฟ้า"]').first().waitFor({ timeout: 10000 })
  const bareSession = await readSessionRemaining(page)
  check('TC10.9 a bare ?focus= with no mode hint is still single-room',
    bareSession === 1, `session=${bareSession} room(s)`)
}

/* ─── TC11 — Focus no longer OWNS meter replacement (S4.3) ─── */

// A physical meter swap is a ROOM fact — it survives a tenant change — so
// recording it belongs to Meter Continuity, not to this month's observation
// (Lock C: Replace ≠ Recovery). S4.3 removes the mutation from Focus now that the
// route-out added in S4.1 gives a real swap somewhere to go.
//
// Rollover STAYS: it IS a property of this observation.
//
// The round trip itself is TC10's job; the cases here are about what was removed,
// what survived, and what the save path is now incapable of sending.
async function tc11_focusLostReplaceOwnership(page, month) {
  console.log('\n🧪 TC11 — Focus lost the Replace mutation, kept rollover and the route-out')

  const clearDrafts = () => page.evaluate(() => {
    for (const k of Object.keys(localStorage)) {
      if (k.startsWith('nana-meter-draft:')) localStorage.removeItem(k)
    }
  })

  await gotoWorkspace(page, month)
  await clearDrafts()
  await gotoWorkspace(page, month)
  await chip(page, 'ทั้งหมด').click()
  await page.waitForTimeout(400)

  const entry = page.locator('button', { hasText: 'จดมิเตอร์เร็ว' }).first()
  if (await entry.isDisabled()) {
    check('TC11 SKIPPED — no unread room this month to open Focus on', true,
      'reseed or pick a month with unread rows')
    return
  }
  await entry.click()
  await page.locator('input[aria-label="มิเตอร์ไฟฟ้า"]').first().waitFor({ timeout: 8000 })
  const room = await page.locator('p.text-5xl').first().innerText().catch(() => '')

  // ── what was REMOVED ──
  const replaceInFocus = await page.locator('button', { hasText: 'เปลี่ยนมิเตอร์ใหม่' }).count()
  check('TC11.1 Focus offers no "เปลี่ยนมิเตอร์ใหม่" — the mutation is gone',
    replaceInFocus === 0, `${replaceInFocus} toggle(s) found`)

  // ── what SURVIVED ──
  check('TC11.2 Focus still offers the route-out to the room that owns the swap',
    await page.locator('button', { hasText: 'จัดการมิเตอร์ห้องนี้' }).first().isVisible().catch(() => false),
    'round trip itself is TC10')

  const rollover = page.locator('button', { hasText: /^ครบรอบ$/ }).first()
  check('TC11.3 "ครบรอบ" survives — rollover is a property of THIS observation',
    await rollover.isVisible().catch(() => false))

  // ── the save path is now incapable of carrying a replacement ──
  // Toggle rollover so the assertion is two-sided: the flag Focus DOES own must
  // reach the payload, and the one it lost must be absent. A test that only
  // proves an absence would also pass if flags stopped working entirely.
  await rollover.click()
  await page.waitForTimeout(200)
  await page.locator('input[aria-label="มิเตอร์ไฟฟ้า"]').first().fill('88123')
  await page.keyboard.press('Enter')
  await page.locator('input[aria-label="มิเตอร์น้ำ"]').first().fill('8812')
  await page.keyboard.press('Enter')
  await page.waitForTimeout(700)

  // Leave Focus → the workspace auto-restores the draft into the grid, which owns
  // the submit ("whoever owns the denominator owns the flush").
  await page.keyboard.press('Escape')
  await page.waitForTimeout(900)

  // Capture the batch payload and ABORT it — this case is about the request shape,
  // and nothing should be persisted into the dev database to prove it.
  let payload = null
  await page.route('**/meter-readings/batch', async (route) => {
    try { payload = JSON.parse(route.request().postData() || 'null') } catch (_) {}
    await route.abort()
  })
  try {
    await page.locator('button', { hasText: /^บันทึก$|^บันทึก \(/ }).first().click()
    await page.waitForTimeout(900)
    // Rollover usage is large by construction, so the grid raises its
    // anomaly-confirm toast before submitting. Confirm it — the flag under test
    // is exactly the reason the usage looks anomalous.
    const confirm = page.locator('button', { hasText: 'บันทึกต่อ' }).first()
    if (await confirm.isVisible().catch(() => false)) {
      await confirm.click()
      await page.waitForTimeout(1200)
    }
  } finally {
    await page.unroute('**/meter-readings/batch')
  }

  const items = payload && Array.isArray(payload.items) ? payload.items : []
  const mine = items.find((i) => i.is_electricity_meter_rollover || i.is_water_meter_rollover) || items[0]

  check('TC11.4 the flag Focus still owns reaches the payload — rollover survived',
    !!mine && (mine.is_electricity_meter_rollover === true || mine.is_water_meter_rollover === true),
    mine ? JSON.stringify(mine) : `no batch payload captured (${items.length} item(s))`)

  const anyReplaced = items.some((i) =>
    'is_electricity_meter_replaced' in i || 'is_water_meter_replaced' in i)
  check('TC11.5 no is_*_meter_replaced anywhere in a Focus-originated save',
    items.length > 0 && !anyReplaced,
    items.length === 0 ? 'no payload captured' : `${items.length} item(s), none carrying a replacement`)

  await clearDrafts()
  console.log(`     (room used: ${room}; batch aborted, nothing persisted)`)
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
    await tc6_workspaceDenominator(page, month)
    await tc7_exitLineageParity(page, month)
    await tc8_sliceTwoContract(page, month)
    await tc9_focusConsumesTheQueue(page, month)
    await tc10_detourRestoresMode(page, month)
    await tc11_focusLostReplaceOwnership(page, month)
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
