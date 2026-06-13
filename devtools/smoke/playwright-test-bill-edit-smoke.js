// Bill Edit — Behavior Smoke Test
// ----------------------------------------------------------------------
// Pins the user-visible behavior of the BillEditDrawer (FE 11) against
// seeded DRAFT MONTHLY fixtures TC26 (clean) + TC27 (pre-overridden):
//
//   A. Override-add (E101 / TC26)
//      Row CTA → BillDrawer (act) → "แก้ไขบิล" → BillEditDrawer
//      Type new ROOM_RENT amount → บันทึก → re-open
//      Assert "เดิม ฿2,500.00" provenance hint appears (proves persistence).
//
//   B. Manual-add (E101 / TC26)
//      Re-open edit → "เพิ่มรายการ" → fill description + amount
//      บันทึก → re-open → assert manual row visible by description text.
//
//   C. Reset override (E102 / TC27)
//      Row CTA → "แก้ไขบิล" → assert "เดิม ฿2,500.00" hint visible
//      Clear ROOM_RENT input → บันทึก → re-open
//      Assert "เดิม ฿..." hint GONE (proves override removed BE-side).
//
// Selector contract (locked by user):
//   - Drawers MUST be scoped via [role="dialog"] to defeat sidebar / list
//     text collisions (prior incident: "บันทึก" matched sidebar
//     "บันทึกมิเตอร์").
//   - Buttons match by aria-label or by exact role+name; never bare text.
//   - Assertions verify the persisted state via re-open, not just toast.
//
// Self-contained:
//   1. POST /api/v1/dev/smoke/cleanup
//   2. POST /api/v1/dev/smoke/seed
//   3. Login + change-password as needed
//   4. Navigate /bills (current month)
//   5. Fail-fast if E101/E102 rows are missing (proves seed landed in UI)
//   6. Run A/B/C in order
//   7. Mobile (375px) screenshot of the edit drawer
//
// Screenshots saved to /tmp/bill-edit-*.png. exit(1) on any failure.

const { chromium } = require('playwright')

const BACKEND = 'http://localhost:8080'
const FRONTEND = 'http://localhost:3001'
const ADMIN_USER = 'admin'
const ADMIN_PASS_FRESH = 'admin123'
const ADMIN_PASS_POST = 'admin1234'

// TC26/TC27 from seed_dev_smoke.go — pinned by room number; original
// ROOM_RENT = 2,500.00 baht (rm.BaseRent for fan rooms in นานาคอร์ท).
const ROOM_CLEAN = 'E101' // TC26 — DRAFT MONTHLY no overrides
const ROOM_PRE_OVERRIDDEN = 'E102' // TC27 — DRAFT MONTHLY ROOM_RENT overridden to 2,000.00
// `formatTHB(2500)` returns "฿2,500" without decimals (Intl.NumberFormat
// drops fraction digits when input is an integer per shared/utils/format.ts).
// Match the exact string the OverrideRow renders so the assertion fails
// loudly if formatting drifts (e.g. someone forces 2-digit fractions).
const ORIGINAL_RENT_LABEL = '฿2,500'

async function postDev(path) {
  const res = await fetch(`${BACKEND}/api/v1/dev${path}`, { method: 'POST' })
  if (!res.ok) {
    throw new Error(`POST /api/v1/dev${path} → HTTP ${res.status}`)
  }
  const body = await res.json()
  if (body.status !== 'success') {
    throw new Error(`POST /api/v1/dev${path} → ${JSON.stringify(body)}`)
  }
}

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

/**
 * Locate a BillList row by its room number. Returns a Playwright locator
 * for the row body (clickable div). Survives the 2026-05-29 density
 * refactor where DRAFT/PAID/VOID rows dropped the labelled CTA button
 * in favour of a row-body-click entry path (only FINALIZED unpaid +
 * settlement-collect/refund rows now render a verb CTA — see
 * `feedback_billlist_chrome` + `BillRow/index.tsx getRowCta`).
 *
 * Strategy: scope to a bill section, find the room-number span by exact
 * text, then walk up to the row container. We anchor on the `group`
 * Tailwind class because BillRow uses `group-hover:` for its chevron;
 * removing it would require a broader row-chrome refactor and would
 * fail loudly here.
 */
function billRowByRoom(page, roomNumber) {
  return page
    .locator('section[aria-label^="บิล "]')
    .getByText(roomNumber, { exact: true })
    .first()
    .locator('xpath=ancestor::div[contains(@class, "group")][1]')
}

/** Open BillEditDrawer for a given room (row click → BillDrawer → แก้ไขบิล). */
async function openEditDrawer(page, roomNumber) {
  // Close any stale drawer first — defensive against scenario chains.
  const stale = page.locator('[role="dialog"]')
  if (await stale.count()) {
    await page.keyboard.press('Escape')
    await page.waitForTimeout(300)
  }

  // Row body click opens BillDrawer in view mode. The drawer still
  // surfaces "แก้ไขบิล" for DRAFT MONTHLY bills regardless of open mode,
  // so view-mode entry is sufficient for the edit flow.
  await billRowByRoom(page, roomNumber).click({ timeout: 8000 })

  const dialog = page.locator('[role="dialog"]')
  await dialog.waitFor({ state: 'visible', timeout: 5000 })

  // From BillDrawer's action section: "แก้ไขบิล" is the outline CTA shown
  // when DRAFT + MONTHLY + onEdit. Triggers REPLACE pattern: BillDrawer
  // closes + BillEditDrawer opens at the same right-side slot.
  await dialog
    .getByRole('button', { name: 'แก้ไขบิล', exact: true })
    .click({ timeout: 5000 })

  // BillEditDrawer mounts under the SAME [role="dialog"] selector (Sheet
  // primitive). Confirm it's the edit drawer by checking BOTH:
  //   - h2 contains room identity ("ห้อง E101") — identity-first title
  //   - dialog text contains "แก้ไข" (subtitle: "...แก้ไขบิลรายเดือน")
  // The combined check defeats false-positives if the view drawer were
  // still mounted (it would have the room h2 but no "แก้ไข" copy).
  await page.waitForFunction(
    (rn) => {
      const dlg = document.querySelector('[role="dialog"]')
      if (!dlg) return false
      const heading = dlg.querySelector('h2, h3, [role="heading"]')
      const headingMatches = heading?.textContent?.includes(`ห้อง ${rn}`) ?? false
      const editAffordance = dlg.textContent?.includes('แก้ไข') ?? false
      return headingMatches && editAffordance
    },
    roomNumber,
    { timeout: 5000 },
  )
}

/**
 * Wait until every Sheet/Dialog has fully detached. The REPLACE pattern
 * (BillDrawer → BillEditDrawer) keeps the outgoing drawer mounted for
 * ~200ms during slide-out, so during scenario chains there can briefly
 * be TWO `[role="dialog"]` nodes — a single Playwright locator would
 * hit strict-mode then. waitForFunction polls until count === 0.
 */
async function waitForAllDialogsClosed(page, timeout = 8000) {
  await page.waitForFunction(
    () => document.querySelectorAll('[role="dialog"]').length === 0,
    null,
    { timeout },
  )
}

/** Click the dialog's primary save button + wait for it to close. */
async function saveDrawer(page) {
  // .last() targets the currently-active drawer when an outgoing
  // REPLACE sibling is still in the DOM (translate-*-full, mid-animation).
  // Exact "บันทึก" defeats sidebar's "บันทึกมิเตอร์" + the cancel
  // button "ยกเลิก".
  await page
    .locator('[role="dialog"]')
    .last()
    .getByRole('button', { name: 'บันทึก', exact: true })
    .click({ timeout: 5000 })
  await waitForAllDialogsClosed(page)
}

/** Cancel out of the edit drawer (no persistence). */
async function cancelDrawer(page) {
  await page
    .locator('[role="dialog"]')
    .last()
    .getByRole('button', { name: 'ยกเลิก', exact: true })
    .click({ timeout: 5000 })
  await waitForAllDialogsClosed(page, 5000)
}

function fail(msg) {
  console.error('❌ FAIL:', msg)
  process.exit(1)
}

;(async () => {
  console.log('▶ pre-cleanup + re-seed smoke fixtures')
  await postDev('/smoke/cleanup')
  await postDev('/smoke/seed')

  const browser = await chromium.launch({
    headless: process.env.SMOKE_HEADLESS === '1',
    slowMo: process.env.SMOKE_HEADLESS === '1' ? 0 : 120,
  })
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  const page = await ctx.newPage()

  await login(page)
  console.log('▶ login ok')

  await page.goto(`${FRONTEND}/bills`, { waitUntil: 'networkidle' })
  await page.waitForTimeout(1200)

  // ── Fail-fast: verify seeded rows are visible (Plan C) ─────────────
  // DRAFT rows have no labelled CTA — locate the row body via billRowByRoom
  // (same path the scenarios use to open the drawer).
  for (const room of [ROOM_CLEAN, ROOM_PRE_OVERRIDDEN]) {
    const count = await billRowByRoom(page, room).count()
    if (count === 0) {
      await page.screenshot({ path: `/tmp/bill-edit-FAIL-${room}.png`, fullPage: true })
      fail(
        `expected DRAFT MONTHLY row for ${room} on /bills — seed fixture missing or BillList not surfacing it. ` +
          `Make sure seed_dev_smoke.go TC26/TC27 ran for the current billing month, and that the BillList ` +
          `view chip default ("บิลรายเดือน") still includes DRAFT bills.`,
      )
    }
  }
  console.log('▶ seed rows visible:', ROOM_CLEAN, '+', ROOM_PRE_OVERRIDDEN)

  await page.screenshot({ path: '/tmp/bill-edit-list-desktop.png', fullPage: true })

  // ─────────────────────────────────────────────────────────────────
  // Scenario A — Override-add on E101 (TC26 clean → set ROOM_RENT=2000)
  // ─────────────────────────────────────────────────────────────────
  console.log('▶ A: override-add E101')
  await openEditDrawer(page, ROOM_CLEAN)

  const dialog = page.locator('[role="dialog"]')
  const rentInput = dialog.locator('[aria-label="จำนวนเงิน ค่าเช่าห้อง"]')
  await rentInput.waitFor({ state: 'visible', timeout: 5000 })
  // Empty before any override = no pre-fill. onFocus selects all; type
  // replaces. Use fill() to be deterministic regardless of focus state.
  await rentInput.fill('2000')
  await page.screenshot({ path: '/tmp/bill-edit-A-typed.png', fullPage: false })

  await saveDrawer(page)

  // Verify persistence: re-open, assert OverrideRow shows the original
  // amount hint (proves BE persisted the override + responded with
  // is_overridden=true + original_amount).
  await openEditDrawer(page, ROOM_CLEAN)
  const hintVisible = await page
    .locator(`[role="dialog"]`)
    .locator(`text=เดิม ${ORIGINAL_RENT_LABEL}`)
    .first()
    .isVisible({ timeout: 3000 })
    .catch(() => false)
  if (!hintVisible) {
    fail(
      `A: after override-add on ${ROOM_CLEAN}, expected "เดิม ${ORIGINAL_RENT_LABEL}" provenance hint to appear on re-open. ` +
        `Override was not persisted (or response did not flip is_overridden=true).`,
    )
  }
  await page.screenshot({ path: '/tmp/bill-edit-A-persisted.png', fullPage: false })
  console.log('  ✓ override persisted, hint visible')

  // Stay in the drawer for Scenario B (chained — same bill).

  // ─────────────────────────────────────────────────────────────────
  // Scenario B — Manual-add on E101 (chain: same drawer still open)
  // ─────────────────────────────────────────────────────────────────
  console.log('▶ B: manual-add E101')
  const MANUAL_DESC = 'ค่าทดสอบ SMOKE'
  const MANUAL_AMT = '123'
  // ManualItemEditor exposes preset chips ("ค่าทำความสะอาด", "ค่าซ่อมแซม",
  // "ค่ากุญแจ") + a custom-row trigger labelled "เพิ่มเอง" (was "เพิ่มรายการ"
  // before the 2026-05-23 chip redesign). Match via partial substring so
  // future copy tweaks don't re-break.
  await dialog
    .getByRole('button', { name: /เพิ่มเอง/, exact: false })
    .click({ timeout: 5000 })
  // Description input — aria-label="ชื่อรายการ" (was "คำอธิบายรายการ"
  // before the 2026-05-23 ManualItemRow rename).
  await dialog.locator('[aria-label="ชื่อรายการ"]').last().fill(MANUAL_DESC)
  // Default mode = flat amount; aria-label="จำนวนเงิน" (no suffix).
  // .last() since there could be multiple manual rows; we just added one.
  await dialog.locator('[aria-label="จำนวนเงิน"]').last().fill(MANUAL_AMT)
  await page.screenshot({ path: '/tmp/bill-edit-B-typed.png', fullPage: false })

  await saveDrawer(page)

  // Verify persistence: re-open, assert the manual row description is
  // present in the drawer body (BillEditDrawer renders existing MANUAL
  // items via buildInitialFormState).
  await openEditDrawer(page, ROOM_CLEAN)
  const manualVisible = await page
    .locator(`[role="dialog"]`)
    .locator(`input[aria-label="ชื่อรายการ"][value="${MANUAL_DESC}"]`)
    .first()
    .isVisible({ timeout: 3000 })
    .catch(() => false)
  if (!manualVisible) {
    fail(
      `B: after manual-add on ${ROOM_CLEAN}, expected description "${MANUAL_DESC}" to be present on re-open. ` +
        `Manual item was not persisted.`,
    )
  }
  await page.screenshot({ path: '/tmp/bill-edit-B-persisted.png', fullPage: false })
  console.log('  ✓ manual item persisted')

  await cancelDrawer(page)

  // ─────────────────────────────────────────────────────────────────
  // Scenario C — Reset override on E102 (TC27 pre-overridden → empty)
  // ─────────────────────────────────────────────────────────────────
  console.log('▶ C: reset override E102')
  await openEditDrawer(page, ROOM_PRE_OVERRIDDEN)

  // Sanity: the seeded ROOM_RENT override = 2000.00. Drawer should
  // show "เดิม ฿2,500.00" BEFORE we clear (proves seed worked + BE
  // marks the row is_overridden=true on the response).
  const hintBefore = await page
    .locator(`[role="dialog"]`)
    .locator(`text=เดิม ${ORIGINAL_RENT_LABEL}`)
    .first()
    .isVisible({ timeout: 3000 })
    .catch(() => false)
  if (!hintBefore) {
    fail(
      `C: expected "เดิม ${ORIGINAL_RENT_LABEL}" hint on E102 BEFORE reset (seed TC27 pre-overridden). ` +
        `Either seed.Overrides did not persist, or BE response is not marking is_overridden=true.`,
    )
  }

  const rentInput2 = page
    .locator('[role="dialog"]')
    .locator('[aria-label="จำนวนเงิน ค่าเช่าห้อง"]')
  await rentInput2.waitFor({ state: 'visible', timeout: 5000 })
  await rentInput2.fill('') // empty = "remove override" per Interpretation B
  await page.screenshot({ path: '/tmp/bill-edit-C-cleared.png', fullPage: false })

  await saveDrawer(page)

  // Verify persistence: re-open, assert the hint is GONE (proves BE
  // pruned the ROOM_RENT key from the overrides jsonb map).
  await openEditDrawer(page, ROOM_PRE_OVERRIDDEN)
  const hintAfter = await page
    .locator(`[role="dialog"]`)
    .locator(`text=เดิม ${ORIGINAL_RENT_LABEL}`)
    .first()
    .isVisible({ timeout: 1500 })
    .catch(() => false)
  if (hintAfter) {
    fail(
      `C: after reset on ${ROOM_PRE_OVERRIDDEN}, expected "เดิม ${ORIGINAL_RENT_LABEL}" hint to disappear. ` +
        `Override was NOT removed BE-side (empty input should prune the key).`,
    )
  }
  await page.screenshot({ path: '/tmp/bill-edit-C-cleared-persisted.png', fullPage: false })
  console.log('  ✓ override removed, hint gone')

  // ─────────────────────────────────────────────────────────────────
  // Mobile (375px) — capture the edit drawer at the mobile viewport so
  // the developer can eyeball the sticky footer + density.
  // ─────────────────────────────────────────────────────────────────
  await page.setViewportSize({ width: 375, height: 812 })
  await page.waitForTimeout(400)
  await page.screenshot({ path: '/tmp/bill-edit-mobile.png', fullPage: false })
  await cancelDrawer(page)

  console.log('✅ bill-edit smoke passed (3 scenarios + persistence asserted)')
  await browser.close()
})().catch((err) => {
  console.error('💥 unexpected error:', err)
  process.exit(1)
})
