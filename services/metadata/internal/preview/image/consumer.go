package image

import (
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"go.uber.org/zap"

	"github.com/Ajay01103/go-dropbox/metadata/internal/preview/common"
	"github.com/Ajay01103/go-dropbox/metadata/internal/repository"
	"github.com/Ajay01103/go-dropbox/pkg/events"
	eventsv2 "github.com/Ajay01103/go-dropbox/pkg/gen/events/v2"
	"github.com/Ajay01103/go-dropbox/pkg/natsx"
)

// backOff: entries past the first are the slow safety net; the wrapper's
// retry schedule drives the quick retries. BackOff[0] (5s) is above any
// transient storage hiccup plus the heartbeat interval; later entries back
// off to 30m so a poisoned message doesn't hot-loop for days.
var backOff = []time.Duration{
	5 * time.Second, 30 * time.Second, 2 * time.Minute, 10 * time.Minute, 30 * time.Minute,
}

// Worker consumes files.v2.stored.image and generates image thumbnails with
// PER-FILE keys (thumbnails/<file_id>/<content_sha>.jpg). It receives ONLY
// image events — the upload service publishes each family on its own
// subject, so a video-only upload never wakes this worker.
type Worker struct {
	*common.FamilyConsumer
}

// New wires the JetStream consumer onto the image generator.
func New(url string, storage *common.Storage, repo *repository.MetadataRepo, logger *zap.Logger) (*Worker, error) {
	conn, err := natsx.Connect(url)
	if err != nil {
		return nil, common.Wrap("connect nats for image preview worker", err)
	}
	return &Worker{common.NewFamilyConsumer(conn, storage, repo, logger,
		&Generator{storage: storage, logger: logger},
		eventsv2.ContentFamily_CONTENT_FAMILY_IMAGE,
		events.ConsumerPreviewImageV2, events.SubjFileStoredImage,
		jetstream.ConsumerConfig{
			Durable:       events.ConsumerPreviewImageV2,
			FilterSubject: events.SubjFileStoredImage,
			AckPolicy:     jetstream.AckExplicitPolicy,
			AckWait:       2 * time.Minute,
			MaxDeliver:    6,
			MaxAckPending: 8,
			BackOff:       backOff,
		},
	)}, nil
}
