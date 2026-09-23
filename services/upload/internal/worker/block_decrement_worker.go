package worker

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/Ajay01103/go-dropbox/pkg/events"
	eventsv2 "github.com/Ajay01103/go-dropbox/pkg/gen/events/v2"
	"github.com/Ajay01103/go-dropbox/pkg/natsx"
	"github.com/Ajay01103/go-dropbox/upload/internal/repository"
)

// v2 worker: consumes batched DecrefRequested messages from
// BLOCK_REFS_CMD / blocks.v2.decref.requested, applies every op through the
// ledger (terminal in DB), then publishes the per-batch DecrefCompleted as
// the LAST step and acks.
//
// PROCESSING PROFILE (drives the consumer config — see natsx.DecrefConsumer):
// one message = up to 500 ops × one LWT each ≈ 10–25s, worst case ~2m. That
// is why this consumer runs with WithPullMaxMessages(2) + WithHeartbeat(30s)
// and MaxAckPending 4 — the B1 prefetch trap re-derived for batching.
//
// Ledger statuses collapse to CLAIMED → APPLIED | ABSENT | INVALID at the
// worker level:
//
//	APPLIED        — ref count decremented
//	ABSENT         — block row already gone (benign; retires DECREMENTED_WITH_ERRORS)
//	INVALID        — malformed hash (permanent; recorded, job still completes)

const maxOpsPerBatch = 1000 // sanity cap: proto batches are ≤500, reject bigger

// BlockDecrementWorker handles batched decrement requests.
type BlockDecrementWorker struct {
	repo          *repository.BlockRepo
	staleAfter    time.Duration
	gcGracePeriod time.Duration
	logger        *zap.Logger
	conn          *nats.Conn
	js          jetstream.JetStream
	cc            jetstream.ConsumeContext
}

// NewBlockDecrementWorker builds the worker. staleAfter comes from
// BLOCK_LEDGER_STALE (default 90s).
func NewBlockDecrementWorker(url string, staleAfter, gcGracePeriod time.Duration, repo *repository.BlockRepo, logger *zap.Logger) (*BlockDecrementWorker, error) {
	if url == "" {
		return nil, errors.New("nats url is required")
	}
	if staleAfter <= 0 {
		staleAfter = 90 * time.Second
	}
	if gcGracePeriod <= 0 {
		gcGracePeriod = 24 * time.Hour
	}
	conn, err := natsx.Connect(url)
	if err != nil {
		return nil, fmt.Errorf("connect nats for decrement worker: %w", err)
	}
	return &BlockDecrementWorker{
		repo: repo, staleAfter: staleAfter, gcGracePeriod: gcGracePeriod,
		logger: logger, conn: conn,
	}, nil
}

// Start ensures topology and begins consuming. The consumer is created
// with the full target config via EnsurePullConsumer.
func (w *BlockDecrementWorker) Start(ctx context.Context) error {
	js, err := jetstream.New(w.conn)
	if err != nil {
		return fmt.Errorf("create jetstream: %w", err)
	}
	w.js = js

	if err := natsx.EnsureTopology(ctx, js, 1); err != nil {
		return fmt.Errorf("ensure v2 topology: %w", err)
	}
	if err := natsx.EnsureDLQ(ctx, js); err != nil {
		return err
	}

	// BackOff[0] = staleAfter + 5s, built at runtime: NATS must not redeliver
	// before a claimed op's takeover window has elapsed.
	backOff := []time.Duration{
		w.staleAfter + 5*time.Second,
		5 * time.Minute, 15 * time.Minute, 1 * time.Hour,
	}
	cons, err := natsx.EnsurePullConsumer(ctx, js, events.StreamCmd, events.ConsumerDecrefV2,
		jetstream.ConsumerConfig{
			Durable:       events.ConsumerDecrefV2,
			FilterSubject: events.SubjDecrefRequested,
			AckPolicy:     jetstream.AckExplicitPolicy,
			AckWait:       backOff[0],
			MaxDeliver:    10,
			MaxAckPending: 4, // re-derived for batched cost; see file header
			BackOff:       backOff,
		})
	if err != nil {
		return fmt.Errorf("ensure decref-v2 consumer: %w", err)
	}

	cc, err := natsx.Consume(ctx, cons, events.ConsumerDecrefV2, slog.New(slogZap{w.logger}), js, w.handle,
		natsx.WithPullMaxMessages(2),        // bounded prefetch for slow batches
		natsx.WithHeartbeat(30*time.Second), // a 500-op batch extends its own ack deadline
		natsx.WithRetrySchedule([]time.Duration{10 * time.Second, 30 * time.Second, 2 * time.Minute}),
	)
	if err != nil {
		return fmt.Errorf("start decref-v2 consumer: %w", err)
	}
	w.cc = cc
	go func() {
		<-ctx.Done()
		_ = w.Close()
	}()
	return nil
}

func (w *BlockDecrementWorker) Close() error {
	if w.cc != nil {
		w.cc.Stop()
	}
	if w.conn != nil {
		w.conn.Close()
	}
	return nil
}

func (w *BlockDecrementWorker) handle(ctx context.Context, msg jetstream.Msg) natsx.Outcome {
	var req eventsv2.DecrefRequested
	if err := proto.Unmarshal(msg.Data(), &req); err != nil {
		w.logger.Error("invalid v2 decrement request payload", zap.Error(err))
		return natsx.Term("bad payload: " + err.Error())
	}
	if _, err := w.parseJobID(req.GetJobId()); err != nil {
		w.logger.Error("invalid v2 decrement job id", zap.String("jobID", req.GetJobId()))
		return natsx.Term("invalid job id")
	}
	if n := len(req.GetOps()); n == 0 || n > maxOpsPerBatch {
		w.logger.Error("invalid v2 decrement op count", zap.Int("ops", n))
		return natsx.Term("invalid op count")
	}

	results := make([]*eventsv2.DecrefResult, 0, len(req.GetOps()))
	for _, op := range req.GetOps() {
		res, claimedByOther, err := w.applyOp(ctx, req.GetJobId(), op)
		if err != nil {
			w.logger.Warn("v2 decrement op will be retried",
				zap.String("jobID", req.GetJobId()),
				zap.Uint32("occurrence", op.GetOccurrence()),
				zap.Bool("claimedByOther", claimedByOther),
				zap.Error(err))
			if claimedByOther {
				// Another delivery holds a fresh claim; retry after the stale
				// window so the takeover CAS can win.
				return natsx.RetryAfter(w.staleAfter, err)
			}
			return natsx.Retry(err) // transient DB error; schedule applies
		}
		results = append(results, res)
	}

	// Ledger ops are terminal; publish is the commit point. Publish FIRST
	// with a dedup MsgId, THEN ack — a crash after publish re-runs the
	// idempotent handler harmlessly.
	completion := &eventsv2.DecrefCompleted{
		JobId:       req.GetJobId(),
		BatchIndex:  req.GetBatchIndex(),
		BatchCount:  req.GetBatchCount(),
		Results:     results,
		ProcessedAt: timestamppb.Now(),
	}
	msgID := events.MsgIDDecrefCompleted(req.GetJobId(), req.GetBatchIndex())
	if err := natsx.PublishProto(ctx, w.js, events.SubjDecrefCompleted, msgID, completion); err != nil {
		w.logger.Error("publish v2 completion", zap.String("jobID", req.GetJobId()), zap.Error(err))
		return natsx.Retry(err)
	}
	return natsx.Ack()
}

// applyOp maps one v2 op through the ledger to an outcome. The op's terminal
// ledger state is written before returning, so the completion publish is the
// only remaining side effect.
func (w *BlockDecrementWorker) applyOp(ctx context.Context, jobID string, op *eventsv2.DecrefOp) (*eventsv2.DecrefResult, bool, error) {
	hashHex := hex.EncodeToString(op.GetBlockHash())
	claim, err := w.repo.ClaimBlockDecrement(ctx, jobID, int(op.GetOccurrence()), hashHex, w.staleAfter)
	if err != nil {
		return nil, false, err
	}
	row := claim.Operation

	if !claim.Claimed {
		switch row.Status {
		case repository.BlockDecrementClaimed:
			return nil, true, fmt.Errorf("decrement operation is currently claimed")
		default:
			// Terminal on a previous delivery: replay the recorded result.
			return w.resultFrom(row, row.Status), claim.WasReplay == true, nil
		}
	}

	// We hold the claim — do the work.
	if !isValidHash(hashHex) {
		row.Status, row.Error = repository.BlockDecrementFailed, "invalid block hash"
		if err := w.repo.UpdateBlockDecrement(ctx, row); err != nil {
			return nil, false, err
		}
		return w.resultFrom(row, repository.BlockDecrementFailed), false, nil
	}

	newCount, err := w.repo.DecrementRefCount(ctx, hashHex)
	if err != nil {
		if !repository.IsBlockNotFound(err) {
			return nil, false, err
		}
		// Block row already gone: benign ABSENT, not an error.
		row.Status, row.Error = repository.BlockDecrementComplete, "already absent"
		if err := w.repo.UpdateBlockDecrement(ctx, row); err != nil {
			return nil, false, err
		}
		return &eventsv2.DecrefResult{
			Occurrence: op.GetOccurrence(), BlockHash: op.GetBlockHash(),
			Outcome: eventsv2.DecrefResult_ALREADY_ABSENT,
		}, false, nil
	}

	row.Status, row.NewRefCount = repository.BlockDecrementCounterApplied, newCount
	row.BecameGCCandidate = newCount == 0
	if row.BecameGCCandidate {
		if err := w.repo.MarkGCCandidate(ctx, hashHex, w.gcGracePeriod); err != nil {
			return nil, false, err
		}
	}
	if err := w.repo.UpdateBlockDecrement(ctx, row); err != nil {
		return nil, false, err
	}
	return w.resultFrom(row, repository.BlockDecrementCounterApplied), false, nil
}

// resultFrom translates a ledger row + outcome class into a v2 DecrefResult.
func (w *BlockDecrementWorker) resultFrom(row repository.BlockDecrementOperation, status string) *eventsv2.DecrefResult {
	hash, _ := hex.DecodeString(row.BlockHash)
	out := eventsv2.DecrefResult_OUTCOME_UNSPECIFIED
	switch status {
	case repository.BlockDecrementCounterApplied:
		out = eventsv2.DecrefResult_APPLIED
	case repository.BlockDecrementComplete:
		out = eventsv2.DecrefResult_ALREADY_ABSENT
	case repository.BlockDecrementFailed:
		out = eventsv2.DecrefResult_INVALID
	}
	return &eventsv2.DecrefResult{
		Occurrence:        uint32(row.Occurrence),
		BlockHash:         hash,
		Outcome:           out,
		NewRefCount:       row.NewRefCount,
		BecameGcCandidate: row.BecameGCCandidate,
	}
}

func (w *BlockDecrementWorker) parseJobID(id string) (interface{}, error) {
	// uuid.Parse is done inside ClaimBlockDecrement via gocql.ParseUUID; here
	// we only sanity-check non-empty to keep the Term path testable.
	if id == "" {
		return nil, errors.New("empty job id")
	}
	return id, nil
}

func isValidHash(hashHex string) bool {
	if len(hashHex) != 64 {
		return false
	}
	_, err := hex.DecodeString(hashHex)
	return err == nil
}

// slogZap bridges the upload service's zap logger to the *slog.Logger the
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
