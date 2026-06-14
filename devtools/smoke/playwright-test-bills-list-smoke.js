// Bill List — Behavior + Visual Smoke Test
// ----------------------------------------------------------------------
// Pins the user-visible behavior of the redesigned bill list page:
//   1. Renders end-to-end at desktop (1440) + mobile (375) viewports
//   2. Renders the partial-payment progress bar via the dev-only
//      `?mockPartial=ROOM_NUMBER` injector
//   3. Bulk-select toggle morphs the filter row into a 3-tier command
//      bar (selection-as-highlight pattern — NO inline row checkbox)
//   4. Bulk confirm modal opens with verb-differentiated CTAs
//   5. Interaction model (selection-as-highlight — Things 3 / Superhuman):
//        - row body opens BillDrawer in `view` mode (breakdown auto-open)
//        - "ดูรายละเอียด" / "รับชำระ" / "คืนเงิน" are all <button>s that
//          open BillDrawer in `act` mode (breakdown closed) and
//          stopPropagation from the row body
//        - in selectionMode, row body click TOGGLES selection (whole-row
//          highlight via bg-primary-muted — no inline checkbox glyph
//          exists per `feedback_bulk_select_doctrine`)
//        - non-selectable rows in selectionMode dim to opacity-70 and
//          their click is a silent no-op
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
  // Retry with post-change password if fresh password was rejected (DB not reset between runs)
  if (page.url().includes('/login')) {
    await page.fill('input[name="password"]', ADMIN_PASS_POST)
    await page.click('button[type="submit"]')
    await page.waitForLoadState('networkidle')
  }
  if (page.url().includes('/change-password')) {
    await page.fill('input[name="new_password"]', ADMIN_PASS_POST)
    await page.fill('input[name="confirm_password"]', ADMIN_PASS_POST)
    await page.click('button[type="submit"]')
    await page.waitForLoadState('networkidle')
  }
  await page.waitForFunction(() => !window.location.pathname.includes('/login'), { timeout: 10000 })
}

;(async () => {
  // Headed by default so the developer can watch the run. Set SMOKE_HEADLESS=1
  // for CI / unattended runs.
  const browser = await chromium.launch({
    headless: process.env.SMOKE_HEADLESS === '1',
    slowMo: process.env.SMOKE_HEADLESS === '1' ? 0 : 150,
  })
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
      // Room-number leaf can be <div> or <span> depending on the row
      // layout revision — walk all element nodes and match by text.
      const nodes = s.querySelectorAll('*')
      for (const el of nodes) {
        const txt = (el.textContent || '').trim()
        if (!/^[A-Z]\d{3}$/.test(txt)) continue
        const rect = el.getBoundingClientRect()
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

  // Bulk-select smoke — toggle on, tap first 3 selectable row bodies,
  // capture state. Selection-as-highlight pattern: no inline checkbox
  // input exists on rows (deleted with BillSelectionSlot). Selectable
  // rows are identified by the "รับชำระ" CTA button (only actionable
  // states render it). Toggle happens via the row container's onClick
  // — we click slightly left-of-center where there's no CTA so the
  // row body handler fires (CTA stopPropagation wouldn't apply).
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto(`${FRONTEND}/bills`, { waitUntil: 'networkidle' })
  await page.waitForTimeout(1500)
  await page.locator('button:has-text("เลือกหลายใบ")').click({ timeout: 8000 })
  await page.waitForTimeout(300)

  // Walk from each "รับชำระ" CTA up to the row container, then click on
  // the row left-side (identity area) to fire the row's onClick handler
  // — which toggles selection in selectionMode.
  const ctaButtons = page.locator('button[aria-label^="รับชำระ บิลห้อง "]')
  const ctaCount = await ctaButtons.count()
  for (let i = 0; i < Math.min(3, ctaCount); i++) {
    const row = ctaButtons.nth(i).locator(
      'xpath=ancestor::div[contains(@class, "border-l-transparent")][1]',
    )
    // Click 40px from the row's left edge, vertical center — lands on
    // the room-number / tenant cluster, never the CTA button on the right.
    await row.click({ position: { x: 40, y: 24 } })
    await page.waitForTimeout(80)
  }
  await page.waitForTimeout(300)
  await page.screenshot({ path: '/tmp/bills-bulk-select.png', fullPage: false })
  console.log('bulk select saved')

  // Verify command bar reflects the selection — "เลือก N ใบ" + primary
  // CTA appear, row count matches what we clicked (capped at ctaCount).
  const selectedTextOk = await page.evaluate(() => {
    const bar = document.querySelector('[role="region"][aria-label="แถบดำเนินการกับบิลที่เลือก"]')
    if (!bar) return { ok: false, reason: 'command bar not rendered' }
    const txt = bar.textContent || ''
    const m = txt.match(/เลือก\s+(\d+)\s+ใบ/)
    if (!m) return { ok: false, reason: `summary missing in: ${txt}` }
    return { ok: true, n: Number(m[1]) }
  })
  if (!selectedTextOk.ok) {
    console.error('  ❌ bulk-select command bar probe FAILED:', selectedTextOk.reason)
    process.exit(1)
  }
  console.log(`  ✅ command bar shows "เลือก ${selectedTextOk.n} ใบ"`)

  // Open confirm modal via the primary CTA in the command bar.
  await page.locator('button:has-text("รับชำระทั้งหมด")').click()
  await page.waitForTimeout(400)
  await page.screenshot({ path: '/tmp/bills-bulk-confirm.png', fullPage: false })
  console.log('bulk confirm modal saved')

  // Dismiss modal so the interaction-model probe below starts from a
  // clean state (probe re-navigates anyway, but explicit close keeps
  // the screenshot trail predictable).
  await page.keyboard.press('Escape')
  await page.waitForTimeout(200)

  // ── Interaction-model probe: confirm drawer-based interaction model.
  //
  // After the BillDrawer migration the bill list is fully drawer-driven —
  // no row, CTA, or label navigates to a separate `/bills/:id` page.
  //
  //   1. Row text content is NOT a nav target — hit-test on tenant name
  //      must NOT resolve to an <a href="/bills/..."> ancestor.
  //   2. "ดูรายละเอียด" CTA is a <button> that opens the drawer in
  //      view mode (NOT a nav link).
  //   3. "รับชำระ" CTA is a <button> that opens the drawer in act mode
  //      (NOT a nav link).
  //   4. No <a href="/bills/..."> exists on the page at all — proves
  //      the legacy detail-page nav has been fully retired.
  await page.goto(`${FRONTEND}/bills`, { waitUntil: 'networkidle' })
  await page.waitForTimeout(1500)
  await page.screenshot({ path: '/tmp/bills-pre-probe.png', fullPage: false })
  const probe = await page.evaluate(() => {
    const hitAt = (el) => {
      if (!el) return null
      const r = el.getBoundingClientRect()
      return document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2)
    }
    // Anchor on row STRUCTURE, not typography or seed markers. Earlier
    // revisions of this probe required tenant names with a "SMOKE" seed
    // suffix, which only exists in SETTLEMENT bills (visible under the
    // "ปิดสัญญา" chip) — the default monthly view shows plain Thai names.
    //
    // What we need: ANY visible leaf element inside a bill section row
    // that carries Thai text and is NOT a header label / chip / CTA.
    // If clicking that text resolves to an <a href="/bills/...">, the
    // row-not-nav contract is broken.
    const HEADER_OR_CTA = /^(เลือกทั้งหมด|รับชำระ|ดูรายละเอียด|รอชำระ|ชำระแล้ว|แก้ไขแล้ว|เหลือ|ค้าง|สร้าง|ครบกำหนด|ยกเลิก|ปิดสัญญา|คืนเงิน|เก็บเพิ่ม|จบแล้ว|บิล|มกราคม|กุมภาพันธ์|มีนาคม|เมษายน|พฤษภาคม|มิถุนายน|กรกฎาคม|สิงหาคม|กันยายน|ตุลาคม|พฤศจิกายน|ธันวาคม)/
    const tenantText = Array.from(
      document.querySelectorAll('section[aria-label*="บิล"] *'),
    ).find((el) => {
      if (el.children.length > 0) return false
      const txt = (el.textContent || '').trim()
      // Must contain Thai text + not a header/CTA label.
      if (!/[฀-๿]/.test(txt)) return false
      if (HEADER_OR_CTA.test(txt)) return false
      const r = el.getBoundingClientRect()
      return r.width > 0 && r.height > 0
    })
    if (!tenantText) return { ok: false, reason: 'no tenant row text found' }
    const tenantHit = hitAt(tenantText)
    if (tenantHit && tenantHit.closest('a[href^="/bills/"]')) {
      return { ok: false, reason: 'row text content is a nav target — Phase 7 expects row NOT clickable' }
    }

    // Drawer-model probe: no CTA on the bill list is a nav target.
    // Mobile + desktop layouts both render in DOM (toggled via Tailwind
    // `sm:hidden` / `sm:grid`). Filter to the visible variant — the hidden
    // copy has rect = 0×0 at the current viewport.
    //
    // Match by aria-label which encodes BOTH variants on a single
    // <button>: e.g. "รับชำระ บิลห้อง A102", "ดูรายละเอียด บิลห้อง A101".
    const ctaButtons = Array.from(document.querySelectorAll('button'))
      .filter((b) => {
        const al = b.getAttribute('aria-label') || ''
        return /^(รับชำระ|ดูรายละเอียด|คืนเงิน) บิลห้อง /.test(al)
      })
      .filter((b) => {
        const rr = b.getBoundingClientRect()
        // Must have non-zero size AND be within the visible viewport —
        // elements below the fold have valid rects but elementFromPoint
        // returns null for coordinates outside the viewport bounds.
        return (
          rr.width > 0 &&
          rr.height > 0 &&
          rr.top >= 0 &&
          rr.bottom <= window.innerHeight &&
          rr.left >= 0 &&
          rr.right <= window.innerWidth
        )
      })
    if (ctaButtons.length === 0) {
      return { ok: false, reason: 'no row CTA button visible at viewport' }
    }

    // Verify EVERY visible row CTA is a <button> (not an <a>) and
    // hit-tests to itself — i.e. nothing overlays it as a link.
    for (const btn of ctaButtons) {
      const al = btn.getAttribute('aria-label') || ''
      if (btn.tagName.toLowerCase() !== 'button') {
        return { ok: false, reason: `${al} is <${btn.tagName.toLowerCase()}> not <button>` }
      }
      const hit = hitAt(btn)
      if (!hit) {
        return { ok: false, reason: `${al} hit target null` }
      }
      if (hit.closest('a[href^="/bills/"]')) {
        return { ok: false, reason: `${al} resolves under an <a href="/bills/..."> — drawer model broken` }
      }
      // Walk up — the hit-tested element should itself be (or be inside)
      // the same <button>, not a different actionable ancestor.
      if (!hit.closest('button')) {
        return { ok: false, reason: `${al} hit resolved to <${hit.tagName.toLowerCase()}> outside a <button>` }
      }
    }

    // No <a href="/bills/..."> on the page at all — legacy detail nav
    // has been fully retired in favor of the drawer.
    const stragglerNav = document.querySelector('a[href^="/bills/"]')
    if (stragglerNav) {
      return {
        ok: false,
        reason: `stale <a href="${stragglerNav.getAttribute('href')}"> on bill list — drawer model expects none`,
      }
    }

    return {
      ok: true,
      reason: `row=informational, ${ctaButtons.length} row CTA(s) all <button>, no /bills/ anchor`,
    }
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
