package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	httpclient "net/http"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"go.uber.org/zap"

	metadataPB "github.com/Ajay01103/go-dropbox/metadata/gen/pb"
	metadatapbconnect "github.com/Ajay01103/go-dropbox/metadata/gen/pb/pbconnect"
	"github.com/Ajay01103/go-dropbox/pkg/interceptor"
	"github.com/Ajay01103/go-dropbox/upload/internal/repository"
)

// ─── In-browser editing: block staging + versioned commit ───────────────────
//
// A save is two steps against this service, in this order:
//
//  1. WriteEditBlock, once per block the file does not already hold. The bytes
//     are hash-verified here (the same rule ReceiveChunk applies) and stored,
//     but deliberately NOT given a reference yet: the block is staged at
//     ref_count = 0 and registered as a GC candidate, so a save that is
//     abandoned or loses its race leaks nothing.
//  2. CommitEdit, once. The metadata service owns the version row and the head
//     pointer, and refuses to name bytes it cannot read back; only after it
//     reports a win does this service give the new version's blocks their
//     references and publish the versioned FileStored event.
//
// Doing the ref bookkeeping after the win is what makes the two services agree
// without a distributed transaction: a conflict leaves every count exactly as
// it was, and a crash between staging and commit leaves only staged blocks,
// which the GC reaps.

// editStageGracePeriod is how long a staged block survives without a commit
// claiming it. It is shorter than the general block GC grace period on
// purpose: a staged block is known to be unreferenced (that is what staging
// means), and an editor that has not committed within this window has either
// failed or been abandoned.
const editStageGracePeriod = time.Hour

// WriteEditBlockResult reports what happened to one block.
type WriteEditBlockResult struct {
	// Written is true when this call stored the bytes (staged).
	Written bool
	// Reused is true when the block was already in storage.
	Reused    bool
	SizeBytes int64
}

// CommitEditResult mirrors the metadata service's verdict, plus the transfer
// accounting the caller can show (how much of the version it did not have to
// send).
type CommitEditResult struct {
	Committed bool
	// Duplicate reports that the save had already been committed, so no block
	// references were added and no event was published for this call.
	Duplicate       bool
	Conflict        bool
	Version         int32
	ContentHash     string
	HeadVersion     int32
	HeadContentHash string
	BlocksReused    int64
}

// WriteEditBlock stores one block of a pending save.
//
// The block's hash is verified against the bytes before anything is written:
// this is the trust boundary for content-addressed storage, and a block that
// did not match its name would otherwise be indistinguishable from the real one
// for every future reader.
func (s *UploadService) WriteEditBlock(ctx context.Context, userID, fileID string, index int32, blockHash string, data []byte) (WriteEditBlockResult, error) {
	if fileID == "" {
		return WriteEditBlockResult{}, errors.New("file_id is required")
	}
	if userID == "" {
		return WriteEditBlockResult{}, errors.New("user id is required")
	}
	if index < 0 {
		return WriteEditBlockResult{}, fmt.Errorf("block index %d is invalid", index)
	}
	if len(blockHash) != sha256.Size*2 {
		return WriteEditBlockResult{}, errors.New("block hash must be a hex sha256 digest")
	}
	if _, err := hex.DecodeString(blockHash); err != nil {
		return WriteEditBlockResult{}, fmt.Errorf("block hash is not hex: %w", err)
	}
	if len(data) == 0 {
		return WriteEditBlockResult{}, errors.New("block data is empty")
	}
	// One block per request, bounded by the largest block size the service
	// hands out. Without this an authorized caller could stage unbounded bytes
	// under hashes no commit will ever reference (they would be GC'd, but only
	// after the grace period).
	if maxBlock := s.maxBlockSize(); maxBlock > 0 && int64(len(data)) > maxBlock {
		return WriteEditBlockResult{}, fmt.Errorf("block of %d bytes exceeds the %d byte block size", len(data), maxBlock)
	}
	actual := sha256.Sum256(data)
	if hex.EncodeToString(actual[:]) != blockHash {
		return WriteEditBlockResult{}, errors.New("block checksum mismatch")
	}
	if s.blockRepo == nil || s.blockGateway == nil {
		return WriteEditBlockResult{}, errors.New("block storage is not configured")
	}

	existing, err := s.blockRepo.GetBlock(ctx, blockHash)
	switch {
	case err == nil:
		// The block is already known. Repair the object if it vanished under a
		// zero-ref row (a crashed GC), then report it as reused: the caller
		// did not have to send it, and either way the bytes are there now.
		exists, existsErr := s.blockGateway.BlockExists(ctx, blockHash)
		if existsErr != nil {
			return WriteEditBlockResult{}, fmt.Errorf("check block storage: %w", existsErr)
		}
		if !exists {
			if _, writeErr := s.blockGateway.WriteBlock(ctx, blockHash, data); writeErr != nil {
				return WriteEditBlockResult{}, fmt.Errorf("repair missing block storage: %w", writeErr)
			}
		}
		return WriteEditBlockResult{Reused: true, SizeBytes: existing.SizeBytes}, nil
	case !repository.IsBlockNotFound(err):
		return WriteEditBlockResult{}, fmt.Errorf("lookup block: %w", err)
	}

	etag, err := s.blockGateway.WriteBlock(ctx, blockHash, data)
	if err != nil {
		return WriteEditBlockResult{}, fmt.Errorf("write block: %w", err)
	}
	if err := s.blockRepo.InsertStagedBlock(ctx, blockHash, int64(len(data)), etag, s.blockBackend); err != nil {
		return WriteEditBlockResult{}, fmt.Errorf("insert staged block: %w", err)
	}
	// Re-read to learn whether the row is ours or a concurrent uploader created
	// it with a live reference first (IF NOT EXISTS makes that a no-op for us).
	// Only a row that is still at zero references gets a GC candidate: a
	// candidate for a referenced block is harmless (the GC's fence requires
	// ref_count = 0) but needlessly rescans forever.
	staged, err := s.blockRepo.GetBlock(ctx, blockHash)
	if err == nil && staged.RefCount == 0 {
		if err := s.blockRepo.MarkGCCandidate(ctx, blockHash, editStageGracePeriod); err != nil {
			s.logger.Warn("mark staged block for GC failed",
				zap.String("blockHash", blockHash), zap.Error(err))
		}
		return WriteEditBlockResult{Written: true, SizeBytes: int64(len(data))}, nil
	}
	return WriteEditBlockResult{Reused: true, SizeBytes: int64(len(data))}, nil
}

// CommitEdit turns staged blocks into the next immutable version of a file.
//
// The metadata service arbitrates: it re-checks ownership, the base version,
// the block list's shape, that every block is readable with the size it claims,
// and the document's sniffed type. This function only adds the things that are
// its own to add — block references and the event the preview pipeline
// listens on — and only after that arbitration reports a win.
func (s *UploadService) CommitEdit(ctx context.Context, userID string, req CommitEditRequest) (CommitEditResult, error) {
	if err := s.validateCommitEdit(req); err != nil {
		return CommitEditResult{}, err
	}
	sizes := blockSizesForEdit(req)

	verdict, err := s.callMetadataCommitVersion(ctx, req, sizes)
	if err != nil {
		return CommitEditResult{}, err
	}
	if verdict.GetConflict() || !verdict.GetCommitted() {
		return CommitEditResult{
			Conflict:        true,
			HeadVersion:     verdict.GetHeadVersion(),
			HeadContentHash: verdict.GetHeadContentHash(),
		}, nil
	}

	// A duplicate verdict means this exact save already landed (a retry after a
	// lost response). The version exists and already owns its references: giving
	// them out again would inflate every count, so nothing is applied and
	// nothing is published a second time.
	if verdict.GetDuplicate() {
		s.logger.Info("edit commit was already applied; retry resolved as success",
			zap.String("fileID", req.FileID), zap.Int32("version", verdict.GetVersion()))
		return CommitEditResult{
			Committed:       true,
			Duplicate:       true,
			Version:         verdict.GetVersion(),
			ContentHash:     verdict.GetContentHash(),
			HeadVersion:     verdict.GetVersion(),
			HeadContentHash: verdict.GetContentHash(),
		}, nil
	}

	reused, refErr := s.applyEditReferences(ctx, req.BlockHashes)
	if refErr != nil {
		// The version is durable; a reference that did not land is a leak the
		// block GC will not fix on its own, so it is worth a loud log and a
		// retry-worthy error, but not a failed save (the user's bytes are safe).
		s.logger.Error("giving a committed version its block references failed",
			zap.String("fileID", req.FileID), zap.Int32("version", verdict.GetVersion()),
			zap.Error(refErr))
	}
	s.publishEditStoredEvent(ctx, userID, req, verdict.GetVersion())

	return CommitEditResult{
		Committed:       true,
		Version:         verdict.GetVersion(),
		ContentHash:     verdict.GetContentHash(),
		HeadVersion:     verdict.GetVersion(),
		HeadContentHash: verdict.GetContentHash(),
		BlocksReused:    reused,
	}, nil
}

// CommitEditRequest is one save: the complete new version's ordered block list,
// whose bytes are already in storage.
type CommitEditRequest struct {
	FileID      string
	BaseVersion int32
	CommitID    string
	SizeBytes   int64
	BlockSize   int64
	BlockHashes []string
	// BlockSizes is optional; when present it must agree with what the server
	// derives from size and block size.
	BlockSizes  []int64
	Editor      string
	Filename    string
	ContentType string
}

// validateCommitEdit rejects a malformed save before anything is written. The
// metadata service validates the same shape again (it is the arbiter); failing
// here just means a bad request costs no round trip and no block writes.
func (s *UploadService) validateCommitEdit(req CommitEditRequest) error {
	if req.FileID == "" {
		return errors.New("file_id is required")
	}
	if req.BaseVersion <= 0 {
		return errors.New("base_version must be positive")
	}
	if req.SizeBytes <= 0 {
		return errors.New("size_bytes must be positive")
	}
	if req.BlockSize <= 0 {
		return errors.New("block_size must be positive")
	}
	if len(req.BlockHashes) == 0 {
		return errors.New("block_hashes is required")
	}
	if expected := (req.SizeBytes + req.BlockSize - 1) / req.BlockSize; int64(len(req.BlockHashes)) != expected {
		return fmt.Errorf("block list of %d does not cover %d bytes at %d bytes per block", len(req.BlockHashes), req.SizeBytes, req.BlockSize)
	}
	if len(req.BlockSizes) > 0 && len(req.BlockSizes) != len(req.BlockHashes) {
		return errors.New("block_sizes must pair positionally with block_hashes")
	}
	expectedSizes := blockSizesForEdit(req)
	for i, hash := range req.BlockHashes {
		if len(hash) != sha256.Size*2 {
			return fmt.Errorf("block %d has an invalid hash", i)
		}
		if _, err := hex.DecodeString(hash); err != nil {
			return fmt.Errorf("block %d has an invalid hash: %w", i, err)
		}
		if len(req.BlockSizes) > 0 && req.BlockSizes[i] != expectedSizes[i] {
			return fmt.Errorf("block %d declares %d bytes but the version adds up to %d", i, req.BlockSizes[i], expectedSizes[i])
		}
	}
	return nil
}

// blockSizesForEdit derives each block's exact size: every non-final block is
// the block size, the final block is the remainder.
func blockSizesForEdit(req CommitEditRequest) []int64 {
	sizes := make([]int64, len(req.BlockHashes))
	for i := range req.BlockHashes {
		sizes[i] = req.BlockSize
	}
	if last := len(sizes) - 1; last >= 0 {
		if remainder := req.SizeBytes % req.BlockSize; remainder != 0 {
			sizes[last] = remainder
		}
	}
	return sizes
}

// callMetadataCommitVersion forwards the save to the metadata service with the
// caller's own access token, so that service's ownership check is the caller's,
// not this service's.
func (s *UploadService) callMetadataCommitVersion(ctx context.Context, req CommitEditRequest, sizes []int64) (*metadataPB.CommitVersionResponse, error) {
	if s.cfg.MetadataURL == "" {
		return nil, errors.New("metadata service URL is not configured")
	}
	token, err := interceptor.AccessTokenFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("missing access token for metadata call: %w", err)
	}
	client := metadatapbconnect.NewMetadataServiceClient(
		connect.NewClient(connecthttp.NewTransport(httpclient.DefaultClient, s.cfg.MetadataURL, connecthttp.WithReadMaxBytes(0))),
	)
	callCtx, info := connect.NewClientContext(ctx)
	info.RequestHeader().Set("Authorization", "Bearer "+token)

	response, err := client.CommitVersion(callCtx, &metadataPB.CommitVersionRequest{
		FileId:      req.FileID,
		BaseVersion: req.BaseVersion,
		CommitId:    req.CommitID,
		SizeBytes:   req.SizeBytes,
		BlockSize:   req.BlockSize,
		BlockHashes: req.BlockHashes,
		BlockSizes:  sizes,
		Editor:      req.Editor,
		Filename:    req.Filename,
		ContentType: req.ContentType,
	})
	if err != nil {
		return nil, fmt.Errorf("commit version: %w", err)
	}
	return response, nil
}

// applyEditReferences gives the committed version's blocks their references.
//
// One increment per *occurrence* in the new list, not per unique hash: a
// version that holds the same block twice holds two references, which is
// exactly what the purge path decrements when that version is deleted. Every
// hash in the list also stops being a GC candidate — that is what protects a
// block that this save staged moments ago.
//
// Returns how many occurrences the file already held (ref_count > 0 before the
// increments), which is the part of the save the caller did not have to send.
func (s *UploadService) applyEditReferences(ctx context.Context, blockHashes []string) (int64, error) {
	if s.blockRepo == nil {
		return 0, errors.New("block repository is not configured")
	}
	occurrences := make(map[string]int, len(blockHashes))
	for _, hash := range blockHashes {
		occurrences[hash]++
	}
	var reused int64
	var firstErr error
	for hash, count := range occurrences {
		if err := s.blockRepo.RemoveGCCandidate(ctx, hash); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("clear gc candidate %s: %w", hash, err)
		}
		if existing, err := s.blockRepo.GetBlock(ctx, hash); err == nil && existing.RefCount > 0 {
			reused += int64(count)
		}
		for i := 0; i < count; i++ {
			if err := s.blockRepo.IncrementRefCount(ctx, hash); err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("increment reference %s: %w", hash, err)
				}
				break
			}
		}
	}
	return reused, firstErr
}

// publishEditStoredEvent announces the new version on the same subject a fresh
// upload does. Preview consumers read the version off the event, so the
// thumbnail pipeline regenerates for the new bytes instead of the ones it
// already rendered.
func (s *UploadService) publishEditStoredEvent(ctx context.Context, userID string, req CommitEditRequest, version int32) {
	if s.eventPublisher == nil {
		return
	}
	family := sniffContentFamily(ctx, s.blockGateway, firstBlockHash(req.BlockHashes))
	event := &FileStoredEvent{
		FileID:        req.FileID,
		FileVersion:   int64(version),
		BlockHashList: req.BlockHashes,
		ContentType:   req.ContentType,
		SizeBytes:     req.SizeBytes,
		OwnerID:       userID,
		StoredAtUnix:  time.Now().Unix(),
		ContentFamily: family,
	}
	if err := s.eventPublisher.PublishFileStored(ctx, event); err != nil {
		// The metadata service has already written the pending row, so the
		// thumbnail sweeper repairs this. Never fail a committed save over it.
		s.logger.Warn("file stored event publish failed after edit commit",
			zap.String("fileID", req.FileID), zap.Int64("version", event.FileVersion), zap.Error(err))
	}
}

// maxBlockSize is the largest block this service hands out, whichever constant
// the configuration set: sessions are created with the chunk size, while
// BLOCK_SIZE_BYTES is the legacy value older files were split with.
func (s *UploadService) maxBlockSize() int64 {
	if s.cfg.BlockSizeBytes > s.cfg.ChunkSizeBytes {
		return s.cfg.BlockSizeBytes
	}
	return s.cfg.ChunkSizeBytes
}
