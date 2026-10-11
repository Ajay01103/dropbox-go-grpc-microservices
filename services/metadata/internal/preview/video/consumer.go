package video

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

// backOff is steeper than the image worker's: external-binary failures are
// usually deterministic, so fewer, longer-spaced retries avoid wasting a
// heavy ffmpeg process on hopeless inputs.
var backOff = []time.Duration{
	10 * time.Second, 1 * time.Minute, 5 * time.Minute, 30 * time.Minute,
}

// Worker consumes files.v2.stored.video and generates frame previews via
// sandboxed ffmpeg. Resource envelope: ffmpeg is heavy — MaxAckPending 2,
// AckWait 5m (probe + seek + encode can be slow on large files), MaxDeliver 4.
type Worker struct {
	*common.FamilyConsumer
}

// New wires the video consumer; fails fast if ffmpeg/ffprobe are missing so
// the dependency surfaces at boot (the registry degrades just this family).
func New(url string, storage *common.Storage, repo *repository.MetadataRepo, logger *zap.Logger) (*Worker, error) {
	conn, err := natsx.Connect(url)
	if err != nil {
		return nil, common.Wrap("connect nats for video preview worker", err)
	}
	gen, err := newVideoGenerator(storage, logger)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return &Worker{common.NewFamilyConsumer(conn, storage, repo, logger,
		gen,
		eventsv2.ContentFamily_CONTENT_FAMILY_VIDEO,
		events.ConsumerPreviewVideoV2, events.SubjFileStoredVideo,
		jetstream.ConsumerConfig{
			Durable:       events.ConsumerPreviewVideoV2,
			FilterSubject: events.SubjFileStoredVideo,
			AckPolicy:     jetstream.AckExplicitPolicy,
			AckWait:       5 * time.Minute,
			MaxDeliver:    4,
			MaxAckPending: 2,
			BackOff:       backOff,
		},
	)}, nil
}
