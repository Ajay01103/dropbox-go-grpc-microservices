package main

import "testing"

// TestMapState pins the legacy-state mapping the backfill applies so the
// sweeper (B2b) and the v2 coordinator never see states they don't know:
//
//	DECREMENTED_WITH_ERRORS -> DECREMENTED (coordinator re-runs metadata
//	                          removal idempotently; error detail survives in
//	                          has_reconciliation_errors, which is kept)
//	METADATA_REMOVED        -> COMPLETE    (removeMetadataAndComplete already ran)
func TestMapState(t *testing.T) {
	cases := map[string]struct {
		want   string
		mapped bool
	}{
		"DECREMENTED_WITH_ERRORS": {"DECREMENTED", true},
		"METADATA_REMOVED":        {"COMPLETE", true},
		"PENDING":                 {"PENDING", false},
		"DECREMENT_REQUESTED":     {"DECREMENT_REQUESTED", false},
		"DECREMENTED":             {"DECREMENTED", false},
		"COMPLETE":                {"COMPLETE", false},
		"FAILED":                  {"FAILED", false},
	}
	for state, tc := range cases {
		got, mapped := mapState(state)
		if got != tc.want || mapped != tc.mapped {
			t.Errorf("mapState(%q) = (%q, %v), want (%q, %v)", state, got, mapped, tc.want, tc.mapped)
		}
	}
}

// TestLegacyStateMapCoversAllBuckets ensures the scanner's state list and the
// mapping stay in sync — every scanned state is either mappable or already a
// v2 state.
func TestLegacyStateMapCoversAllBuckets(t *testing.T) {
	v2States := map[string]bool{
		"PENDING": true, "DECREMENT_REQUESTED": true, "DECREMENTED": true,
		"COMPLETE": true, "FAILED": true,
	}
	for _, state := range states {
		target, _ := mapState(state)
		if !v2States[target] {
			t.Errorf("state %q maps to %q which is not a v2 state", state, target)
		}
	}
}
