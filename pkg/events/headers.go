package events

import "fmt"

// Header names used on published messages.
const (
	// HeaderMsgID is the standard JetStream de-duplication header
	// (Nats-Msg-Id). Set via nats.MsgId / jetstream.WithMsgID.
	HeaderMsgID = "Nats-Msg-Id"

	// HeaderType carries the protobuf message full name (e.g. the X-Type
	// convention from Part B). informational today; enforced in v2.
	HeaderType = "X-Type"

	// HeaderDLQReason is set on messages published to the DLQ stream by
	// pkg/natsx when a message is terminated or exhausts its deliveries.
	HeaderDLQReason = "X-Dlq-Reason"

	// HeaderTraceparent carries W3C trace context (Part B convention).
	HeaderTraceparent = "traceparent"
)

// MsgID builders. These are the only place dedup IDs are constructed, so a
// change here is a deliberate, reviewable contract change.

// MsgIDFileStored returns the de-dup ID for a v2 FileStored event:
// "stored:<file_id>:<version>". A thumbnail-sweeper republish reuses this
// exact ID on purpose — like a PENDING purge re-drive, the republish is a
// no-op if the original secretly succeeded, so dedup swallowing it is the
// desired behavior (unlike REQUESTED re-drives, which are genuine
// loss-retries and MUST get a fresh ID).
func MsgIDFileStored(fileID string, version int64) string {
	return fmt.Sprintf("stored:%s:%d", fileID, version)
}

// MsgIDDecrefRequested returns the de-dup ID for the FIRST publish of a
// decrement request batch (v2 scheme).
func MsgIDDecrefRequested(jobID string, batchIndex uint32) string {
	return fmt.Sprintf("decref:%s:%d", jobID, batchIndex)
}

// MsgIDDecrefRequestedRedrive returns the de-dup ID for a sweeper re-drive of
// a decrement request batch. A re-drive needs a NEW id, because the dedup
// window would otherwise silently swallow it.
func MsgIDDecrefRequestedRedrive(jobID string, batchIndex, attempt uint32) string {
	return fmt.Sprintf("decref:%s:%d:r%d", jobID, batchIndex, attempt)
}

// MsgIDDecrefCompleted returns the de-dup ID for a v2 decrement completion.
func MsgIDDecrefCompleted(jobID string, batchIndex uint32) string {
	return fmt.Sprintf("decref-done:%s:%d", jobID, batchIndex)
}
