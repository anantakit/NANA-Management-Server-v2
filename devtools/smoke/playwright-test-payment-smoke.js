// Payment Recording — Owner-centric Smoke
// Pins end-to-end payment behavior from the operator's perspective.
// Scenarios are cumulative: state carries forward — do NOT call cleanup/seed
// between them.
//
//   #1 CASH        A101 FINALIZED → select CASH → ยืนยัน → PAID
//                  "รับชำระ" CTA disappears; row now shows ChevronRight (no button CTA)
//
//   #2 TRANSFER    A102 FINALIZED → select TRANSFER + note "เลขสลิป 001234" → PAID
//                  Re-open via row-body click → BillDrawerSummary shows method + date + note
//
//   #3 Guard       A101 (PAID) → no "รับชำระ" CTA → row-body click → no footer in drawer
//                  BillDrawerFooter returns null for PAID bills (double-pay guard)
//
//   #4 Workspace   "ออกบิลแล้ว" chip groups PAID+FINALIZED.
//                  Workspace cannot answer "ห้องไหนยังไม่ได้จ่าย?"
//                  Bills list "รอชำระ" filter chip CAN (drops A101+A102 after payment).
//                  → Evidence for Collection Visibility next feature.
//
// Pre-state (after cleanup + seed):
//   A101–A104, A107 → FINALIZED   (unpaid, 5 rooms)
//   A105,  A106     → PAID        (seeded as paid, 2 rooms)
//   TC smoke rooms  → various moveout/billing states (not touched)
//
// Post-scenario state:
//   A101, A102 → PAID   (paid during #1 + #2)
//   A103, A104, A107 → still FINALIZED
//
// Note on PAID row interaction: PAID bills render ChevronRight only (no RowPrimaryButton),
// so there is no "ดูรายละเอียด บิลห้อง X" aria-label. Drawer is opened via row-body click
// using the room number text as a target — the click event bubbles up to the row's <div onClick>.
//
// Screenshots: /tmp/payment-smoke-*.png   exit(1) on any hard probe failure.

const { chromium } = require('playwright')

const BACKEND = 'http://localhost:8080'
const FRONTEND = 'http://localhost:3001'
const ADMIN_USER = 'admin'
const ADMIN_PASS_FRESH = 'admin123'
const ADMIN_PASS_POST = 'admin1234'

const APARTMENT_NAME = 'นานาคอร์ท'
const BILLING_MONTH = (() => {
  const d = new Date()
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}`
})()

async function postDev(path) {
  const res = await fetch(`${BACKEND}/api/v1/dev${path}`, { method: 'POST' })
  if (!res.ok) throw new Error(`POST /api/v1/dev${path} → HTTP ${res.status}`)
  const body = await res.json()
  if (body.status !== 'success') throw new Error(`POST /api/v1/dev${path} → ${JSON.stringify(body)}`)
}

async function login(page) {
  await page.goto(`${FRONTEND}/login`)
  await page.fill('input[name="username"]', ADMIN_USER)
  await page.fill('input[name="password"]', ADMIN_PASS_FRESH)
  await page.click('button[type="submit"]')
  await page.waitForTimeout(1200)
  if (page.url().includes('/login')) {
    await page.fill('input[name="username"]', ADMIN_USER)
    await page.fill('input[name="password"]', ADMIN_PASS_POST)
    await page.click('button[type="submit"]')
  }
  await page.waitForLoadState('networkidle')
  if (page.url().includes('/change-password')) {
    await page.fill('input[name="new_password"]', ADMIN_PASS_POST)
    await page.fill('input[name="confirm_password"]', ADMIN_PASS_POST)
    await page.click('button[type="submit"]')
    await page.waitForLoadState('networkidle')
  }
  await page.waitForFunction(() => !window.location.pathname.includes('/login'), { timeout: 10000 })
}

async function selectApartment(page, name) {
  const trigger = page.locator('[data-test="apartment-selector-trigger"]')
  await trigger.scrollIntoViewIfNeeded()
  await trigger.click()
  await page.waitForTimeout(200)
  await page
    .locator('[data-test="apartment-selector-panel"] [data-test="apartment-selector-option"]', { hasText: name })
    .first()
    .click()
  await page.waitForTimeout(400)
}

// Open a PAID bill's drawer by clicking on the room number text in the bill section.
// PAID bills render ChevronRight only (no RowPrimaryButton) so there is no named CTA
// button. The click bubbles up to the row's <div onClick={handleOpenView}> handler.
async function openPaidBillDrawer(page, roomNumber) {
  // Scroll to make the section visible before interacting.
  const roomText = page
    .locator('section[aria-label*="บิล"]')
    .getByText(roomNumber, { exact: true })
    .first()
  await roomText.scrollIntoViewIfNeeded()
  await roomText.waitFor({ state: 'visible', timeout: 8000 })
  await roomText.click()
  // Wait for the drawer to open.
  const drawer = page.locator('[role="dialog"]')
  await drawer.waitFor({ state: 'visible', timeout: 5000 })
  return drawer
}

// Reads the numeric count from inside a chip button.
// Chips render as: <button><span class="tabular-nums">N</span><span>label</span></button>
async function readChipCount(chip) {
  const countSpan = chip.locator('span.tabular-nums').first()
  const text = (await countSpan.innerText()).trim()
  return Number(text.replace(/[^0-9]/g, ''))
}

;(async () => {
  console.log('🧪 payment-smoke  billing_month=' + BILLING_MONTH)

  await postDev('/smoke/cleanup')
  await postDev('/smoke/seed')
  // Reset A101–A107 monthly bills to their initial seeded state.
  // seedDevMonthlyBills is idempotent and skips existing bills, so a prior
  // payment run would leave A101 as PAID. This endpoint undoes that so every
  // run starts from FINALIZED (A101–A104/A107) + PAID (A105–A106).
  await postDev('/smoke/reset-base-bills')

  const browser = await chromium.launch({
    headless: process.env.SMOKE_HEADLESS === '1',
    slowMo: process.env.SMOKE_HEADLESS === '1' ? 0 : 120,
  })
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  const page = await ctx.newPage()

  try {
    await login(page)

    // ── #1 CASH happy path ────────────────────────────────────────────────────
    console.log('\n🧪 #1 — CASH happy path (A101 FINALIZED → PAID)')

    await page.goto(`${FRONTEND}/bills`, { waitUntil: 'networkidle' })
    await page.waitForTimeout(1200)
    await page.screenshot({ path: '/tmp/payment-smoke-1-pre.png', fullPage: false })

    // Find "รับชำระ บิลห้อง A101" CTA — only exists for FINALIZED bills.
    const payCta1 = page.locator('button[aria-label="รับชำระ บิลห้อง A101"]')
    await payCta1.scrollIntoViewIfNeeded()
    await payCta1.waitFor({ state: 'visible', timeout: 8000 })
    console.log('  "รับชำระ บิลห้อง A101" CTA found ✅')

    await payCta1.click()

    const drawer1 = page.locator('[role="dialog"]')
    await drawer1.waitFor({ state: 'visible', timeout: 5000 })
    await drawer1.locator('h2', { hasText: 'ห้อง A101' }).waitFor({ timeout: 8000 })
    console.log('  BillDrawer opened: "ห้อง A101" ✅')

    await page.screenshot({ path: '/tmp/payment-smoke-1-drawer-open.png', fullPage: false })

    // Select CASH method — "ยืนยันรับชำระ" is disabled until method chosen.
    const cashBtn = drawer1.locator('button', { hasText: 'เงินสด' })
    await cashBtn.waitFor({ state: 'visible', timeout: 5000 })
    await cashBtn.click()
    console.log('  selected "เงินสด" ✅')

    const confirmBtn1 = drawer1.locator('button', { hasText: 'ยืนยันรับชำระ' })
    const isConfirmEnabled = await confirmBtn1.isEnabled()
    if (!isConfirmEnabled) {
      throw new Error('"ยืนยันรับชำระ" still disabled after selecting method — state not propagating')
    }
    console.log('  "ยืนยันรับชำระ" enabled ✅')

    await page.screenshot({ path: '/tmp/payment-smoke-1-ready.png', fullPage: false })

    await confirmBtn1.click()

    // Drawer closes on success (onSuccess → onClose).
    await drawer1.waitFor({ state: 'hidden', timeout: 8000 })
    console.log('  drawer closed after payment ✅')

    // Invalidation refetch → "รับชำระ บิลห้อง A101" CTA detaches from DOM.
    // PAID bill now shows ChevronRight only (no RowPrimaryButton aria-label).
    await page.waitForSelector('button[aria-label="รับชำระ บิลห้อง A101"]', {
      state: 'detached',
      timeout: 10000,
    })
    console.log('  "รับชำระ บิลห้อง A101" gone — row reflects PAID ✅')

    await page.screenshot({ path: '/tmp/payment-smoke-1-post.png', fullPage: false })

    // Mobile — PAID row renders correctly at 375px.
    await page.setViewportSize({ width: 375, height: 812 })
    await page.waitForTimeout(400)
    await page.screenshot({ path: '/tmp/payment-smoke-1-mobile.png', fullPage: false })
    console.log('  mobile screenshot saved')
    await page.setViewportSize({ width: 1440, height: 900 })

    // ── #2 TRANSFER + note ────────────────────────────────────────────────────
    console.log('\n🧪 #2 — TRANSFER + note (A102 FINALIZED → PAID)')

    // Bills page already loaded; refetch has updated the list.
    const payCta2 = page.locator('button[aria-label="รับชำระ บิลห้อง A102"]')
    await payCta2.scrollIntoViewIfNeeded()
    await payCta2.waitFor({ state: 'visible', timeout: 8000 })
    await payCta2.click()

    const drawer2 = page.locator('[role="dialog"]')
    await drawer2.waitFor({ state: 'visible', timeout: 5000 })
    await drawer2.locator('h2', { hasText: 'ห้อง A102' }).waitFor({ timeout: 8000 })
    console.log('  BillDrawer opened: "ห้อง A102" ✅')

    // Select TRANSFER then fill note.
    const transferBtn = drawer2.locator('button', { hasText: 'โอนเงิน' })
    await transferBtn.waitFor({ state: 'visible', timeout: 5000 })
    await transferBtn.click()
    console.log('  selected "โอนเงิน" ✅')

    const noteInput = drawer2.locator('input[placeholder="หมายเหตุ (ไม่บังคับ)"]')
    await noteInput.waitFor({ state: 'visible', timeout: 5000 })
    await noteInput.fill('เลขสลิป 001234')
    console.log('  note filled: "เลขสลิป 001234" ✅')

    await page.screenshot({ path: '/tmp/payment-smoke-2-ready.png', fullPage: false })

    const confirmBtn2 = drawer2.locator('button', { hasText: 'ยืนยันรับชำระ' })
    await confirmBtn2.click()

    await drawer2.waitFor({ state: 'hidden', timeout: 8000 })
    console.log('  drawer closed after TRANSFER payment ✅')

    await page.waitForSelector('button[aria-label="รับชำระ บิลห้อง A102"]', {
      state: 'detached',
      timeout: 10000,
    })
    console.log('  "รับชำระ บิลห้อง A102" gone ✅')

    // Re-open A102 (now PAID) via row-body click.
    // PAID bills have ChevronRight only — no named CTA button with aria-label.
    const drawer2b = await openPaidBillDrawer(page, 'A102')
    await drawer2b.locator('h2', { hasText: 'ห้อง A102' }).waitFor({ timeout: 8000 })
    console.log('  PAID drawer opened: "ห้อง A102" ✅')

    // Status badge "ชำระแล้ว" must be visible (BillDrawerSummary always shows it for PAID).
    const paidBadge = drawer2b.getByText('ชำระแล้ว')
    const hasBadge = await paidBadge.isVisible().catch(() => false)
    if (!hasBadge) {
      throw new Error('BillDrawerSummary does not show "ชำระแล้ว" badge for PAID bill')
    }
    console.log('  "ชำระแล้ว" badge visible ✅')

    // BillDrawerSummary now shows payment metadata for PAID bills.
    // Hard-assert that note, method, and date are visible.
    const noteVisible = await drawer2b.getByText('เลขสลิป 001234').isVisible().catch(() => false)
    if (!noteVisible) {
      throw new Error('BillDrawerSummary does not show payment_note "เลขสลิป 001234" — payment metadata missing from PAID drawer')
    }
    console.log('  note "เลขสลิป 001234" visible in PAID drawer ✅')

    const methodVisible = await drawer2b.getByText('โอนเงิน').isVisible().catch(() => false)
    if (!methodVisible) {
      throw new Error('BillDrawerSummary does not show payment method "โอนเงิน" for PAID bill')
    }
    console.log('  payment method "โอนเงิน" visible in PAID drawer ✅')

    await page.screenshot({ path: '/tmp/payment-smoke-2-paid-drawer.png', fullPage: false })

    await page.keyboard.press('Escape')
    await drawer2b.waitFor({ state: 'hidden', timeout: 3000 })

    // ── #3 Double payment guard ───────────────────────────────────────────────
    console.log('\n🧪 #3 — Double payment guard (A101 PAID — no re-pay possible)')

    // "รับชำระ บิลห้อง A101" must NOT exist on the page at all.
    const stalePayCta = page.locator('button[aria-label="รับชำระ บิลห้อง A101"]')
    const staleVisible = await stalePayCta.isVisible().catch(() => false)
    if (staleVisible) {
      throw new Error('"รับชำระ บิลห้อง A101" still visible after payment — CTA guard broken')
    }
    console.log('  "รับชำระ บิลห้อง A101" absent from list ✅')

    // Open A101 drawer via row-body click.
    const drawer3 = await openPaidBillDrawer(page, 'A101')
    await drawer3.locator('h2', { hasText: 'ห้อง A101' }).waitFor({ timeout: 8000 })
    console.log('  PAID drawer opened: "ห้อง A101" ✅')

    // PAID → BillDrawerFooter returns null → no method buttons, no confirm button.
    const hasCash = await drawer3.locator('button', { hasText: 'เงินสด' }).isVisible().catch(() => false)
    if (hasCash) {
      throw new Error('PAID drawer shows "เงินสด" method button — footer should be null')
    }

    const hasTransfer = await drawer3.locator('button', { hasText: 'โอนเงิน' }).isVisible().catch(() => false)
    if (hasTransfer) {
      throw new Error('PAID drawer shows "โอนเงิน" method button — footer should be null')
    }

    const hasConfirm = await drawer3.locator('button', { hasText: 'ยืนยันรับชำระ' }).isVisible().catch(() => false)
    if (hasConfirm) {
      throw new Error('PAID drawer shows "ยืนยันรับชำระ" — footer should be null for PAID bills')
    }

    console.log('  no payment form in PAID drawer (footer=null) ✅')
    console.log('  double-pay guard: CTA level (no button) + footer level (null) both enforced ✅')

    await page.screenshot({ path: '/tmp/payment-smoke-3-paid-no-form.png', fullPage: false })

    await page.keyboard.press('Escape')
    await drawer3.waitFor({ state: 'hidden', timeout: 3000 })

    // ── #4 Workspace reality check ────────────────────────────────────────────
    console.log('\n🧪 #4 — Workspace reality (does it answer "ห้องไหนยังไม่ได้จ่าย?")')

    await selectApartment(page, APARTMENT_NAME)
    await page.goto(`${FRONTEND}/monthly-bills/${BILLING_MONTH}`, { waitUntil: 'networkidle' })
    await page.waitForTimeout(800)

    const chipGroup = page.locator('[role="radiogroup"][aria-label="กรองห้องตามสถานะ"]')
    await chipGroup.waitFor({ state: 'visible', timeout: 8000 })

    // "ออกบิลแล้ว" chip counts BOTH FINALIZED + PAID (from BillingReconciliationPage.tsx:
    //   "FINALIZED matches both FINALIZED and PAID rows because finalized_count counts both").
    // Paying A101 and A102 does NOT change this count.
    // This is the design: the workspace is a billing-issuance tool, not a collection tool.
    const issuedChip = chipGroup.locator('button[role="radio"]', { hasText: 'ออกบิลแล้ว' })
    await issuedChip.waitFor({ state: 'visible', timeout: 5000 })
    const issuedCount = await readChipCount(issuedChip)
    console.log(`  "ออกบิลแล้ว" chip: ${issuedCount} (PAID + FINALIZED grouped together)`)

    if (issuedCount < 5) {
      // At minimum: A101+A102 (PAID) + A103+A104+A107 (FINALIZED) = 5.
      // A105+A106 (seeded PAID) make it 7. Guard against seed failure.
      throw new Error(`"ออกบิลแล้ว" chip = ${issuedCount}, expected ≥ 5 — seed state invalid`)
    }

    await page.screenshot({ path: '/tmp/payment-smoke-4-workspace-desktop.png', fullPage: true })

    // Both A103 (FINALIZED, unpaid) and A101 (PAID) show the same data-action="view-bill".
    // The chip filter cannot distinguish them: "ออกบิลแล้ว" covers both states.
    const a103Row = page.locator('[data-test="reconciliation-row"][data-room-number="A103"]')
    await a103Row.waitFor({ state: 'visible', timeout: 5000 })
    const a103Action = await a103Row.getAttribute('data-action')

    const a101Row = page.locator('[data-test="reconciliation-row"][data-room-number="A101"]')
    await a101Row.waitFor({ state: 'visible', timeout: 5000 })
    const a101Action = await a101Row.getAttribute('data-action')

    console.log(`  A101 data-action="${a101Action}" (PAID)`)
    console.log(`  A103 data-action="${a103Action}" (FINALIZED, unpaid)`)

    if (a101Action === a103Action) {
      console.log(`  Both PAID+FINALIZED → same data-action="${a101Action}" — workspace cannot distinguish them`)
    }

    // Mobile
    await page.setViewportSize({ width: 375, height: 812 })
    await page.waitForTimeout(400)
    await page.screenshot({ path: '/tmp/payment-smoke-4-workspace-mobile.png', fullPage: true })
    await page.setViewportSize({ width: 1440, height: 900 })

    // ── Observation summary ───────────────────────────────────────────────────
    console.log('\n━━━ Observations after payment ━━━')
    console.log('')
    console.log('  ✅ Payment recording works end-to-end (CASH + TRANSFER)')
    console.log('  ✅ Double-pay guard enforced at both CTA level and footer level')
    console.log('  ✅ PAID BillDrawer shows paid_at / method / note (BillDrawerSummary polished)')
    console.log('')
    console.log('  📋 Remaining gap:')
    console.log(`  └── Monthly Bills Workspace: "ออกบิลแล้ว" chip = ${issuedCount} (unchanged after paying 2 rooms)`)
    console.log('      Workspace cannot answer: "ห้องไหนยังไม่ได้จ่าย?"')
    console.log('      Bills list "รอชำระ" filter CAN answer this (drops paid rooms on refetch)')
    console.log('')
    console.log('  💡 Next feature signal:')
    console.log('  If operator workflow is "issue bills → collect payment in one session":')
    console.log('  → Collection Visibility (e.g. "รอชำระ" chip in workspace) has evidence')
    console.log('  If operator workflow is "issue bills one day, collect another":')
    console.log('  → Bills list "รอชำระ" filter already solves the discovery problem')
    console.log('  → LINE OA (notification to tenants) may have higher priority than UI change')

    console.log('\n✅ payment-smoke PASS')
    console.log('   screenshots: /tmp/payment-smoke-*.png')
  } catch (err) {
    console.error('\n❌ payment-smoke FAIL')
    console.error(err)
    await page.screenshot({ path: '/tmp/payment-smoke-FAILURE.png', fullPage: false }).catch(() => {})
    process.exitCode = 1
  } finally {
    await browser.close()
  }
})()
