// consumer.go — the JetStream side of the thumbnail worker: consumes
// FileStored events (files.v2.stored) on FILE_EVENTS via the natsx Outcome
// wrapper, and drives the storage plumbing in generator.go.
package thumbnail

import (
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	eventsv2 "github.com/Ajay01103/go-dropbox/pkg/gen/events/v2"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/Ajay01103/go-dropbox/metadata/internal/repository"
	"github.com/Ajay01103/go-dropbox/pkg/events"
	"github.com/Ajay01103/go-dropbox/pkg/natsx"
)

// thumbnailBackOff: entries past the first are the slow safety net; the
// wrapper's retry schedule drives the quick retries. BackOff[0] (5s) is
// above any transient storage hiccup plus the heartbeat interval; later
// entries back off to 30m so a poisoned message doesn't hot-loop for days.
var thumbnailBackOff = []time.Duration{
	5 * time.Second, 30 * time.Second, 2 * time.Minute, 10 * time.Minute, 30 * time.Minute,
}

// ThumbnailWorker consumes FileStored events (files.v2.stored) and generates
// thumbnails with PER-FILE keys: thumbnails/<file_id>/<content_sha>.jpg.
//
// The per-file prefix fixes the shared-key purge bug: a purge deletes
// thumbnails/<file_id>/ without touching other files' thumbnails.
type ThumbnailWorker struct {
	*Worker
	conn *nats.Conn
	cc   jetstream.ConsumeContext
}

// NewThumbnailWorker wires the JetStream consumer onto the storage plumbing
// in generator.go (blocks, S3/local thumbnails, repo).
func NewThumbnailWorker(url string, base *Worker) (*ThumbnailWorker, error) {
	conn, err := natsx.Connect(url)
	if err != nil {
		return nil, fmt.Errorf("connect nats for thumbnail worker: %w", err)
	}
	return &ThumbnailWorker{Worker: base, conn: conn}, nil
}

func (w *ThumbnailWorker) Start(ctx context.Context) error {
	js, err := jetstream.New(w.conn)
	if err != nil {
		return fmt.Errorf("create jetstream: %w", err)
	}

	// FILE_EVENTS and the DLQ are idempotent creates; ensure them here so
	// boot order between upload and metadata doesn't matter.
	if err := natsx.EnsureTopology(ctx, js, 1); err != nil {
		return fmt.Errorf("ensure thumbnail topology: %w", err)
	}
	if err := natsx.EnsureDLQ(ctx, js); err != nil {
		return err
	}

	cons, err := natsx.EnsurePullConsumer(ctx, js, events.StreamFiles, events.ConsumerThumbnailV2,
		jetstream.ConsumerConfig{
			Durable:       events.ConsumerThumbnailV2,
			FilterSubject: events.SubjFileStored,
			AckPolicy:     jetstream.AckExplicitPolicy,
			AckWait:       2 * time.Minute,
			MaxDeliver:    6,
			MaxAckPending: 1,
			BackOff:       thumbnailBackOff,
		})
	if err != nil {
		return fmt.Errorf("ensure thumbnail consumer: %w", err)
	}

	cc, err := natsx.Consume(ctx, cons, events.ConsumerThumbnailV2, slog.New(slogAdapter{w.logger}), js, w.handleFileStored,
		natsx.WithPullMaxMessages(1),
		natsx.WithHeartbeat(30*time.Second),
		natsx.WithDeadLetterHook(w.onDeadLetter),
	)
	if err != nil {
		return fmt.Errorf("start thumbnail consumer: %w", err)
	}
	w.cc = cc
	return nil
}

func (w *ThumbnailWorker) Close() error {
	if w.cc != nil {
		w.cc.Stop()
	}
	if w.conn != nil {
		w.conn.Close()
	}
	return nil
}

// slogAdapter bridges the zap logger the metadata service uses to the
// *slog.Logger the natsx wrapper expects.
type slogAdapter struct{ l *zap.Logger }

func (a slogAdapter) Enabled(_ context.Context, level slog.Level) bool {
	switch {
	case level >= slog.LevelError:
		return a.l.Core().Enabled(zapcore.ErrorLevel)
	case level >= slog.LevelWarn:
		return a.l.Core().Enabled(zapcore.WarnLevel)
	case level >= slog.LevelInfo:
		return a.l.Core().Enabled(zapcore.InfoLevel)
	default:
		return a.l.Core().Enabled(zapcore.DebugLevel)
	}
}

func (a slogAdapter) Handle(_ context.Context, r slog.Record) error {
	fields := make([]zap.Field, 0, r.NumAttrs()+2)
	r.Attrs(func(attr slog.Attr) bool {
		fields = append(fields, zap.Any(attr.Key, attr.Value.Resolve()))
		return true
	})
	switch {
	case r.Level >= slog.LevelError:
		a.l.Error(r.Message, fields...)
	case r.Level >= slog.LevelWarn:
		a.l.Warn(r.Message, fields...)
	case r.Level >= slog.LevelInfo:
		a.l.Info(r.Message, fields...)
	default:
		a.l.Debug(r.Message, fields...)
	}
	return nil
}

func (a slogAdapter) WithAttrs(attrs []slog.Attr) slog.Handler {
	return a // attribute forwarding is not needed for the wrapper's logs
}

func (a slogAdapter) WithGroup(string) slog.Handler { return a }

// perFileThumbnailKey builds the thumbnail key: the file's own prefix plus
// the content hash of the ordered block list, so the thumbnailExists dedup
// still works across redeliveries of the same file.
func perFileThumbnailKey(fileID string, blockHashes []string) string {
	return fmt.Sprintf("thumbnails/%s/%s.jpg", fileID, hex.EncodeToString(sha256OfBlocks(blockHashes)))
}

// onDeadLetter marks the thumbnail row failed and clears the pending
// work-list row after the message has been successfully published to the DLQ.
// Best-effort: errors are logged only.
func (w *ThumbnailWorker) onDeadLetter(ctx context.Context, m jetstream.Msg, reason string) {
	var event eventsv2.FileStored
	if err := proto.Unmarshal(m.Data(), &event); err != nil {
		return // can't identify the file; nothing to mark
	}
	w.markFailed(ctx, &event)
}

func (w *ThumbnailWorker) markFailed(ctx context.Context, event *eventsv2.FileStored) {
	if _, err := w.repo.SetThumbnailLWT(ctx, event.GetOwnerId(), event.GetFileId(), "", "failed"); err != nil {
		w.logger.Warn("dead-letter hook: failed to mark thumbnail failed",
			zap.String("fileID", event.GetFileId()), zap.Error(err))
	}
	if err := w.repo.DeleteThumbnailPending(ctx, pendingBucket(event.GetStoredAt()), event.GetFileId()); err != nil {
		w.logger.Warn("dead-letter hook: failed to delete pending row",
			zap.String("fileID", event.GetFileId()), zap.Error(err))
	}
}

// handleFileStored processes one FileStored event: generates the thumbnail
// under a per-file key and clears the pending work-list row on completion.
func (w *ThumbnailWorker) handleFileStored(ctx context.Context, msg jetstream.Msg) natsx.Outcome {
	var event eventsv2.FileStored
	if err := proto.Unmarshal(msg.Data(), &event); err != nil {
		w.logger.Error("invalid file stored event", zap.Error(err))
		return natsx.Ack() // poison payload — no redelivery will fix it
	}
	if event.GetFileId() == "" || event.GetOwnerId() == "" {
		w.logger.Warn("file stored event missing ids", zap.String("fileID", event.GetFileId()))
		return natsx.Ack()
	}
	hashes := hashesToStrings(event.GetBlockHashes())
	if len(hashes) == 0 {
		w.logger.Warn("file stored event has no blocks", zap.String("fileID", event.GetFileId()))
		return natsx.Ack()
	}

	status := "failed"
	thumbnailKey := ""
	key, err := w.generateToKey(perFileThumbnailKey(event.GetFileId(), hashes), hashes)
	switch {
	case err == nil:
		thumbnailKey = key
		status = "ready"
	case isPermanent(err):
		w.logger.Warn("thumbnail generation failed permanently",
			zap.String("fileID", event.GetFileId()), zap.Error(err))
	default:
		// Transient: retry on the schedule; the pending row stays as the
		// sweeper's backstop in case the message itself is lost.
		w.logger.Warn("thumbnail generation failed transiently",
			zap.String("fileID", event.GetFileId()), zap.Error(err))
		return natsx.Retry(err)
	}

	// LWT-guarded update: if the file row vanished (deleted while we were
	// generating), applied=false. Ack instead of resurrecting a ghost row.
	applied, err := w.repo.SetThumbnailLWT(ctx, event.GetOwnerId(), event.GetFileId(), thumbnailKey, status)
	if err != nil {
		w.logger.Error("failed to update thumbnail metadata", zap.String("fileID", event.GetFileId()), zap.Error(err))
		return natsx.Retry(err) // DB error — transient
	}
	if !applied {
		w.logger.Info("file row vanished during thumbnail generation; acking without update",
			zap.String("fileID", event.GetFileId()))
	}

	// Generation finished (ready or permanent failure): clear the work list.
	// The bucket is derived from the event's stored_at, which normally matches
	// the row's created_at hour; a rare hour-boundary miss self-heals in the
	// next sweep pass (the sweeper reads rows WITH their own buckets).
	if err := w.repo.DeleteThumbnailPending(ctx, pendingBucket(event.GetStoredAt()), event.GetFileId()); err != nil {
		// Non-fatal: a stale pending row just causes one harmless sweep pass
		// that finds the file no longer pending and self-heals.
		w.logger.Warn("failed to delete thumbnail pending row",
			zap.String("fileID", event.GetFileId()), zap.Error(err))
	}
	return natsx.Ack()
}

// hashesToStrings converts the proto [][]byte block hashes (raw 32-byte
// SHA-256 digests, as published by the upload service and pinned by the
// golden test) to their lowercase hex string form.
func hashesToStrings(hashes [][]byte) []string {
	out := make([]string, 0, len(hashes))
	for _, h := range hashes {
		out = append(out, hex.EncodeToString(h))
	}
	return out
}

// pendingBucket derives the thumbnail_pending bucket from the event's
// stored_at timestamp; a zero/missing timestamp falls back to now.
func pendingBucket(ts *timestamppb.Timestamp) string {
	t := time.Now().UTC()
	if ts != nil {
		if st := ts.AsTime(); !st.IsZero() {
			t = st
		}
	}
	return repository.ThumbnailBucket(t)
}
