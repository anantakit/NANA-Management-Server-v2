// Tool Page Chrome Contract Smoke Test
// ----------------------------------------------------------------------
// Locks the chrome grammar shared across the bill / move-out / meter
// tool-page family. After Phase A/B/C of the 2026-05-13 chrome
// unification, all four tool pages render the same `ToolPageHeader`
// primitive (breadcrumb + h1 + optional subtitle + optional action slot).
// This smoke asserts the contract per page so a chrome regression
// (e.g. someone re-introducing a hero-size h1 on BillGenerate, or
// dropping the breadcrumb from MoveOutQueue) fails loud.
//
// Pages covered:
//   /bills                  (BillList — Resource-centric list)
//   /bills/generate         (BillGenerate — Workflow tool/launcher)
//   /move-out               (MoveOutQueue — Workflow queue/launcher)
//   /meter-readings         (MeterReadingBatch — Workflow workspace)
//
// What we assert per page:
//   - exactly one <h1> in <main> with the expected verb-neutral title
//   - h1 sizing class includes `text-lg` (mobile) + `text-xl` (sm:)
//   - breadcrumb eyebrow exists, starts with sidebar apartment name,
//     ends with the page-name noun, joined by " · "
//
// Run:  FRONTEND=http://localhost:3005 BACKEND=http://localhost:8080 \
//       npm run smoke:tool-chrome
const { chromium } = require('playwright')

const FRONTEND = process.env.FRONTEND || 'http://localhost:3001'
const BACKEND = process.env.BACKEND || 'http://localhost:8080'
const ADMIN_USER = 'admin'
const ADMIN_PASS_FRESH = 'admin123'
const ADMIN_PASS_POST = 'admin1234'

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
    await page.waitForFunction(() => !window.location.pathname.includes('/change-password'), { timeout: 8000 })
  }
  await page.waitForFunction(() => !window.location.pathname.includes('/login'), { timeout: 10000 })
}

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

// ─── Per-page chrome contract ───────────────────────────────────────────

const PAGES = [
  { path: '/bills', label: 'BillList', h1: 'รายการบิล', pageNoun: 'รายการบิล' },
  { path: '/bills/generate', label: 'BillGenerate', h1: 'ออกบิล', pageNoun: 'ออกบิล' },
  { path: '/move-out', label: 'MoveOutQueue', h1: 'คิวงานย้ายออก', pageNoun: 'ย้ายออก' },
  { path: '/meter-readings', label: 'MeterReadingBatch', h1: 'บันทึกมิเตอร์', pageNoun: 'บันทึกมิเตอร์' },
]

async function assertPageChrome(page, spec, apartmentName) {
  await page.goto(`${FRONTEND}${spec.path}`, { waitUntil: 'networkidle' })
  await page.waitForTimeout(700)

  // 1. Exactly one h1 in <main> with the expected text. Scoped to <main>
  //    to avoid matching any incidental h1 in modals/drawers/sidebar.
  const h1Texts = await page.evaluate(() => {
    const main = document.querySelector('main') || document.body
    return Array.from(main.querySelectorAll('h1')).map((h) => ({
      text: h.textContent?.trim() || '',
      className: h.className,
    }))
  })
  check(`${spec.label}: exactly one h1 in <main>`, h1Texts.length === 1,
    h1Texts.length === 0 ? 'no h1 found' : `found ${h1Texts.length}`)
  if (h1Texts.length === 1) {
    check(`${spec.label}: h1 text = "${spec.h1}"`, h1Texts[0].text === spec.h1,
      `got "${h1Texts[0].text}"`)
    // Sizing contract: text-lg (mobile) + sm:text-xl (desktop). Locked in
    // ToolPageHeader; any drift means someone re-introduced a bespoke h1.
    const cls = h1Texts[0].className
    check(`${spec.label}: h1 sizing = text-lg sm:text-xl`,
      cls.includes('text-lg') && cls.includes('sm:text-xl'),
      `got "${cls}"`)
  }

  // 2. Breadcrumb eyebrow: "<apartment> · <pageNoun>". Scoped to <main>
  //    so we don't match navigation breadcrumbs elsewhere.
  const breadcrumb = await page.evaluate(({ pageNoun }) => {
    const main = document.querySelector('main') || document.body
    // The breadcrumb is the first <p> with text-text-dim that contains the
    // page noun joined by " · ". Just search for any text matching the
    // pattern within the header region (anything before the h1).
    const h1 = main.querySelector('h1')
    if (!h1) return null
    let node = h1.previousElementSibling
    while (node) {
      const txt = (node.textContent || '').trim()
      if (txt.includes(' · ') && txt.endsWith(pageNoun)) return txt
      node = node.previousElementSibling
    }
    // Also walk up to the parent and look at first child <p>
    const parent = h1.parentElement
    if (parent) {
      for (const el of parent.children) {
        if (el === h1) break
        const txt = (el.textContent || '').trim()
        if (txt.includes(' · ') && txt.endsWith(pageNoun)) return txt
      }
    }
    return null
  }, { pageNoun: spec.pageNoun })
  check(`${spec.label}: breadcrumb present + ends with "${spec.pageNoun}"`, !!breadcrumb,
    breadcrumb ? `"${breadcrumb}"` : 'not found')
  if (breadcrumb && apartmentName) {
    check(`${spec.label}: breadcrumb starts with "${apartmentName}"`,
      breadcrumb.startsWith(apartmentName),
      `got "${breadcrumb}"`)
  }
}

;(async () => {
  console.log(`🔗 FRONTEND = ${FRONTEND}  |  BACKEND = ${BACKEND}`)
  const token = await loginApi()
  const apartments = await fetchApartments(token)
  const apartmentName = apartments[0]?.name
  console.log(`📋 sidebar apartment: ${apartmentName}`)

  const browser = await chromium.launch({
    headless: process.env.SMOKE_HEADLESS === '1',
    slowMo: process.env.SMOKE_HEADLESS === '1' ? 0 : 60,
  })
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  const page = await ctx.newPage()
  await login(page)

  for (const spec of PAGES) {
    console.log(`\n🧪 ${spec.label} (${spec.path})`)
    await assertPageChrome(page, spec, apartmentName)
  }

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
