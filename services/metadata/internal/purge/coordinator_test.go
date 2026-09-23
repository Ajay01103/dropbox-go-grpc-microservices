package purge

import (
	"testing"
)

// TestOrphanCompletionOutcome pins the B1 orphan-ack policy: the first
// orphanNakThreshold deliveries of a completion with no matching job row are
// retried after orphanNakDelay (visibility-race window), then acked for good
// — ending the infinite redelivery storm.
func TestOrphanCompletionOutcome(t *testing.T) {
	for d := 1; d <= orphanNakThreshold; d++ {
		out := orphanCompletionOutcome(d)
		if out.Kind() != "retry_after" {
			t.Fatalf("delivery %d: want retry_after, got %s", d, out.Kind())
		}
		if out.Delay() != orphanNakDelay {
			t.Fatalf("delivery %d: want delay %v, got %v", d, orphanNakDelay, out.Delay())
		}
	}
	for _, d := range []int{orphanNakThreshold + 1, 20, 1000} {
		if out := orphanCompletionOutcome(d); out.Kind() != "ack" {
			t.Fatalf("delivery %d: want ack, got %s", d, out.Kind())
		}
	}
	if out := orphanCompletionOutcome(0); out.Kind() != "retry_after" {
		t.Fatalf("delivery 0 (metadata missing): want retry_after, got %s", out.Kind())
	}
}
