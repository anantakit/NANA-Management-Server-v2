// Meter Reading Batch — Apartment Scope Contract Smoke Test
// ----------------------------------------------------------------------
// Locks the behavior contract for MeterReadingBatchPage scope handling.
//
//   - CASE 1 (dirty-then-sidebar)  sidebar switch while dirty → banner → confirm → draft preserved
//   - CASE 3 (no dual selector)    no page-local apartment selector in <main>
//   - CASE R1 (dirty input storage cannot represent)  pending draft ⇒ autosave SKIPPED,
//                                  yet freshly typed input must still hold the scope
//   - CASE R2 (no reliance on debounce timing)  draft writes physically blocked ⇒ the
//                                  banner must still appear, so the guard cannot be storage-derived
//   - CASE R3 (restored draft)     a draft that EXISTS for the scope holds it, even once
//                                  restored — a deliberate behaviour change, locked on purpose
//   - CASE 5 / 6 (Focus lifecycle) an accepted scope change evicts the Focus session once;
//                                  `?focus=` is never re-consumed in the new scope
//   - CASE 7 (the flash)           sampled per animation frame across a switch: no render
//                                  pairs the new apartment with the old apartment's rooms
//
// Note: CASE 2 (deep-link from bill-generate) was removed 2026-06-16 when
// /bills/generate and MonthlyPreflightCard were deleted as part of the
// /monthly-bills workspace migration.
//
// R1/R2 are the red-first gate for the scope-reset lifecycle debt round
// (DEBT_SCOPE_RESET_LIFECYCLE_HANDOFF.md). They exist because the pending-switch
// decision must read AUTHORITATIVE dirty truth, never `loadDraft() !== null`:
// autosave is debounced 1000 ms AND skipped entirely while a pending draft is
// showing, so storage provably cannot represent what the operator just typed.
//
// Run:  FRONTEND=http://localhost:3005 npm run smoke:meter-scope
//       (FRONTEND defaults to http://localhost:3001 to match other smokes)
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

// ─── Login (matches moveout-detail / bills-list smoke pattern) ──────────

async function login(page) {
  await page.goto(`${FRONTEND}/login`, { waitUntil: 'domcontentloaded' })
  // Try post-change password first (DB likely already migrated).
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

// ─── Sidebar helpers ────────────────────────────────────────────────────
//
// The global apartment selector lives at the top of the sidebar (button
// → dropdown). Clicking it opens a list; clicking an option calls
// useSelectedApartment.selectApartment(id) which updates the global
// store. Targeted by `data-test` attributes on the Sidebar primitive so
// the smoke survives Tailwind class refactors.

async function selectApartmentViaSidebar(page, apartmentName) {
  const trigger = page.locator('[data-test="apartment-selector-trigger"]')
  await trigger.scrollIntoViewIfNeeded()
  await trigger.click()
  await page.waitForTimeout(200)
  await page.locator(
    '[data-test="apartment-selector-panel"] [data-test="apartment-selector-option"]',
    { hasText: apartmentName },
  ).first().click()
  await page.waitForTimeout(400)
}

// ─── Scope / draft helpers (shared by R1 + R2) ──────────────────────────

/** The billing month the page defaults to — mirrors `currentBillingMonth()`. */
function currentBillingMonth() {
  const now = new Date()
  return `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, '0')}`
}

/** Where `เดือนก่อนหน้า` lands from the default month. */
function previousBillingMonth() {
  const now = new Date()
  const d = new Date(now.getFullYear(), now.getMonth() - 1, 1)
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}`
}

/**
 * Writes a draft slot directly, so a scope can be ARRIVED AT with one already
 * in it. Seeding rather than typing is the point: the operator must not have
 * made this draft during this session, which is what makes it creator-unaware.
 */
async function seedDraft(page, apartmentId, month, roomId, water) {
  await page.evaluate(
    ({ apartmentId, month, roomId, water }) => {
      localStorage.setItem(
        `nana-meter-draft:${apartmentId}:${month}`,
        JSON.stringify({ apartmentId, month, updatedAt: Date.now(), rooms: { [roomId]: { water } } }),
      )
    },
    { apartmentId, month, roomId, water },
  )
}

/** Chrome shows the COMMITTED apartment, so the breadcrumb IS the active scope. */
async function readActiveScopeName(page) {
  return page.evaluate(() => {
    const h1 = Array.from(document.querySelectorAll('h1')).find((el) =>
      el.textContent?.includes('บันทึกมิเตอร์'),
    )
    const crumb = h1?.previousElementSibling?.textContent || ''
    return crumb.split('·')[0].trim()
  })
}

async function clearAllMeterDrafts(page) {
  await page.evaluate(() => {
    Object.keys(localStorage)
      .filter((k) => k.startsWith('nana-meter-draft:'))
      .forEach((k) => localStorage.removeItem(k))
  })
}

async function readDraft(page, apartmentId, month) {
  return page.evaluate(
    ({ apartmentId, month }) =>
      JSON.parse(localStorage.getItem(`nana-meter-draft:${apartmentId}:${month}`) || 'null'),
    { apartmentId, month },
  )
}

/** All water values currently held in a draft slot, as strings. */
function draftWaterValues(draft) {
  return Object.values(draft?.rooms || {}).map((r) => String(r?.water ?? ''))
}

const switchBanner = (page) => page.locator('[role="status"]', { hasText: 'สลับเป็น' })

/** Opens the workspace on an explicit month so the draft key is deterministic. */
async function openWorkspace(page, month) {
  await page.goto(`${FRONTEND}/meter-readings?month=${month}`, { waitUntil: 'networkidle' })
  await page.waitForTimeout(800)
}

/** The visible water inputs, in render order — `nth` is a stable room handle. */
function waterInputs(page) {
  return page.locator('input[aria-label^="มิเตอร์น้ำห้อง"]:visible')
}

// ─── Apartment fetch via API (for stable IDs) ───────────────────────────

async function loginApi() {
  for (const password of [ADMIN_PASS_POST, ADMIN_PASS_FRESH]) {
    const res = await fetch(`${BACKEND}/api/v1/auth/login`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username: ADMIN_USER, password }),
    })
    if (res.ok) {
      const data = await res.json()
      return data.data?.access_token || data.access_token
    }
  }
  throw new Error('API login failed')
}

async function fetchRooms(token, apartmentId) {
  const res = await fetch(`${BACKEND}/api/v1/apartments/${apartmentId}/rooms?limit=100`, {
    headers: { Authorization: `Bearer ${token}` },
  })
  if (!res.ok) throw new Error(`fetchRooms: ${res.status}`)
  const data = await res.json()
  return data.data || []
}

async function fetchApartments(token) {
  const res = await fetch(`${BACKEND}/api/v1/apartments`, {
    headers: { Authorization: `Bearer ${token}` },
  })
  if (!res.ok) throw new Error(`fetchApartments: ${res.status}`)
  const data = await res.json()
  return data.data || []
}

// ─── Cases ──────────────────────────────────────────────────────────────

async function case1_sidebarSwitchWhileDirty(page, apartments) {
  console.log('\n🧪 CASE 1 — sidebar switch while dirty → banner → confirm → draft preserved')
  const [aptA, aptB] = apartments

  // Set sidebar to A by selecting A from the dropdown (defensive — may
  // already be selected; selecting again is idempotent).
  await page.goto(`${FRONTEND}/meter-readings`, { waitUntil: 'networkidle' })
  await selectApartmentViaSidebar(page, aptA.name)
  await page.waitForLoadState('networkidle')
  await page.waitForTimeout(800)

  // Find first water input that has aria-label="ค่าน้ำปัจจุบัน".
  const firstWaterInput = page.locator('input[aria-label^="มิเตอร์น้ำห้อง"]:visible').first()
  await firstWaterInput.waitFor({ state: 'visible', timeout: 8000 })
  // Capture the row number this input belongs to (for later restoration assertion).
  const draftRoomNumber = await firstWaterInput.evaluate((el) => {
    const row = el.closest('[data-room-number]')
    return row?.getAttribute('data-room-number') || ''
  })
  const draftValue = '12345'
  await firstWaterInput.fill(draftValue)
  // Trigger blur so React Hook Form / useMeterReadingBatch picks it up.
  await page.locator('body').click({ position: { x: 10, y: 10 } })
  // Wait > 1000ms debounce so the draft hits localStorage.
  await page.waitForTimeout(1400)

  // Verify draft persisted.
  const draftStored = await page.evaluate(({ aptId }) => {
    const keys = Object.keys(localStorage).filter((k) => k.startsWith('nana-meter-draft:'))
    const apartmentKey = keys.find((k) => k.startsWith(`nana-meter-draft:${aptId}:`))
    if (!apartmentKey) return null
    return JSON.parse(localStorage.getItem(apartmentKey) || 'null')
  }, { aptId: aptA.id })
  check('C1.1 draft written to localStorage (owner key = apt A)', !!draftStored)
  check('C1.2 draft contains the typed water value', !!draftStored && Object.values(draftStored?.rooms || {}).some((r) => r?.water === Number(draftValue) || r?.water === draftValue))

  // Switch sidebar to B → expect ApartmentSwitchBanner.
  await selectApartmentViaSidebar(page, aptB.name)
  await page.waitForTimeout(400)
  const banner = page.locator('[role="status"]', { hasText: 'สลับเป็น' })
  const bannerVisible = await banner.isVisible().catch(() => false)
  check('C1.3 banner appears when sidebar changes while dirty', bannerVisible)

  if (bannerVisible) {
    // Click confirm button "สลับเป็น {aptB.name}"
    await banner.locator('button', { hasText: `สลับเป็น ${aptB.name}` }).click()
    await page.waitForLoadState('networkidle')
    await page.waitForTimeout(800)
    // Assert apartment changed (the form should now show B's rooms — a
    // distinct room number set is hard to assert without B-specific fixtures,
    // so we check that the banner is gone + at least one input exists).
    const bannerGone = !(await banner.isVisible().catch(() => false))
    check('C1.4 banner clears after confirm', bannerGone)

    // Switch back to A.
    await selectApartmentViaSidebar(page, aptA.name)
    await page.waitForLoadState('networkidle')
    await page.waitForTimeout(800)

    // Now check that the previous draft for A is still in localStorage
    // (per page-layout.md rule: drafts keyed by apartment+month are not
    // destroyed by a confirm-switch).
    const draftAfterReturn = await page.evaluate(({ aptId }) => {
      const keys = Object.keys(localStorage).filter((k) => k.startsWith(`nana-meter-draft:${aptId}:`))
      return keys.length > 0 ? JSON.parse(localStorage.getItem(keys[0]) || 'null') : null
    }, { aptId: aptA.id })
    check('C1.5 apartment A draft still in localStorage after round-trip', !!draftAfterReturn)

    // Wait for inputs to render then assert the row we typed in still
    // shows the draft value (the form re-hydrates from localStorage).
    // Draft restoration is user-gated (not auto): the DraftBanner surfaces
    // "พบร่างที่ยังไม่ได้บันทึก" with a "กู้คืน" button. We assert the banner
    // shows + click กู้คืน + then assert the input rehydrated with the
    // typed value. This verifies the contract end-to-end.
    if (draftRoomNumber) {
      const draftBanner = page.locator('text=พบร่างที่ยังไม่ได้บันทึก').first()
      const bannerShown = await draftBanner.isVisible().catch(() => false)
      check('C1.6 DraftBanner surfaces on return to apt A', bannerShown)
      if (bannerShown) {
        await page.locator('button', { hasText: 'กู้คืน' }).first().click()
        await page.waitForTimeout(500)
        const restoredInput = page.locator(`input[aria-label="มิเตอร์น้ำห้อง ${draftRoomNumber}"]:visible`).first()
        try {
          await restoredInput.waitFor({ state: 'visible', timeout: 4000 })
          const restoredVal = await restoredInput.inputValue()
          check(`C1.7 row ${draftRoomNumber} input rehydrates to ${draftValue}`, restoredVal === draftValue, `got "${restoredVal}"`)
        } catch (e) {
          check(`C1.7 row ${draftRoomNumber} input rehydrates to ${draftValue}`, false, `input not visible: ${e.message}`)
        }
      }
    }
  }

  // Clean up: discard draft so it doesn't pollute other cases.
  await page.evaluate(({ aptId }) => {
    Object.keys(localStorage)
      .filter((k) => k.startsWith(`nana-meter-draft:${aptId}:`))
      .forEach((k) => localStorage.removeItem(k))
  }, { aptId: aptA.id })
}

// ─── R1 — dirty input that storage CANNOT represent ─────────────────────
//
// Reaching the state the audit measured: once a pending draft is showing,
// `useMeterDraft` skips autosave entirely (`if (hasPendingDraft) return`), so
// everything typed afterwards is dirty and is NEVER written to storage. The
// scope decision must still hold the apartment.

async function caseR1_dirtyInputStorageCannotRepresent(page, apartments) {
  console.log('\n🧪 CASE R1 — pending draft ⇒ autosave skipped ⇒ typed input must still hold the scope')
  const [aptA, aptB] = apartments
  const month = currentBillingMonth()

  await openWorkspace(page, month)
  await selectApartmentViaSidebar(page, aptA.name)
  await page.waitForLoadState('networkidle')
  await clearAllMeterDrafts(page)
  await openWorkspace(page, month)

  // (1) Create a pending draft: type, let the debounce write it, then reload so
  // `useMeterDraft` detects the slot on mount → hasPendingDraft = true.
  const seedInput = waterInputs(page).first()
  await seedInput.waitFor({ state: 'visible', timeout: 8000 })
  const seedValue = '11111'
  await seedInput.fill(seedValue)
  await page.waitForTimeout(1400)
  const seeded = await readDraft(page, aptA.id, month)
  check('R1.1 seed draft written to storage', !!seeded && draftWaterValues(seeded).includes(seedValue))

  await openWorkspace(page, month)
  const draftBannerShown = await page
    .locator('text=พบร่างที่ยังไม่ได้บันทึก')
    .first()
    .isVisible()
    .catch(() => false)
  check('R1.2 pending-draft banner showing ⇒ autosave is now SKIPPED', draftBannerShown)

  // (2)(3) Type into a DIFFERENT row and do NOT wait for the debounce.
  const liveInput = waterInputs(page).nth(1)
  await liveInput.waitFor({ state: 'visible', timeout: 8000 })
  const liveValue = '22222'
  await liveInput.fill(liveValue)

  // (4) Request the scope change immediately.
  await selectApartmentViaSidebar(page, aptB.name)
  await page.waitForTimeout(300)

  const bannerVisible = await switchBanner(page).isVisible().catch(() => false)
  check('R1.3 pending-switch banner appears for input storage never saw', bannerVisible)

  const activeScope = await readActiveScopeName(page)
  check('R1.4 active scope did NOT change before confirm', activeScope === aptA.name,
    `chrome shows "${activeScope}"`)

  // The discriminator: storage still holds only the seeded value. A guard reading
  // `loadDraft()` cannot possibly have learned about `liveValue`.
  const duringSwitch = await readDraft(page, aptA.id, month)
  check('R1.5 storage does NOT contain the freshly typed value (guard is not draft-derived)',
    !draftWaterValues(duringSwitch).includes(liveValue),
    `draft waters = [${draftWaterValues(duringSwitch).join(', ')}]`)

  // (5) Cancel ⇒ the typed value survives.
  if (bannerVisible) {
    await switchBanner(page).locator('button', { hasText: `ใช้ ${aptA.name} ต่อ` }).click()
    await page.waitForTimeout(300)
  }
  const afterCancel = await liveInput.inputValue().catch(() => '')
  check('R1.6 cancel keeps the typed value', afterCancel === liveValue, `got "${afterCancel}"`)
  const scopeAfterCancel = await readActiveScopeName(page)
  check('R1.7 cancel keeps the active scope', scopeAfterCancel === aptA.name, `chrome shows "${scopeAfterCancel}"`)

  // CANCEL is sticky: the sidebar still points at B, and re-picking B is not a
  // new request — the operator's decision stands until the selector moves again.
  await selectApartmentViaSidebar(page, aptB.name)
  await page.waitForTimeout(300)
  const bannerResurfaced = await switchBanner(page).isVisible().catch(() => false)
  check('R1.8 cancel is sticky — re-picking the SAME apartment is not a new request', !bannerResurfaced)

  // (6) A genuine new request (selector moves A → B again) ⇒ banner ⇒ confirm switches.
  await selectApartmentViaSidebar(page, aptA.name)
  await page.waitForTimeout(300)
  await selectApartmentViaSidebar(page, aptB.name)
  await page.waitForTimeout(300)
  const bannerAgain = await switchBanner(page).isVisible().catch(() => false)
  check('R1.9 banner returns when the selector genuinely moves again', bannerAgain)
  if (bannerAgain) {
    await switchBanner(page).locator('button', { hasText: `สลับเป็น ${aptB.name}` }).click()
    await page.waitForLoadState('networkidle')
    await page.waitForTimeout(800)
    const scopeAfterConfirm = await readActiveScopeName(page)
    check('R1.10 confirm switches the active scope', scopeAfterConfirm === aptB.name,
      `chrome shows "${scopeAfterConfirm}"`)
  }

  await clearAllMeterDrafts(page)
}

// ─── R2 — no reliance on debounce timing ────────────────────────────────
//
// R2 exists so the fix cannot pass by accidentally being fast enough. Draft
// writes are physically blocked for the whole case, so storage can never
// represent the typed input no matter how long the debounce is given. If the
// banner still appears, the decision read authoritative dirty truth.

async function caseR2_noRelianceOnDebounceTiming(page, apartments) {
  console.log('\n🧪 CASE R2 — draft writes blocked ⇒ the banner must still appear')
  const [aptA, aptB] = apartments
  const month = currentBillingMonth()

  await openWorkspace(page, month)
  await selectApartmentViaSidebar(page, aptA.name)
  await page.waitForLoadState('networkidle')
  await clearAllMeterDrafts(page)
  await openWorkspace(page, month)

  // Block ONLY meter-draft writes. Everything else in localStorage behaves normally.
  await page.evaluate(() => {
    const real = localStorage.setItem.bind(localStorage)
    window.__restoreDraftWrites = () => { localStorage.setItem = real }
    window.__blockedDraftWrites = 0
    localStorage.setItem = (k, v) => {
      if (String(k).startsWith('nana-meter-draft:')) {
        window.__blockedDraftWrites++
        return
      }
      return real(k, v)
    }
  })

  const noDraftBanner = !(await page
    .locator('text=พบร่างที่ยังไม่ได้บันทึก')
    .first()
    .isVisible()
    .catch(() => false))
  check('R2.1 starts with pending draft = false', noDraftBanner)

  const input = waterInputs(page).first()
  await input.waitFor({ state: 'visible', timeout: 8000 })
  const typed = '33333'
  await input.fill(typed)

  // IMMEDIATELY request a scope change — no debounce wait.
  await selectApartmentViaSidebar(page, aptB.name)
  await page.waitForTimeout(300)

  const stored = await readDraft(page, aptA.id, month)
  check('R2.2 storage holds no draft at decision time', stored === null,
    stored ? `found ${JSON.stringify(stored.rooms)}` : '')

  const bannerVisible = await switchBanner(page).isVisible().catch(() => false)
  check('R2.3 banner appears with storage provably empty', bannerVisible)

  const activeScope = await readActiveScopeName(page)
  check('R2.4 active scope did NOT change before confirm', activeScope === aptA.name,
    `chrome shows "${activeScope}"`)

  if (bannerVisible) {
    await switchBanner(page).locator('button', { hasText: `ใช้ ${aptA.name} ต่อ` }).click()
    await page.waitForTimeout(300)
  }

  await page.evaluate(() => window.__restoreDraftWrites?.())
  await clearAllMeterDrafts(page)
}

// ─── R3 — a restored draft still holds the scope ────────────────────────
//
// DELIBERATE BEHAVIOUR CHANGE, locked here on purpose (scope-reset lifecycle
// round, 2026-07-31). The guard used to ask whether a draft was still
// UNACKNOWLEDGED; it now asks whether one EXISTS for the scope. Restoring a
// draft does not clear its slot, so before this round the workspace would swap
// apartment silently while the restored values sat visible in the grid.
//
// Category 2 in page-layout.md is explicit that a pending draft is committed
// state and must not be swapped silently — so this is the doctrine, not a
// regression. The banner's own copy is written for exactly this moment: it
// reassures the operator that the draft is kept.

async function caseR3_restoredDraftStillHoldsTheScope(page, apartments) {
  console.log('\n🧪 CASE R3 — a draft that exists for the scope holds it, even once restored')
  const [aptA, aptB] = apartments
  const month = currentBillingMonth()

  await openWorkspace(page, month)
  await selectApartmentViaSidebar(page, aptA.name)
  await clearAllMeterDrafts(page)
  await openWorkspace(page, month)

  const input = waterInputs(page).first()
  await input.waitFor({ state: 'visible', timeout: 8000 })
  await input.fill('44444')
  await page.waitForTimeout(1400)

  await openWorkspace(page, month)
  const draftBanner = page.locator('text=พบร่างที่ยังไม่ได้บันทึก').first()
  check('R3.1 a pending draft is showing', await draftBanner.isVisible().catch(() => false))

  await page.locator('button', { hasText: 'กู้คืน' }).first().click()
  await page.waitForTimeout(700)
  check('R3.2 restoring does NOT clear the slot — the draft still exists',
    (await readDraft(page, aptA.id, month)) !== null)

  await selectApartmentViaSidebar(page, aptB.name)
  await page.waitForTimeout(400)
  check('R3.3 the switch is held for the operator to answer',
    await switchBanner(page).isVisible().catch(() => false))
  const activeScope = await readActiveScopeName(page)
  check('R3.4 the restored work keeps its scope', activeScope === aptA.name,
    `chrome shows "${activeScope}"`)

  await switchBanner(page)
    .locator('button', { hasText: `ใช้ ${aptA.name} ต่อ` })
    .click()
    .catch(() => {})
  await clearAllMeterDrafts(page)
}

// ─── Focus eviction on an accepted scope change ─────────────────────────
//
// The Focus session is evicted by the ACCEPTED scope transition, and by nothing
// else. Its guards — the once-per-param `?focus=` consume, the exit edge, the
// return origin — are load-bearing and must keep their owner: a remount would
// reset them silently, and the exit handoff NAVIGATES.

const focusIsActive = (page) => page.locator('input[aria-label="มิเตอร์น้ำ"]').first().isVisible().catch(() => false)

async function case5_focusEvictedOnMonthChange(page, apartments, roomId) {
  console.log('\n🧪 CASE 5 — changing the month while a Focus session is running evicts it')
  const [aptA] = apartments
  const month = currentBillingMonth()

  await openWorkspace(page, month)
  await selectApartmentViaSidebar(page, aptA.name)
  await clearAllMeterDrafts(page)
  // The month the eviction lands in already holds a draft, and the operator did
  // NOT make it in this session — see C5.4.
  await seedDraft(page, aptA.id, previousBillingMonth(), roomId, 4242)
  await openWorkspace(page, month)

  await page.locator('button', { hasText: 'จดมิเตอร์เร็ว' }).first().click()
  await page.waitForTimeout(500)
  check('C5.1 Focus session started', await focusIsActive(page))

  await page.locator('[aria-label="เดือนก่อนหน้า"]').first().click()
  await page.waitForLoadState('networkidle')
  await page.waitForTimeout(900)
  check('C5.2 Focus evicted by the month change', !(await focusIsActive(page)))
  check('C5.3 the workspace is back on the grid for the new month',
    await page.locator('button', { hasText: 'จดมิเตอร์เร็ว' }).first().isVisible().catch(() => false))

  // ─── C5.4 — an eviction is not a handoff ───
  //
  // The scope the operator is thrown into is one they have not been working in,
  // so its draft is creator-UNAWARE and must meet the recovery banner like any
  // other cold arrival. That is what the scope effect always MEANT ("a new scope
  // must go through the normal banner gate") and could not enforce: the exit
  // edge fires a render AFTER that effect clears the handoff flags, so the
  // auto-restore flag came straight back up and the new month hydrated silently
  // — suppressing a recovery prompt for work the operator had never seen.
  //
  // A boolean could not hold the difference; `resolveFocusExit` can, because the
  // eviction declares itself at the call site instead of being inferred from an
  // `isActive` transition that looks identical to a finished sweep.
  const bannerShown = await page
    .locator('text=พบร่างที่ยังไม่ได้บันทึก')
    .first()
    .isVisible()
    .catch(() => false)
  check('C5.4 the evicted-into scope still meets the recovery banner (eviction ≠ handoff)',
    bannerShown, bannerShown ? '' : 'banner suppressed — an eviction was treated as a workflow handoff')

  await clearAllMeterDrafts(page)
}

async function case6_focusParamNotConsumedInTheWrongScope(page, apartments, roomId) {
  console.log('\n🧪 CASE 6 — opening with ?focus= then changing the month does not re-consume it')
  const [aptA] = apartments
  const month = currentBillingMonth()

  await openWorkspace(page, month)
  await selectApartmentViaSidebar(page, aptA.name)
  await clearAllMeterDrafts(page)

  await page.goto(`${FRONTEND}/meter-readings?month=${month}&focus=${roomId}&focusMode=queue`, {
    waitUntil: 'networkidle',
  })
  await page.waitForTimeout(1200)
  check('C6.1 ?focus= entered a Focus session', await focusIsActive(page))
  check('C6.2 the entry params were consumed and stripped from the URL',
    !page.url().includes('focus='), page.url())

  await page.locator('[aria-label="เดือนก่อนหน้า"]').first().click()
  await page.waitForLoadState('networkidle')
  await page.waitForTimeout(1200)
  check('C6.3 Focus evicted, and the target is NOT re-consumed in the new month',
    !(await focusIsActive(page)))

  await clearAllMeterDrafts(page)
}

// ─── The flash — a new scope must never show the old scope's rows ───────
//
// Sampled every animation frame across the switch, so an intermediate render
// would be caught even if it never settled.

async function case7_noStaleScopeFlash(page, apartments) {
  console.log('\n🧪 CASE 7 — a new scope never renders room identity from the old one')
  const [aptA, aptB] = apartments
  const month = currentBillingMonth()

  await openWorkspace(page, month)
  await selectApartmentViaSidebar(page, aptA.name)
  await clearAllMeterDrafts(page)
  await openWorkspace(page, month)

  const renderedRooms = () =>
    page.evaluate(() =>
      Array.from(document.querySelectorAll('[data-room-number]')).map((el) =>
        el.getAttribute('data-room-number'),
      ),
    )
  const roomsOfA = await renderedRooms()

  await page.evaluate(() => {
    window.__samples = []
    window.__sampling = true
    const tick = () => {
      if (!window.__sampling) return
      const h1 = Array.from(document.querySelectorAll('h1')).find((el) =>
        el.textContent?.includes('บันทึกมิเตอร์'),
      )
      const crumb = (h1?.previousElementSibling?.textContent || '').split('·')[0].trim()
      const rooms = Array.from(document.querySelectorAll('[data-room-number]')).map((el) =>
        el.getAttribute('data-room-number'),
      )
      window.__samples.push({ crumb, rooms })
      requestAnimationFrame(tick)
    }
    requestAnimationFrame(tick)
  })

  await selectApartmentViaSidebar(page, aptB.name)
  await page.waitForLoadState('networkidle')
  await page.waitForTimeout(1200)
  const samples = await page.evaluate(() => {
    window.__sampling = false
    return window.__samples
  })
  const roomsOfB = await renderedRooms()

  const onlyA = roomsOfA.filter((r) => !roomsOfB.includes(r))
  const contaminated = samples.filter(
    (s) => s.crumb === aptB.name && s.rooms.some((r) => onlyA.includes(r)),
  )
  check('C7.1 the switch was actually observed', samples.length > 0 && roomsOfA.length > 0,
    `${samples.length} frame(s), A=${roomsOfA.length} rooms, B=${roomsOfB.length} rooms`)
  check('C7.2 no frame pairs the new apartment with the old apartment’s rooms',
    contaminated.length === 0,
    contaminated.length ? `${contaminated.length} contaminated frame(s)` : `${onlyA.length} discriminating room(s)`)

  await clearAllMeterDrafts(page)
}

async function case3_noDualSelector(page) {
  console.log('\n🧪 CASE 3 — no page-local apartment selector (dual selector eliminated)')
  await page.goto(`${FRONTEND}/meter-readings`, { waitUntil: 'networkidle' })
  await page.waitForTimeout(800)
  // The page-local apartment Select lives inside the BatchHeader and
  // carries aria-label="เลือกอาคาร" + id="apartment-select". After
  // Phase C neither should exist anywhere in the page body. The
  // sidebar selector has no such id/aria-label.
  const localSelectExists = await page.locator('main #apartment-select, main [aria-label="เลือกอาคาร"]').count() > 0
  // Also accept any <select>/Select with explicit apartment-selector semantics
  // inside <main>. We deliberately do NOT match `[aria-label*="อาคาร"]` because
  // section eyebrows use that ("อาคาร A") and would false-positive.
  const anyLocalApartmentControl = await page.evaluate(() => {
    const main = document.querySelector('main') || document.body
    const sels = main.querySelectorAll('select#apartment-select, [role="combobox"][aria-label="เลือกอาคาร"], [aria-label="เลือกอาคาร"]')
    return Array.from(sels).filter((el) => !el.closest('aside')).length
  })
  check('C3.1 no #apartment-select / [aria-label="เลือกอาคาร"] in <main>', !localSelectExists)
  check('C3.2 no apartment combobox/select inside <main>', anyLocalApartmentControl === 0,
    anyLocalApartmentControl > 0 ? `found ${anyLocalApartmentControl} element(s)` : '')
}

// ─── Runner ─────────────────────────────────────────────────────────────

;(async () => {
  console.log(`🔗 FRONTEND = ${FRONTEND}`)
  const token = await loginApi()
  const apartments = await fetchApartments(token)
  if (apartments.length < 2) {
    console.error(`❌ need >= 2 apartments to test apartment switching; got ${apartments.length}`)
    process.exit(1)
  }
  console.log(`📋 using apartments: A=${apartments[0].name}, B=${apartments[1].name}`)
  const rooms = await fetchRooms(token, apartments[0].id)
  if (rooms.length === 0) {
    console.error('❌ need >= 1 room in apartment A for the ?focus= case')
    process.exit(1)
  }
  const focusRoomId = rooms[0].id

  const browser = await chromium.launch({
    headless: process.env.SMOKE_HEADLESS === '1',
    slowMo: process.env.SMOKE_HEADLESS === '1' ? 0 : 80,
  })
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  const page = await ctx.newPage()
  await login(page)

  await case1_sidebarSwitchWhileDirty(page, apartments)
  await caseR1_dirtyInputStorageCannotRepresent(page, apartments)
  await caseR2_noRelianceOnDebounceTiming(page, apartments)
  await caseR3_restoredDraftStillHoldsTheScope(page, apartments)
  await case5_focusEvictedOnMonthChange(page, apartments, focusRoomId)
  await case6_focusParamNotConsumedInTheWrongScope(page, apartments, focusRoomId)
  await case7_noStaleScopeFlash(page, apartments)
  await case3_noDualSelector(page)

  await browser.close()

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
