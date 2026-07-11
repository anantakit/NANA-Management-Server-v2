// F1 — Exit-meter rollover / replacement: full operator-to-bill HTTP smoke.
//
// Proves the ENTIRE chain through the real production stack (no browser):
//
//   operator HTTP action (record-exit-meter with the hardware flag)
//     → DTO binding (the 4 flags decode from JSON)
//     → MeterReading semantics (rollover digit-wrap / replacement zeroes previous)
//     → settlement generation (exitReading.ElectricityUsed() → the meter line)
//     → persisted bill lines (quantity / unit_price / amount / meter_previous)
//     → final bill total → finalize
//
// A narrow "did the flag persist?" check is deliberately NOT what this is — F1 is
// only end-to-end covered when the flag changes the REAL settlement bill the tenant
// would pay. Two scenarios:
//
//   Rollover:    prior MONTHLY elec 99000 → record EXIT current 500 + rollover flag.
//                current < previous is ACCEPTED; usage wraps to (99999-99000)+500=1499.
//                Settlement electricity line: quantity 1499, priced at the rate.
//   Replacement: record EXIT current 45 + replaced flag → previous zeroed → usage 45.
//                Settlement electricity line: quantity 45, meter_previous 0.
//
// Run: `make dev` running, then `make smoke-exit-meter-flags`
//   (node smoke-exit-meter-flags-http.js). Requires ENV=development (dev endpoints).

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

function lineByType(bill, type) {
  return (bill.line_items || []).find((li) => li.line_type === type)
}

// round2 avoids float dust when reconstructing baht amounts (satang/100).
function round2(n) {
  return Math.round(n * 100) / 100
}

// runScenario drives one case operator-action → bill and asserts the whole chain.
async function runScenario(token, fx, c, label, flagField) {
  console.log(`\n${label}`)
  const elecRate = fx.electricity_rate / 100 // satang → baht per unit
  const waterRate = fx.water_rate / 100

  // 1. Operator records the EXIT meter WITH the hardware flag. For rollover this
  //    is current < previous — pre-F1 the exit-create path could not express it
  //    and this would have been rejected.
  const exit = await req('POST', `/move-out-notices/${c.notice_id}/record-exit-meter`, {
    token,
    body: {
      actual_move_out_date: c.move_out_date,
      electricity_current: c.exit_electricity_current,
      water_current: c.exit_water_current,
      [flagField]: true,
    },
  })
  ok(exit.status === 200 || exit.status === 201, `record-exit-meter ${exit.status} (flag ${flagField})`)

  // 2. Generate the settlement DRAFT (FULL_MONTH for a deterministic rent line).
  const gen = await req('POST', `/move-out-notices/${c.notice_id}/generate-settlement`, {
    token, body: { rent_mode: 'FULL_MONTH_KEEP_DEPOSIT' },
  })
  ok(gen.status === 200 || gen.status === 201, `generate-settlement ${gen.status}`)

  // 3. Inspect the REAL persisted bill — this is where flag → usage → money is proven.
  const bill = await currentSettlementBill(token, c.contract_id)

  const elec = lineByType(bill, 'ELECTRICITY')
  ok(!!elec, 'settlement bill has an ELECTRICITY line')
  ok(elec.quantity === c.expected_electricity_usage,
    `electricity quantity = ${elec.quantity} units (want ${c.expected_electricity_usage} — flag calc)`)
  ok(elec.unit_price === elecRate,
    `electricity unit_price = ฿${elec.unit_price} (want ฿${elecRate})`)
  ok(elec.amount === round2(c.expected_electricity_usage * elecRate),
    `electricity amount = ฿${elec.amount} (want ฿${round2(c.expected_electricity_usage * elecRate)} = ${c.expected_electricity_usage}×${elecRate})`)
  ok(elec.meter_previous === c.expected_electricity_previous,
    `electricity meter_previous = ${elec.meter_previous} (want ${c.expected_electricity_previous})`)
  ok(elec.meter_current === c.exit_electricity_current,
    `electricity meter_current = ${elec.meter_current} (want ${c.exit_electricity_current})`)

  const water = lineByType(bill, 'WATER')
  ok(!!water, 'settlement bill has a WATER line (clean control)')
  ok(water.quantity === c.expected_water_usage,
    `water quantity = ${water.quantity} units (want ${c.expected_water_usage})`)
  ok(water.amount === round2(c.expected_water_usage * waterRate),
    `water amount = ฿${water.amount} (want ฿${round2(c.expected_water_usage * waterRate)})`)

  // 4. Total must aggregate every line (chain integrity — the flag-driven meter
  //    line is really in the bill total the tenant pays).
  const sum = round2((bill.line_items || []).reduce((acc, li) => acc + li.amount, 0))
  ok(bill.total_amount === sum,
    `bill total ฿${bill.total_amount} = Σ line amounts ฿${sum}`)

  // 5. The settlement must be issuable end-to-end.
  const fin = await req('POST', `/move-out-notices/${c.notice_id}/finalize-settlement`, { token })
  ok(fin.status === 200 || fin.status === 201, `finalize-settlement ${fin.status}`)
}

async function main() {
  console.log('F1 — Exit-meter rollover/replacement full-flow HTTP smoke\n')
  const token = await login()
  console.log('logged in')

  const setup = await req('POST', '/dev/smoke/exit-meter-flags-setup', { token })
  ok(setup.status === 200, 'dev fixture setup 200')
  const fx = setup.json.data

  await runScenario(token, fx, fx.rollover, 'SCENARIO 1 — rollover at move-out', 'is_electricity_meter_rollover')
  await runScenario(token, fx, fx.replacement, 'SCENARIO 2 — meter replacement at move-out', 'is_electricity_meter_replaced')

  console.log(`\n✅ PASS — ${passed} assertions`)
}

main().catch((e) => {
  console.error(`\n❌ ${e.message}`)
  process.exit(1)
})
