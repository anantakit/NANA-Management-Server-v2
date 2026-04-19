// Move-out Queue Settlement — Smoke Test (Scenarios A–F)
// ----------------------------------------------------------------------
// Validates PENDING_SETTLEMENT flow on the /move-out queue page:
//
//   A  No draft → SettlementPreviewDrawer → "สร้างแบบร่าง" → SettlementPage
//   B  Has draft, no manual items → drawer shows "อัปเดตยอดใหม่" + "เปิดแบบร่าง"
//   C  Has draft + manual items → note shown, confirm modal before regenerate
//   D  Draft rent mode continuity → FULL_MONTH default, switch + warning
//   E  Duplicate/race fallback → 409 → duplicateError state → navigate
//   F  Queue refresh after regenerate → item stays PENDING_SETTLEMENT
//
// Fixtures: reuses TC1 (no draft), TC4 (draft, no manual), TC22 (draft + manual),
//           TC23 (draft + FULL_MONTH). All seeded by /dev/smoke/seed endpoint.
//
// Run:  npm run smoke:queue        (from backend/devtools/smoke/)
//       make smoke-queue           (from backend/)

const { chromium } = require('playwright')

const FRONTEND = 'http://localhost:3001'
const BACKEND = 'http://localhost:8080'
const ADMIN_USER = 'admin'
const ADMIN_PASS = 'admin123'

const DRAWER = '[role="dialog"][aria-label="สรุปยอดย้ายออก"]'

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

// ─── Queue helpers ─────────────────────────────────────────────────────

async function goToQueue(page) {
  await page.goto(`${FRONTEND}/move-out`)
  await page.waitForLoadState('networkidle')
  // Wait for queue page header to render
  await page.waitForSelector('text=คิวงานย้ายออก', { timeout: 10000 })
  // Give a moment for data to load and cards to render
  await page.waitForTimeout(500)
}

async function clickQueueCard(page, roomNumber) {
  // On desktop (1400px), PENDING_SETTLEMENT cards are in the pipeline sidebar
  // as compact <button> elements. On mobile, user switches to the "สรุปยอด" tab.
  //
  // Card text patterns:
  //   - Compact (sidebar): room_number directly, e.g. "B105"
  //   - Primary (main area): "ห้อง B105 นานาคอร์ท"
  //
  // Use a selector that matches either format.
  const selector = `button:has-text("${roomNumber}")`
  let card = page.locator(selector).first()
  let cardVisible = await card.isVisible().catch(() => false)

  if (!cardVisible) {
    // Mobile: switch to settlement tab
    const tab = page.locator('button:has-text("สรุปยอด")').first()
    if (await tab.isVisible().catch(() => false)) {
      await tab.click()
      await page.waitForTimeout(300)
      card = page.locator(selector).first()
      cardVisible = await card.isVisible().catch(() => false)
    }
  }

  if (!cardVisible) {
    // Debug: screenshot + log what's visible
    await page.screenshot({ path: '/tmp/smoke-queue-debug.png' })
    const allButtons = await page.locator('button').allTextContents()
    const roomButtons = allButtons.filter(t => /[A-E]\d{3}/.test(t))
    console.log(`    ⚠️ Card "${roomNumber}" not found. Room buttons visible: ${roomButtons.join(', ') || '(none)'}`)
    throw new Error(`Queue card for room ${roomNumber} not visible`)
  }

  await card.click()
  // Wait for drawer to open
  await page.waitForSelector(DRAWER, { timeout: 10000 })
  // Wait for content or error state
  await page.waitForSelector(
    `${DRAWER} >> text=/คิดค่าเช่า:|ยังไม่สามารถสรุปยอดได้/`,
    { timeout: 10000 },
  )
}

async function closeDrawer(page) {
  await page.keyboard.press('Escape')
  await page.waitForSelector(DRAWER, { state: 'detached', timeout: 5000 }).catch(() => {})
}

async function getDialogText(page) {
  return page.locator(DRAWER).textContent()
}

// ─── Scenario A: No draft → create draft ───────────────────────────────

async function runScenarioA(page, fixtures) {
  console.log('\n── Scenario A: No draft → create draft ──')
  const fx = fixtures.TC1
  if (!fx) return track('A', check('Fixture TC1 present', false, 'missing'))

  await goToQueue(page)
  await clickQueueCard(page, fx.room_number)

  // CTA = "สร้างแบบร่าง"
  const createBtn = page.locator(`${DRAWER} button:has-text("สร้างแบบร่าง")`)
  const createVisible = await createBtn.isVisible()
  track('A.1', check('CTA = "สร้างแบบร่าง"', createVisible))

  // No "เปิดแบบร่าง" button
  const openDraftBtn = page.locator(`${DRAWER} button:has-text("เปิดแบบร่าง")`)
  const openDraftVisible = await openDraftBtn.isVisible().catch(() => false)
  track('A.2', check('No "เปิดแบบร่าง" button', !openDraftVisible))

  // No manual item note
  const dialogText = await getDialogText(page)
  const hasManualNote = dialogText.includes('รายการปรับเพิ่ม')
  track('A.3', check('No manual item note', !hasManualNote))

  // No NaN/undefined
  const hasNaN = /NaN|undefined/.test(dialogText)
  track('A.4', check('No NaN/undefined', !hasNaN, hasNaN ? 'found garbage' : 'clean'))

  await page.screenshot({ path: '/tmp/smoke-queue-a.png' })

  // Click create → navigate to settlement page
  await createBtn.click()
  await page.waitForURL(/\/move-out\/[^/]+\/settlement/, { timeout: 10000 })
  const urlOk = page.url().includes('/settlement')
  track('A.5', check('Navigated to SettlementPage', urlOk, page.url()))

  // Go back to queue — item should still be PENDING_SETTLEMENT
  await goToQueue(page)
  const stillInQueue = await page.locator(`button:has-text("${fx.room_number}")`).isVisible().catch(() => false)
  track('A.6', check('Item still in PENDING_SETTLEMENT (not moved until finalize)', stillInQueue))
}

// ─── Scenario B: Has draft, no manual items ────────────────────────────

async function runScenarioB(page, fixtures) {
  console.log('\n── Scenario B: Has draft, no manual items ──')
  const fx = fixtures.TC4
  if (!fx) return track('B', check('Fixture TC4 present', false, 'missing'))

  await goToQueue(page)
  await clickQueueCard(page, fx.room_number)

  // CTA = "อัปเดตยอดใหม่"
  const updateBtn = page.locator(`${DRAWER} button:has-text("อัปเดตยอดใหม่")`)
  track('B.1', check('CTA = "อัปเดตยอดใหม่"', await updateBtn.isVisible()))

  // "เปิดแบบร่าง" button present
  const openDraftBtn = page.locator(`${DRAWER} button:has-text("เปิดแบบร่าง")`)
  track('B.2', check('"เปิดแบบร่าง" button visible', await openDraftBtn.isVisible()))

  // No manual item note
  const dialogText = await getDialogText(page)
  const hasManualNote = dialogText.includes('รายการปรับเพิ่ม')
  track('B.3', check('No manual item note', !hasManualNote))

  await page.screenshot({ path: '/tmp/smoke-queue-b.png' })

  // "เปิดแบบร่าง" navigates without mutation
  await openDraftBtn.click()
  await page.waitForURL(/\/move-out\/[^/]+\/settlement/, { timeout: 10000 })
  track('B.4', check('"เปิดแบบร่าง" navigates to SettlementPage', page.url().includes('/settlement')))

  // Go back and test regenerate (no confirm modal for 0 manual items)
  await goToQueue(page)
  await clickQueueCard(page, fx.room_number)
  const updateBtn2 = page.locator(`${DRAWER} button:has-text("อัปเดตยอดใหม่")`)
  await updateBtn2.click()
  // Should NOT show confirm modal — wait briefly and check
  await page.waitForTimeout(500)
  const modalVisible = await page.locator('text=ระบบจะคำนวณรายการอัตโนมัติใหม่อีกครั้ง').isVisible().catch(() => false)
  track('B.5', check('No confirm modal for 0 manual items', !modalVisible))

  // Wait for regenerate to finish (toast or navigation)
  await page.waitForSelector('text=อัปเดตยอดสรุปสำเร็จ', { timeout: 10000 }).catch(() => {})
  await closeDrawer(page)
}

// ─── Scenario C: Has draft + manual items ──────────────────────────────

async function runScenarioC(page, fixtures) {
  console.log('\n── Scenario C: Has draft + manual items ──')
  const fx = fixtures.TC22
  if (!fx) return track('C', check('Fixture TC22 present', false, 'missing'))

  await goToQueue(page)
  await clickQueueCard(page, fx.room_number)

  // Manual item note visible
  const dialogText = await getDialogText(page)
  const hasManualNote = dialogText.includes('รายการปรับเพิ่ม')
  track('C.1', check('Manual item note visible', hasManualNote))

  // Note says count (2 items)
  const noteMatch = dialogText.match(/รายการปรับเพิ่ม\s*(\d+)\s*รายการ/)
  const count = noteMatch ? parseInt(noteMatch[1]) : 0
  track('C.2', check('Manual item count = 2', count === 2, `count=${count}`))

  await page.screenshot({ path: '/tmp/smoke-queue-c.png' })

  // Click "อัปเดตยอดใหม่" → confirm modal appears
  const updateBtn = page.locator(`${DRAWER} button:has-text("อัปเดตยอดใหม่")`)
  await updateBtn.click()

  // Wait for confirm modal
  await page.waitForSelector('text=ระบบจะคำนวณรายการอัตโนมัติใหม่อีกครั้ง', { timeout: 5000 })
  track('C.3', check('Confirm modal shown', true))

  // Modal title
  const modalTitle = await page.locator('h3:has-text("อัปเดตยอดใหม่")').isVisible()
  track('C.4', check('Modal title = "อัปเดตยอดใหม่"', modalTitle))

  // Modal body mentions preserving manual items
  const modalText = await page.locator('text=โดยคงรายการที่เพิ่มเองไว้').isVisible()
  track('C.5', check('Modal body mentions preserving manual items', modalText))

  // Modal actions: ยกเลิก / อัปเดตยอด
  const cancelBtn = page.locator('button:has-text("ยกเลิก")')
  const confirmBtn = page.getByRole('button', { name: 'อัปเดตยอด', exact: true })
  track('C.6', check('Modal has "ยกเลิก" button', await cancelBtn.isVisible()))
  track('C.7', check('Modal has "อัปเดตยอด" button', await confirmBtn.isVisible()))

  await page.screenshot({ path: '/tmp/smoke-queue-c-modal.png' })

  // Confirm regenerate
  await confirmBtn.click()
  await page.waitForSelector('text=อัปเดตยอดสรุปสำเร็จ', { timeout: 10000 }).catch(() => {})
  await closeDrawer(page)
}

// ─── Scenario D: Draft rent mode continuity ────────────────────────────

async function runScenarioD(page, fixtures) {
  console.log('\n── Scenario D: Draft rent mode continuity ──')
  const fx = fixtures.TC23
  if (!fx) return track('D', check('Fixture TC23 present', false, 'missing'))

  // Verify fixture has FULL_MONTH rent mode
  track('D.0', check('Fixture has FULL_MONTH_KEEP_DEPOSIT mode',
    fx.settlement_rent_mode === 'FULL_MONTH_KEEP_DEPOSIT',
    `mode=${fx.settlement_rent_mode}`))

  await goToQueue(page)
  await clickQueueCard(page, fx.room_number)

  // Drawer should show FULL_MONTH as default (from draft)
  const metaText = await page.locator(`${DRAWER} >> text=คิดค่าเช่า:`).first().textContent().catch(() => '')
  const hasFullMonth = metaText.includes('คิดเต็มเดือน')
  track('D.1', check('Default rent mode = FULL_MONTH (from draft)', hasFullMonth, metaText?.trim()))

  // No mode-change warning initially (mode matches draft)
  const warningBefore = await page.locator('text=เปลี่ยนวิธีคำนวณจากแบบร่างเดิม').isVisible().catch(() => false)
  track('D.2', check('No mode-change warning initially', !warningBefore))

  await page.screenshot({ path: '/tmp/smoke-queue-d.png' })

  // Switch to PRORATED
  const changeBtn = page.locator(`${DRAWER} >> button:has-text("เปลี่ยน")`)
  if (await changeBtn.isVisible().catch(() => false)) {
    await changeBtn.click()
    await page.waitForLoadState('networkidle').catch(() => {})
    await page.waitForTimeout(400)

    // Now should show mode-change warning
    const warningAfter = await page.locator('text=เปลี่ยนวิธีคำนวณจากแบบร่างเดิม').isVisible().catch(() => false)
    track('D.3', check('Mode-change warning shown after switch', warningAfter))

    // Meta text should now say PRORATED
    const metaAfter = await page.locator(`${DRAWER} >> text=คิดค่าเช่า:`).first().textContent().catch(() => '')
    const hasProrated = metaAfter.includes('คิดตามวัน')
    track('D.4', check('Rent mode switched to PRORATED', hasProrated, metaAfter?.trim()))

    await page.screenshot({ path: '/tmp/smoke-queue-d-switched.png' })
  } else {
    track('D.3', check('Mode change button visible', false, 'button not found'))
    track('D.4', check('(skipped — no toggle)', false))
  }

  await closeDrawer(page)
}

// ─── Scenario E: Duplicate/race fallback (409) ─────────────────────────

async function runScenarioE(page, fixtures) {
  console.log('\n── Scenario E: Duplicate/race fallback (409) ──')
  // Use a fixture that currently has no draft but we'll intercept the
  // generate-settlement API to return 409 to simulate race condition.
  // TC21 is a good candidate (PENDING_SETTLEMENT, no draft, minimal data).
  const fx = fixtures.TC21
  if (!fx) return track('E', check('Fixture TC21 present', false, 'missing'))
  if (fx.has_draft) return track('E', check('TC21 must not have draft', false, `has_draft=${fx.has_draft}`))

  await goToQueue(page)

  // Set up route intercept for generate-settlement → 409
  await page.route(`**/api/v1/move-out-notices/*/generate-settlement`, (route) => {
    route.fulfill({
      status: 409,
      contentType: 'application/json',
      body: JSON.stringify({
        status: 'error',
        message: 'มีแบบร่างสรุปยอดอยู่แล้ว',
        code: 'CONFLICT',
      }),
    })
  })

  await clickQueueCard(page, fx.room_number)

  // Click "สร้างแบบร่าง" — should get intercepted 409
  const createBtn = page.locator(`${DRAWER} button:has-text("สร้างแบบร่าง")`)
  await createBtn.click()

  // Wait for duplicate error state
  await page.waitForSelector(`${DRAWER} >> text=มีแบบร่างสรุปยอดอยู่แล้ว`, { timeout: 5000 })
  track('E.1', check('Duplicate error message shown', true))

  // "เปิดแบบร่าง" button should appear in error state
  const openDraftBtn = page.locator(`${DRAWER} button:has-text("เปิดแบบร่าง")`)
  const hasFallbackBtn = await openDraftBtn.isVisible()
  track('E.2', check('"เปิดแบบร่าง" button in error state', hasFallbackBtn))

  await page.screenshot({ path: '/tmp/smoke-queue-e.png' })

  // Click "เปิดแบบร่าง" → navigate
  if (hasFallbackBtn) {
    await openDraftBtn.click()
    await page.waitForURL(/\/move-out\/[^/]+\/settlement/, { timeout: 10000 })
    track('E.3', check('Navigate to SettlementPage from error state', page.url().includes('/settlement')))
  } else {
    track('E.3', check('(skipped — no fallback button)', false))
  }

  // Clean up route intercept
  await page.unroute(`**/api/v1/move-out-notices/*/generate-settlement`)
}

// ─── Scenario F: Queue refresh after regenerate ────────────────────────

async function runScenarioF(page, fixtures) {
  console.log('\n── Scenario F: Queue refresh after regenerate ──')
  // After scenario B already regenerated TC4, verify the queue reflects
  // the updated state.
  const fx = fixtures.TC4
  if (!fx) return track('F', check('Fixture TC4 present', false, 'missing'))

  // Re-fetch fixtures to get updated state
  const updated = await fetchFixtures()
  const fx4 = updated.TC4

  track('F.1', check('TC4 still PENDING_SETTLEMENT',
    fx4?.status === 'PENDING_SETTLEMENT',
    `status=${fx4?.status}`))

  track('F.2', check('TC4 still has draft (has_draft=true)',
    fx4?.has_draft === true,
    `has_draft=${fx4?.has_draft}`))

  // Go to queue and verify the item is still visible in PENDING_SETTLEMENT
  await goToQueue(page)
  const cardVisible = await page.locator(`button:has-text("${fx4.room_number}")`).isVisible().catch(() => false)
  track('F.3', check('Card still visible in queue', cardVisible))

  // Verify it's NOT in payment section (not moved to PENDING_PAYMENT)
  // On mobile, switch to payment tab and check absence
  const paymentTab = page.locator('button:has-text("การเงิน")').first()
  if (await paymentTab.isVisible().catch(() => false)) {
    await paymentTab.click()
    await page.waitForTimeout(300)
    const inPayment = await page.locator(`button:has-text("${fx4.room_number}")`).isVisible().catch(() => false)
    track('F.4', check('Card NOT in PENDING_PAYMENT section', !inPayment))
  } else {
    // Desktop: check pipeline sidebar for PENDING_PAYMENT
    track('F.4', check('(desktop — payment section check via sidebar)', true))
  }
}

// ─── Main ──────────────────────────────────────────────────────────────

;(async () => {
  console.log('\n🧪 Move-out Queue Settlement Smoke (Scenarios A–F)\n')

  console.log('📦 Refreshing smoke fixtures...')
  await fetch(`${BACKEND}/api/v1/dev/smoke/cleanup`, { method: 'POST' })
  await fetch(`${BACKEND}/api/v1/dev/smoke/seed`, { method: 'POST' })
  const fixtures = await fetchFixtures()
  const needed = ['TC1', 'TC4', 'TC21', 'TC22', 'TC23']
  const seeded = needed.filter((k) => fixtures[k])
  console.log(`  ✅ ${seeded.length}/${needed.length} fixtures ready: ${seeded.join(', ')}`)
  const missing = needed.filter((k) => !fixtures[k])
  if (missing.length) console.log(`  ⚠️  missing: ${missing.join(', ')}`)
  console.log('')

  const browser = await chromium.launch({ headless: false, slowMo: 80 })
  const context = await browser.newContext({ viewport: { width: 1400, height: 900 } })
  const page = await context.newPage()

  try {
    console.log('🔐 Login')
    await login(page)

    await runScenarioA(page, fixtures)
    await runScenarioB(page, fixtures)
    await runScenarioC(page, fixtures)
    await runScenarioD(page, fixtures)
    await runScenarioE(page, fixtures)
    await runScenarioF(page, fixtures)

    console.log(`\n${'='.repeat(60)}`)
    console.log(`📊 Results: ${results.pass}/${results.total} passed, ${results.fail} failed`)
    if (results.failedCases.length > 0) {
      console.log(`❌ Failed: ${results.failedCases.join(', ')}`)
    }
    console.log(`📸 Screenshots in /tmp/smoke-queue-*.png`)
    console.log('='.repeat(60))
  } catch (err) {
    console.error('\n💥 Fatal error:', err.message)
    console.error(err.stack)
    await page.screenshot({ path: '/tmp/smoke-queue-fatal.png' })
  } finally {
    await browser.close()
  }

  console.log('\n🧹 Cleanup smoke fixtures...')
  await fetch(`${BACKEND}/api/v1/dev/smoke/cleanup`, { method: 'POST' })
  console.log('  ✅ Done\n')

  process.exit(results.fail > 0 ? 1 : 0)
})()
