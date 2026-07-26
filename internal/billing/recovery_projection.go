package billing

import "nana/internal/meterreading"

// ProjectRecoveryUsageOverlay builds the per-utility billing input for a month
// where a READING_RECOVERY anchor coexists with a real consumption reading.
//
// Owner lock (2026-07-18): recovery authority is UTILITY-SCOPED. An electricity
// recovery may override the electricity dimension only; it must NOT suppress the
// real monthly reading for water (and mirror for water). Recovery is a
// utility-scoped overlay, not a whole-reading-row winner.
//
// The returned reading is a COPY of `recovery` — it keeps the recovery row's
// identity + metadata (ID, AnchorReason, RecoverySourceReadingID, *_recorded), so
// the ADJUSTMENT FK, reconciliation resolver, and lineage are unchanged. For each
// utility the recovery does NOT affect, previous/current are copied from the real
// consumption row so that utility bills its genuine usage. The affected utility
// keeps the anchor's re-baselined prev==current (usage 0) and its recorded value
// (→ source-priced refund via the untouched resolver).
//
// Combines ONLY the recovery row and a real non-anchor MONTHLY consumption row
// for the same room+month — no heuristic fallback to another month or reading
// type. No-op (returns `recovery` unchanged) when `recovery` is not a recovery
// anchor or `consumption` is nil. Pure; never persisted.
func ProjectRecoveryUsageOverlay(recovery, consumption *meterreading.MeterReading) *meterreading.MeterReading {
	if recovery == nil || consumption == nil {
		return recovery
	}
	if recovery.AnchorReason == nil ||
		*recovery.AnchorReason != meterreading.AnchorReasonReadingRecovery {
		return recovery
	}

	projected := *recovery // shallow copy — preserves ID + recovery metadata

	// Electricity: unaffected → adopt the real consumption usage. "Affected" is
	// the SAME predicate the resolver + finalize gate use (IsAffected), so the
	// overlay scope can never drift from the refund scope.
	if !(recovery.ElectricityRecorded != nil &&
		IsAffected(int64(*recovery.ElectricityRecorded), int64(recovery.ElectricityCurrent))) {
		projected.ElectricityPrevious = consumption.ElectricityPrevious
		projected.ElectricityCurrent = consumption.ElectricityCurrent
	}
	// Water: unaffected → adopt the real consumption usage.
	if !(recovery.WaterRecorded != nil &&
		IsAffected(int64(*recovery.WaterRecorded), int64(recovery.WaterCurrent))) {
		projected.WaterPrevious = consumption.WaterPrevious
		projected.WaterCurrent = consumption.WaterCurrent
	}
	return &projected
}
