package natsx

import (
	"context"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/Ajay01103/go-dropbox/pkg/events"
)

// DLQSubject returns the dead-letter subject for a message that failed on a
// stream's durable consumer: dlq.<stream>.<consumer>.
func DLQSubject(stream, consumer string) string {
	return fmt.Sprintf("dlq.%s.%s", stream, consumer)
}

// dlq publishes the original message to the DLQ with the termination reason,
// the original subject and consumer preserved as headers. It reports whether
// the publish succeeded so the caller can decide between Term and Nak — a
// failed DLQ publish must not silently drop the message.
//
// stream is the stream the message arrived on (from the message metadata);
// an empty stream name makes routing impossible and fails the publish.
func dlq(ctx context.Context, js jetstream.JetStream, stream, consumer string, m jetstream.Msg, reason string) bool {
	if js == nil || stream == "" {
		return false
	}

	header := copyHeaders(m.Headers())
	header.Set(events.HeaderDLQReason, reason)
	header.Set("X-Dlq-Original-Subject", m.Subject())
	header.Set("X-Dlq-Consumer", consumer)
	header.Set("X-Dlq-Timestamp", time.Now().UTC().Format(time.RFC3339))

	msg := &nats.Msg{
		Subject: DLQSubject(stream, consumer),
		Data:    m.Data(),
		Header:  header,
	}

	// Bounded timeout so a broken DLQ stream cannot wedge the consume loop.
	pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := js.PublishMsg(pctx, msg); err != nil {
		return false
	}
	return true
}

// copyHeaders clones the incoming message headers (nil-safe) so trace context
// survives the move to the DLQ.
func copyHeaders(src nats.Header) nats.Header {
	dst := make(nats.Header, len(src))
	for k, vs := range src {
		dst[k] = append([]string(nil), vs...)
	}
	return dst
}
