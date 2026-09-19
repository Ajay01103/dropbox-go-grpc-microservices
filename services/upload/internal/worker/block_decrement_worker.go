package worker

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	blocks "github.com/Ajay01103/go-dropbox/pkg/gen/blocks/blocks/v1"
	"github.com/Ajay01103/go-dropbox/upload/internal/repository"
	"github.com/gocql/gocql"
	"github.com/nats-io/nats.go"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	blockRefsStream   = "BLOCK_REFS"
	blockRefsConsumer = "upload-block-decrement-worker"
)

type BlockDecrementWorker struct {
	repo         *repository.BlockRepo
	url          string
	requested    string
	completed    string
	staleAfter   time.Duration
	gcGracePeriod time.Duration
	logger       *zap.Logger
	conn         *nats.Conn
	js           nats.JetStreamContext
	sub          *nats.Subscription
}

func NewBlockDecrementWorker(url, requested, completed string, staleAfter, gcGracePeriod time.Duration, repo *repository.BlockRepo, logger *zap.Logger) (*BlockDecrementWorker, error) {
	if url == "" || requested == "" || completed == "" {
		return nil, errors.New("nats URL and block reference subjects are required")
	}
	if staleAfter <= 0 {
		staleAfter = 10 * time.Second
	}
	if gcGracePeriod <= 0 {
		gcGracePeriod = 24 * time.Hour
	}
	conn, err := nats.Connect(url)
	if err != nil {
		return nil, fmt.Errorf("connect nats for block decrement worker: %w", err)
	}
	js, err := conn.JetStream()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("create block decrement jetstream context: %w", err)
	}
	// Do NOT create the stream here — the purge coordinator (metadata service)
	// owns the BLOCK_REFS stream and creates it with WorkQueuePolicy.
	// We just verify it exists so we fail fast on misconfiguration.
	if _, err := js.StreamInfo(blockRefsStream); err != nil {
		conn.Close()
		return nil, fmt.Errorf("BLOCK_REFS stream not found — start the metadata service first: %w", err)
	}
	return &BlockDecrementWorker{repo: repo, url: url, requested: requested, completed: completed,
		staleAfter: staleAfter, gcGracePeriod: gcGracePeriod, logger: logger, conn: conn, js: js}, nil
}

func (w *BlockDecrementWorker) Start(ctx context.Context) error {
	// AckWait must match staleAfter so that NATS only re-delivers after our
	// stale-claim takeover window has elapsed.
	sub, err := w.js.Subscribe(w.requested, w.handle,
		nats.ConsumerName(blockRefsConsumer),
		nats.ManualAck(),
		nats.AckWait(w.staleAfter+5*time.Second), // slight buffer over staleAfter
	)
	if err != nil {
		return fmt.Errorf("subscribe to %s: %w", w.requested, err)
	}
	w.sub = sub
	go func() {
		<-ctx.Done()
		_ = w.Close()
	}()
	return nil
}

func (w *BlockDecrementWorker) Close() error {
	if w.sub != nil {
		_ = w.sub.Drain()
	}
	if w.conn != nil {
		w.conn.Close()
	}
	return nil
}

func (w *BlockDecrementWorker) handle(msg *nats.Msg) {
	var request blocks.BlockRefDecrementRequested
	if err := proto.Unmarshal(msg.Data, &request); err != nil {
		w.logger.Error("invalid block decrement request", zap.Error(err))
		_ = msg.Term() // poison pill — terminate, don't re-deliver
		return
	}
	if _, err := gocql.ParseUUID(request.GetJobId()); err != nil || len(request.GetOperations()) == 0 {
		w.logger.Error("invalid block decrement request fields", zap.String("jobID", request.GetJobId()))
		_ = msg.Term() // malformed — terminate
		return
	}

	results := make([]*blocks.BlockDecrementResult, 0, len(request.GetOperations()))
	wasReplay := false
	for _, requested := range request.GetOperations() {
		if requested == nil {
			w.logger.Error("block decrement request contains nil operation", zap.String("jobID", request.GetJobId()))
			_ = msg.Term()
			return
		}
		result, replay, claimedByOther, err := w.processOperation(context.Background(), request.GetJobId(), requested)
		if err != nil {
			w.logger.Warn("block decrement operation will be retried",
				zap.String("jobID", request.GetJobId()),
				zap.Uint32("occurrence", requested.GetOccurrence()),
				zap.Error(err))
			if claimedByOther {
				// Another in-flight delivery holds this claim. Back off until
				// the stale window elapses — don't spin.
				_ = msg.NakWithDelay(w.staleAfter)
			} else {
				_ = msg.Nak()
			}
			return
		}
		wasReplay = wasReplay || replay
		results = append(results, result)
	}

	completion := &blocks.BlockRefDecrementCompleted{
		SchemaVersion: 1, JobId: request.GetJobId(), Results: results,
		ProcessedAt: timestamppb.Now(), WasReplay: wasReplay,
	}
	payload, err := proto.Marshal(completion)
	if err != nil {
		w.logger.Error("marshal block decrement completion", zap.Error(err))
		_ = msg.Nak()
		return
	}
	// No MsgId dedup on completion: the coordinator checks the job's DB state
	// before acting, so duplicate completions are harmless and we must not
	// suppress re-publishes when the coordinator crashed mid-update.
	if _, err := w.js.Publish(w.completed, payload); err != nil {
		w.logger.Error("publish block decrement completion", zap.String("jobID", request.GetJobId()), zap.Error(err))
		_ = msg.Nak()
		return
	}
	for _, result := range results {
		operation := repository.BlockDecrementOperation{
			JobID:             request.GetJobId(),
			Occurrence:        int(result.GetOccurrence()),
			BlockHash:         result.GetBlockHash(),
			NewRefCount:       result.GetNewRefCount(),
			BecameGCCandidate: result.GetBecameGcCandidate(),
			Error:             result.GetError(),
			Status:            repository.BlockDecrementComplete,
		}
		if err := w.repo.UpdateBlockDecrement(context.Background(), operation); err != nil {
			w.logger.Error("mark block decrement complete", zap.String("jobID", request.GetJobId()), zap.Error(err))
			_ = msg.Nak()
			return
		}
	}
	_ = msg.Ack()
}

// processOperation claims and executes one block decrement.
// Returns (result, wasReplay, claimedByOther, error).
// claimedByOther=true means the row is CLAIMED by a concurrent delivery;
// the caller should NakWithDelay rather than re-delivering immediately.
func (w *BlockDecrementWorker) processOperation(ctx context.Context, jobID string, requested *blocks.BlockDecrementOperation) (*blocks.BlockDecrementResult, bool, bool, error) {
	claim, err := w.repo.ClaimBlockDecrement(ctx, jobID, int(requested.GetOccurrence()), requested.GetBlockHash(), w.staleAfter)
	if err != nil {
		return nil, false, false, err
	}
	op := claim.Operation

	if !claim.Claimed {
		switch op.Status {
		case repository.BlockDecrementClaimed:
			// Row is claimed by a concurrent delivery and not yet stale.
			// Signal the caller to back off with NakWithDelay.
			return nil, claim.WasReplay, true, fmt.Errorf("decrement operation is currently claimed")

		case repository.BlockDecrementCounterApplied, repository.BlockDecrementComplete, repository.BlockDecrementFailed:
			// Operation already reached a terminal state on a previous delivery.
			// Return the recorded result so this delivery can ACK and move on.
			return &blocks.BlockDecrementResult{
				Occurrence:        uint32(op.Occurrence),
				BlockHash:         op.BlockHash,
				NewRefCount:       op.NewRefCount,
				BecameGcCandidate: op.BecameGCCandidate,
				Error:             op.Error,
			}, claim.WasReplay, false, nil
		}
	}

	// We hold the claim — do the work.
	if len(op.BlockHash) != 64 || !isHex(op.BlockHash) {
		op.Status, op.Error = repository.BlockDecrementFailed, "invalid block hash"
		if err := w.repo.UpdateBlockDecrement(ctx, op); err != nil {
			return nil, claim.WasReplay, false, err
		}
	} else {
		newCount, err := w.repo.DecrementRefCount(ctx, op.BlockHash)
		if err != nil {
			if !repository.IsBlockNotFound(err) {
				return nil, claim.WasReplay, false, err
			}
			// Block already gone — treat as a non-fatal idempotent condition.
			op.Status, op.Error = repository.BlockDecrementFailed, err.Error()
		} else {
			op.Status, op.NewRefCount = repository.BlockDecrementCounterApplied, newCount
			op.BecameGCCandidate = newCount == 0
			if op.BecameGCCandidate {
				if err := w.repo.MarkGCCandidate(ctx, op.BlockHash, w.gcGracePeriod); err != nil {
					return nil, claim.WasReplay, false, err
				}
			}
		}
		if err := w.repo.UpdateBlockDecrement(ctx, op); err != nil {
			return nil, claim.WasReplay, false, err
		}
	}

	return &blocks.BlockDecrementResult{
		Occurrence:        uint32(op.Occurrence),
		BlockHash:         op.BlockHash,
		NewRefCount:       op.NewRefCount,
		BecameGcCandidate: op.BecameGCCandidate,
		Error:             op.Error,
	}, claim.WasReplay, false, nil
}

func isHex(hash string) bool {
	_, err := hex.DecodeString(hash)
	return err == nil
}
