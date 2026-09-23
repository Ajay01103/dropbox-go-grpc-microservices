// Package natsx holds the NATS/JetStream mechanics shared by the services:
// connect, topology (EnsureTopology + consumer specs), the Outcome-based
// consume wrapper with DLQ, and publish helpers.
//
// It depends on pkg/events (the contract) and nats.go. Handlers never call
// msg.Nak()/msg.Term() directly — they return an Outcome and this package
// applies it (see consume.go).
//
// NOTE (Part A): the topology and consume machinery is introduced now but is
// wired into the live handlers in Part C phase 2+. Until then it is
// compile-verified, test-covered code with no runtime callers.
package natsx

import (
	"fmt"

	"github.com/nats-io/nats.go"
)

// Connect opens a NATS connection.
func Connect(url string) (*nats.Conn, error) {
	if url == "" {
		return nil, fmt.Errorf("natsx: url is empty")
	}
	nc, err := nats.Connect(url)
	if err != nil {
		return nil, fmt.Errorf("natsx: connect: %w", err)
	}
	return nc, nil
}
