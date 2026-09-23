package repository

import "testing"

// TestParsePurgeState pins the B4 legacy-state safety net: the two pre-B4
// state strings map onto their modern equivalents so a job the pre-deploy
// backfill missed is handled consistently by Get (normalizes on read), the
// sweeper and the purge-status API.
//
//   - DECREMENTED_WITH_ERRORS → DECREMENTED: v2 collapses error detail into
//     invalid_ops; the coordinator re-runs metadata removal either way.
//   - METADATA_REMOVED → COMPLETE: the only remaining step was re-marking the
//     state; metadata removal itself is idempotent.
func TestParsePurgeState(t *testing.T) {
	cases := []struct{ in, want string }{
		{"DECREMENTED_WITH_ERRORS", PurgeDecremented},
		{"METADATA_REMOVED", PurgeComplete},
		{"PENDING", PurgePending},
		{"DECREMENT_REQUESTED", PurgeDecrementRequested},
		{"DECREMENTED", PurgeDecremented},
		{"COMPLETE", PurgeComplete},
		{"FAILED", PurgeFailed},
		{"SOMETHING_ELSE", "SOMETHING_ELSE"}, // unknown passes through
		{"", ""},
	}
	for _, tc := range cases {
		if got := ParsePurgeState(tc.in); got != tc.want {
			t.Errorf("ParsePurgeState(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
