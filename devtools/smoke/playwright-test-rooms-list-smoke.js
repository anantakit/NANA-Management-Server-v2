// Room List — Header + Filter Polish Smoke Test
// ----------------------------------------------------------------------
// Pins the two follow-up polish behaviors after the BillList-family
// alignment:
//   1. ตั้งค่าอาคาร button is ICON-ONLY on mobile (375px) — no visible
//      label text. aria-label remains so SR users get the name.
//   2. Floor/building section header STAYS visible when an active
//      filter narrows the result to a single group (e.g. picking
//      "ย้ายออก" surfaces a single floor — we still want to know
//      which floor).
//
// Screenshots saved to /tmp/rooms-*.png. Hard probes process.exit(1) on
// failure so this can sit inside `smoke:all` without silent regressions.
const { chromium } = require('playwright')

const FRONTEND = 'http://localhost:3001'
const BACKEND = 'http://localhost:8080'
const ADMIN_USER = 'admin'
const ADMIN_PASS_FRESH = 'admin123'
const ADMIN_PASS_POST = 'admin1234'

// Cloned from playwright-test-bills-list-smoke.js / moveout-detail —
// proven login + change-password handler.
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
  await page.waitForFunction(
    () => !window.location.pathname.includes('/login'),
    { timeout: 10000 },
  )
}

async function loginApi() {
  for (const password of [ADMIN_PASS_POST, ADMIN_PASS_FRESH]) {
    const res = await fetch(`${BACKEND}/api/v1/auth/login`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username: ADMIN_USER, password }),
    })
    const json = await res.json()
    if (json.status === 'success') return json.data.access_token
  }
  throw new Error('API login failed for both fresh + post-change passwords')
}

async function pickApartmentId(token) {
  const res = await fetch(`${BACKEND}/api/v1/apartments`, {
    headers: { Authorization: `Bearer ${token}` },
  })
  if (!res.ok) throw new Error(`GET apartments failed: ${res.status}`)
  const json = await res.json()
  const list = json.data ?? []
  if (list.length === 0) throw new Error('no apartments in seed data')
  // First apartment is fine — both checks are layout-driven, not
  // identity-driven. Pick the one with the most rooms so we have a
  // realistic multi-floor case for the unfiltered desktop render.
  return list[0].id
}

;(async () => {
  const browser = await chromium.launch({
    headless: process.env.SMOKE_HEADLESS === '1',
    slowMo: process.env.SMOKE_HEADLESS === '1' ? 0 : 150,
  })
  const ctx = await browser.newContext({
    viewport: { width: 1280, height: 800 },
  })
  const page = await ctx.newPage()

  try {
    await login(page)
    const token = await loginApi()
    const apartmentId = await pickApartmentId(token)
    console.log('apartmentId:', apartmentId)
    const roomsUrl = `${FRONTEND}/apartments/${apartmentId}/rooms`

    // ── CHECK 1: mobile (375px) icon-only ตั้งค่าอาคาร ────────────────
    await page.setViewportSize({ width: 375, height: 812 })
    await page.goto(roomsUrl, { waitUntil: 'networkidle' })
    await page.waitForTimeout(800)
    await page.screenshot({ path: '/tmp/rooms-mobile-375.png', fullPage: true })
    console.log('mobile saved')

    // Settings button: visible label should be empty on mobile. The
    // <span> wrapping the label uses `hidden sm:inline`, so on mobile
    // it's display:none → the button's textContent for visible nodes
    // is empty. aria-label="ตั้งค่าอาคาร" stays as the accessible name.
    const mobileLabelProbe = await page.evaluate(() => {
      const btn = document.querySelector(
        'button[aria-label="ตั้งค่าอาคาร"]',
      )
      if (!btn) return { ok: false, reason: 'settings button not found' }
      // Use innerText (respects display:none) instead of textContent
      // (which counts hidden text). Visible label on mobile must be
      // empty.
      const visible = (btn.innerText || '').trim()
      if (visible !== '') {
        return {
          ok: false,
          reason: `mobile shows visible label "${visible}" — expected icon-only`,
        }
      }
      // Sanity: button is rendered visible at this viewport.
      const rect = btn.getBoundingClientRect()
      if (rect.width === 0 || rect.height === 0) {
        return { ok: false, reason: 'settings button has 0×0 hit box' }
      }
      return {
        ok: true,
        reason: `icon-only on mobile (${Math.round(rect.width)}×${Math.round(rect.height)})`,
      }
    })
    if (!mobileLabelProbe.ok) {
      console.error('  ❌ CHECK 1 FAILED:', mobileLabelProbe.reason)
      await page.screenshot({ path: '/tmp/rooms-check1-fail.png' })
      process.exit(1)
    }
    console.log('  ✅ CHECK 1:', mobileLabelProbe.reason)

    // ── CHECK 2: desktop filter → section header still visible ───────
    //
    // The fix's actual contract: hasActiveFilter forces section
    // headers EVEN when results collapse to a single group. To
    // exercise that path we need a filter combination that narrows
    // the seed to one floor/building. Search by room prefix (typed
    // into the search input) is the most reliable narrower —
    // independent of how seed data distributes status across buildings.
    await page.setViewportSize({ width: 1280, height: 800 })
    await page.goto(roomsUrl, { waitUntil: 'networkidle' })
    await page.waitForTimeout(800)

    // Pick a search query that targets a single building. Read the
    // first room number from the rendered list and use its prefix.
    const sample = await page.evaluate(() => {
      const link = document.querySelector('a[aria-label^="ห้อง "]')
      const m = link && link.getAttribute('aria-label').match(/^ห้อง (\S+)/)
      return m ? m[1] : null
    })
    if (!sample) {
      console.error('  ❌ CHECK 2 SETUP FAILED: no rooms rendered')
      process.exit(1)
    }
    // Use a 2-char prefix like "A1" or "B2" → narrows to a single
    // floor in the seed (rooms numbered A101..A108, A201..A208 etc).
    const narrowQuery = sample.slice(0, 2)
    console.log('narrowing search to prefix:', narrowQuery)

    const searchInput = page.getByLabel('ค้นหาห้องหรือผู้เช่า')
    await searchInput.fill(narrowQuery)
    await page.waitForTimeout(500)

    await page.screenshot({
      path: '/tmp/rooms-filtered-section.png',
      fullPage: true,
    })
    console.log('filtered saved')

    // Probe section headers + group count. The fix passes if the
    // result collapses to exactly ONE group AND that group's header
    // is rendered. (If it stays multi-group the pre-fix code would
    // also show headers — can't distinguish — so we require single
    // group here to exercise the new contract.)
    const sectionProbe = await page.evaluate(() => {
      const labelRegex = /^(ชั้น \d+|อาคาร [A-Z]+|อื่นๆ)$/
      const labels = Array.from(
        document.querySelectorAll('div.text-sm.font-medium.text-text'),
      )
        .map((el) => (el.textContent || '').trim())
        .filter((txt) => labelRegex.test(txt))
        .filter((_, i, arr) => arr.indexOf(_) === i)
      // Group count = distinct top-level <section aria-label=...>
      // inside the table shell.
      const sections = document.querySelectorAll(
        'section[aria-label]:has(a[aria-label^="ห้อง "])',
      )
      const rows = document.querySelectorAll('a[aria-label^="ห้อง "]')
      return { labels, groupCount: sections.length, rowCount: rows.length }
    })
    console.log('   group count after narrow:', sectionProbe.groupCount)
    console.log('   row count after narrow:', sectionProbe.rowCount)
    console.log('   labels visible:', sectionProbe.labels.join(', ') || '(none)')

    if (sectionProbe.rowCount === 0) {
      console.error('  ❌ CHECK 2 FAILED: narrow query returned no rows')
      process.exit(1)
    }
    if (sectionProbe.groupCount !== 1) {
      console.error(
        '  ❌ CHECK 2 INCONCLUSIVE: expected single group after narrow, got',
        sectionProbe.groupCount,
      )
      console.error('    cannot distinguish new behavior from old')
      process.exit(1)
    }
    if (sectionProbe.labels.length === 0) {
      console.error(
        '  ❌ CHECK 2 FAILED: single group + active filter, but NO section header rendered — fix broken',
      )
      process.exit(1)
    }
    console.log(
      '  ✅ CHECK 2: single-group filtered result still shows section header — labels:',
      sectionProbe.labels.join(', '),
    )

    // ── CHECK 3: row CTA hierarchy is flat ────────────────────────────
    //
    // After the directory-flatten pass, every row CTA must use the
    // same lightweight text+chevron shape. No outlined buttons, no
    // bordered chrome on row CTAs. Probes a fresh unfiltered page so
    // we hit all states (OCCUPIED + MOVING_OUT mix at minimum).
    await page.setViewportSize({ width: 1280, height: 800 })
    await page.goto(roomsUrl, { waitUntil: 'networkidle' })
    await page.waitForTimeout(800)
    const ctaProbe = await page.evaluate(() => {
      // Each row is a Link with aria-label="ห้อง <num>". CTA lives in
      // the last grid cell as a <span> containing the verb + chevron.
      const rows = Array.from(document.querySelectorAll('a[aria-label^="ห้อง "]'))
      if (rows.length === 0) return { ok: false, reason: 'no rows rendered' }
      const labels = []
      for (const row of rows.slice(0, 20)) {
        // Find the visible CTA span — the rightmost child carrying the
        // CTA text. Identify by the chevron icon (svg) sibling pattern.
        const ctaSpan = row.querySelector(
          ':scope > .sm\\:grid > div:last-child > span',
        )
        if (!ctaSpan) {
          // Mobile-only render path falls back to the 2-line block.
          // Skip — desktop probe is sufficient.
          continue
        }
        const txt = (ctaSpan.textContent || '').trim()
        labels.push(txt)
        // Rule 1 — CTA must not have a border (no outlined-button
        // chrome). Computed border-width on all sides must be 0.
        const cs = getComputedStyle(ctaSpan)
        const borderW = [
          cs.borderTopWidth,
          cs.borderRightWidth,
          cs.borderBottomWidth,
          cs.borderLeftWidth,
        ]
        for (const w of borderW) {
          if (w && w !== '0px') {
            return {
              ok: false,
              reason: `CTA "${txt}" has border ${w} — expected flat text+chevron`,
            }
          }
        }
        // Rule 2 — must include a chevron icon.
        const svg = ctaSpan.querySelector('svg')
        if (!svg) {
          return {
            ok: false,
            reason: `CTA "${txt}" missing chevron — flatten broken`,
          }
        }
      }
      // Anti-vacuous-pass floor: if the internal CTA selector drifts
      // (e.g. RoomRow grid wrapper class renamed), `:scope > .sm:grid
      // > div:last-child > span` returns nothing → loop body skips →
      // probe returns ok=true with sampled=0. Require a minimum sample
      // so a broken selector fails loud instead of silent green.
      if (labels.length < 5) {
        return {
          ok: false,
          reason: `only sampled ${labels.length} CTAs — selector likely drifted`,
        }
      }
      return { ok: true, sampled: labels.length, labels: [...new Set(labels)] }
    })
    if (!ctaProbe.ok) {
      console.error('  ❌ CHECK 3 FAILED:', ctaProbe.reason)
      await page.screenshot({ path: '/tmp/rooms-check3-fail.png' })
      process.exit(1)
    }
    console.log(
      '  ✅ CHECK 3: row CTA hierarchy flat — sampled',
      ctaProbe.sampled,
      'rows · verbs:',
      ctaProbe.labels.join(' / '),
    )

    await browser.close()
    console.log('\n━━━ Room List smoke OK ━━━')
  } catch (err) {
    console.error('fatal:', err.stack || err.message)
    await page
      .screenshot({ path: '/tmp/rooms-smoke-fatal.png', fullPage: true })
      .catch(() => {})
    await browser.close()
    process.exit(1)
  }
})()
