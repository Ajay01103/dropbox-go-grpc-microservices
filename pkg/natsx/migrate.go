package natsx

import (
	"context"
	"fmt"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/Ajay01103/go-dropbox/pkg/events"
)

// EnsureDLQ idempotently creates the DLQ stream used by the Consume wrapper's
// dead-lettering. Additive-only; safe to call from both services at boot.
func EnsureDLQ(ctx context.Context, js jetstream.JetStream) error {
	cfg := jetstream.StreamConfig{
		Name:      events.StreamDLQ,
		Subjects:  []string{events.DLQFilter},
		Retention: jetstream.LimitsPolicy,
		Storage:   jetstream.FileStorage,
		MaxAge:    14 * 24 * time.Hour,
		Discard:   jetstream.DiscardOld,
	}
	if _, err := js.CreateOrUpdateStream(ctx, cfg); err != nil {
		return fmt.Errorf("natsx: ensure %s stream: %w", events.StreamDLQ, err)
	}
	return nil
}

// EnsureStreamDuplicates updates a stream's dedup window in place.
// It is a READ-MODIFY-WRITE: it fetches the live StreamConfig and mutates
// only Duplicates. Constructing a fresh StreamConfig instead would either
// hard-error (Retention is not updatable) or silently reset MaxAge /
// MaxConsumers on the stream.
func EnsureStreamDuplicates(ctx context.Context, js jetstream.JetStream, stream string, window time.Duration) error {
	s, err := js.Stream(ctx, stream)
	if err != nil {
		return fmt.Errorf("natsx: stream %s: %w", stream, err)
	}
	info := s.CachedInfo()
	if info.Config.Duplicates == window {
		return nil
	}
	cfg := info.Config // full live config; only Duplicates changes
	cfg.Duplicates = window
	if _, err := js.UpdateStream(ctx, cfg); err != nil {
		return fmt.Errorf("natsx: update %s duplicates: %w", stream, err)
	}
	return nil
}

// EnsurePullConsumer returns the named durable as a pull consumer, creating
// it if absent and updating the updatable fields (AckWait, MaxDeliver,
// MaxAckPending, BackOff) in place when it exists. Delivery mode cannot be
// changed on an existing consumer, but every consumer in the v2 topology is
// created pull-based by this function, so nothing to migrate.
//
// target must be the COMPLETE desired ConsumerConfig — nothing is inherited
// from the existing consumer, so a partial config cannot silently produce
// MaxDeliver -1 or a 30s AckWait.
func EnsurePullConsumer(ctx context.Context, js jetstream.JetStream, stream, name string,
	target jetstream.ConsumerConfig) (jetstream.Consumer, error) {

	if target.Durable == "" {
		target.Durable = name
	}
	cons, err := js.CreateOrUpdateConsumer(ctx, stream, target)
	if err != nil {
		return nil, fmt.Errorf("natsx: ensure pull consumer %s/%s: %w", stream, name, err)
	}
	return cons, nil
}
