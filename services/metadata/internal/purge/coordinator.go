package purge

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Ajay01103/go-dropbox/metadata/internal/repository"
	blockspb "github.com/Ajay01103/go-dropbox/pkg/gen/blocks/blocks/v1"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	requestedSubject = "blocks.refs.decrement.requested"
	completedSubject = "blocks.refs.decrement.completed"
	streamName       = "BLOCK_REFS"
)

// ThumbnailDeleter can remove a thumbnail object from storage. The thumbnail
// worker satisfies this interface, but a nil value is also accepted (deletion
// is skipped gracefully).
type ThumbnailDeleter interface {
	DeleteThumbnail(ctx context.Context, key string) error
}

type Coordinator struct {
	repo              *repository.PurgeRepo
	metadataRepo      *repository.MetadataRepo
	thumbnailDeleter  ThumbnailDeleter
	conn              *nats.Conn
	js                nats.JetStreamContext
	logger            *zap.Logger
	sub               *nats.Subscription
	requestedSubject  string
	completedSubject  string
}

func NewCoordinator(url, requested, completed string, repo *repository.PurgeRepo, metadataRepo *repository.MetadataRepo, logger *zap.Logger) (*Coordinator, error) {
	if url == "" {
		return nil, errors.New("nats url is empty")
	}
	if requested == "" {
		requested = requestedSubject
	}
	if completed == "" {
		completed = completedSubject
	}
	conn, err := nats.Connect(url)
	if err != nil {
		return nil, fmt.Errorf("connect purge nats: %w", err)
	}
	js, err := conn.JetStream()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("create purge jetstream: %w", err)
	}
	if err := ensureStream(js); err != nil {
		conn.Close()
		return nil, err
	}
	return &Coordinator{
		repo: repo, metadataRepo: metadataRepo,
		conn: conn, js: js, logger: logger,
		requestedSubject: requested, completedSubject: completed,
	}, nil
}

// SetThumbnailDeleter wires in a ThumbnailDeleter so the coordinator can
// remove thumbnail objects when a file is permanently deleted. Call this
// before Start.
func (c *Coordinator) SetThumbnailDeleter(d ThumbnailDeleter) {
	c.thumbnailDeleter = d
}

// ensureStream creates or repairs the BLOCK_REFS JetStream stream.
// The stream uses WorkQueuePolicy so each subject-filtered message is consumed
// by exactly one subscriber group and deleted once acknowledged.
func ensureStream(js nats.JetStreamContext) error {
	info, err := js.StreamInfo(streamName)
	if errors.Is(err, nats.ErrStreamNotFound) {
		_, err = js.AddStream(&nats.StreamConfig{
			Name:      streamName,
			Subjects:  []string{"blocks.refs.>"},
			Retention: nats.WorkQueuePolicy,
			Storage:   nats.FileStorage,
			MaxAge:    30 * 24 * time.Hour,
			// Allow up to two unique consumers on this stream so that the
			// upload worker (requested subject) and this coordinator
			// (completed subject) can each have their own durable.
			MaxConsumers: 2,
		})
		if err != nil {
			return fmt.Errorf("create %s stream: %w", streamName, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect %s stream: %w", streamName, err)
	}
	// Repair subject list if it was previously misconfigured.
	if len(info.Config.Subjects) != 1 || info.Config.Subjects[0] != "blocks.refs.>" {
		info.Config.Subjects = []string{"blocks.refs.>"}
		if _, err := js.UpdateStream(&info.Config); err != nil {
			return fmt.Errorf("update %s stream subjects: %w", streamName, err)
		}
	}
	return nil
}

func (c *Coordinator) Start(ctx context.Context) error {
	sub, err := c.js.Subscribe(
		c.completedSubject,
		c.handleCompletion,
		nats.ConsumerName("metadata-purge-worker"),
		nats.ManualAck(),
		nats.AckWait(30*time.Second),
	)
	if err != nil {
		return fmt.Errorf("subscribe to %s: %w", completedSubject, err)
	}
	c.sub = sub
	go func() {
		<-ctx.Done()
		_ = c.Close()
	}()
	return nil
}

func (c *Coordinator) Close() error {
	if c.sub != nil {
		_ = c.sub.Drain()
	}
	if c.conn != nil {
		c.conn.Close()
	}
	return nil
}

func (c *Coordinator) Request(ctx context.Context, ownerID, fileID string) (repository.PurgeJob, error) {
	file, err := c.metadataRepo.GetFileByID(ctx, ownerID, fileID)
	if err != nil {
		return repository.PurgeJob{}, err
	}
	if !file.IsDeleted {
		return repository.PurgeJob{}, errors.New("file must be soft-deleted before permanent deletion")
	}

	job, created, err := c.repo.Create(ctx, file, "permanent_delete")
	if err != nil {
		return repository.PurgeJob{}, err
	}

	// If a job already exists and is past requesting, just return its status.
	if !created && job.State != repository.PurgePending && job.State != repository.PurgeDecrementRequested {
		return job, nil
	}

	if err := c.publishRequest(ctx, job); err != nil {
		return job, err
	}

	if job.State == repository.PurgePending {
		if err := c.repo.UpdateState(ctx, job, repository.PurgeDecrementRequested, false, ""); err != nil {
			return job, err
		}
		job.State = repository.PurgeDecrementRequested
	}
	return job, nil
}

func (c *Coordinator) Status(ctx context.Context, ownerID, jobID string) (repository.PurgeJob, error) {
	job, err := c.repo.Get(ctx, jobID)
	if err != nil {
		return repository.PurgeJob{}, err
	}
	if job.OwnerID != ownerID {
		return repository.PurgeJob{}, errors.New("purge job owner mismatch")
	}
	return job, nil
}

func (c *Coordinator) publishRequest(ctx context.Context, job repository.PurgeJob) error {
	id, err := uuid.Parse(job.JobID)
	if err != nil {
		return err
	}
	operations := make([]*blockspb.BlockDecrementOperation, 0, len(job.BlockHashList))
	for index, hash := range job.BlockHashList {
		operations = append(operations, &blockspb.BlockDecrementOperation{
			Occurrence: uint32(index),
			BlockHash:  hash,
		})
	}
	payload, err := proto.Marshal(&blockspb.BlockRefDecrementRequested{
		SchemaVersion: 1,
		JobId:         job.JobID,
		Operations:    operations,
		Reason:        job.Reason,
		RequestedAt:   timestamppb.New(time.Now().UTC()),
	})
	if err != nil {
		return fmt.Errorf("marshal decrement request: %w", err)
	}
	// Use the job UUID as the dedup ID for the request so that clicking
	// "permanent delete" twice for the same file doesn't enqueue duplicate work.
	if _, err := c.js.Publish(c.requestedSubject, payload, nats.MsgId(id.String())); err != nil {
		return fmt.Errorf("publish decrement request: %w", err)
	}
	return nil
}

func (c *Coordinator) handleCompletion(msg *nats.Msg) {
	var event blockspb.BlockRefDecrementCompleted
	if err := proto.Unmarshal(msg.Data, &event); err != nil {
		c.logger.Error("invalid block decrement completion — terminating message", zap.Error(err))
		_ = msg.Term()
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	job, err := c.repo.Get(ctx, event.GetJobId())
	if err != nil {
		c.logger.Error("load purge job for completion",
			zap.String("jobID", event.GetJobId()), zap.Error(err))
		_ = msg.Nak()
		return
	}

	// Idempotency: if the job is already in a terminal or post-decrement state,
	// just ACK — we may have crashed after a previous completion was processed.
	switch job.State {
	case repository.PurgeComplete,
		repository.PurgeMetadataRemoved,
		repository.PurgeDecremented,
		repository.PurgeDecrementedWithError:
		// State was already advanced; ensure metadata cleanup runs if needed.
		if job.State == repository.PurgeDecremented || job.State == repository.PurgeDecrementedWithError {
			if err := c.removeMetadataAndComplete(ctx, job); err != nil {
				c.logger.Error("metadata removal retry failed",
					zap.String("jobID", job.JobID), zap.Error(err))
				_ = msg.Nak()
				return
			}
		}
		_ = msg.Ack()
		return
	}

	// Determine if any block operations had errors.
	hasErrors := false
	var firstError string
	for _, result := range event.GetResults() {
		if result.GetError() != "" {
			hasErrors = true
			if firstError == "" {
				firstError = result.GetError()
			}
		}
	}

	nextState := repository.PurgeDecremented
	if hasErrors {
		nextState = repository.PurgeDecrementedWithError
	}

	if err := c.repo.UpdateState(ctx, job, nextState, hasErrors, firstError); err != nil {
		c.logger.Error("update purge job state to decremented",
			zap.String("jobID", job.JobID), zap.Error(err))
		_ = msg.Nak()
		return
	}
	job.State = nextState

	if err := c.removeMetadataAndComplete(ctx, job); err != nil {
		c.logger.Error("metadata removal failed",
			zap.String("jobID", job.JobID), zap.Error(err))
		_ = msg.Nak()
		return
	}

	_ = msg.Ack()
}

// removeMetadataAndComplete removes the file's metadata projections and
// advances the purge job to COMPLETE. It is idempotent: a missing file row is
// treated as already removed (the projection delete may have committed before a
// prior crash).
func (c *Coordinator) removeMetadataAndComplete(ctx context.Context, job repository.PurgeJob) error {
	if job.State != repository.PurgeMetadataRemoved && job.State != repository.PurgeComplete {
		file, err := c.metadataRepo.GetFileByID(ctx, job.OwnerID, job.FileID)
		if err != nil {
			if !strings.Contains(err.Error(), "file not found") {
				return fmt.Errorf("load file for purge: %w", err)
			}
			// File projections already gone — treat as removed.
		} else if file.Filename != "" {
			// Delete the thumbnail from storage before removing DB projections
			// so that a crash here causes a retry that re-attempts both steps.
			if file.ThumbnailKey != "" && c.thumbnailDeleter != nil {
				if err := c.thumbnailDeleter.DeleteThumbnail(ctx, file.ThumbnailKey); err != nil {
					// Log but don't fail the purge — a stale thumbnail is not
					// critical and the GC scan can catch it later if needed.
					c.logger.Warn("purge: failed to delete thumbnail",
						zap.String("jobID", job.JobID),
						zap.String("fileID", job.FileID),
						zap.String("thumbnailKey", file.ThumbnailKey),
						zap.Error(err))
				} else {
					c.logger.Info("purge: thumbnail deleted",
						zap.String("jobID", job.JobID),
						zap.String("thumbnailKey", file.ThumbnailKey))
				}
			}

			if err := c.metadataRepo.RemoveFileProjections(ctx, file); err != nil {
				return fmt.Errorf("remove file projections: %w", err)
			}
		}

		if err := c.repo.UpdateState(ctx, job, repository.PurgeMetadataRemoved, job.HasReconciliationErrors, job.LastError); err != nil {
			return fmt.Errorf("mark metadata removed: %w", err)
		}
		job.State = repository.PurgeMetadataRemoved
	}

	if job.State != repository.PurgeComplete {
		if err := c.repo.UpdateState(ctx, job, repository.PurgeComplete, job.HasReconciliationErrors, job.LastError); err != nil {
			return fmt.Errorf("mark purge complete: %w", err)
		}
		job.State = repository.PurgeComplete
	}

	// Clean up all purge tracking rows now that the job is fully done.
	// Failures here are non-fatal — the job has already completed successfully
	// and rows will eventually expire via gc_grace_seconds.
	if err := c.repo.DeleteCompletedJob(ctx, job); err != nil {
		c.logger.Warn("purge: failed to clean up job rows (non-fatal)",
			zap.String("jobID", job.JobID),
			zap.String("fileID", job.FileID),
			zap.Error(err))
	}

	return nil
}
