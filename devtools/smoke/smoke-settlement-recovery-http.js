// Epic B — Settlement Recovery Reconciliation: HTTP smoke.
//
// Locks the D1 fix through the REAL production chain (handler → service →
// resolver → repository → Postgres → JSON response) without a browser — the FE
// presentation (aggregation + stale indicator) is a separate follow-up, so this
// smoke asserts the API, not the UI.
//
// Nothing terminal is seeded: the refund is produced by the real resolver. The
// dev fixture only sets up the mis-read source month (a FINALIZED bill that
// charged 300 phantom electricity units at ฿8/unit) + a PENDING_SETTLEMENT
// move-out. The smoke then drives:
//
//   generate-settlement
//     → baseline-correction (operator enters the true reading 1200)
//     → finalize-settlement            ❌ 400 stale (the D1 symptom)
//     → regenerate-settlement          (resolver emits the ฿-2,400 refund)
//     → GET bill: assert refund line at the SOURCE rate
//     → finalize-settlement            ✅
//     → correct-settlement             (post-FINALIZED path: re-emits the refund)
//     → GET bill: assert refund line again
//     → finalize-settlement            ✅
//
// Run: `make dev` running, then `node smoke-settlement-recovery-http.js`.
// Requires: backend on :8080 with ENV=development (dev endpoints gated).

const BACKEND = process.env.BACKEND || 'http://localhost:8080'
const V1 = `${BACKEND}/api/v1`

let passed = 0
function ok(cond, msg) {
  if (!cond) throw new Error(`ASSERT FAILED: ${msg}`)
  passed++
  console.log(`  ✓ ${msg}`)
}

async function req(method, path, { token, body } = {}) {
  const headers = { 'Content-Type': 'application/json' }
  if (token) headers.Authorization = `Bearer ${token}`
  const r = await fetch(`${V1}${path}`, { method, headers, body: body ? JSON.stringify(body) : undefined })
  let json = null
  try { json = await r.json() } catch { /* empty body */ }
  return { status: r.status, json }
}

async function login() {
  for (const pw of ['admin1234', 'admin123']) {
    const r = await req('POST', '/auth/login', { body: { username: 'admin', password: pw } })
    if (r.status !== 200 || !r.json?.data?.access_token) continue
    let token = r.json.data.access_token
    if (r.json.data.must_change_password) {
      await req('POST', '/auth/change-password', { token, body: { current_password: pw, new_password: 'admin1234' } })
      const r2 = await req('POST', '/auth/login', { body: { username: 'admin', password: 'admin1234' } })
      token = r2.json.data.access_token
    }
    return token
  }
  throw new Error('login failed (tried admin1234 / admin123)')
}

// currentSettlementBill returns the single non-VOID settlement bill (with line
// items) for the contract.
async function currentSettlementBill(token, contractID) {
  const list = await req('GET', `/bills?contract_id=${contractID}&bill_type=SETTLEMENT&limit=50`, { token })
  const live = (list.json?.data || []).filter((b) => b.status !== 'VOID')
  ok(live.length === 1, `exactly one live settlement bill (got ${live.length})`)
  const detail = await req('GET', `/bills/${live[0].id}`, { token })
  ok(detail.status === 200, 'GET settlement bill detail 200')
  return detail.json.data
}

function recoveryRefundLines(bill) {
  return (bill.line_items || []).filter((li) => li.line_type === 'ADJUSTMENT' && li.adjustment_recovery_reading_id)
}

async function assertRefund(token, fx, label) {
  const bill = await currentSettlementBill(token, fx.contract_id)
  const refunds = recoveryRefundLines(bill)
  ok(refunds.length === 1, `${label}: exactly 1 recovery refund line (got ${refunds.length})`)
  const r = refunds[0]
  const wantAmount = fx.expected_refund / 100 // satang → baht
  const wantRate = fx.source_electricity_rate / 100
  ok(r.amount === wantAmount, `${label}: refund amount = ฿${r.amount} (want ฿${wantAmount}, SOURCE rate)`)
  ok(r.unit_price === wantRate, `${label}: refund unit_price = ฿${r.unit_price} (want ฿${wantRate}, source not contract rate)`)
  ok(r.quantity === 300, `${label}: over-record quantity = ${r.quantity} (want 300)`)
  ok(r.adjustment_utility === 'ELECTRICITY', `${label}: refund utility = ${r.adjustment_utility}`)
}

async function main() {
  console.log('Epic B — Settlement Recovery HTTP smoke\n')
  const token = await login()
  console.log('logged in\n')

  // Fresh, re-runnable fixture.
  const setup = await req('POST', '/dev/smoke/settlement-recovery-setup', { token })
  ok(setup.status === 200, 'dev fixture setup 200')
  const fx = setup.json.data
  const apt = fx.apartment_id
  const notice = fx.notice_id

  console.log('\nLEG 1 — D1 regenerate resolution')
  // 1. Generate the settlement DRAFT (no recovery yet).
  const gen = await req('POST', `/move-out-notices/${notice}/generate-settlement`, { token, body: { rent_mode: 'FULL_MONTH_KEEP_DEPOSIT' } })
  ok(gen.status === 200 || gen.status === 201, `generate-settlement ${gen.status}`)

  // 2. Operator records the true reading (1200) as a recovery from the mis-read
  //    source (1500). Server derives the over-record; water sent unchanged (clean).
  const corr = await req('POST', `/apartments/${apt}/meter-readings/baseline-corrections`, {
    token,
    body: {
      source_reading_id: fx.source_reading_id,
      room_id: fx.room_id,
      electricity_current: fx.physical_electricity,
      water_current: fx.physical_water,
      anchor_note: 'จดค่าไฟเดือนก่อนเกินจริง แก้เป็นค่าที่อ่านได้',
    },
  })
  ok(corr.status === 200 || corr.status === 201, `baseline-correction ${corr.status}`)

  // 3. Finalize is BLOCKED by the shared freshness gate — the D1 symptom.
  const blocked = await req('POST', `/move-out-notices/${notice}/finalize-settlement`, { token })
  ok(blocked.status === 400, `finalize blocked by freshness gate (${blocked.status})`)

  // 4. Regenerate discovers the recovery and emits the refund.
  const regen = await req('POST', `/move-out-notices/${notice}/regenerate-settlement`, { token, body: { rent_mode: 'FULL_MONTH_KEEP_DEPOSIT' } })
  ok(regen.status === 200 || regen.status === 201, `regenerate-settlement ${regen.status}`)
  await assertRefund(token, fx, 'after regenerate')

  // 5. Finalize now succeeds — the gate is cleared.
  const fin = await req('POST', `/move-out-notices/${notice}/finalize-settlement`, { token })
  ok(fin.status === 200 || fin.status === 201, `finalize after regenerate ${fin.status}`)

  console.log('\nLEG 2 — Correct (post-FINALIZED) re-emits the refund')
  // 6. Correcting the FINALIZED settlement voids it (its refund line goes to a
  //    VOID bill → recovery unreflected again) and the new DRAFT re-emits the refund.
  const correct = await req('POST', `/move-out-notices/${notice}/correct-settlement`, { token, body: { correction_reason: 'ตรวจสอบยอดปิดสัญญาใหม่หลังแก้มิเตอร์' } })
  ok(correct.status === 200 || correct.status === 201, `correct-settlement ${correct.status}`)
  await assertRefund(token, fx, 'after correct')

  // 7. Finalize the corrected settlement.
  const fin2 = await req('POST', `/move-out-notices/${notice}/finalize-settlement`, { token })
  ok(fin2.status === 200 || fin2.status === 201, `finalize after correct ${fin2.status}`)

  console.log(`\n✅ PASS — ${passed} assertions`)
}

main().catch((e) => {
  console.error(`\n❌ ${e.message}`)
  process.exit(1)
})
