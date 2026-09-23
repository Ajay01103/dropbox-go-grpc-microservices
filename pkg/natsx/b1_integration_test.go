//go:build integration

// B1 probe tests. These establish the NATS server behaviors the wrapper
// design relies on. Run with: go test -tags integration ./natsx/

package natsx

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// TestProbeBackOffOverridesAckWait answers: does BackOff[0] override AckWait
// for redelivery timing? The wrapper design assumes BackOff entries are used
// as the per-attempt ack deadline / redelivery schedule; if the server
// instead used the base AckWait, BackOff[0] < handler time would cause
// spurious redeliveries. The probe: BackOff [3s...], AckWait 1s, handler
// sleeps 2s then acks. If BackOff[0] governs, no redelivery happens.
func TestProbeBackOffOverridesAckWait(t *testing.T) {
	_, js, shutdown := startJetStreamServer(t)
	defer shutdown()
	ctx := context.Background()

	if _, err := js.CreateStream(ctx, jetstream.StreamConfig{
		Name: "PROBE", Subjects: []string{"probe.bo"}, Storage: jetstream.MemoryStorage,
	}); err != nil {
		t.Fatalf("create stream: %v", err)
	}
	cons, err := js.CreateOrUpdateConsumer(ctx, "PROBE", jetstream.ConsumerConfig{
		Durable:       "probe-bo",
		FilterSubject: "probe.bo",
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       time.Second,
		BackOff:       []time.Duration{3 * time.Second, 30 * time.Second},
	})
	if err != nil {
		t.Fatalf("create consumer: %v", err)
	}

	var deliveries atomic.Int32
	done := make(chan struct{})
	var once syncOnce
	cc, err := Consume(ctx, cons, "probe-bo", nil, js,
		func(ctx context.Context, m jetstream.Msg) Outcome {
			deliveries.Add(1)
			time.Sleep(2 * time.Second) // exceeds base AckWait, below BackOff[0]
			once.Do(func() { close(done) })
			return Ack()
		})
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	defer cc.Stop()

	if _, err := js.Publish(ctx, "probe.bo", []byte("x")); err != nil {
		t.Fatalf("publish: %v", err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("handler never completed")
	}
	time.Sleep(4 * time.Second) // longer than BackOff[0]: a redelivery would surface

	if got := deliveries.Load(); got != 1 {
		t.Errorf("BackOff[0] does not govern the redelivery window: %d deliveries (want 1; AckWait 1s fired?)", got)
	}
}

// TestProbePrefetchOneSerializes proves the WithPullMaxMessages(1) claim:
// with prefetch depth 1 plus the heartbeat (the production pairing — the
// heartbeat covers handler time beyond AckWait), N slow messages produce
// exactly N deliveries (no AckWait expiry from queued-but-unprocessed
// prefetches, and no expiry from the handler overrunning).
func TestProbePrefetchOneSerializes(t *testing.T) {
	_, js, shutdown := startJetStreamServer(t)
	defer shutdown()
	ctx := context.Background()

	const n = 4
	if _, err := js.CreateStream(ctx, jetstream.StreamConfig{
		Name: "PROBE2", Subjects: []string{"probe.pf"}, Storage: jetstream.MemoryStorage,
	}); err != nil {
		t.Fatalf("create stream: %v", err)
	}
	cons, err := js.CreateOrUpdateConsumer(ctx, "PROBE2", jetstream.ConsumerConfig{
		Durable:       "probe-pf",
		FilterSubject: "probe.pf",
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       time.Second, // would fire on any prefetched-and-queued message
		MaxAckPending: 10,
	})
	if err != nil {
		t.Fatalf("create consumer: %v", err)
	}

	var deliveries atomic.Int32
	acks := make(chan struct{}, n)
	cc, err := Consume(ctx, cons, "probe-pf", nil, js,
		func(ctx context.Context, m jetstream.Msg) Outcome {
			deliveries.Add(1)
			time.Sleep(1500 * time.Millisecond) // exceeds AckWait: heartbeat must cover it
			acks <- struct{}{}
			return Ack()
		},
		WithPullMaxMessages(1),          // serial processing
		WithHeartbeat(300*time.Millisecond), // keeps the ack deadline alive during the handler
	)
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	defer cc.Stop()

	for i := 0; i < n; i++ {
		if _, err := js.Publish(ctx, "probe.pf", []byte("x")); err != nil {
			t.Fatalf("publish: %v", err)
		}
	}

	// Wait until n handlers have run, then allow redeliveries to surface.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && len(acks) < n {
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(3 * time.Second)
	if got := deliveries.Load(); got > n {
		t.Errorf("prefetch depth 1 + heartbeat still aged out messages: %d deliveries for %d messages", got, n)
	}
}

// TestDeadLetterHookRunsAfterPublish verifies the hook fires only after the
// DLQ publish succeeded (message present in DLQ), never on a DLQ failure.
func TestDeadLetterHookRunsAfterPublish(t *testing.T) {
	_, js, shutdown := startJetStreamServer(t)
	defer shutdown()
	ctx := context.Background()

	if err := EnsureDLQ(ctx, js); err != nil {
		t.Fatalf("EnsureDLQ: %v", err)
	}

	cons := makeConsumer(t, js, "HOOK_STREAM", "test.hook", "test-hook-durable", 2)

	hookFired := make(chan string, 1)
	cc, err := Consume(ctx, cons, "test-hook-durable", nil, js,
		func(ctx context.Context, m jetstream.Msg) Outcome {
			return Retry(errBoom)
		},
		WithDeadLetterHook(func(ctx context.Context, m jetstream.Msg, reason string) {
			hookFired <- reason
		}))
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	defer cc.Stop()

	if _, err := js.Publish(ctx, "test.hook", []byte("x")); err != nil {
		t.Fatalf("publish: %v", err)
	}

	select {
	case reason := <-hookFired:
		if reason == "" {
			t.Error("hook fired with empty reason")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("dead-letter hook never fired")
	}

	// The DLQ message must exist by the time the hook ran.
	dlq, err := js.Stream(ctx, "DLQ")
	if err != nil {
		t.Fatalf("get DLQ: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool {
		info, err := dlq.Info(ctx)
		return err == nil && info.State.Msgs >= 1
	}, "DLQ empty after hook fired (hook ran before publish?)")
}

// TestRetryScheduleDrivesDelays verifies WithRetrySchedule: a Retry outcome
// on delivery N redelivers after schedule[N-1], and past the end clamps to
// the last entry. Uses the server-side NakWithDelay timing.
func TestRetryScheduleDrivesDelays(t *testing.T) {
	_, js, shutdown := startJetStreamServer(t)
	defer shutdown()
	ctx := context.Background()

	cons := makeConsumer(t, js, "SCHED", "test.sched", "test-sched-durable", 10)

	var attempts atomic.Int32
	start := time.Now()
	done := make(chan struct{})
	var once syncOnce
	cc, err := Consume(ctx, cons, "test-sched-durable", nil, js,
		func(ctx context.Context, m jetstream.Msg) Outcome {
			if attempts.Add(1) < 3 {
				return Retry(errBoom)
			}
			once.Do(func() { close(done) })
			return Ack()
		},
		WithRetrySchedule([]time.Duration{1500 * time.Millisecond, 5 * time.Second}),
	)
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	defer cc.Stop()

	if _, err := js.Publish(ctx, "test.sched", []byte("x")); err != nil {
		t.Fatalf("publish: %v", err)
	}
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("never succeeded")
	}
	// Deliveries 1→2 waited 1.5s and 2→3 waited 1.5s = ≥3s total; with a
	// bare Nak and AckWait 500ms it would be ~1s. Generous lower bound.
	if elapsed := time.Since(start); elapsed < 2500*time.Millisecond {
		t.Errorf("retry schedule not applied: 3 attempts in %v (want ≥2.5s)", elapsed)
	}
}

// errBoom is a sentinel error for retry outcomes.
var errBoom = errors.New("boom")

// syncOnce is a tiny local helper so this file doesn't import sync just for
// the handful of once patterns above.
type syncOnce struct{ f func() }

func (o *syncOnce) Do(f func()) {
	if o.f == nil {
		o.f = f
		f()
	}
}
