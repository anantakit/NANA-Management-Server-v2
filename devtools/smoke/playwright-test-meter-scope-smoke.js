// Meter Reading Batch — Apartment Scope Contract Smoke Test
// ----------------------------------------------------------------------
// Locks the behavior contract for MeterReadingBatchPage scope handling.
//
//   - CASE 1 (dirty-then-sidebar)  sidebar switch while dirty → banner → confirm → draft preserved
//   - CASE 3 (no dual selector)    no page-local apartment selector in <main>
//
// Note: CASE 2 (deep-link from bill-generate) was removed 2026-06-16 when
// /bills/generate and MonthlyPreflightCard were deleted as part of the
// /monthly-bills workspace migration.
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

  const browser = await chromium.launch({
    headless: process.env.SMOKE_HEADLESS === '1',
    slowMo: process.env.SMOKE_HEADLESS === '1' ? 0 : 80,
  })
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  const page = await ctx.newPage()
  await login(page)

  await case1_sidebarSwitchWhileDirty(page, apartments)
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
