//go:build integration

// Package natsx integration tests. Run with:
//
//	go test -tags integration ./natsx/
//
// These spin up an embedded NATS server with JetStream (nats-server/v2 as a
// test dependency) and exercise the Consume wrapper end to end. They are NOT
// part of the default `go test ./...` run — the wrapper ships as dead code
// until Part C phase 2 wires it in, and this suite is the guard that phase
// depends on.
package natsx

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// startJetStreamServer boots an embedded NATS server with JetStream enabled,
// listening only on localhost with an ephemeral port and in-memory storage.
func startJetStreamServer(t *testing.T) (nc *nats.Conn, js jetstream.JetStream, shutdown func()) {
	t.Helper()

	srv, err := server.NewServer(&server.Options{
		Host:   "127.0.0.1",
		Port:   -1,
		JetStream: true,
		StoreDir: t.TempDir(),
		NoLog:    true,
	})
	if err != nil {
		t.Fatalf("start embedded nats-server: %v", err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(10 * time.Second) {
		t.Fatal("embedded nats-server not ready in time")
	}

	nc, err = nats.Connect(srv.ClientURL())
	if err != nil {
		srv.Shutdown()
		t.Fatalf("connect to embedded server: %v", err)
	}
	js, err = jetstream.New(nc)
	if err != nil {
		nc.Close()
		srv.Shutdown()
		t.Fatalf("create jetstream: %v", err)
	}
	return nc, js, func() { nc.Close(); srv.Shutdown() }
}

// makeConsumer creates a stream + durable with the given limits so each test
// controls its own retry/exhaustion behavior.
func makeConsumer(t *testing.T, js jetstream.JetStream, stream, subject, durable string, maxDeliver int) jetstream.Consumer {
	t.Helper()
	ctx := context.Background()

	if _, err := js.CreateStream(ctx, jetstream.StreamConfig{
		Name:      stream,
		Subjects:  []string{subject},
		Storage:   jetstream.MemoryStorage,
		Retention: jetstream.LimitsPolicy,
	}); err != nil {
		t.Fatalf("create stream: %v", err)
	}
	cons, err := js.CreateOrUpdateConsumer(ctx, stream, jetstream.ConsumerConfig{
		Durable:       durable,
		FilterSubject: subject,
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       500 * time.Millisecond,
		MaxDeliver:    maxDeliver,
	})
	if err != nil {
		t.Fatalf("create consumer: %v", err)
	}
	return cons
}

func TestConsumeAckPath(t *testing.T) {
	_, js, shutdown := startJetStreamServer(t)
	defer shutdown()

	cons := makeConsumer(t, js, "TEST_STREAM", "test.ack", "test-ack-durable", 3)

	var handled atomic.Int32
	done := make(chan struct{})
	var once sync.Once

	cc, err := Consume(context.Background(), cons, "test-ack-durable", slog.Default(), js,
		func(ctx context.Context, m jetstream.Msg) Outcome {
			handled.Add(1)
			once.Do(func() { close(done) })
			return Ack()
		})
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	defer cc.Stop()

	if _, err := js.Publish(context.Background(), "test.ack", []byte("hello")); err != nil {
		t.Fatalf("publish: %v", err)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handler never ran")
	}
	time.Sleep(200 * time.Millisecond) // let any redelivery surface

	if got := handled.Load(); got != 1 {
		t.Errorf("handler ran %d times, want 1 (acked messages must not redeliver)", got)
	}
}

func TestConsumeRetryThenAck(t *testing.T) {
	_, js, shutdown := startJetStreamServer(t)
	defer shutdown()

	cons := makeConsumer(t, js, "TEST_STREAM", "test.retry", "test-retry-durable", 10)

	var attempts atomic.Int32
	done := make(chan struct{})
	var once sync.Once

	cc, err := Consume(context.Background(), cons, "test-retry-durable", slog.Default(), js,
		func(ctx context.Context, m jetstream.Msg) Outcome {
			if attempts.Add(1) < 3 {
				return Retry(errors.New("transient"))
			}
			once.Do(func() { close(done) })
			return Ack()
		})
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	defer cc.Stop()

	if _, err := js.Publish(context.Background(), "test.retry", []byte("hello")); err != nil {
		t.Fatalf("publish: %v", err)
	}

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("handler never succeeded")
	}
	if got := attempts.Load(); got != 3 {
		t.Errorf("attempts = %d, want 3", got)
	}
}

func TestConsumeExhaustionDeadLetters(t *testing.T) {
	_, js, shutdown := startJetStreamServer(t)
	defer shutdown()
	ctx := context.Background()

	// DLQ stream must exist for the dead-letter publish to succeed.
	if _, err := js.CreateStream(ctx, jetstream.StreamConfig{
		Name:      "DLQ",
		Subjects:  []string{"dlq.>"},
		Storage:   jetstream.MemoryStorage,
		Retention: jetstream.LimitsPolicy,
	}); err != nil {
		t.Fatalf("create DLQ stream: %v", err)
	}

	cons := makeConsumer(t, js, "TEST_STREAM", "test.exhaust", "test-exhaust-durable", 2)

	var attempts atomic.Int32
	cc, err := Consume(context.Background(), cons, "test-exhaust-durable", slog.Default(), js,
		func(ctx context.Context, m jetstream.Msg) Outcome {
			attempts.Add(1)
			return Retry(errors.New("permanent failure"))
		})
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	defer cc.Stop()

	if _, err := js.Publish(ctx, "test.exhaust", []byte("poison")); err != nil {
		t.Fatalf("publish: %v", err)
	}

	// Wait for MaxDeliver (2) deliveries, then fetch the DLQ message via a
	// short-lived ephemeral pull consumer on the DLQ stream.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		dlqCons, err := js.CreateOrUpdateConsumer(ctx, "DLQ", jetstream.ConsumerConfig{
			Durable: "dlq-inspector", FilterSubject: "dlq.>",
			AckPolicy: jetstream.AckExplicitPolicy,
		})
		if err == nil {
			msgs, err := dlqCons.Fetch(1, jetstream.FetchMaxWait(time.Second))
			if err == nil {
				for msg := range msgs.Messages() {
					if msg.Subject() != "dlq.TEST_STREAM.test-exhaust-durable" {
						t.Fatalf("DLQ subject = %q, want dlq.TEST_STREAM.test-exhaust-durable", msg.Subject())
					}
					if reason := msg.Headers().Get("X-Dlq-Reason"); reason == "" {
						t.Error("DLQ message missing X-Dlq-Reason header")
					}
					if attempts.Load() < 2 {
						t.Fatalf("dead-lettered after only %d attempts", attempts.Load())
					}
					_ = msg.Ack()
					return // success: message landed on the DLQ
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("message never dead-lettered to DLQ (attempts=%d)", attempts.Load())
}

func TestConsumePanicBecomesRetry(t *testing.T) {
	_, js, shutdown := startJetStreamServer(t)
	defer shutdown()

	cons := makeConsumer(t, js, "TEST_STREAM", "test.panic", "test-panic-durable", 10)

	var attempts atomic.Int32
	done := make(chan struct{})
	var once sync.Once

	cc, err := Consume(context.Background(), cons, "test-panic-durable", slog.Default(), js,
		func(ctx context.Context, m jetstream.Msg) Outcome {
			if attempts.Add(1) < 2 {
				panic("handler bug")
			}
			once.Do(func() { close(done) })
			return Ack()
		})
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	defer cc.Stop()

	if _, err := js.Publish(context.Background(), "test.panic", []byte("boom")); err != nil {
		t.Fatalf("publish: %v", err)
	}

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("handler never recovered from panic and succeeded")
	}
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, d time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timeout: %s", msg)
}

func TestConsumeDLQFailureNaksInsteadOfTerm(t *testing.T) {
	_, js, shutdown := startJetStreamServer(t)
	defer shutdown()
	ctx := context.Background()

	// No DLQ stream yet. MaxDeliver -1 so exhaustion can't mask the behavior
	// under test.
	cons := makeConsumer(t, js, "TEST_STREAM", "test.nodlq", "test-nodlq-durable", -1)

	var attempts atomic.Int32
	cc, err := Consume(ctx, cons, "test-nodlq-durable", slog.Default(), js,
		func(ctx context.Context, m jetstream.Msg) Outcome {
			attempts.Add(1)
			return Term("poison")
		})
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	defer cc.Stop()

	if _, err := js.Publish(ctx, "test.nodlq", []byte("survivor")); err != nil {
		t.Fatalf("publish: %v", err)
	}

	// Phase 1: DLQ is down, so the wrapper must Nak and we keep seeing
	// redeliveries.
	waitFor(t, 10*time.Second, func() bool { return attempts.Load() >= 3 },
		"message not redelivered while DLQ down (Termed on failed DLQ publish?)")

	// Phase 2: bring the DLQ up, so the next delivery dead-letters the message.
	if _, err := js.CreateStream(ctx, jetstream.StreamConfig{
		Name: "DLQ", Subjects: []string{"dlq.>"},
		Storage: jetstream.MemoryStorage, Retention: jetstream.LimitsPolicy,
	}); err != nil {
		t.Fatalf("create DLQ: %v", err)
	}
	dlqStream, err := js.Stream(ctx, "DLQ")
	if err != nil {
		t.Fatalf("get DLQ: %v", err)
	}
	waitFor(t, 10*time.Second, func() bool {
		info, err := dlqStream.Info(ctx)
		return err == nil && info.State.Msgs >= 1
	}, "message never reached DLQ after it came up")

	// Phase 3: now Termed, so deliveries must stop (allow one in-flight).
	n := attempts.Load()
	time.Sleep(time.Second)
	if got := attempts.Load(); got > n+1 {
		t.Errorf("still being delivered after dead-lettering: %d -> %d", n, got)
	}
}

func TestConsumeMaxAckPendingDefault(t *testing.T) {
	// MaxAckPending unset (0) must not panic PullMaxMessages; the wrapper
	// defaults it. We prove it by creating a consumer with no MaxAckPending
	// and running a message through the full loop.
	_, js, shutdown := startJetStreamServer(t)
	defer shutdown()
	ctx := context.Background()

	if _, err := js.CreateStream(ctx, jetstream.StreamConfig{
		Name: "TEST_STREAM", Subjects: []string{"test.map"}, Storage: jetstream.MemoryStorage,
	}); err != nil {
		t.Fatalf("create stream: %v", err)
	}
	cons, err := js.CreateOrUpdateConsumer(ctx, "TEST_STREAM", jetstream.ConsumerConfig{
		Durable: "test-map-durable", FilterSubject: "test.map",
		AckPolicy: jetstream.AckExplicitPolicy,
		// MaxAckPending deliberately unset (0)
	})
	if err != nil {
		t.Fatalf("create consumer: %v", err)
	}

	done := make(chan struct{})
	var once sync.Once
	cc, err := Consume(context.Background(), cons, "test-map-durable", slog.Default(), js,
		func(ctx context.Context, m jetstream.Msg) Outcome {
			once.Do(func() { close(done) })
			return Ack()
		})
	if err != nil {
		t.Fatalf("Consume with unset MaxAckPending: %v", err)
	}
	defer cc.Stop()

	if _, err := js.Publish(ctx, "test.map", []byte("x")); err != nil {
		t.Fatalf("publish: %v", err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handler never ran (MaxAckPending default path broken?)")
	}

	// Sanity: the default constant has the expected value.
	if defaultMaxAckPending <= 0 {
		t.Errorf("defaultMaxAckPending = %d, want a positive value", defaultMaxAckPending)
	}
}
