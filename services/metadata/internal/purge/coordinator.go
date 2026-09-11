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

type Coordinator struct {
	repo             *repository.PurgeRepo
	metadataRepo     *repository.MetadataRepo
	conn             *nats.Conn
	js               nats.JetStreamContext
	logger           *zap.Logger
	sub              *nats.Subscription
	requestedSubject string
	completedSubject string
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
	return &Coordinator{repo: repo, metadataRepo: metadataRepo, conn: conn, js: js, logger: logger, requestedSubject: requested, completedSubject: completed}, nil
}

func ensureStream(js nats.JetStreamContext) error {
	info, err := js.StreamInfo(streamName)
	if errors.Is(err, nats.ErrStreamNotFound) {
		_, err = js.AddStream(&nats.StreamConfig{Name: streamName, Subjects: []string{"blocks.refs.>"}, Retention: nats.WorkQueuePolicy, Storage: nats.FileStorage, MaxAge: 30 * 24 * time.Hour})
		if err != nil {
			return fmt.Errorf("create %s stream: %w", streamName, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect %s stream: %w", streamName, err)
	}
	if len(info.Config.Subjects) == 1 && info.Config.Subjects[0] == "blocks.refs.>" {
		return nil
	}
	info.Config.Subjects = []string{"blocks.refs.>"}
	if _, err := js.UpdateStream(&info.Config); err != nil {
		return fmt.Errorf("update %s stream: %w", streamName, err)
	}
	return nil
}

func (c *Coordinator) Start(ctx context.Context) error {
	sub, err := c.js.Subscribe(c.completedSubject, c.handleCompletion, nats.ConsumerName("metadata-purge-worker"), nats.ManualAck())
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
		operations = append(operations, &blockspb.BlockDecrementOperation{Occurrence: uint32(index), BlockHash: hash})
	}
	payload, err := proto.Marshal(&blockspb.BlockRefDecrementRequested{SchemaVersion: 1, JobId: job.JobID, Operations: operations, Reason: job.Reason, RequestedAt: timestamppb.New(time.Now().UTC())})
	if err != nil {
		return fmt.Errorf("marshal decrement request: %w", err)
	}
	if _, err := c.js.Publish(c.requestedSubject, payload, nats.MsgId(id.String())); err != nil {
		return fmt.Errorf("publish decrement request: %w", err)
	}
	return nil
}

func (c *Coordinator) handleCompletion(msg *nats.Msg) {
	var event blockspb.BlockRefDecrementCompleted
	if err := proto.Unmarshal(msg.Data, &event); err != nil {
		c.logger.Error("invalid block decrement completion", zap.Error(err))
		_ = msg.Term()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	job, err := c.repo.Get(ctx, event.GetJobId())
	if err != nil {
		c.logger.Error("load purge job for completion", zap.String("jobID", event.GetJobId()), zap.Error(err))
		_ = msg.Nak()
		return
	}
	if job.State == repository.PurgeComplete || job.State == repository.PurgeMetadataRemoved {
		_ = msg.Ack()
		return
	}
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
	if hasErrors {
		if err := c.repo.UpdateState(ctx, job, repository.PurgeDecrementedWithError, true, firstError); err != nil {
			_ = msg.Nak()
			return
		}
		_ = msg.Ack()
		return
	}
	if err := c.repo.UpdateState(ctx, job, repository.PurgeDecremented, false, ""); err != nil {
		_ = msg.Nak()
		return
	}
	file, err := c.metadataRepo.GetFileByID(ctx, job.OwnerID, job.FileID)
	if err != nil {
		if strings.Contains(err.Error(), "file not found") {
			// The metadata deletion may have succeeded before a process crash.
			// Treat the missing projections as an idempotent completion.
			file = repository.File{FileID: job.FileID, FolderID: job.FolderID, OwnerID: job.OwnerID, Version: job.FileVersion}
		} else {
			c.logger.Error("load file for purge", zap.String("jobID", job.JobID), zap.Error(err))
			_ = msg.Nak()
			return
		}
	}
	if file.Filename != "" {
		if err := c.metadataRepo.RemoveFileProjections(ctx, file); err != nil {
			_ = msg.Nak()
			return
		}
	}
	if err := c.repo.UpdateState(ctx, job, repository.PurgeMetadataRemoved, false, ""); err != nil {
		_ = msg.Nak()
		return
	}
	if err := c.repo.UpdateState(ctx, job, repository.PurgeComplete, false, ""); err != nil {
		_ = msg.Nak()
		return
	}
	_ = msg.Ack()
}
