// purge-nats purges all messages from the BLOCK_REFS and UPLOAD_EVENTS
// JetStream streams and deletes all known durable consumers.
//
// Run this whenever you wipe the ScyllaDB data in development so that NATS
// does not replay stale messages against an empty database.
//
// Usage (from workspace root or services/upload):
//
//	NATS_URL=nats://localhost:4222 go run ./cmd/purge-nats
package main

import (
	"fmt"
	"log"
	"os"

	"github.com/nats-io/nats.go"
)

func main() {
	url := os.Getenv("NATS_URL")
	if url == "" {
		url = nats.DefaultURL // nats://localhost:4222
	}

	nc, err := nats.Connect(url)
	if err != nil {
		log.Fatalf("connect to NATS at %s: %v", url, err)
	}
	defer nc.Close()

	js, err := nc.JetStream()
	if err != nil {
		log.Fatalf("create JetStream context: %v", err)
	}

	type streamSpec struct {
		name      string
		consumers []string
	}

	streams := []streamSpec{
		{
			name: "BLOCK_REFS",
			consumers: []string{
				"upload-block-decrement-worker", // upload service
				"metadata-purge-worker",         // metadata service
			},
		},
		{
			name: "UPLOAD_EVENTS",
			consumers: []string{
				"metadata-thumbnail-worker", // metadata service
			},
		},
	}

	for _, spec := range streams {
		info, err := js.StreamInfo(spec.name)
		if err != nil {
			fmt.Printf("stream %-20s not found, skipping\n", spec.name)
			continue
		}
		fmt.Printf("stream %-20s  msgs=%d  bytes=%d\n",
			spec.name, info.State.Msgs, info.State.Bytes)

		// Delete consumers first — they hold per-consumer sequence state and
		// unacked delivery counts that would interfere after a DB wipe.
		for _, consumer := range spec.consumers {
			if err := js.DeleteConsumer(spec.name, consumer); err != nil {
				// Not found is fine — the service may not have started yet.
				fmt.Printf("  consumer %-40s  skipped (%v)\n", consumer, err)
			} else {
				fmt.Printf("  consumer %-40s  deleted\n", consumer)
			}
		}

		// Purge all messages from the stream.
		if err := js.PurgeStream(spec.name); err != nil {
			log.Printf("  WARN: purge %s failed: %v\n", spec.name, err)
		} else {
			info, _ = js.StreamInfo(spec.name)
			remaining := uint64(0)
			if info != nil {
				remaining = info.State.Msgs
			}
			fmt.Printf("  stream %-20s  purged  remaining=%d\n", spec.name, remaining)
		}
	}

	fmt.Println("done — safe to restart services")
}
