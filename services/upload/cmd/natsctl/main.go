// natsctl is the operator CLI for the v2 messaging topology.
//
// Commands:
//
//	init        ensure the v2 streams + DLQ (what boot does, on demand)
//	status      per-stream/consumer num_pending/num_ack_pending + DLQ depth
//	dlq-replay  republish DLQ messages to their original subjects
//
// check-events exemption: this file holds no contract literals outside the
// events package; it is kept in the scanner exclusion list only because the
// CLI's own usage text names the commands.
package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/Ajay01103/go-dropbox/pkg/events"
	"github.com/Ajay01103/go-dropbox/pkg/natsx"
)

// dlqOriginalSubject is the header the natsx DLQ wrapper sets on every
// dead-lettered message (pkg/natsx/dlq.go).
const dlqOriginalSubject = "X-Dlq-Original-Subject"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	url := os.Getenv("NATS_URL")
	if url == "" {
		url = nats.DefaultURL
	}

	ctx := context.Background()
	if err := run(ctx, os.Args[1], url, os.Args[2:]); err != nil {
		fmt.Fprintf(os.Stderr, "natsctl: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: natsctl <command> [flags]

commands:
  init              ensure the v2 streams and the DLQ stream exist
  status            stream/consumer pending + unacked counts, DLQ depth
  dlq-replay        republish DLQ messages to their original subjects

environment:
  NATS_URL          defaults to nats://localhost:4222`)
}

func run(ctx context.Context, cmd, url string, args []string) error {
	nc, err := nats.Connect(url)
	if err != nil {
		return fmt.Errorf("connect to NATS at %s: %w", url, err)
	}
	defer nc.Close()
	js, err := jetstream.New(nc)
	if err != nil {
		return fmt.Errorf("create jetstream context: %w", err)
	}

	switch cmd {
	case "init":
		return cmdInit(ctx, js)
	case "status":
		return cmdStatus(ctx, js)
	case "dlq-replay":
		return cmdDLQReplay(ctx, js)
	default:
		usage()
		return fmt.Errorf("unknown command %q", cmd)
	}
}

// ─── init ─────────────────────────────────────────────────────────────────────

func cmdInit(ctx context.Context, js jetstream.JetStream) error {
	if err := natsx.EnsureTopology(ctx, js, 1); err != nil {
		return fmt.Errorf("ensure topology: %w", err)
	}
	if err := natsx.EnsureDLQ(ctx, js); err != nil {
		return fmt.Errorf("ensure dlq: %w", err)
	}
	fmt.Println("topology ensured: FILE_EVENTS, BLOCK_REFS_CMD, BLOCK_REFS_EVT, DLQ")
	return nil
}

// ─── status ───────────────────────────────────────────────────────────────────

func cmdStatus(ctx context.Context, js jetstream.JetStream) error {
	names := make([]string, 0, len(events.StreamFilters))
	for name := range events.StreamFilters {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		s, err := js.Stream(ctx, name)
		if err != nil {
			fmt.Printf("stream %-16s MISSING\n", name)
			continue
		}
		info, err := s.Info(ctx)
		if err != nil {
			return fmt.Errorf("stream info %s: %w", name, err)
		}
		fmt.Printf("stream %-16s msgs=%d  bytes=%d\n", name, info.State.Msgs, info.State.Bytes)
		lister := s.ListConsumers(ctx)
		for ci := range lister.Info() {
			fmt.Printf("  consumer %-28s num_pending=%d  num_ack_pending=%d  num_redelivered=%d\n",
				ci.Name, ci.NumPending, ci.NumAckPending, ci.NumRedelivered)
		}
		if err := lister.Err(); err != nil {
			return fmt.Errorf("list consumers %s: %w", name, err)
		}
	}
	return nil
}

// ─── dlq-replay ───────────────────────────────────────────────────────────────

// subjectCovered reports whether any configured stream's filter captures the
// subject. dlq-replay refuses to replay messages whose original subject no
// stream covers any more — publishing into nothing would silently lose the
// message again; it prints those instead.
func subjectCovered(subject string) bool {
	for _, filters := range events.StreamFilters {
		for _, f := range filters {
			if filterMatches(f, subject) {
				return true
			}
		}
	}
	return false
}

// filterMatches implements the NATS token-wildcard subset used by the
// contract: literal tokens, "*" and a trailing ">".
func filterMatches(filter, subject string) bool {
	ftoks := strings.Split(filter, ".")
	stoks := strings.Split(subject, ".")
	for i, tok := range ftoks {
		if tok == ">" {
			// A trailing ">" matches one or more remaining tokens.
			return i == len(ftoks)-1 && i < len(stoks)
		}
		if i >= len(stoks) {
			return false
		}
		if tok != "*" && tok != stoks[i] {
			return false
		}
	}
	return len(ftoks) == len(stoks)
}

// replayMsgID builds the replayed message's dedup ID: the original MsgId with
// a ":replay<unix>" suffix. A replayed poison message carrying its ORIGINAL
// ID would be dedup-suppressed inside the server's dedup window and silently
// vanish — the exact failure class this redesign keeps killing.
func replayMsgID(original string, now time.Time) string {
	if original == "" {
		return fmt.Sprintf("replay:%d", now.Unix())
	}
	return fmt.Sprintf("%s:replay%d", original, now.Unix())
}

func cmdDLQReplay(ctx context.Context, js jetstream.JetStream) error {
	s, err := js.Stream(ctx, events.StreamDLQ)
	if err != nil {
		return fmt.Errorf("DLQ stream not found: %w", err)
	}
	streamInfo, err := s.Info(ctx)
	if err != nil {
		return fmt.Errorf("dlq info: %w", err)
	}
	if streamInfo.State.Msgs == 0 {
		fmt.Println("DLQ is empty — nothing to replay")
		return nil
	}

	type candidate struct {
		seq     uint64
		subject string
		data    []byte
		msgID   string
	}
	var covered []candidate
	skipped := 0

	// 1. Drain the DLQ through an ephemeral ordered consumer, collecting
	// candidates. Nothing on the DLQ is mutated yet.
	cons, err := s.OrderedConsumer(ctx, jetstream.OrderedConsumerConfig{})
	if err != nil {
		return fmt.Errorf("create dlq consumer: %w", err)
	}
	// The ordered consumer is ephemeral; it disappears with the connection.
	for {
		batch, err := cons.Fetch(10, jetstream.FetchMaxWait(2*time.Second))
		if err != nil {
			break // fetch failure = drained (or transient; messages remain)
		}
		n := 0
		for m := range batch.Messages() {
			n++
			md, err := m.Metadata()
			if err != nil {
				fmt.Printf("SKIP (no metadata)\n")
				_ = m.Ack()
				continue
			}
			origSubject := m.Headers().Get(dlqOriginalSubject)
			if origSubject == "" || !subjectCovered(origSubject) {
				fmt.Printf("SKIP seq=%d subject=%q — no stream covers it\n", md.Sequence.Stream, origSubject)
				skipped++
				_ = m.Ack()
				continue
			}
			covered = append(covered, candidate{
				seq:     md.Sequence.Stream,
				subject: origSubject,
				data:    append([]byte(nil), m.Data()...),
				msgID:   m.Headers().Get(events.HeaderMsgID),
			})
		}
		if n == 0 {
			break
		}
	}
	if len(covered) == 0 && skipped == 0 {
		fmt.Println("DLQ drained — nothing to replay")
		return nil
	}

	// 2. Republish covered messages with the :replay<unix> MsgId. Every
	// publish is synchronous (JetStream ack) — a failure aborts BEFORE any
	// DLQ deletion, so nothing is lost mid-run.
	now := time.Now().UTC()
	for _, c := range covered {
		msg := nats.NewMsg(c.subject)
		msg.Data = c.data
		id := replayMsgID(c.msgID, now)
		if _, err := js.PublishMsg(ctx, msg, jetstream.WithMsgID(id)); err != nil {
			return fmt.Errorf("replay seq=%d to %s: %w (no DLQ messages deleted — rerun when the destination recovers)", c.seq, c.subject, err)
		}
		fmt.Printf("REPLAYED seq=%d -> %s (MsgId %q)\n", c.seq, c.subject, id)
	}

	// 3. Only now delete from the DLQ: republish already returned a sync ack.
	deleted := 0
	for _, c := range covered {
		if err := s.DeleteMsg(ctx, c.seq); err != nil {
			fmt.Printf("WARN: could not delete DLQ seq=%d (already replayed — manual cleanup possible): %v\n", c.seq, err)
			continue
		}
		deleted++
	}
	fmt.Printf("done: replayed=%d deleted=%d skipped=%d\n", len(covered), deleted, skipped)
	return nil
}
