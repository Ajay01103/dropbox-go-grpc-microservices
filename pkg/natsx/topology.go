package natsx

import (
	"context"
	"fmt"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/Ajay01103/go-dropbox/pkg/events"
)

// retentionOf translates the NATS-free policy constants in pkg/events.
func retentionOf(p events.RetentionPolicy) jetstream.RetentionPolicy {
	switch p {
	case events.RetentionWorkQueue:
		return jetstream.WorkQueuePolicy
	case events.RetentionInterest:
		return jetstream.InterestPolicy
	default:
		return jetstream.LimitsPolicy
	}
}

// retentionByStream is the one place a stream's retention policy is decided.
// Keys are stream names from events.StreamFilters.
func retentionByStream(name string) events.RetentionPolicy {
	switch name {
	case events.StreamCmd:
		return events.RetentionWorkQueue
	default:
		return events.RetentionLimits
	}
}

// EnsureTopology idempotently creates the streams declared in
// events.StreamFilters. It is the SOLE topology owner: both services call it
// at boot so start order never matters.
func EnsureTopology(ctx context.Context, js jetstream.JetStream, replicas int) error {
	for name, subjects := range events.StreamFilters {

		cfg := jetstream.StreamConfig{
			Name:         name,
			Subjects:     subjects,
			Retention:    retentionOf(retentionByStream(name)),
			Storage:      jetstream.FileStorage,
			MaxAge:       7 * 24 * time.Hour,
			Duplicates:   time.Hour,
			Replicas:     replicas,
			MaxConsumers: -1,
			Discard:      jetstream.DiscardOld,
		}
		if name == events.StreamDLQ {
			cfg.MaxAge = 14 * 24 * time.Hour
			cfg.Duplicates = 0
		}

		if _, err := js.CreateOrUpdateStream(ctx, cfg); err != nil {
			return fmt.Errorf("natsx: ensure stream %s: %w", cfg.Name, err)
		}
	}
	return nil
}

// Consumer specs for the v2 topology (Part B.4). Each consumer is created by
// the service that runs it via CreateOrUpdateConsumer. MaxDeliver must be
// greater than len(BackOff); an explicit NakWithDelay overrides BackOff.
var (
	// ThumbnailConsumer runs in the metadata service on FILE_EVENTS.
	ThumbnailConsumer = jetstream.ConsumerConfig{
		Durable:       events.ConsumerThumbnailV2,
		FilterSubject: events.SubjFileStored,
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       2 * time.Minute,
		MaxDeliver:    6,
		MaxAckPending: 8,
		BackOff: []time.Duration{
			5 * time.Second, 30 * time.Second, 2 * time.Minute, 10 * time.Minute, 30 * time.Minute,
		},
	}

	// DecrefConsumer runs in the upload service on BLOCK_REFS_CMD.
	//
	// RE-DERIVED FOR BATCHING: a batch of up to
	// 500 ops × one LWT each is a 10–25s handler (worst case ~2m), so the
	// prefetch trap applies — with a deep prefetch the server starts every
	// delivered message's AckWait timer at delivery and queued batches age
	// out before the serial callback reaches them. Hence:
	//   - MaxAckPending 4 (a deep prefetch would age queued batches out),
	//   - consumers run with natsx.WithPullMaxMessages(2) + WithHeartbeat(30s)
	//     so a slow batch extends its own ack deadline,
	//   - BackOff[0] = staleAfter + 5s built at runtime (BLOCK_LEDGER_STALE,
	//     90s) so redelivery sees a stale claim it can take over.
	DecrefConsumer = jetstream.ConsumerConfig{
		Durable:       events.ConsumerDecrefV2,
		FilterSubject: events.SubjDecrefRequested,
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       2 * time.Minute,
		MaxDeliver:    10,
		MaxAckPending: 4,
		BackOff: []time.Duration{
			95 * time.Second, 5 * time.Minute, 15 * time.Minute, time.Hour,
		},
	}

	// PurgeCompletionConsumer runs in the metadata service on BLOCK_REFS_EVT.
	PurgeCompletionConsumer = jetstream.ConsumerConfig{
		Durable:       events.ConsumerPurgeCompletionV2,
		FilterSubject: events.SubjDecrefCompleted,
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       2 * time.Minute,
		MaxDeliver:    8,
		MaxAckPending: 16,
		BackOff: []time.Duration{
			2 * time.Second, 10 * time.Second, time.Minute, 5 * time.Minute, 15 * time.Minute,
		},
	}
)
