package repository

import "testing"

// TestIndexedStitchState pins the work-index membership rule the sweeper's
// ListPending query depends on: only states that carry work are published to
// stitched_objects_by_state. 'ready' is finished and 'skipped' is permanently
// ineligible for the file's current layout, so both leave the index — a parked
// file that kept an index row would be re-read (and re-logged) on every sweep.
func TestIndexedStitchState(t *testing.T) {
	cases := []struct {
		state StitchState
		want  bool
	}{
		{state: StitchStatePending, want: true},
		{state: StitchStateStitching, want: true},
		{state: StitchStateFailed, want: true},
		{state: StitchStateReady, want: false},
		{state: StitchStateSkipped, want: false},
		{state: StitchState(""), want: false},
	}
	for _, testCase := range cases {
		if got := indexedStitchState(testCase.state); got != testCase.want {
			t.Fatalf("indexedStitchState(%q) = %v, want %v", testCase.state, got, testCase.want)
		}
	}
}
