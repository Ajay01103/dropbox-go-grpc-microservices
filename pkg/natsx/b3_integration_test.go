//go:build integration

// B3 decref-v2 processing-profile probe. The v2 decrement worker processes
// BATCHES of up to 500 ops per message (10-25s+ per batch), so v1's consumer
// tuning (MaxAckPending 16, no prefetch cap) would let batch-messages sit in
// the queue past their AckWait timers and redeliver unprocessed — the exact
// B1 bug class, freshly introduced by batching. These probes pin the amended
// profile: PullMaxMessages(2) + heartbeat + MaxAckPending(4), with
// BackOff[0] > worst-case batch time. Run with: go test -tags integration ./natsx/

package natsx

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// TestProbeBatchedSlowMessagesPrefetchTwoHeartbeat is the production decref-v2
// pairing scaled down: 4 "batch" messages, prefetch 2, MaxAckPending 4,
// heartbeat, and a handler that sleeps ~2x the base AckWait. Every message
// must be delivered and processed EXACTLY once — a queued message aging past
// AckWait without its heartbeat being honored would show up as a redelivery.
func TestProbeBatchedSlowMessagesPrefetchTwoHeartbeat(t *testing.T) {
	_, js, shutdown := startJetStreamServer(t)
	defer shutdown()
	ctx := context.Background()

	if _, err := js.CreateStream(ctx, jetstream.StreamConfig{
		Name: "PROBE_BATCH", Subjects: []string{"probe.batch"},
		Storage: jetstream.MemoryStorage,
	}); err != nil {
		t.Fatalf("create stream: %v", err)
	}
	cons, err := js.CreateOrUpdateConsumer(ctx, "PROBE_BATCH", jetstream.ConsumerConfig{
		Durable:       "probe-batch",
		FilterSubject: "probe.batch",
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       2 * time.Second,
		MaxAckPending: 4,
		BackOff:       []time.Duration{10 * time.Second, 30 * time.Second},
	})
	if err != nil {
		t.Fatalf("create consumer: %v", err)
	}

	var mu sync.Mutex
	seen := make(map[string]int) // msg id -> delivery count
	processed := make(map[string]bool)
	const total = 4
	allDone := make(chan struct{})
	var outstanding int

	for i := 0; i < 4; i++ {
		if _, err := js.Publish(ctx, "probe.batch", []byte(fmt.Sprintf("batch-%d", i))); err != nil {
			t.Fatalf("publish: %v", err)
		}
	}

	cc, err := Consume(ctx, cons, "probe-batch", nil, js,
		func(ctx context.Context, m jetstream.Msg) Outcome {
			id := string(m.Data())
			mu.Lock()
			seen[id]++
			outstanding++
			mu.Unlock()
			defer func() {
				mu.Lock()
				outstanding--
				done := outstanding == 0 && len(processed) == total
				mu.Unlock()
				if done {
					select {
					case <-allDone:
					default:
						close(allDone)
					}
				}
			}()
			time.Sleep(4 * time.Second) // 2x base AckWait — "slow batch"
			mu.Lock()
			processed[id] = true
			mu.Unlock()
			return Ack()
		},
		WithPullMaxMessages(2),        // production prefetch for slow batches
		WithHeartbeat(500*time.Millisecond), // scaled heartbeat
	)
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	defer cc.Stop()

	select {
	case <-allDone:
	case <-time.After(30 * time.Second):
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("timeout; deliveries: %v", seen)
	}

	// Allow a grace period for any spurious redelivery to surface.
	time.Sleep(6 * time.Second)
	mu.Lock()
	defer mu.Unlock()
	for id, n := range seen {
		if n != 1 {
			t.Errorf("message %s delivered %d times, want exactly 1 (spurious redelivery?)", id, n)
		}
	}
	if len(seen) != total {
		t.Errorf("saw %d distinct messages, want %d", len(seen), total)
	}
}

// TestProbeMaxAckPendingFourCapsInFlight verifies MaxAckPending is honored
// server-side: with 8 published messages, MaxAckPending 4, and handlers that
// block on a gate, at most 4 can be outstanding at once.
func TestProbeMaxAckPendingFourCapsInFlight(t *testing.T) {
	_, js, shutdown := startJetStreamServer(t)
	defer shutdown()
	ctx := context.Background()

	if _, err := js.CreateStream(ctx, jetstream.StreamConfig{
		Name: "PROBE_CAP", Subjects: []string{"probe.cap"},
		Storage: jetstream.MemoryStorage,
	}); err != nil {
		t.Fatalf("create stream: %v", err)
	}
	cons, err := js.CreateOrUpdateConsumer(ctx, "PROBE_CAP", jetstream.ConsumerConfig{
		Durable:       "probe-cap",
		FilterSubject: "probe.cap",
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       30 * time.Second,
		MaxAckPending: 4,
	})
	if err != nil {
		t.Fatalf("create consumer: %v", err)
	}

	release := make(chan struct{})
	var mu sync.Mutex
	handling := 0
	maxHandling := 0

	for i := 0; i < 8; i++ {
		if _, err := js.Publish(ctx, "probe.cap", []byte("m")); err != nil {
			t.Fatalf("publish: %v", err)
		}
	}

	cc, err := Consume(ctx, cons, "probe-cap", nil, js,
		func(ctx context.Context, m jetstream.Msg) Outcome {
			mu.Lock()
			handling++
			if handling > maxHandling {
				maxHandling = handling
			}
			mu.Unlock()
			<-release
			mu.Lock()
			handling--
			mu.Unlock()
			return Ack()
		},
		WithHeartbeat(200*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	defer cc.Stop()

	// Wait until the cap is reached, then sample for stability.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		reached := maxHandling >= 4
		mu.Unlock()
		if reached {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(500 * time.Millisecond) // any overflow would push maxHandling higher
	mu.Lock()
	defer mu.Unlock()
	close(release)
	if maxHandling > 4 {
		t.Errorf("max concurrent handlers = %d, want <= 4 (MaxAckPending not honored)", maxHandling)
	}
	if maxHandling < 4 {
		t.Logf("note: max concurrent handlers = %d (prefetch depth may cap below MaxAckPending)", maxHandling)
	}
}
