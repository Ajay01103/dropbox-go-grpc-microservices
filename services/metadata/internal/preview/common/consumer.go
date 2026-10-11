package common

import (
	"context"
	"encoding/hex"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/Ajay01103/go-dropbox/metadata/internal/repository"
	"github.com/Ajay01103/go-dropbox/pkg/events"
	eventsv2 "github.com/Ajay01103/go-dropbox/pkg/gen/events/v2"
	"github.com/Ajay01103/go-dropbox/pkg/natsx"
)

// FamilyConsumer is the shared JetStream scaffold of the three preview
// workers. Each family embeds it with its own ConsumerConfig (resource
// envelope), Generator, and subject — near-duplication by design (see the
// plan): the families must not share a failure domain, so their control
// flow lives in their own packages and shares only this plumbing.
type FamilyConsumer struct {
	storage   *Storage
	repo      *repository.MetadataRepo
	logger    *zap.Logger
	generator Generator
	family    eventsv2.ContentFamily
	consumer  string
	subject   string
	cfg       jetstream.ConsumerConfig
	conn      *nats.Conn
	cc        jetstream.ConsumeContext
}

// NewFamilyConsumer wires one family's consumer. Exported for the family
// packages; not usable without a concrete Generator.
func NewFamilyConsumer(conn *nats.Conn, storage *Storage, repo *repository.MetadataRepo, logger *zap.Logger,
	gen Generator, family eventsv2.ContentFamily, consumerName, subject string, cfg jetstream.ConsumerConfig) *FamilyConsumer {
	return &FamilyConsumer{
		storage: storage, repo: repo, logger: logger,
		generator: gen, family: family, consumer: consumerName, subject: subject,
		cfg: cfg, conn: conn,
	}
}

// Generator is the family-specific "bytes in → preview JPEG stored out" step.
type Generator interface {
	// Family this generator handles; used for the defensive guard + logs.
	Family() eventsv2.ContentFamily
	// GenerateToKey produces a preview for the given blocks and stores it at
	// exactly key. Returns the key on success. Errors must be classifiable
	// via IsPermanent.
	GenerateToKey(ctx context.Context, key string, blockHashes []string) (string, error)
}

// Start ensures the stream/DLQ topology and the family's pull consumer, then
// begins consuming with MaxAckPending 1 pull (prefetch trap: long ffmpeg/pdf
// jobs would age out deeply prefetched messages).
func (c *FamilyConsumer) Start(ctx context.Context) error {
	js, err := jetstream.New(c.conn)
	if err != nil {
		return wrap("create jetstream", err)
	}
	if err := natsx.EnsureTopology(ctx, js, 1); err != nil {
		return wrap("ensure preview topology", err)
	}
	if err := natsx.EnsureDLQ(ctx, js); err != nil {
		return err
	}
	cons, err := natsx.EnsurePullConsumer(ctx, js, events.StreamFiles, c.consumer, c.cfg)
	if err != nil {
		return wrap("ensure preview consumer", err)
	}
	cc, err := natsx.Consume(ctx, cons, c.consumer, slog.New(slogAdapter{c.logger}), js, c.handleFileStored,
		natsx.WithPullMaxMessages(1),
		natsx.WithHeartbeat(30*time.Second),
		natsx.WithDeadLetterHook(c.onDeadLetter),
	)
	if err != nil {
		return wrap("start preview consumer", err)
	}
	c.cc = cc
	return nil
}

// Close stops the consumer and the NATS connection.
func (c *FamilyConsumer) Close() error {
	if c.cc != nil {
		c.cc.Stop()
	}
	if c.conn != nil {
		c.conn.Close()
	}
	return nil
}

// handleFileStored processes one FileStored event for this family.
//
// Defensive family guard first (§12.3 of the plan): if a routing bug ever
// delivered a foreign-family event here, ack it loudly instead of feeding a
// video to the image decoder.
func (c *FamilyConsumer) handleFileStored(ctx context.Context, msg jetstream.Msg) natsx.Outcome {
	var event eventsv2.FileStored
	if err := proto.Unmarshal(msg.Data(), &event); err != nil {
		c.logger.Error("invalid file stored event", zap.String("family", FamilyName(c.family)), zap.Error(err))
		return natsx.Ack() // poison payload — no redelivery will fix it
	}
	if event.GetFileId() == "" || event.GetOwnerId() == "" {
		c.logger.Warn("file stored event missing ids", zap.String("family", FamilyName(c.family)), zap.String("fileID", event.GetFileId()))
		return natsx.Ack()
	}
	if event.GetContentFamily() != c.family {
		c.logger.Error("FAMILY MISMATCH: foreign-family event reached this consumer (subject routing bug?)",
			zap.String("family", FamilyName(c.family)),
			zap.String("eventFamily", FamilyName(event.GetContentFamily())),
			zap.String("fileID", event.GetFileId()))
		return natsx.Ack()
	}
	hashes := HashesToStrings(event.GetBlockHashes())
	if len(hashes) == 0 {
		c.logger.Warn("file stored event has no blocks", zap.String("family", FamilyName(c.family)), zap.String("fileID", event.GetFileId()))
		return natsx.Ack()
	}

	status := "failed"
	thumbnailKey := ""
	key, err := c.generator.GenerateToKey(ctx, PerFileThumbnailKey(event.GetFileId(), hashes), hashes)
	switch {
	case err == nil:
		thumbnailKey = key
		status = "ready"
	case IsPermanent(err):
		c.logger.Warn("preview generation failed permanently",
			zap.String("family", FamilyName(c.family)),
			zap.String("fileID", event.GetFileId()), zap.Error(err))
	default:
		// Transient: retry on the schedule; the pending row stays as the
		// sweeper's backstop in case the message itself is lost.
		c.logger.Warn("preview generation failed transiently",
			zap.String("family", FamilyName(c.family)),
			zap.String("fileID", event.GetFileId()), zap.Error(err))
		return natsx.Retry(err)
	}

	// LWT-guarded update: if the file row vanished (deleted while we were
	// generating), applied=false. Ack instead of resurrecting a ghost row.
	applied, err := c.repo.SetThumbnailLWT(ctx, event.GetOwnerId(), event.GetFileId(), thumbnailKey, status, c.family)
	if err != nil {
		c.logger.Error("failed to update thumbnail metadata",
			zap.String("family", FamilyName(c.family)),
			zap.String("fileID", event.GetFileId()), zap.Error(err))
		return natsx.Retry(err) // DB error — transient
	}
	if !applied {
		c.logger.Info("file row vanished during preview generation; acking without update",
			zap.String("family", FamilyName(c.family)),
			zap.String("fileID", event.GetFileId()))
	}

	// Generation finished (ready or permanent failure): clear the work list.
	// A rare hour-boundary bucket miss self-heals in the next sweep pass.
	if err := c.repo.DeleteThumbnailPending(ctx, PendingBucket(event.GetStoredAt()), event.GetFileId()); err != nil {
		c.logger.Warn("failed to delete preview pending row",
			zap.String("family", FamilyName(c.family)),
			zap.String("fileID", event.GetFileId()), zap.Error(err))
	}
	return natsx.Ack()
}

// onDeadLetter marks the thumbnail row failed and clears the pending row
// after the message has been published to the DLQ. Best-effort.
func (c *FamilyConsumer) onDeadLetter(ctx context.Context, m jetstream.Msg, reason string) {
	var event eventsv2.FileStored
	if err := proto.Unmarshal(m.Data(), &event); err != nil {
		return // can't identify the file; nothing to mark
	}
	if _, err := c.repo.SetThumbnailLWT(ctx, event.GetOwnerId(), event.GetFileId(), "", "failed", c.family); err != nil {
		c.logger.Warn("dead-letter hook: failed to mark thumbnail failed",
			zap.String("family", FamilyName(c.family)),
			zap.String("fileID", event.GetFileId()), zap.Error(err))
	}
	if err := c.repo.DeleteThumbnailPending(ctx, PendingBucket(event.GetStoredAt()), event.GetFileId()); err != nil {
		c.logger.Warn("dead-letter hook: failed to delete pending row",
			zap.String("family", FamilyName(c.family)),
			zap.String("fileID", event.GetFileId()), zap.Error(err))
	}
}

// HashesToStrings converts the proto [][]byte block hashes (raw 32-byte
// SHA-256 digests) to lowercase hex strings.
func HashesToStrings(hashes [][]byte) []string {
	out := make([]string, 0, len(hashes))
	for _, h := range hashes {
		out = append(out, hex.EncodeToString(h))
	}
	return out
}

// PendingBucket derives the pending-work bucket from the event's stored_at.
func PendingBucket(ts *timestamppb.Timestamp) string {
	t := time.Now().UTC()
	if ts != nil {
		if st := ts.AsTime(); !st.IsZero() {
			t = st
		}
	}
	return repository.ThumbnailBucket(t)
}

// ─── small local helpers (avoid extra imports in call sites) ────────────────

// Wrap wraps an error with a message (exported for the family packages).
func Wrap(msg string, err error) error { return &wrapError{msg: msg, err: err} }

func wrap(msg string, err error) error { return Wrap(msg, err) }

type wrapError struct {
	msg string
	err error
}

func (e *wrapError) Error() string { return e.msg + ": " + e.err.Error() }
func (e *wrapError) Unwrap() error { return e.err }

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
