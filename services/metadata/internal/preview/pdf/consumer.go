package pdf

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

// backOff: between image and video — external-binary failures are usually
// deterministic (encrypted/corrupt PDFs never render), so retries are few.
var backOff = []time.Duration{
	10 * time.Second, 1 * time.Minute, 10 * time.Minute, 30 * time.Minute,
}

// Worker consumes files.v2.stored.pdf and generates first-page previews via
// sandboxed pdftoppm. Resource envelope: AckWait 3m, MaxDeliver 4,
// MaxAckPending 4.
type Worker struct {
	*common.FamilyConsumer
}

// New wires the pdf consumer; fails fast if pdftoppm is missing so the
// dependency surfaces at boot (the registry degrades just this family).
func New(url string, storage *common.Storage, repo *repository.MetadataRepo, logger *zap.Logger) (*Worker, error) {
	conn, err := natsx.Connect(url)
	if err != nil {
		return nil, common.Wrap("connect nats for pdf preview worker", err)
	}
	gen, err := newPDFGenerator(storage, logger)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return &Worker{common.NewFamilyConsumer(conn, storage, repo, logger,
		gen,
		eventsv2.ContentFamily_CONTENT_FAMILY_PDF,
		events.ConsumerPreviewPDFV2, events.SubjFileStoredPDF,
		jetstream.ConsumerConfig{
			Durable:       events.ConsumerPreviewPDFV2,
			FilterSubject: events.SubjFileStoredPDF,
			AckPolicy:     jetstream.AckExplicitPolicy,
			AckWait:       3 * time.Minute,
			MaxDeliver:    4,
			MaxAckPending: 4,
			BackOff:       backOff,
		},
	)}, nil
}
