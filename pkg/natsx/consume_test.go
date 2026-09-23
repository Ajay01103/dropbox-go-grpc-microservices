package natsx

import (
	"errors"
	"testing"
	"time"

	"github.com/Ajay01103/go-dropbox/pkg/events"
)

func TestOutcomeConstructors(t *testing.T) {
	if got := Ack(); got.k != kAck {
		t.Errorf("Ack() kind = %v, want kAck", got.k)
	}

	err := errors.New("boom")
	r := Retry(err)
	if r.k != kRetry || r.err != err {
		t.Errorf("Retry() = %+v, want kind kRetry with err", r)
	}

	ra := RetryAfter(5*time.Second, err)
	if ra.k != kRetryAfter || ra.delay != 5*time.Second || ra.err != err {
		t.Errorf("RetryAfter() = %+v, want kind kRetryAfter, 5s delay, err", ra)
	}
	if got := RetryAfter(-1*time.Second, err); got.delay != 0 {
		t.Errorf("RetryAfter(-1s) delay = %v, want clamped to 0", got.delay)
	}

	tm := Term("bad payload")
	if tm.k != kTerm || tm.reason != "bad payload" {
		t.Errorf("Term() = %+v, want kind kTerm with reason", tm)
	}
}

func TestDLQSubject(t *testing.T) {
	if got, want := DLQSubject("FILE_EVENTS", "thumbnail-v2"), "dlq.FILE_EVENTS.thumbnail-v2"; got != want {
		t.Errorf("DLQSubject = %q, want %q", got, want)
	}
	if got, want := DLQSubject("BLOCK_REFS_CMD", "decref-v2"), "dlq.BLOCK_REFS_CMD.decref-v2"; got != want {
		t.Errorf("DLQSubject = %q, want %q", got, want)
	}
}

func TestConsumerSpecsFollowContract(t *testing.T) {
	// MaxDeliver must exceed len(BackOff), otherwise the last backoff tier is
	// never reached before exhaustion.

	// Kept simple and direct: check each spec explicitly.
	if ThumbnailConsumer.MaxDeliver <= len(ThumbnailConsumer.BackOff) {
		t.Errorf("ThumbnailConsumer MaxDeliver %d must be > len(BackOff) %d",
			ThumbnailConsumer.MaxDeliver, len(ThumbnailConsumer.BackOff))
	}
	if DecrefConsumer.MaxDeliver <= len(DecrefConsumer.BackOff) {
		t.Errorf("DecrefConsumer MaxDeliver %d must be > len(BackOff) %d",
			DecrefConsumer.MaxDeliver, len(DecrefConsumer.BackOff))
	}
	if PurgeCompletionConsumer.MaxDeliver <= len(PurgeCompletionConsumer.BackOff) {
		t.Errorf("PurgeCompletionConsumer MaxDeliver %d must be > len(BackOff) %d",
			PurgeCompletionConsumer.MaxDeliver, len(PurgeCompletionConsumer.BackOff))
	}

	if ThumbnailConsumer.Durable != events.ConsumerThumbnailV2 ||
		ThumbnailConsumer.FilterSubject != events.SubjFileStored {
		t.Errorf("ThumbnailConsumer wired to wrong durable/filter: %+v", ThumbnailConsumer)
	}
	if DecrefConsumer.Durable != events.ConsumerDecrefV2 ||
		DecrefConsumer.FilterSubject != events.SubjDecrefRequested {
		t.Errorf("DecrefConsumer wired to wrong durable/filter: %+v", DecrefConsumer)
	}
	if PurgeCompletionConsumer.Durable != events.ConsumerPurgeCompletionV2 ||
		PurgeCompletionConsumer.FilterSubject != events.SubjDecrefCompleted {
		t.Errorf("PurgeCompletionConsumer wired to wrong durable/filter: %+v", PurgeCompletionConsumer)
	}

	// Decref's first backoff (2m) must exceed the 90s stale claim window so a
	// redelivery can take over the claim (Part B.3).
	if DecrefConsumer.BackOff[0] <= 90*time.Second {
		t.Errorf("DecrefConsumer first BackOff = %v, want > 90s staleAfter", DecrefConsumer.BackOff[0])
	}
}
