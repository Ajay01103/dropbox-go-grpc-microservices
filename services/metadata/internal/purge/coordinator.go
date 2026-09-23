package purge

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Ajay01103/go-dropbox/metadata/internal/repository"
	"github.com/Ajay01103/go-dropbox/pkg/events"
	eventsv2 "github.com/Ajay01103/go-dropbox/pkg/gen/events/v2"
	"github.com/Ajay01103/go-dropbox/pkg/natsx"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ThumbnailDeleter can remove a thumbnail object from storage. The thumbnail
// worker satisfies this interface, but a nil value is also accepted (deletion
// is skipped gracefully).
type ThumbnailDeleter interface {
	// DeleteThumbnail deletes the thumbnail object for fileID stored under
	// key. Implementations must refuse keys outside thumbnails/<file_id>/
	// so a malformed row can never cause a cross-file deletion.
	DeleteThumbnail(ctx context.Context, fileID, key string) error
}

type Coordinator struct {
	repo             *repository.PurgeRepo
	metadataRepo     *repository.MetadataRepo
	thumbnailDeleter ThumbnailDeleter
	conn             *nats.Conn
	js             jetstream.JetStream
	logger           *zap.Logger
	cc             jetstream.ConsumeContext
}

func NewCoordinator(url string, repo *repository.PurgeRepo, metadataRepo *repository.MetadataRepo, logger *zap.Logger) (*Coordinator, error) {
	if url == "" {
		return nil, errors.New("nats url is empty")
	}
	conn, err := nats.Connect(url)
	if err != nil {
		return nil, fmt.Errorf("connect purge nats: %w", err)
	}
	js, err := jetstream.New(conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("create purge jetstream: %w", err)
	}
	return &Coordinator{
		repo: repo, metadataRepo: metadataRepo,
		conn: conn, js: js, logger: logger,
	}, nil
}

// SetThumbnailDeleter wires in a ThumbnailDeleter so the coordinator can
// remove thumbnail objects when a file is permanently deleted. Call this
// before Start.
func (c *Coordinator) SetThumbnailDeleter(d ThumbnailDeleter) {
	c.thumbnailDeleter = d
}

// maxOpsPerBatch is the v2 batch size (design B.5): a DecrefRequested carries
// at most 500 operations, each identified by its GLOBAL occurrence index.
const maxOpsPerBatch = 500

func (c *Coordinator) Start(ctx context.Context) error {
	// The streams (FILE_EVENTS, BLOCK_REFS_CMD, BLOCK_REFS_EVT) and the DLQ
	// are idempotent creates; ensure them before any consumer is made so boot
	// order between the services doesn't matter. EnsureTopology is the sole
	// topology owner.
	if err := natsx.EnsureTopology(ctx, c.js, 1); err != nil {
		return fmt.Errorf("ensure topology: %w", err)
	}
	// The Consume wrapper dead-letters to the DLQ stream; make sure it exists
	// before any message can exhaust its retries (additive-only, idempotent).
	if err := natsx.EnsureDLQ(ctx, c.js); err != nil {
		return err
	}

	cons, err := natsx.EnsurePullConsumer(ctx, c.js, events.StreamEvt, events.ConsumerPurgeCompletionV2,
		jetstream.ConsumerConfig{
			Durable:       events.ConsumerPurgeCompletionV2,
			FilterSubject: events.SubjDecrefCompleted,
			AckPolicy:     jetstream.AckExplicitPolicy,
			AckWait:       2 * time.Minute,
			MaxDeliver:    8,
			MaxAckPending: 8,
			BackOff: []time.Duration{
				2 * time.Second, 10 * time.Second, 1 * time.Minute,
				5 * time.Minute, 15 * time.Minute, 30 * time.Minute,
			},
		})
	if err != nil {
		return fmt.Errorf("ensure purge completion v2 consumer: %w", err)
	}
	cc, err := natsx.Consume(ctx, cons, events.ConsumerPurgeCompletionV2, slog.New(slogZap{c.logger}), c.js, c.handleCompletion,
		natsx.WithPullMaxMessages(1),
		natsx.WithHeartbeat(30*time.Second),
	)
	if err != nil {
		return fmt.Errorf("start purge completion v2 consumer: %w", err)
	}
	c.cc = cc
	go func() {
		<-ctx.Done()
		_ = c.Close()
	}()
	return nil
}

func (c *Coordinator) Close() error {
	if c.cc != nil {
		c.cc.Stop()
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

	if err := c.publishRequestV2(ctx, job); err != nil {
		return job, err
	}

	if job.State == repository.PurgePending {
		if err := c.repo.UpdateState(ctx, job, repository.PurgeDecrementRequested, ""); err != nil {
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

// publishRequestV2 splits the job's block list into ≤500-op batches and
// publishes one blocks.v2.decref.requested message per batch, each with its
// own MsgId (decref:<job>:<batch>), then records batch_count so the sweeper
// can re-drive any batch that never landed.
//
// Ordering within one job is safe: every batch carries the same job ID and
// the completion side unions into sets keyed by batch index, so batches may
// complete in any order and be recorded in any order.
func (c *Coordinator) publishRequestV2(ctx context.Context, job repository.PurgeJob) error {
	batchCount := (len(job.BlockHashList) + maxOpsPerBatch - 1) / maxOpsPerBatch
	if batchCount < 1 {
		batchCount = 1
	}
	if err := c.repo.SetBatchCount(ctx, job.JobID, batchCount); err != nil {
		return fmt.Errorf("record batch count: %w", err)
	}
	for batch := 0; batch < batchCount; batch++ {
		lo := batch * maxOpsPerBatch
		hi := lo + maxOpsPerBatch
		if hi > len(job.BlockHashList) {
			hi = len(job.BlockHashList)
		}
		ops := make([]*eventsv2.DecrefOp, 0, hi-lo)
		for i := lo; i < hi; i++ {
			hashBytes, err := hex.DecodeString(job.BlockHashList[i])
			if err != nil {
				return fmt.Errorf("decode block hash %q (batch %d): %w", job.BlockHashList[i], batch, err)
			}
			ops = append(ops, &eventsv2.DecrefOp{
				// GLOBAL occurrence index across the whole job — the ledger is
				// occurrence-addressed, so batching must not renumber ops.
				Occurrence: uint32(i),
				BlockHash:  hashBytes,
			})
		}
		evt := &eventsv2.DecrefRequested{
			JobId:       job.JobID,
			BatchIndex:  uint32(batch),
			BatchCount:  uint32(batchCount),
			Ops:         ops,
			Reason:      job.Reason,
			RequestedAt: timestamppb.New(time.Now().UTC()),
		}
		// FIRST publish of this batch: plain per-batch MsgId. A sweeper
		// PENDING re-drive of a v2 job also goes through here for batches the
		// original never published — the per-batch MsgId is still fresh for
		// those, and dedup-swallowed for any that secretly did land.
		if err := natsx.PublishProto(ctx, c.js, events.SubjDecrefRequested,
			events.MsgIDDecrefRequested(job.JobID, uint32(batch)), evt); err != nil {
			return fmt.Errorf("publish v2 decrement request (batch %d/%d): %w", batch+1, batchCount, err)
		}
	}
	return nil
}

// PublishRequestRedrive re-publishes only the batches a REQUESTED job is
// still missing, with attempt-suffixed MsgIds (decref:<job>:<batch>:r<attempt>).
// The original per-batch publish was already delivered at least once, so a
// re-drive is a genuine loss-retry and MUST NOT reuse a MsgId the dedup
// window might swallow.
func (c *Coordinator) PublishRequestRedrive(ctx context.Context, job repository.PurgeJob, attempt int) error {
	if job.BatchCount <= 0 {
		return errors.New("v2 redrive on job with no batch_count")
	}
	done := make(map[int]bool, len(job.BatchesDone))
	for _, b := range job.BatchesDone {
		done[b] = true
	}
	for batch := 0; batch < job.BatchCount; batch++ {
		if done[batch] {
			continue
		}
		if err := c.publishBatchRedrive(ctx, job, batch, attempt); err != nil {
			return err
		}
	}
	return nil
}

// publishBatchRedrive rebuilds and republishes one batch of a v2 job.
func (c *Coordinator) publishBatchRedrive(ctx context.Context, job repository.PurgeJob, batch, attempt int) error {
	lo := batch * maxOpsPerBatch
	hi := lo + maxOpsPerBatch
	if hi > len(job.BlockHashList) {
		hi = len(job.BlockHashList)
	}
	if lo >= len(job.BlockHashList) {
		return fmt.Errorf("batch %d out of range for job with %d blocks", batch, len(job.BlockHashList))
	}
	ops := make([]*eventsv2.DecrefOp, 0, hi-lo)
	for i := lo; i < hi; i++ {
		hashBytes, err := hex.DecodeString(job.BlockHashList[i])
		if err != nil {
			return fmt.Errorf("decode block hash %q (batch %d): %w", job.BlockHashList[i], batch, err)
		}
		ops = append(ops, &eventsv2.DecrefOp{
			Occurrence: uint32(i),
			BlockHash:  hashBytes,
		})
	}
	evt := &eventsv2.DecrefRequested{
		JobId:       job.JobID,
		BatchIndex:  uint32(batch),
		BatchCount:  uint32(job.BatchCount),
		Ops:         ops,
		Reason:      job.Reason,
		RequestedAt: timestamppb.New(time.Now().UTC()),
	}
	return natsx.PublishProto(ctx, c.js, events.SubjDecrefRequested,
		events.MsgIDDecrefRequestedRedrive(job.JobID, uint32(batch), uint32(attempt)), evt)
}

// PublishRequest is the sweeper's v2 re-drive entry point (SweeperDeps).
// PENDING (batch_count unset) → full publish with plain per-batch MsgIds;
// REQUESTED (batch_count set) → re-publish only the missing batches with
// attempt-suffixed MsgIds.
func (c *Coordinator) PublishRequest(ctx context.Context, job repository.PurgeJob, attempt int) error {
	if job.BatchCount <= 0 {
		return c.publishRequestV2(ctx, job)
	}
	return c.PublishRequestRedrive(ctx, job, attempt)
}

// orphanNakThreshold: a completion for a missing job row is NAKed with a
// short delay this many times before being acked. Covers a coordinator crash
// between the worker's publish and the job row becoming visible / a job row
// deleted out from under an in-flight completion. After the threshold the
// message is acked — the row is gone for good and infinite redelivery just
// burns the log stream (the storm this fixes).
const orphanNakThreshold = 3
const orphanNakDelay = 3 * time.Second

// orphanCompletionOutcome maps the delivery count of an orphaned completion
// (job row missing) to its Outcome: brief retries to cover visibility races,
// then Ack once the row is provably gone for good.
func orphanCompletionOutcome(delivery int) natsx.Outcome {
	if delivery <= orphanNakThreshold {
		return natsx.RetryAfter(orphanNakDelay, errOrphanedCompletion)
	}
	return natsx.Ack()
}

var errOrphanedCompletion = errors.New("purge job not found for completion")

// handleCompletion processes a blocks.v2.decref.completed message: one
// batch of one job. Per-batch accounting unions into the job row's
// batches_done/invalid_ops sets (idempotent under duplicate completions);
// when the last batch lands the job advances to DECREMENTED and the saga
// finishes with metadata removal.
//
// INVALID ops do NOT fail the job (design failure matrix has no FAILED path
// for a bad hash): they are recorded in invalid_ops, logged once at ERROR,
// and the purge completes — a bad hash row will never get better.
func (c *Coordinator) handleCompletion(ctx context.Context, msg jetstream.Msg) natsx.Outcome {
	var event eventsv2.DecrefCompleted
	if err := proto.Unmarshal(msg.Data(), &event); err != nil {
		c.logger.Error("invalid v2 decrement completion — dead-lettering", zap.Error(err))
		return natsx.Term("invalid v2 completion payload: " + err.Error())
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	job, err := c.repo.Get(ctx, event.GetJobId())
	if err != nil {
		if strings.Contains(err.Error(), "purge job not found") {
			md, _ := msg.Metadata()
			delivery := 1
			if md != nil {
				delivery = int(md.NumDelivered)
			}
			out := orphanCompletionOutcome(delivery)
			if out == natsx.Ack() {
				c.logger.Warn("orphaned v2 completion acked after retries",
					zap.String("jobID", event.GetJobId()),
					zap.Uint32("batch", event.GetBatchIndex()),
					zap.Int("delivery", delivery))
			}
			return out
		}
		c.logger.Error("load purge job for v2 completion",
			zap.String("jobID", event.GetJobId()), zap.Error(err))
		return natsx.Retry(err) // DB error — transient
	}

	// Idempotency: terminal or post-decrement state — the batch outcome no
	// longer matters, just ack.
	switch job.State {
	case repository.PurgeComplete,
		repository.PurgeDecremented,
		repository.PurgeFailed:
		return natsx.Ack()
	}

	if event.GetBatchCount() > 0 && job.BatchCount > 0 && event.GetBatchCount() != uint32(job.BatchCount) {
		c.logger.Error("v2 completion batch_count mismatch",
			zap.String("jobID", job.JobID),
			zap.Uint32("eventBatchCount", event.GetBatchCount()),
			zap.Int("jobBatchCount", job.BatchCount))
		return natsx.Retry(errors.New("v2 batch_count mismatch"))
	}

	invalid := make([]int, 0)
	for _, r := range event.GetResults() {
		if r.GetOutcome() == eventsv2.DecrefResult_INVALID {
			invalid = append(invalid, int(r.GetOccurrence()))
		}
	}
	allDone, err := c.repo.RecordBatch(ctx, job.JobID, int(event.GetBatchIndex()), invalid)
	if err != nil {
		c.logger.Error("record v2 batch",
			zap.String("jobID", job.JobID),
			zap.Uint32("batch", event.GetBatchIndex()), zap.Error(err))
		return natsx.Retry(err)
	}
	if len(invalid) > 0 {
		c.logger.Error("v2 decrement batch had INVALID ops; recorded, purge continues",
			zap.String("jobID", job.JobID),
			zap.Uint32("batch", event.GetBatchIndex()),
			zap.Any("invalidOccurrences", invalid))
	}
	if !allDone {
		// Batches complete independently; ack this one and wait for the rest.
		return natsx.Ack()
	}

	// Last batch: advance and finish. INVALID ops keep the job DECREMENTED
	// (the decrement DID apply for everything valid); they are visible via
	// invalid_ops on the status API.
	lastError := ""
	if len(job.InvalidOps) > 0 {
		lastError = fmt.Sprintf("%d invalid ops recorded", len(job.InvalidOps))
	}
	if err := c.repo.UpdateState(ctx, job, repository.PurgeDecremented, lastError); err != nil {
		c.logger.Error("update purge job state to decremented (v2)",
			zap.String("jobID", job.JobID), zap.Error(err))
		return natsx.Retry(err)
	}
	job.State = repository.PurgeDecremented

	if err := c.RemoveMetadataAndComplete(ctx, job); err != nil {
		c.logger.Error("metadata removal failed (v2)",
			zap.String("jobID", job.JobID), zap.Error(err))
		return natsx.Retry(err)
	}
	return natsx.Ack()
}

// slogZap bridges the metadata service's zap logger to the *slog.Logger the
// natsx wrapper expects.
type slogZap struct{ l *zap.Logger }

func (h slogZap) Enabled(_ context.Context, level slog.Level) bool {
	switch {
	case level >= slog.LevelError:
		return h.l.Core().Enabled(zapcore.ErrorLevel)
	case level >= slog.LevelWarn:
		return h.l.Core().Enabled(zapcore.WarnLevel)
	case level >= slog.LevelInfo:
		return h.l.Core().Enabled(zapcore.InfoLevel)
	default:
		return h.l.Core().Enabled(zapcore.DebugLevel)
	}
}

func (h slogZap) Handle(_ context.Context, r slog.Record) error {
	fields := make([]zap.Field, 0, r.NumAttrs()+2)
	r.Attrs(func(attr slog.Attr) bool {
		fields = append(fields, zap.Any(attr.Key, attr.Value.Resolve()))
		return true
	})
	switch {
	case r.Level >= slog.LevelError:
		h.l.Error(r.Message, fields...)
	case r.Level >= slog.LevelWarn:
		h.l.Warn(r.Message, fields...)
	case r.Level >= slog.LevelInfo:
		h.l.Info(r.Message, fields...)
	default:
		h.l.Debug(r.Message, fields...)
	}
	return nil
}

func (h slogZap) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h slogZap) WithGroup(string) slog.Handler { return h }

// RemoveMetadataAndComplete finishes the saga once decrements have landed:
// removes the file's metadata projections and marks the job COMPLETE. It is
// idempotent and exported because the recovery sweeper re-drives jobs stuck
// in a post-decrement state. There is no intermediate METADATA_REMOVED
// checkpoint: metadata removal is idempotent, so a crash anywhere here is
// repaired by re-running the whole step.
func (c *Coordinator) RemoveMetadataAndComplete(ctx context.Context, job repository.PurgeJob) error {
	if job.State == repository.PurgeComplete {
		return nil
	}

	file, err := c.metadataRepo.GetFileByID(ctx, job.OwnerID, job.FileID)
	if err != nil {
		if !strings.Contains(err.Error(), "file not found") {
			return fmt.Errorf("load file for purge: %w", err)
		}
		// The files_by_id row is gone (already purged, or a job whose
		// core rows were removed before a crash). The other projections may
		// still exist — e.g. the updated_at-keyed sort indexes were never
		// rewritten by soft delete, so their rows outlive files_by_id. Run the
		// projection removal with the job's own folder/filename values; every
		// step is idempotent, and the updated_at-keyed sweeps are
		// timestamp-independent.
		file = repository.File{
			FileID: job.FileID, FolderID: job.FolderID, OwnerID: job.OwnerID,
			Filename: "", BlockHashList: job.BlockHashList,
		}
		if err := c.metadataRepo.RemoveFileProjections(ctx, file); err != nil {
			return fmt.Errorf("remove residual projections for vanished file: %w", err)
		}
	} else if file.Filename != "" {
		// Delete the thumbnail from storage before removing DB projections
		// so that a crash here causes a retry that re-attempts both steps.			// DeleteThumbnail enforces the thumbnails/<file_id>/ prefix rule, so
			// a malformed row can never cause a cross-file deletion.
		if file.ThumbnailKey != "" && c.thumbnailDeleter != nil {
			if err := c.thumbnailDeleter.DeleteThumbnail(ctx, job.FileID, file.ThumbnailKey); err != nil {
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

	if err := c.repo.UpdateState(ctx, job, repository.PurgeComplete, job.LastError); err != nil {
		return fmt.Errorf("mark purge complete: %w", err)
	}
	return nil
}
