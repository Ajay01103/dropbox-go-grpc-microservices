package natsx

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// kind classifies an Outcome.
type kind int

const (
	kAck kind = iota
	kRetry
	kRetryAfter
	kTerm
)

// defaultMaxAckPending is used when the consumer config leaves MaxAckPending
// unset (0) or negative, because jetstream.PullMaxMessages panics/errors on
// those values.
const defaultMaxAckPending = 1000

// defaultHeartbeatCap bounds WithHeartbeat: after this many beats the ticker
// stops, so a wedged handler eventually stops extending its own ack deadline
// and the message redelivers.
const defaultHeartbeatCap = 15

// dlqFailureRetryDelay is how long a Termined message waits before
// redelivery when its DLQ publish failed — long enough to avoid a hot loop,
// short enough that DLQ recovery is picked up quickly.
const dlqFailureRetryDelay = time.Second

// Outcome is what a handler returns instead of touching the message's ack
// methods directly. The Consume wrapper translates it into Ack / Nak /
// NakWithDelay / Term (+ DLQ) and applies the retry-exhaustion policy.
//
// Use it for:
//
//	Ack                — done, or a benign duplicate/orphan.
//	Retry(err)          — transient failure; the retry schedule (or BackOff)
//	                      applies.
//	RetryAfter(d, err) — retry after a specific delay (claimed by another
//	                     delivery, replica lag).
//	Term(reason)       — permanent/poison message: DLQ + Term.
type Outcome struct {
	k      kind
	delay  time.Duration
	err    error
	reason string
}

// Ack acknowledges successful (or benign) processing.
func Ack() Outcome { return Outcome{k: kAck} }

// Retry requests redelivery on the retry schedule (see WithRetrySchedule);
// without a schedule it falls back to a plain Nak (BackOff applies).
func Retry(err error) Outcome { return Outcome{k: kRetry, err: err} }

// RetryAfter requests redelivery after a specific delay.
func RetryAfter(d time.Duration, err error) Outcome {
	if d < 0 {
		d = 0
	}
	return Outcome{k: kRetryAfter, delay: d, err: err}
}

// Term terminates the message permanently and dead-letters it with a reason.
func Term(reason string) Outcome { return Outcome{k: kTerm, reason: reason} }

// Kind reports which outcome constructor produced this Outcome. Intended for
// tests that pin handler retry policies.
func (o Outcome) Kind() string {
	switch o.k {
	case kAck:
		return "ack"
	case kRetry:
		return "retry"
	case kRetryAfter:
		return "retry_after"
	case kTerm:
		return "term"
	default:
		return "unknown"
	}
}

// Delay returns the delay carried by a RetryAfter outcome (zero otherwise).
func (o Outcome) Delay() time.Duration { return o.delay }

// Handler processes a single JetStream message and returns an Outcome.
type Handler func(ctx context.Context, m jetstream.Msg) Outcome

// DeadLetterHook runs after a message has been successfully published to the
// DLQ, before Term. Use it for last-mile side effects (e.g. marking a
// thumbnail row failed). It must be fast and best-effort; errors are ignored.
type DeadLetterHook func(ctx context.Context, m jetstream.Msg, reason string)

// consumeOpts carries the tunables set via ConsumeOption.
type consumeOpts struct {
	pullMaxMessages int // 0 → default from MaxAckPending
	heartbeat       time.Duration
	heartbeatCap    int
	deadLetterHook  DeadLetterHook
	retrySchedule   []time.Duration
}

// ConsumeOption tunes the Consume wrapper.
type ConsumeOption func(*consumeOpts)

// WithPullMaxMessages sets the pull prefetch depth. The default derives from
// the consumer's MaxAckPending. Slow, serial handlers should use a small
// value (1): AckWait timers start at delivery time, so a deep prefetch ages
// the last message by (depth-1) × handler time before the callback reaches
// it, causing spurious redeliveries.
func WithPullMaxMessages(n int) ConsumeOption {
	return func(o *consumeOpts) {
		if n > 0 {
			o.pullMaxMessages = n
		}
	}
}

// WithHeartbeat sends msg.InProgress() every d while a handler is running,
// extending the ack deadline during long processing. A cap (default 15
// beats) stops the ticker so a wedged handler still eventually redelivers.
func WithHeartbeat(d time.Duration) ConsumeOption {
	return func(o *consumeOpts) {
		if d > 0 {
			o.heartbeat = d
		}
	}
}

// WithHeartbeatCap overrides the default heartbeat cap.
func WithHeartbeatCap(n int) ConsumeOption {
	return func(o *consumeOpts) {
		if n > 0 {
			o.heartbeatCap = n
		}
	}
}

// WithDeadLetterHook registers a hook invoked after a successful DLQ publish,
// just before Term. Not invoked when the DLQ publish fails (the message is
// being retried instead).
func WithDeadLetterHook(h DeadLetterHook) ConsumeOption {
	return func(o *consumeOpts) {
		o.deadLetterHook = h
	}
}

// WithRetrySchedule drives Retry outcomes: delivery N redelivers after
// schedule[min(N-1, len-1)]. Makes quick retries explicit so consumer BackOff
// can be reserved as a slow safety net (BackOff[0] ≥ max handler time).
func WithRetrySchedule(schedule []time.Duration) ConsumeOption {
	return func(o *consumeOpts) {
		o.retrySchedule = schedule
	}
}

// Consume wraps cons.Consume with the Outcome contract:
//
//   - handler panics are recovered and become Retry (redelivery)
//   - Ack / NakWithDelay / Nak are applied from the Outcome
//   - Term outcomes and retry exhaustion (NumDelivered >= MaxDeliver) are
//     dead-lettered to dlq.<stream>.<consumer> with X-Dlq-Reason, then
//     terminated; if the DLQ publish itself fails the message is Nak'd
//     instead (unless deliveries are already exhausted) so nothing is
//     silently lost while the DLQ stream is missing or broken
//   - Retry outcomes redeliver on the retry schedule when one is configured
//   - retries log at Debug, the first failure at Warn (once), DLQ at Error
//     (once) — one log line per message, not one per delivery
func Consume(ctx context.Context, cons jetstream.Consumer, name string, log *slog.Logger,
	js jetstream.JetStream, h Handler, options ...ConsumeOption) (jetstream.ConsumeContext, error) {

	cfg := cons.CachedInfo().Config
	if log == nil {
		log = slog.Default()
	}

	opts := consumeOpts{heartbeatCap: defaultHeartbeatCap}
	for _, apply := range options {
		apply(&opts)
	}

	maxAckPending := cfg.MaxAckPending
	if maxAckPending <= 0 {
		maxAckPending = defaultMaxAckPending
	}
	prefetch := opts.pullMaxMessages
	if prefetch <= 0 {
		prefetch = maxAckPending
	}

	return cons.Consume(func(m jetstream.Msg) {
		md, _ := m.Metadata()

		// Extend the ack deadline while the handler works. Stopped when the
		// handler returns, or after the cap so a wedged handler redelivers.
		stopHeartbeat := runHeartbeat(ctx, m, opts.heartbeat, opts.heartbeatCap)
		out := safeApply(ctx, h, m)
		stopHeartbeat()

		exhausted := cfg.MaxDeliver > 0 && md != nil && int(md.NumDelivered) >= cfg.MaxDeliver

		switch {
		case out.k == kAck:
			_ = m.Ack()

		case out.k == kTerm || exhausted:
			reason := out.reason
			if reason == "" {
				reason = fmt.Sprint(out.err)
			}
			// The DLQ subject is derived from the stream the message arrived
			// on (in the metadata), so it works for any stream without a
			// static subject→stream table.
			stream := ""
			if md != nil {
				stream = md.Stream
			}
			if dlqOK := dlq(ctx, js, stream, name, m, reason); !dlqOK && !exhausted {
				// The DLQ publish failed (stream missing, NATS down...).
				// Don't Term — that would silently drop the message. Nak with
				// a delay so it comes back once the DLQ recovers, without
				// spinning. On exhaustion we still Term: redelivering forever
				// would be worse.
				// NOTE (zombie case): if deliveries are already exhausted the
				// server will not redeliver past MaxDeliver, so a failed DLQ
				// publish leaves the message permanently unacked. The ERROR
				// below (with consumer/subject/delivery) is the detection
				// signal; watch num_ack_pending on the durables post-deploy.
				log.Error("dlq publish failed; requeueing message",
					"consumer", name, "subject", m.Subject(),
					"delivery", mdNum(md), "exhausted", exhausted, "reason", reason)
				_ = m.NakWithDelay(dlqFailureRetryDelay)
				return
			}
			log.Error("message dead-lettered",
				"consumer", name, "subject", m.Subject(), "reason", reason)
			if opts.deadLetterHook != nil {
				opts.deadLetterHook(ctx, m, reason)
			}
			_ = m.Term()

		case out.k == kRetryAfter:
			log.Debug("retry scheduled",
				"consumer", name, "delay", out.delay, "err", out.err)
			_ = m.NakWithDelay(out.delay)

		default: // kRetry
			if md != nil && md.NumDelivered == 1 {
				log.Warn("handler failed, will retry",
					"consumer", name, "err", out.err)
			} else {
				log.Debug("retrying",
					"consumer", name, "delivery", mdNum(md), "err", out.err)
			}
			if d, ok := scheduleRetryDelay(opts.retrySchedule, mdNum(md)); ok {
				_ = m.NakWithDelay(d)
			} else {
				_ = m.Nak()
			}
		}
	}, jetstream.PullMaxMessages(prefetch))
}

// scheduleRetryDelay picks the redelivery delay for delivery N from the
// schedule, clamping to the last entry. ok=false when no schedule exists.
func scheduleRetryDelay(schedule []time.Duration, delivery int) (d time.Duration, ok bool) {
	if len(schedule) == 0 {
		return 0, false
	}
	idx := delivery - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(schedule) {
		idx = len(schedule) - 1
	}
	if schedule[idx] < 0 {
		return 0, true
	}
	return schedule[idx], true
}

// runHeartbeat periodically extends the ack deadline while the handler works.
// Returns a stop function; safe to call with heartbeat disabled (no-op).
func runHeartbeat(ctx context.Context, m jetstream.Msg, every time.Duration, maxBeats int) (stop func()) {
	if every <= 0 {
		return func() {}
	}
	done := make(chan struct{})
	var once sync.Once
	go func() {
		defer once.Do(func() { close(done) })
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		beats := 0
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if maxBeats > 0 && beats >= maxBeats {
					return // wedged handler: let AckWait expire
				}
				beats++
				_ = m.InProgress()
			}
		}
	}()
	return func() { once.Do(func() { close(done) }) }
}

// safeApply invokes h with panic recovery; a panic becomes Retry so the
// message is redelivered rather than lost.
func safeApply(ctx context.Context, h Handler, m jetstream.Msg) (out Outcome) {
	defer func() {
		if r := recover(); r != nil {
			out = Retry(fmt.Errorf("handler panic: %v\n%s", r, debug.Stack()))
		}
	}()
	return h(ctx, m)
}

func mdNum(md *jetstream.MsgMetadata) int {
	if md == nil {
		return 0
	}
	return int(md.NumDelivered)
}
