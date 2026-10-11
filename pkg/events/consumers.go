package events

// Durable consumer names — the only consumers in the topology.
//
// One preview consumer per content family (multi-format preview pipeline):
// each binds to exactly one files.v2.stored.<family> subject, so job
// families never share a failure domain, resource envelope, or scaling
// policy. See subjects.go for the family→subject contract.
const (
	ConsumerPreviewImageV2 = "preview-image-v2"
	ConsumerPreviewVideoV2 = "preview-video-v2"
	ConsumerPreviewPDFV2   = "preview-pdf-v2"

	ConsumerDecrefV2          = "decref-v2"
	ConsumerPurgeCompletionV2 = "purge-completion-v2"
)
