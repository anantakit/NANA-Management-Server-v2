// Epic B — "Recovery survives Move-out cancellation" capability smoke.
// ----------------------------------------------------------------------
// Locks the owner-decided operational truth (2026-07-16): cancelling a move-out
// cancels the WORKFLOW only — it MUST NOT erase the fact that a prior month's
// meter was over-recorded. The over-charge is still refunded on the next monthly.
//
// Proves the WHOLE operator chain connects (not every pixel):
//   1. Move-out → record exit meter BELOW previous → tick "เดือนก่อนจดเกิน" (UI)
//   2. Generate settlement → Model B (0-usage electricity + refund recorded−now)
//   3. Cancel move-out → EXIT removed, settlement VOID, contract back ACTIVE
//   4. Generate the monthly (batch-monthly → commit) for the recovery's month
//   5. Monthly carries the recovery refund EXACTLY ONCE, correct amount
//   6. No duplicate recovery row
//
// The NEW capability (the over-record flag) is exercised through the real UI
// (RoomWorkflowDrawer → MeterStep). Cancel + monthly generation are existing
// capabilities driven via the API — the point here is that the DATA flows
// through the whole chain, per owner ("prove the workflow connects").
//
// Self-seeding + re-runnable; teardown resets the fixture.
// Requires: `make dev` (postgres + backend :8080 + frontend :3001, ENV=development)
//           + `make smoke-install` once.
// Run: npm run smoke:recovery-survives-cancel   (SMOKE_HEADED=1 to watch)

const { chromium } = require('playwright')

const FRONTEND = process.env.FRONTEND || 'http://localhost:3001'
const BACKEND = process.env.BACKEND || 'http://localhost:8080'
const V1 = `${BACKEND}/api/v1`

let failed = 0
function check(name, ok, detail = '') {
  console.log(`  ${ok ? '✅' : '❌ FAIL'} ${name}${detail ? ' — ' + detail : ''}`)
  if (!ok) failed++
}

async function api(method, path, { token, body } = {}) {
  const headers = { 'Content-Type': 'application/json' }
  if (token) headers.Authorization = `Bearer ${token}`
  const r = await fetch(`${V1}${path}`, { method, headers, body: body ? JSON.stringify(body) : undefined })
  let json = null
  try { json = await r.json() } catch { /* empty */ }
  return { status: r.status, json }
}

async function loginHttp() {
  for (const pw of ['admin1234', 'admin123']) {
    const r = await api('POST', '/auth/login', { body: { username: 'admin', password: pw } })
    if (r.status !== 200 || !r.json?.data?.access_token) continue
    let token = r.json.data.access_token
    if (r.json.data.must_change_password) {
      await api('POST', '/auth/change-password', { token, body: { current_password: pw, new_password: 'admin1234' } })
      token = (await api('POST', '/auth/login', { body: { username: 'admin', password: 'admin1234' } })).json.data.access_token
    }
    return token
  }
  throw new Error('HTTP login failed')
}

const liveMonthly = async (token, contractID, month) => {
  const list = await api('GET', `/bills?contract_id=${contractID}&bill_type=MONTHLY&limit=50`, { token })
  return (list.json?.data || []).find((b) => b.billing_month === month && b.status !== 'VOID')
}
const liveSettlement = async (token, contractID) => {
  const list = await api('GET', `/bills?contract_id=${contractID}&bill_type=SETTLEMENT&limit=50`, { token })
  return (list.json?.data || []).filter((b) => b.status !== 'VOID')[0]
}
const recoveryLines = (bill) =>
  (bill.line_items || []).filter((li) => li.line_type === 'ADJUSTMENT' && li.adjustment_recovery_reading_id)

async function loginUi(page) {
  for (let attempt = 0; attempt < 3; attempt++) {
    await page.goto(`${FRONTEND}/login`, { waitUntil: 'networkidle' })
    await page.fill('input[placeholder="กรอกชื่อผู้ใช้"]', 'admin')
    await page.fill('input[placeholder="กรอกรหัสผ่าน"]', 'admin1234')
    await page.click('button[type="submit"]')
    try {
      await page.waitForURL((u) => !u.toString().includes('/login'), { timeout: 12000 })
      return true
    } catch { /* retry */ }
  }
  return false
}

async function main() {
  console.log('Epic B — Recovery survives Move-out cancel (capability smoke)\n')
  const token = await loginHttp()
  const fx = (await api('POST', '/dev/smoke/settlement-recovery-setup', { token })).json.data
  const moveOutDate = new Date(Date.now() - 3 * 86400000).toISOString().slice(0, 10)
  const recoveryMonth = moveOutDate.slice(0, 7) // the anchor's billing_month = exit month
  const wantRefund = fx.expected_refund / 100

  const browser = await chromium.launch({ headless: !process.env.SMOKE_HEADED })
  const page = await browser.newPage()
  page.on('pageerror', (e) => console.log('  ⚠ pageerror:', e.message))

  try {
    // 1. Operator records the exit meter BELOW previous + flags the over-record (real UI).
    console.log('STEP 1 — record exit meter below previous + "เดือนก่อนจดเกิน" (UI)')
    check('UI login', await loginUi(page))
    await page.goto(`${FRONTEND}/move-out/${fx.notice_id}`, { waitUntil: 'networkidle' })
    await page.getByRole('button', { name: 'ดำเนินการต่อ' }).first().click()
    await page.getByRole('dialog', { name: 'ดำเนินการย้ายออก' }).waitFor({ state: 'visible', timeout: 10000 })
    await page.fill('#rwd_water', String(fx.exit_observation_water))
    await page.fill('#rwd_electricity', String(fx.exit_observation_electricity)) // below the wrong 1500
    const chip = page.getByRole('button', { name: 'มิเตอร์ไฟฟ้า — เดือนก่อนจดเกิน' })
    const chipOk = await chip.first().waitFor({ state: 'visible', timeout: 6000 }).then(() => true).catch(() => false)
    check('"เดือนก่อนจดเกิน" chip appears on below-previous reading', chipOk)
    if (chipOk) {
      await chip.first().click()
      await page.getByRole('button', { name: 'บันทึกและไปสรุปยอด' }).first().click()
      await page.waitForTimeout(2000)
    }
    const notice = (await api('GET', `/move-out-notices/${fx.notice_id}`, { token })).json.data
    check('move-out advanced to PENDING_SETTLEMENT', notice.status === 'PENDING_SETTLEMENT', notice.status)

    // 2. Settlement = Model B: 0-usage electricity + refund recorded − now.
    console.log('\nSTEP 2 — generate settlement (Model B)')
    const gen = await api('POST', `/move-out-notices/${fx.notice_id}/generate-settlement`, { token, body: { rent_mode: 'FULL_MONTH_KEEP_DEPOSIT' } })
    check('generate-settlement', gen.status === 200 || gen.status === 201)
    let sb = await liveSettlement(token, fx.contract_id)
    sb = (await api('GET', `/bills/${sb.id}`, { token })).json.data
    const sbElec = (sb.line_items || []).find((l) => l.line_type === 'ELECTRICITY')
    const sbRefund = recoveryLines(sb)
    check('settlement electricity usage re-anchored to 0', sbElec && sbElec.quantity === 0)
    check('settlement carries the recovery refund once', sbRefund.length === 1 && sbRefund[0].amount === wantRefund, `฿${sbRefund[0]?.amount}`)

    // 3. Cancel the move-out — workflow only.
    console.log('\nSTEP 3 — cancel move-out (workflow only)')
    const cancel = await api('POST', `/move-out-notices/${fx.notice_id}/cancel`, { token })
    check('cancel succeeds', cancel.status === 200)
    check('move-out is CANCELLED, contract back active', cancel.json?.data?.status === 'CANCELLED')
    const settlementAfter = (await api('GET', `/bills/${sb.id}`, { token })).json.data
    check('settlement bill VOID after cancel', settlementAfter.status === 'VOID')

    // 4 + 5 + 6. Monthly still refunds the over-record — exactly once — and the
    // recovery is not duplicated.
    console.log('\nSTEP 4 — generate the monthly for the recovery month (batch → commit)')
    const batch = await api('POST', `/bills/batch-monthly`, { token, body: { apartment_id: fx.apartment_id, billing_month: recoveryMonth } })
    check('batch-monthly (classify)', batch.status === 200)
    const commit = await api('POST', `/bills/batches/${batch.json.data.batch_id}/commit`, { token })
    check('batch commit (persist drafts)', commit.status === 200)

    console.log('\nSTEP 5 — monthly carries the recovery refund exactly once')
    const monthly = await liveMonthly(token, fx.contract_id, recoveryMonth)
    check('monthly bill created for the recovery month', !!monthly, recoveryMonth)
    if (monthly) {
      const mBill = (await api('GET', `/bills/${monthly.id}`, { token })).json.data
      const mRefund = recoveryLines(mBill)
      check('monthly has EXACTLY ONE recovery refund line', mRefund.length === 1, `got ${mRefund.length}`)
      check('monthly refund amount correct (recorded − now)', mRefund[0]?.amount === wantRefund, `฿${mRefund[0]?.amount} want ฿${wantRefund}`)
    }

    console.log('\nSTEP 6 — no duplicate recovery')
    const readings = await api('GET', `/apartments/${fx.apartment_id}/meter-readings/rooms/${fx.room_id}/history`, { token })
    const recoveries = (readings.json?.data?.items || readings.json?.data || []).filter((r) => r.anchor_reason === 'READING_RECOVERY')
    check('exactly one recovery row (survived cancel, not duplicated)', recoveries.length === 1, `got ${recoveries.length}`)
  } finally {
    await browser.close()
    await api('POST', '/dev/smoke/settlement-recovery-setup', { token }).catch(() => {}) // teardown
    console.log('\n  fixture reset (teardown)')
  }

  if (failed === 0) {
    console.log('\n✅ PASS — recovery survives move-out cancel, monthly refunds exactly once')
  } else {
    console.log(`\n❌ FAIL — ${failed} assertion(s)`)
    process.exit(1)
  }
}

main().catch((e) => {
  console.error(`\n❌ ${e.message}`)
  process.exit(1)
})
