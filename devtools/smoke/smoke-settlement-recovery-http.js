// Epic B — Settlement Recovery (Model B): HTTP smoke.
//
// Locks the OWNER-DECIDED Model B ontology (observe-once-at-exit,
// EPIC_B_SETTLEMENT_RECOVERY_MODELB_ONTOLOGY_SCOPE.md) through the REAL
// production chain (handler → service → resolver → repo → Postgres → JSON),
// no browser.
//
// The operator observes the meter ONCE at move-out and records it via
// record-exit-meter with the over-record flag ("เดือนก่อนจดเกิน"). The move-out
// flow re-anchors from that single observation (a READING_RECOVERY event created
// BEFORE the exit reading — §0.1), so the exit bills 0 usage on the corrected
// utility and the UNCHANGED settlement resolver refunds recorded − now. No
// reconstructed "true value last month"; no separate baseline-correction step;
// no D1 regenerate dance (the recovery exists before generate).
//
//   record-exit-meter (elec=1240, is_electricity_over_record) → anchor 1240 + exit 0-usage
//     → generate-settlement       (resolver emits refund = 1500−1240 = 260u)
//     → GET bill: elec usage 0 · refund −฿2,080 @ source rate · water bills normally
//     → finalize-settlement        ✅ (gate cleared; recovery reflected)
//
// Also asserts the mid-cycle timeline is HONORED (owner point 3): a real earlier
// baseline-correction is a distinct observation → exit bills real usage + that
// recovery's refund; Model B does not rewrite it.
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

async function liveSettlementBill(token, contractID) {
  const list = await req('GET', `/bills?contract_id=${contractID}&bill_type=SETTLEMENT&limit=50`, { token })
  const live = (list.json?.data || []).filter((b) => b.status !== 'VOID')
  ok(live.length === 1, `exactly one live settlement bill (got ${live.length})`)
  const detail = await req('GET', `/bills/${live[0].id}`, { token })
  ok(detail.status === 200, 'GET settlement bill detail 200')
  return detail.json.data
}

const lineOf = (bill, type, util) =>
  (bill.line_items || []).find((li) => li.line_type === type && (util === undefined || li.adjustment_utility === util))

function moveOutDate() {
  // 3 days ago, matching the fixture's ScheduledMoveOutDate window.
  return new Date(Date.now() - 3 * 86400000).toISOString().slice(0, 10)
}

async function main() {
  console.log('Epic B — Settlement Recovery Model B HTTP smoke\n')
  const token = await login()
  console.log('logged in\n')

  console.log('LEG 1 — Model B: over-record surfaced at move-out (single observation)')
  const fx = (await req('POST', '/dev/smoke/settlement-recovery-setup', { token })).json.data
  ok(!!fx?.notice_id, 'dev fixture setup (notice PENDING_METER, no pre-recorded exit)')
  const rate = fx.source_electricity_rate / 100

  // The operator reads the meter ONCE (1240) and flags the electricity over-record.
  const rec = await req('POST', `/move-out-notices/${fx.notice_id}/record-exit-meter`, {
    token,
    body: {
      actual_move_out_date: moveOutDate(),
      electricity_current: fx.exit_observation_electricity, // 1240 (below the wrong 1500 → over-record)
      water_current: fx.exit_observation_water,             // 228 (water not over-recorded)
      is_electricity_over_record: true,
    },
  })
  ok(rec.status === 200, `record-exit-meter with over-record flag ${rec.status}`)

  const gen = await req('POST', `/move-out-notices/${fx.notice_id}/generate-settlement`, { token, body: { rent_mode: 'FULL_MONTH_KEEP_DEPOSIT' } })
  ok(gen.status === 200 || gen.status === 201, `generate-settlement ${gen.status}`)

  const bill = await liveSettlementBill(token, fx.contract_id)
  const elec = lineOf(bill, 'ELECTRICITY')
  const refund = lineOf(bill, 'ADJUSTMENT', 'ELECTRICITY')
  const water = lineOf(bill, 'WATER')
  ok(elec && elec.quantity === 0 && elec.amount === 0, `electricity usage line re-anchored to 0 (got qty=${elec?.quantity} ฿${elec?.amount})`)
  ok(!!refund, 'recovery refund ADJUSTMENT line present')
  const wantRefund = fx.expected_refund / 100 // satang → baht
  ok(refund.amount === wantRefund, `refund = ฿${refund.amount} (want ฿${wantRefund} = -(recorded−observed)×rate)`)
  ok(refund.unit_price === rate, `refund unit_price = ฿${refund.unit_price} (source rate)`)
  ok(refund.quantity === fx.source_recorded_electricity - fx.exit_observation_electricity,
    `over-record quantity = ${refund.quantity} (recorded ${fx.source_recorded_electricity} − observed ${fx.exit_observation_electricity})`)
  ok(refund.adjustment_utility === 'ELECTRICITY', `refund utility = ${refund.adjustment_utility}`)
  ok(!!water && water.quantity > 0, `water bills normally from carried baseline (qty=${water?.quantity})`)

  const fin = await req('POST', `/move-out-notices/${fx.notice_id}/finalize-settlement`, { token })
  ok(fin.status === 200 || fin.status === 201, `finalize after generate ${fin.status} (gate cleared, no D1 dance)`)

  console.log('\nLEG 2 — mid-cycle recovery HONORED (owner point 3: distinct observation, not rewritten)')
  const fx2 = (await req('POST', '/dev/smoke/settlement-recovery-setup', { token })).json.data
  // A real earlier observation (1200) via the standalone baseline-correction.
  const corr = await req('POST', `/apartments/${fx2.apartment_id}/meter-readings/baseline-corrections`, {
    token,
    body: { source_reading_id: fx2.source_reading_id, room_id: fx2.room_id, electricity_current: 1200, water_current: 220, anchor_note: 'mid-cycle: จดผิดพบกลางเดือน แก้เป็น 1200' },
  })
  ok(corr.status === 200 || corr.status === 201, `mid-cycle baseline-correction ${corr.status}`)
  // Later, at move-out, the meter reads 1240 — ABOVE the corrected 1200 → normal usage, NO over-record flag.
  const rec2 = await req('POST', `/move-out-notices/${fx2.notice_id}/record-exit-meter`, {
    token,
    body: { actual_move_out_date: moveOutDate(), electricity_current: 1240, water_current: 228 },
  })
  ok(rec2.status === 200, `record-exit-meter (1240 > corrected 1200, no flag) ${rec2.status}`)
  await req('POST', `/move-out-notices/${fx2.notice_id}/generate-settlement`, { token, body: { rent_mode: 'FULL_MONTH_KEEP_DEPOSIT' } })
  const bill2 = await liveSettlementBill(token, fx2.contract_id)
  const elec2 = lineOf(bill2, 'ELECTRICITY')
  const refund2 = lineOf(bill2, 'ADJUSTMENT', 'ELECTRICITY')
  ok(elec2.quantity === 40, `mid-cycle: exit bills REAL usage 40u (1200→1240) — Model A correct here (got ${elec2.quantity})`)
  ok(refund2.quantity === 300 && refund2.amount === -2400, `mid-cycle refund 300u −฿2,400 (recorded−midcycle, not rewritten) (got ${refund2.quantity}u ฿${refund2.amount})`)

  console.log(`\n✅ PASS — ${passed} assertions`)
}

main().catch((e) => {
  console.error(`\n❌ ${e.message}`)
  process.exit(1)
})
