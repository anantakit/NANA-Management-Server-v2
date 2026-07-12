// F1 — Exit-meter rollover/replacement: operator UI reachability smoke.
// ----------------------------------------------------------------------
// Proves the capability is reachable and usable on the CANONICAL operator
// surface — the Move-out Queue → RoomWorkflowDrawer → MeterStep — which the
// HTTP smoke (smoke-exit-meter-flags-http.js) BYPASSES by POSTing
// record-exit-meter directly. That HTTP smoke proves the Settlement bill math;
// THIS one proves the missing UI reachability:
//
//   Queue "รอจดมิเตอร์" → click card → workflow drawer opens at MeterStep
//     → rollover/replacement toggles are visible + usable (desktop AND mobile)
//     → a legitimate current < previous is BLOCKED until the flag makes it valid
//     → the flow completes and record-exit-meter fires
//
// Scenarios:
//   Desktop 1440px — XM-ROLL: enter 500 (< prev 99000) → below-prev blocks →
//                    tick "มิเตอร์ครบรอบ" → unblocks, usage wraps to 1,499 → save.
//   Mobile  375px  — XM-REPL: tick "เปลี่ยนมิเตอร์ใหม่" → previous 0, usage 45 →
//                    44px touch target → save.
//
// Fixtures: dev endpoint /dev/smoke/exit-meter-flags-setup seeds two
// PENDING_METER move-outs (rooms XM-ROLL / XM-REPL, นานาคอร์ท), re-runnable.
// The EXIT reading is recorded through the UI here, not seeded.
//
// Run:  npm run smoke:exit-meter-flags   (from backend/devtools/smoke/)
//       make smoke-exit-meter-flags-ui   (from backend/)

const { chromium } = require('playwright')

const FRONTEND = 'http://localhost:3001'
const BACKEND = 'http://localhost:8080'
const ADMIN_USER = 'admin'
const ADMIN_PASS_FRESH = 'admin123'
const ADMIN_PASS_POST = 'admin1234'

const DRAWER = '[role="dialog"][aria-label="ดำเนินการย้ายออก"]'
const ROLLOVER_ROOM = 'XM-ROLL'
const REPLACE_ROOM = 'XM-REPL'
const SAVE_LABEL = 'บันทึกและไปสรุปยอด'
const BELOW_PREV = 'เลขมิเตอร์ต้องไม่น้อยกว่าครั้งก่อน'

const results = { pass: 0, fail: 0, total: 0, failedCases: [] }
const check = (name, ok, detail = '') => {
  console.log(`  ${ok ? '✅' : '❌'} ${name}${detail ? ` — ${detail}` : ''}`)
  results.total++
  if (ok) results.pass++
  else { results.fail++; results.failedCases.push(name) }
  return ok
}

async function login(page) {
  await page.goto(`${FRONTEND}/login`)
  await page.fill('input[name="username"]', ADMIN_USER)
  await page.fill('input[name="password"]', ADMIN_PASS_FRESH)
  await page.click('button[type="submit"]')
  await page.waitForLoadState('networkidle')
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

async function seed() {
  const res = await fetch(`${BACKEND}/api/v1/dev/smoke/exit-meter-flags-setup`, { method: 'POST' })
  if (!res.ok) throw new Error(`fixture setup failed: ${res.status}`)
}

// Open the move-out queue on the "รอจดมิเตอร์" (PENDING_METER) stage — the
// default active tab — then open a room's workflow drawer from its card.
async function openMeterDrawerFromQueue(page, roomNumber) {
  await page.goto(`${FRONTEND}/move-out`)
  await page.waitForLoadState('networkidle')
  await page.waitForSelector('text=คิวงานย้ายออก', { timeout: 10000 })
  const meterTab = page.locator('button:has-text("รอจดมิเตอร์")').first()
  if (await meterTab.isVisible().catch(() => false)) {
    await meterTab.click()
    await page.waitForTimeout(300)
  }
  const card = page.locator(`button:has-text("${roomNumber}")`).first()
  await card.waitFor({ state: 'visible', timeout: 8000 })
  await card.click()
  await page.waitForSelector(DRAWER, { timeout: 8000 })
  await page.waitForSelector('#rwd_electricity', { timeout: 8000 }) // MeterStep is step 1
  await page.waitForTimeout(500)
}

async function runRollover(page) {
  console.log(`\nDESKTOP — rollover at move-out via Queue (${ROLLOVER_ROOM})`)
  await openMeterDrawerFromQueue(page, ROLLOVER_ROOM)

  const rollover = page.locator('[aria-label="มิเตอร์ไฟฟ้า — มิเตอร์ครบรอบ"]')
  const replaced = page.locator('[aria-label="มิเตอร์ไฟฟ้า — เปลี่ยนมิเตอร์ใหม่"]')
  check('rollover/replacement toggles visible on MeterStep (queue → drawer)',
    (await rollover.isVisible()) && (await replaced.isVisible()))

  // 500 < previous 99000 must be blocked before any flag.
  await page.fill('#rwd_electricity', '500')
  await page.waitForTimeout(500)
  const saveBtn = page.getByRole('button', { name: SAVE_LABEL })
  check('below-previous error shown for 500 < 99000 (no flag)',
    await page.getByText(BELOW_PREV).first().isVisible())
  check('save DISABLED while below-previous and no flag', await saveBtn.isDisabled())

  // Rollover makes the same 500 legitimate.
  await rollover.click()
  await page.waitForTimeout(400)
  await page.fill('#rwd_water', '8600')
  await page.waitForTimeout(400)
  check('below-previous error CLEARED after rollover toggle',
    !(await page.getByText(BELOW_PREV).first().isVisible().catch(() => false)))
  check('rollover chip aria-pressed=true', (await rollover.getAttribute('aria-pressed')) === 'true')
  check('usage shows digit-wrap 1,499 หน่วย',
    await page.getByText('ใช้ไป 1,499 หน่วย').first().isVisible().catch(() => false))
  check('save ENABLED after rollover unblocks the reading', await saveBtn.isEnabled())

  // Complete through the UI; confirm the operator action hit the real endpoint.
  const [resp] = await Promise.all([
    page.waitForResponse((r) => r.url().includes('/record-exit-meter'), { timeout: 8000 }),
    saveBtn.click(),
  ])
  check('record-exit-meter fired from the UI (status < 400)', resp.status() < 400, `HTTP ${resp.status()}`)
  await page.waitForTimeout(1200)
  check('meter step advanced after save (left the meter form)',
    !(await page.locator('#rwd_electricity').isVisible().catch(() => false)))
}

async function runReplacement(page) {
  console.log(`\nMOBILE 375px — meter replacement at move-out via Queue (${REPLACE_ROOM})`)
  await openMeterDrawerFromQueue(page, REPLACE_ROOM)

  const replaced = page.locator('[aria-label="มิเตอร์ไฟฟ้า — เปลี่ยนมิเตอร์ใหม่"]')
  check('replacement toggle visible on MOBILE MeterStep', await replaced.isVisible())
  const box = await replaced.boundingBox()
  check('toggle touch target ≥ 40px on mobile', !!box && box.height >= 40,
    box ? `${Math.round(box.height)}px` : 'no box')

  await page.fill('#rwd_electricity', '45')
  await replaced.click()
  await page.fill('#rwd_water', '320')
  await page.waitForTimeout(400)
  check('replacement chip aria-pressed=true', (await replaced.getAttribute('aria-pressed')) === 'true')
  check('usage counts from zero (45 หน่วย)',
    await page.getByText('ใช้ไป 45 หน่วย').first().isVisible().catch(() => false))
  const saveBtn = page.getByRole('button', { name: SAVE_LABEL })
  check('mobile save ENABLED with replacement flag', await saveBtn.isEnabled())

  const [resp] = await Promise.all([
    page.waitForResponse((r) => r.url().includes('/record-exit-meter'), { timeout: 8000 }),
    saveBtn.click(),
  ])
  check('record-exit-meter fired from mobile UI (status < 400)', resp.status() < 400, `HTTP ${resp.status()}`)
  await page.waitForTimeout(1200)
  check('mobile meter step advanced after save',
    !(await page.locator('#rwd_electricity').isVisible().catch(() => false)))
}

async function main() {
  console.log('━━━ F1 Exit-meter rollover/replacement — operator UI reachability smoke ━━━')
  await seed()
  console.log('  ✅ exit-meter-flags fixtures re-seeded (XM-ROLL / XM-REPL → PENDING_METER)')

  const browser = await chromium.launch({ headless: true })
  try {
    const desktop = await browser.newContext({ viewport: { width: 1440, height: 900 } })
    const dp = await desktop.newPage()
    await login(dp)
    console.log('  ✅ UI login complete')
    await runRollover(dp)
    await desktop.close()

    const mobile = await browser.newContext({ viewport: { width: 375, height: 812 } })
    const mp = await mobile.newPage()
    await login(mp)
    await runReplacement(mp)
    await mobile.close()
  } finally {
    await browser.close()
  }

  console.log(`\n━━━ ${results.pass}/${results.total} passed ━━━`)
  if (results.fail > 0) {
    console.log('  Failed:', results.failedCases.join(', '))
    process.exit(1)
  }
  console.log('✅ PASS — F1 capability reachable + usable on the canonical MeterStep (desktop + mobile)')
}

main().catch((e) => { console.error(`\n❌ ${e.message}`); process.exit(1) })
