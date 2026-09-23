package natsx

import (
	"context"
	"fmt"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"google.golang.org/protobuf/proto"

	"github.com/Ajay01103/go-dropbox/pkg/events"
)

// PublishProto publishes a protobuf message with the v2 message conventions:
// Nats-Msg-Id for de-duplication and X-Type carrying the proto full name.
// The publish is synchronous (server ack) via JetStream.
func PublishProto(ctx context.Context, js jetstream.JetStream, subject, msgID string, msg proto.Message) error {
	payload, err := proto.Marshal(msg)
	if err != nil {
		return fmt.Errorf("natsx: marshal %T: %w", msg, err)
	}

	out := &nats.Msg{
		Subject: subject,
		Data:    payload,
		Header:  nats.Header{},
	}
	if msgID != "" {
		out.Header.Set(events.HeaderMsgID, msgID)
	}
	out.Header.Set(events.HeaderType, string(msg.ProtoReflect().Descriptor().FullName()))

	opts := []jetstream.PublishOpt{}
	if msgID != "" {
		opts = append(opts, jetstream.WithMsgID(msgID))
	}
	if _, err := js.PublishMsg(ctx, out, opts...); err != nil {
		return fmt.Errorf("natsx: publish to %s: %w", subject, err)
	}
	return nil
}
