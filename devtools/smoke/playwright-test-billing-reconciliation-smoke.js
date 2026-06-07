// Billing Reconciliation — Phase 1A audit smoke
// ----------------------------------------------------------------------
// Disposable Checkpoint-C tool: walks all 4 buildings × current month,
// captures desktop + mobile screenshots, scrapes the 1A summary, then
// hits `/bills/generate` for the same scope to cross-check counts against
// the existing preflight readiness card.
//
// Output:
//   /tmp/reconciliation-<slug>-desktop.png
//   /tmp/reconciliation-<slug>-mobile.png
//   /tmp/reconciliation-generate-<slug>-desktop.png  (preflight side-by-side)
//   stdout: tabular cross-check (1A counts vs preflight counts)
//
// Caps + rules:
//   - Apartment list discovered from sidebar (auto-detect, no hardcoded ids)
//   - Hardcoded billing month: 2026-06 (current cycle per locked dev seed)
//   - No assertions — this is a visual+count surfacing tool for operator
//     review at checkpoint C. Diagnoses count mismatches as objective
//     evidence per `feedback_checkpoint_decision_rules`.
const { chromium } = require('playwright')

const FRONTEND = 'http://localhost:3001'
const ADMIN_USER = 'admin'
const ADMIN_PASS_FRESH = 'admin123'
const ADMIN_PASS_POST = 'admin1234'
const BILLING_MONTH = '2026-06'

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

async function listApartments(page) {
  // Discover apartments from the sidebar selector dropdown so the smoke
  // never drifts when seed apartment names change.
  await page.goto(`${FRONTEND}/apartments`, { waitUntil: 'networkidle' })
  await page.locator('[data-test="apartment-selector-trigger"]').click()
  await page.waitForSelector('[data-test="apartment-selector-panel"]')
  const names = await page
    .locator('[data-test="apartment-selector-option"] span.truncate')
    .allInnerTexts()
  // Close panel
  await page.keyboard.press('Escape')
  return names.map((n) => n.trim()).filter(Boolean)
}

async function selectApartment(page, name) {
  // Land on a stable page first so the sidebar trigger has predictable
  // mount state — discovery sometimes leaves the dropdown in a stale
  // open-then-closed state that breaks the next .click().
  await page.goto(`${FRONTEND}/apartments`, { waitUntil: 'networkidle' })
  await page.waitForTimeout(200)
  const trigger = page.locator('[data-test="apartment-selector-trigger"]')
  await trigger.waitFor({ state: 'visible', timeout: 5000 })
  await trigger.click()
  await page.waitForSelector('[data-test="apartment-selector-panel"]', {
    timeout: 5000,
  })
  await page
    .locator('[data-test="apartment-selector-option"]')
    .filter({ hasText: name })
    .first()
    .click()
  await page.waitForTimeout(400)
}

async function scrapeReconciliation(page) {
  // Counts come from InlineStat row — value-then-label pattern.
  // Use the label text to anchor and read the preceding value span.
  const counts = await page.evaluate(() => {
    const rows = document.querySelectorAll(
      'div.flex.items-baseline.gap-1\\.5',
    )
    const out = {}
    for (const row of rows) {
      const value = row.querySelector('span.font-semibold')?.textContent?.trim()
      const label = row.querySelector('span.text-text-dim')?.textContent?.trim()
      if (value !== undefined && label) {
        // Strip Thai-locale commas to get integer
        const n = Number(value.replace(/[^\d-]/g, ''))
        out[label] = Number.isFinite(n) ? n : value
      }
    }
    return out
  })

  // Named-hint lines (ต้องตรวจสอบ / ต้องตัดสินใจ / ค่ามิเตอร์ผิดปกติ)
  const hints = await page.evaluate(() => {
    const lines = Array.from(
      document.querySelectorAll('p.flex.flex-wrap.items-baseline'),
    )
    return lines
      .map((p) => p.textContent?.trim() || '')
      .filter((t) => t.length > 0)
  })

  // Full body room list — entity_id + bucket label + reason text per row.
  const rows = await page.evaluate(() => {
    const rowDivs = Array.from(
      document.querySelectorAll(
        'div.flex.flex-col.gap-1.border-b.border-border-soft',
      ),
    )
    return rowDivs.map((d) => {
      const room = d.querySelector('span.font-semibold.tabular-nums')?.textContent?.trim()
      const bucket = d.querySelector('span.shrink-0.text-sm.font-medium')?.textContent?.trim()
      const reason = d.querySelector('span.text-xs.text-text-dim')?.textContent?.trim() || null
      return { room, bucket, reason }
    })
  })

  return { counts, hints, rows }
}

async function scrapePreflight(page) {
  // MonthlyPreflightCard labels — match by Thai copy.
  const labels = [
    'พร้อมสร้างบิล',
    'ยังไม่ได้จดมิเตอร์',
    'มีบิลเดือนนี้แล้ว',
    'อยู่ระหว่างย้ายออก',
    'ไม่อยู่ในรอบบิล',
  ]
  return page.evaluate((labels) => {
    const result = {}
    for (const label of labels) {
      const node = Array.from(document.querySelectorAll('*')).find(
        (n) => (n.textContent || '').trim() === label && n.children.length === 0,
      )
      if (!node) continue
      // Walk to the row container and pull the numeric sibling.
      let row = node.parentElement
      for (let i = 0; i < 4 && row; i++) {
        const nums = row.querySelectorAll('span.tabular-nums, .font-semibold')
        for (const n of nums) {
          const txt = (n.textContent || '').trim()
          const m = txt.match(/^(\d[\d,]*)$/)
          if (m) {
            result[label] = Number(m[1].replace(/,/g, ''))
            return result
          }
        }
        row = row.parentElement
      }
      // Fallback: walk all descendants of parent and pick first integer
      const all = (row?.querySelectorAll('*') || []).values?.() || []
      // eslint-disable-next-line no-unused-vars
      for (const el of all) {
        const txt = (el.textContent || '').trim()
        if (/^\d+$/.test(txt)) {
          result[label] = Number(txt)
          break
        }
      }
    }
    return result
  }, labels)
}

// Thai apartment names → index-based slug for filenames (Thai → ASCII would
// strip everything; numeric index keeps screenshots ordered for review).
const APT_SLUGS = ['court', 'place', 'mansion', 'easy']
const slugFor = (i) => APT_SLUGS[i] || `apt-${i + 1}`

;(async () => {
  const browser = await chromium.launch({
    headless: process.env.SMOKE_HEADLESS === '1' ? true : false,
    slowMo: process.env.SMOKE_HEADLESS === '1' ? 0 : 80,
  })
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  const page = await ctx.newPage()

  console.log('[1A SMOKE] login + apartment discovery')
  await login(page)
  const apartments = await listApartments(page)
  console.log('[1A SMOKE] apartments:', apartments)

  const results = []

  for (let i = 0; i < apartments.length; i++) {
    const apt = apartments[i]
    const aptSlug = slugFor(i)
    console.log(`\n[1A SMOKE] ▶ ${apt} (${aptSlug})`)

    await selectApartment(page, apt)

    // 1A page — desktop
    await page.setViewportSize({ width: 1440, height: 900 })
    await page.goto(
      `${FRONTEND}/billing-reconciliation?billing_month=${BILLING_MONTH}`,
      { waitUntil: 'networkidle' },
    )
    await page.waitForTimeout(1200)
    await page.screenshot({
      path: `/tmp/reconciliation-${aptSlug}-desktop.png`,
      fullPage: true,
    })
    const recon = await scrapeReconciliation(page)
    console.log('  1A counts:', recon.counts)
    console.log('  1A hints:')
    for (const h of recon.hints) console.log('   ·', h)
    console.log('  1A body bucket breakdown:')
    const buckets = recon.rows.reduce((acc, r) => {
      acc[r.bucket || '?'] = (acc[r.bucket || '?'] || 0) + 1
      return acc
    }, {})
    console.log('   ', buckets, ' (total rows:', recon.rows.length, ')')
    // Per-row reason histogram (NB-only — Ready/AR/PD reasons surface inline)
    const reasons = recon.rows.reduce((acc, r) => {
      if (!r.reason) return acc
      acc[r.reason] = (acc[r.reason] || 0) + 1
      return acc
    }, {})
    console.log('  1A reason histogram:', reasons)
    // Show the rows that fall into the new taxonomy branches so the
    // operator nod has named-row evidence to scan against.
    const taxonomyHits = recon.rows.filter(
      (r) =>
        r.reason === 'ผู้เช่าเข้าใหม่กลางเดือน' ||
        r.reason === 'สัญญายังไม่เริ่มเดือนนี้',
    )
    if (taxonomyHits.length > 0) {
      console.log('  1A new-taxonomy rows:')
      for (const r of taxonomyHits) console.log('   ·', r.room, '→', r.bucket, '/', r.reason)
    }

    // 1A page — mobile
    await page.setViewportSize({ width: 375, height: 812 })
    await page.waitForTimeout(300)
    await page.screenshot({
      path: `/tmp/reconciliation-${aptSlug}-mobile.png`,
      fullPage: true,
    })

    // Preflight side-by-side
    await page.setViewportSize({ width: 1440, height: 900 })
    await page.goto(
      `${FRONTEND}/bills/generate?billing_month=${BILLING_MONTH}`,
      { waitUntil: 'networkidle' },
    )
    await page.waitForTimeout(1200)
    await page.screenshot({
      path: `/tmp/reconciliation-generate-${aptSlug}-desktop.png`,
      fullPage: true,
    })
    const preflight = await scrapePreflight(page)
    console.log('  preflight counts:', preflight)

    results.push({ apt, recon, preflight, buckets })
  }

  console.log('\n[1A SMOKE] cross-check summary (objective overlap)')
  console.log('---------------------------------------------------')
  for (const r of results) {
    console.log(`\n  ${r.apt}`)
    console.log('    1A:', r.recon.counts)
    console.log('    preflight:', r.preflight)
    console.log('    body bucket distribution:', r.buckets)
  }
  console.log('\nscreenshots: /tmp/reconciliation-*.png')

  await browser.close()
})().catch((err) => {
  console.error('SMOKE FAILED:', err)
  process.exit(1)
})
