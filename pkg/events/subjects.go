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

import (
	eventsv2 "github.com/Ajay01103/go-dropbox/pkg/gen/events/v2"
)

// Subjects — the only subjects published or consumed anywhere.
//
// FileStored events are routed BY CONTENT FAMILY: the upload service
// classifies the file once (magic bytes) and publishes to exactly one
// family subject. Each preview worker's durable consumer binds to exactly
// one subject, so a video-only upload can never wake the image worker and
// vice versa — routing correctness lives HERE, not in consumer branching.
const (
	SubjFileStoredImage = "files.v2.stored.image"
	SubjFileStoredVideo = "files.v2.stored.video"
	SubjFileStoredPDF   = "files.v2.stored.pdf"
	// SubjFileStoredOther is published for audit/future use; no consumer
	// ever binds to it.
	SubjFileStoredOther = "files.v2.stored.other"

	SubjDecrefRequested = "blocks.v2.decref.requested"
	SubjDecrefCompleted = "blocks.v2.decref.completed"
)

// SubjFileStoredForFamily maps a classified ContentFamily to its publish
// subject. UNSPECIFIED and OTHER both land on the audit subject: nothing
// consumes them, so a misclassification fails safe (no preview job fires)
// instead of feeding a wrong-family worker.
func SubjFileStoredForFamily(f eventsv2.ContentFamily) string {
	switch f {
	case eventsv2.ContentFamily_CONTENT_FAMILY_IMAGE:
		return SubjFileStoredImage
	case eventsv2.ContentFamily_CONTENT_FAMILY_VIDEO:
		return SubjFileStoredVideo
	case eventsv2.ContentFamily_CONTENT_FAMILY_PDF:
		return SubjFileStoredPDF
	default:
		return SubjFileStoredOther
	}
}
