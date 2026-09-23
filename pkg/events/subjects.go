// Package events is the single source of truth for the messaging contract:
// stream names, subjects, durable consumer names, header names and MsgID
// builders.
//
// Rules:
//   - No NATS import here. This package holds the contract, not the mechanics
//     (see pkg/natsx for connect/consume/publish helpers).
//   - Message schemas live in .proto files (generated into pkg/gen).
//
// Services import this package instead of declaring their own subject/stream
// literals. A rename touches exactly one file.
package events

// Subjects — the only subjects published or consumed anywhere.
const (
	SubjFileStored      = "files.v2.stored"
	SubjDecrefRequested = "blocks.v2.decref.requested"
	SubjDecrefCompleted = "blocks.v2.decref.completed"
)
