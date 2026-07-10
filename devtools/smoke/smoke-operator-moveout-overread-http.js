// Operator end-to-end — exit-triggered over-read at move-out (F1+F2), HTTP.
//
// This is the first walk of the WHOLE operator workflow with NO seeded terminal
// state: the recovery, the exit reading, and the settlement are all produced by
// real endpoints. It proves F2 (a same-month recovery + exit now coexist) end to
// end, through handler → service → repo → DB → response:
//
//   dev fixture: PENDING_METER move-out + a PAID mis-read source month (no exit,
//                no recovery)
//     → baseline-correction   (operator re-anchors to the true reading)
//     → record-exit-meter      (SAME month — was blocked pre-F2, now ✅)
//     → generate-settlement
//     → GET bill: assert the source-priced refund
//     → finalize-settlement    ✅
//
// Run: `make dev` running, then `node smoke-operator-moveout-overread-http.js`.

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
  try { json = await r.json() } catch { /* empty */ }
  return { status: r.status, json }
}

async function login() {
  for (const pw of ['admin1234', 'admin123']) {
    const r = await req('POST', '/auth/login', { body: { username: 'admin', password: pw } })
    if (r.status !== 200 || !r.json?.data?.access_token) continue
    let token = r.json.data.access_token
    if (r.json.data.must_change_password) {
      await req('POST', '/auth/change-password', { token, body: { current_password: pw, new_password: 'admin1234' } })
      token = (await req('POST', '/auth/login', { body: { username: 'admin', password: 'admin1234' } })).json.data.access_token
    }
    return token
  }
  throw new Error('login failed')
}

async function currentSettlement(token, contractID) {
  const list = await req('GET', `/bills?contract_id=${contractID}&bill_type=SETTLEMENT&limit=50`, { token })
  const live = (list.json?.data || []).filter((b) => b.status !== 'VOID')
  ok(live.length === 1, `exactly one live settlement bill (got ${live.length})`)
  const detail = await req('GET', `/bills/${live[0].id}`, { token })
  ok(detail.status === 200, 'GET settlement detail 200')
  return detail.json.data
}

async function main() {
  console.log('Operator end-to-end — exit-triggered over-read at move-out (F1+F2)\n')
  const token = await login()
  console.log('logged in\n')

  const setup = await req('POST', '/dev/smoke/moveout-overread-setup', { token })
  ok(setup.status === 200, 'dev fixture setup 200 (PENDING_METER + PAID mis-read source, no exit/recovery)')
  const fx = setup.json.data

  console.log('\nStep 1 — operator re-anchors the mis-read via a Reading Recovery')
  const corr = await req('POST', `/apartments/${fx.apartment_id}/meter-readings/baseline-corrections`, {
    token,
    body: {
      source_reading_id: fx.source_reading_id,
      room_id: fx.room_id,
      electricity_current: fx.physical_electricity, // the true (lower) reading
      water_current: fx.physical_water,
      anchor_note: 'ย้ายออกแล้วพบว่าเดือนก่อนจดไฟเกิน แก้เป็นค่าจริง',
    },
  })
  ok(corr.status === 200 || corr.status === 201, `baseline-correction ${corr.status} (recovery, billing_month = now)`)

  console.log('\nStep 2 — operator records the EXIT meter in the SAME month (the F2 payoff)')
  const exit = await req('POST', `/move-out-notices/${fx.notice_id}/record-exit-meter`, {
    token,
    body: {
      actual_move_out_date: new Date().toISOString().slice(0, 10),
      electricity_current: fx.physical_electricity, // chains off the re-anchor → usage 0
      water_current: fx.physical_water,
    },
  })
  ok(exit.status === 200 || exit.status === 201, `record-exit-meter ${exit.status} — coexists with the same-month recovery (was blocked pre-F2)`)

  console.log('\nStep 3 — generate + finalize the settlement')
  const gen = await req('POST', `/move-out-notices/${fx.notice_id}/generate-settlement`, { token, body: { rent_mode: 'FULL_MONTH_KEEP_DEPOSIT' } })
  ok(gen.status === 200 || gen.status === 201, `generate-settlement ${gen.status}`)

  const bill = await currentSettlement(token, fx.contract_id)
  const refunds = (bill.line_items || []).filter((li) => li.line_type === 'ADJUSTMENT' && li.adjustment_recovery_reading_id)
  ok(refunds.length === 1, `exactly 1 recovery refund line (got ${refunds.length})`)
  const wantBaht = fx.expected_refund / 100
  ok(refunds[0].amount === wantBaht, `refund = ฿${refunds[0].amount} (want ฿${wantBaht}, source rate)`)
  ok(refunds[0].unit_price === fx.source_electricity_rate / 100, `refund unit_price = ฿${refunds[0].unit_price} (source rate)`)

  const fin = await req('POST', `/move-out-notices/${fx.notice_id}/finalize-settlement`, { token })
  ok(fin.status === 200 || fin.status === 201, `finalize-settlement ${fin.status} — full workflow closed`)

  console.log(`\n✅ PASS — ${passed} assertions (no terminal state seeded; recovery + exit + settlement all created via real endpoints)`)
}

main().catch((e) => {
  console.error(`\n❌ ${e.message}`)
  process.exit(1)
})
