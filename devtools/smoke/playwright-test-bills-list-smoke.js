// Bill List — Behavior + Visual Smoke Test
// ----------------------------------------------------------------------
// Pins the user-visible behavior of the redesigned bill list page:
//   1. Renders end-to-end at desktop (1440) + mobile (375) viewports
//   2. Renders the partial-payment progress bar via the dev-only
//      `?mockPartial=ROOM_NUMBER` injector
//   3. Bulk-select toggle + checkbox column + dark action bar appear
//      in the right order
//   4. Bulk confirm modal opens with verb-differentiated CTAs
//   5. Interaction model (Phase 7 — explicit actions, no row click):
//        - row text content is NOT a nav target
//        - "ดูรายละเอียด" link IS a nav target (<a> to /bills/:id)
//        - "รับชำระ" is a button (action target, not navigation)
//
// Screenshots are saved to /tmp/bills-*.png for eyeballed visual review.
// Failure of the interaction-model probe `process.exit(1)`s.
const { chromium } = require('playwright')

const FRONTEND = 'http://localhost:3001'
const ADMIN_USER = 'admin'
const ADMIN_PASS_FRESH = 'admin123'
const ADMIN_PASS_POST = 'admin1234'

// Cloned exactly from playwright-test-moveout-detail-smoke.js — proven flow.
async function login(page) {
  await page.goto(`${FRONTEND}/login`)
  await page.fill('input[name="username"]', ADMIN_USER)
  await page.fill('input[name="password"]', ADMIN_PASS_FRESH)
  await page.click('button[type="submit"]')
  await page.waitForLoadState('networkidle')
  if (page.url().includes('/change-password')) {
    await page.fill('input[name="new_password"]', ADMIN_PASS_POST)
    await page.fill('input[name="confirm_password"]', ADMIN_PASS_POST)
    await page.click('button[type="submit"]')
    await page.waitForLoadState('networkidle')
  }
  await page.waitForFunction(() => !window.location.pathname.includes('/login'), { timeout: 10000 })
}

;(async () => {
  const browser = await chromium.launch()
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  const page = await ctx.newPage()
  await login(page)
  await page.goto(`${FRONTEND}/bills`, { waitUntil: 'networkidle' })
  await page.waitForTimeout(1500)
  await page.screenshot({ path: '/tmp/bills-desktop.png', fullPage: true })
  console.log('desktop saved')

  await page.setViewportSize({ width: 375, height: 812 })
  await page.waitForTimeout(500)
  await page.screenshot({ path: '/tmp/bills-mobile.png', fullPage: true })
  console.log('mobile saved')

  // Need to identify a FINALIZED bill in the current month to mock partial.
  // Read room numbers from the page DOM after switching back to desktop.
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto(`${FRONTEND}/bills`, { waitUntil: 'networkidle' })
  await page.waitForTimeout(1000)
  const candidateRoom = await page.evaluate(() => {
    // Walk every <div> under each section and pick the first one whose
    // text content matches the room-number pattern AND is visible at
    // the current viewport. Robust to typography changes (room number
    // weight has migrated font-semibold → font-medium between revisions).
    const sections = document.querySelectorAll('section[aria-label*="บิล"]')
    for (const s of sections) {
      const cells = s.querySelectorAll('div')
      for (const d of cells) {
        const txt = (d.textContent || '').trim()
        if (!/^[A-Z]\d{3}$/.test(txt)) continue
        const rect = d.getBoundingClientRect()
        if (rect.width > 0 && rect.height > 0) return txt
      }
    }
    return null
  })
  console.log('candidate room for mock:', candidateRoom)
  if (candidateRoom) {
    await page.goto(
      `${FRONTEND}/bills?mockPartial=${candidateRoom}`,
      { waitUntil: 'networkidle' },
    )
    await page.waitForTimeout(1000)
    await page.screenshot({ path: '/tmp/bills-partial.png', fullPage: true })
    console.log('partial mock saved')

    await page.setViewportSize({ width: 375, height: 812 })
    await page.waitForTimeout(500)
    await page.screenshot({ path: '/tmp/bills-partial-mobile.png', fullPage: true })
    console.log('partial mobile saved')
  }

  // Bulk-select smoke — toggle on, select first 3 rows, capture state.
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto(`${FRONTEND}/bills`, { waitUntil: 'networkidle' })
  await page.waitForTimeout(1500)
  // Click the "เลือกหลายใบ" toggle
  await page.locator('button:has-text("เลือกหลายใบ")').click({ timeout: 8000 })
  await page.waitForTimeout(300)
  // Click first 3 row checkboxes. The input is sr-only so we trigger
  // the click directly on the underlying element (it owns the change
  // handler via React's onChange).
  const rowCount = await page.locator('input[aria-label^="เลือกบิลห้อง"]').count()
  for (let i = 0; i < Math.min(3, rowCount); i++) {
    await page
      .locator('input[aria-label^="เลือกบิลห้อง"]')
      .nth(i)
      .evaluate((el) => el.click())
  }
  await page.waitForTimeout(300)
  await page.screenshot({ path: '/tmp/bills-bulk-select.png', fullPage: false })
  console.log('bulk select saved')

  // Open confirm modal
  await page.locator('button:has-text("รับชำระทั้งหมด")').click()
  await page.waitForTimeout(400)
  await page.screenshot({ path: '/tmp/bills-bulk-confirm.png', fullPage: false })
  console.log('bulk confirm modal saved')

  // ── Interaction-model probe: confirm Phase 7 explicit-action model
  // is wired up correctly (Phase 6's overlay-Link pattern is gone).
  //
  //   1. Row text content is NOT a nav target — hit-test on tenant name
  //      must NOT resolve to an <a href="/bills/..."> ancestor.
  //   2. "ดูรายละเอียด" link IS a nav target — hit-test on that exact
  //      link must resolve to <a href="/bills/...">.
  //   3. "รับชำระ" button is its own action target (not a nav link).
  await page.goto(`${FRONTEND}/bills`, { waitUntil: 'networkidle' })
  await page.waitForTimeout(1500)
  await page.screenshot({ path: '/tmp/bills-pre-probe.png', fullPage: false })
  const probe = await page.evaluate(() => {
    const hitAt = (el) => {
      if (!el) return null
      const r = el.getBoundingClientRect()
      return document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2)
    }
    const tenantText = Array.from(document.querySelectorAll('div.truncate.text-sm.font-medium'))
      .find((el) => el.textContent && el.textContent.includes('SMOKE'))
    if (!tenantText) return { ok: false, reason: 'no tenant row text found' }
    const tenantHit = hitAt(tenantText)
    if (tenantHit && tenantHit.closest('a[href^="/bills/"]')) {
      return { ok: false, reason: 'row text content is a nav target — Phase 7 expects row NOT clickable' }
    }

    // Mobile + desktop layouts both render in DOM (toggled via Tailwind
    // `sm:hidden` / `sm:grid`). Filter to the visible one — the hidden
    // mobile copy has rect = 0×0 at desktop viewport.
    //
    // Match by aria-label so we catch BOTH variants: the compact
    // icon-only secondary on FINALIZED rows (no text) and the full
    // "ดูรายละเอียด" text link on terminal rows.
    const detailLink = Array.from(document.querySelectorAll('a[href^="/bills/"]'))
      .filter((a) => (a.getAttribute('aria-label') || '').includes('ดูรายละเอียด'))
      .find((a) => {
        const rr = a.getBoundingClientRect()
        return rr.width > 0 && rr.height > 0
      })
    if (!detailLink) return { ok: false, reason: 'ดูรายละเอียด link not visible at viewport' }
    const detailHit = hitAt(detailLink)
    if (!detailHit) {
      return { ok: false, reason: 'ดูรายละเอียด hit target is null' }
    }
    if (!detailHit.closest('a[href^="/bills/"]')) {
      return {
        ok: false,
        reason: `ดูรายละเอียด click resolved to <${detailHit.tagName.toLowerCase()}>; parent has no /bills/ link`,
      }
    }

    const recordBtn = Array.from(document.querySelectorAll('button'))
      .filter((b) => (b.textContent || '').trim() === 'รับชำระ')
      .find((b) => {
        const rr = b.getBoundingClientRect()
        return rr.width > 0 && rr.height > 0
      })
    if (!recordBtn) return { ok: false, reason: 'รับชำระ button not visible at viewport' }
    const btnHit = hitAt(recordBtn)
    if (!btnHit || btnHit.tagName.toLowerCase() !== 'button') {
      return { ok: false, reason: `รับชำระ click target is <${btnHit?.tagName.toLowerCase() ?? 'null'}>` }
    }

    return { ok: true, reason: 'row=informational, ดูรายละเอียด=link, รับชำระ=button' }
  })
  if (!probe.ok) {
    console.error('  ❌ interaction-model probe FAILED:', probe.reason)
    process.exit(1)
  }
  console.log('  ✅ interaction-model probe:', probe.reason)

  await browser.close()
  console.log('\n━━━ Bill List smoke OK ━━━')
})().catch((e) => {
  console.error(e.stack || e.message)
  process.exit(1)
})
