package service

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/Ajay01103/go-dropbox/metadata/internal/preview/common"
	"github.com/Ajay01103/go-dropbox/metadata/internal/repository"
	eventsv2 "github.com/Ajay01103/go-dropbox/pkg/gen/events/v2"
)

// ─── In-browser editing: PDF, DOCX, XLSX ────────────────────────────────────
//
// A save is a new immutable version, never a rewrite: one ordered block list
// plus one (file_id, version) row, and a head pointer that moves forward. Two
// editors saving the same base version therefore produce exactly one new
// version and one conflict — the loser is told where the file actually is
// instead of quietly overwriting work it never saw.

// ErrInvalidEdit marks a commit the client must fix: a malformed block list, a
// block size that is not the file's, an oversized payload, bytes that are not
// an editable document. Retrying it unchanged cannot succeed.
var ErrInvalidEdit = errors.New("invalid edit")

// ErrEditBlockUnavailable marks a commit whose blocks are not readable in
// storage right now. Uploaded blocks can still be propagating, so a retry (or
// a re-upload of the missing blocks) is a reasonable response — but no version
// row is written, so nothing downstream ever sees a half-written version.
var ErrEditBlockUnavailable = errors.New("edit block unavailable")

// Editable document types, as sniffed from the bytes.
const (
	EditTypePDF  = "pdf"
	EditTypeDocx = "docx"
	EditTypeXlsx = "xlsx"
)

// Reasons an edit session can be refused. These string values are part of the
// RPC contract (the SPA switches on them to choose its message), so they are
// spelled out here rather than assembled from the type name.
const (
	EditUnavailableDisabled = "disabled"
	EditUnavailableType     = "type_not_editable"
	EditUnavailableTooLarge = "too_large"
)

const (
	// pdfSniffPrefix is how much of the first block the %PDF- magic is looked
	// for in. A PDF header must be at the very start, but a byte-order mark or
	// a few junk bytes ahead of it are common enough to be worth tolerating.
	pdfSniffPrefix = 1024
	// zipSniffTail covers the ZIP central directory. Office files list their
	// parts there, so the last few KiB of the file are enough to tell a DOCX
	// from an XLSX from a plain zip without reading the whole file.
	zipSniffTail = 32 * 1024
	// maxEditBlocks bounds a commit's block list. 100k blocks is far past any
	// cap below (a 25 MiB file at an 8 MiB block size is 4 blocks); the guard
	// exists so a hostile commit cannot make us allocate or HEAD unbounded.
	maxEditBlocks = 100_000
)

// BlockReader is the piece of the content store the edit path needs: enough to
// prove a committed block list exists with the sizes it claims, and to sniff
// the document type from real bytes. Implemented by the content server's
// S3Storage, so it is set in main rather than constructed here.
type BlockReader interface {
	ReadBlockRange(ctx context.Context, hash string, offset, length int64) (io.ReadCloser, error)
	BlockExists(ctx context.Context, hash string, sizeBytes int64) (bool, error)
}

// ConsolidatedInvalidator drops a file's consolidated (Tier B) object so a
// committed version cannot be served from a stitched copy of its predecessor.
type ConsolidatedInvalidator interface {
	DeleteConsolidated(ctx context.Context, fileID string) error
}

// StitchInvalidator clears and re-queues a file's Tier B registration. The
// consolidated object is keyed per file (not per content hash) today, so a
// stale one would serve the superseded version's bytes after a commit.
type StitchInvalidator interface {
	Delete(ctx context.Context, fileID string) error
	Enqueue(ctx context.Context, fileID string, now time.Time) error
}

// SetStitchInvalidator wires the Tier B registry the edit path repairs after a
// commit. Nil leaves consolidated objects alone (a deployment that has the
// stitcher disabled).
func (s *MetadataService) SetStitchInvalidator(invalidator StitchInvalidator) {
	s.stitchInvalidator = invalidator
}

// ManifestInvalidator drops a file's cached download manifest after a commit.
// The content server caches manifests per file for a few minutes; without this
// the editor's reload of the version it just saved can be answered from the
// superseded manifest and refused as a stale base.
type ManifestInvalidator interface {
	InvalidateManifest(fileID string)
}

// SetManifestInvalidator wires the content server whose manifest cache the
// edit path has to drop after a commit. Nil leaves the cache to its TTL.
func (s *MetadataService) SetManifestInvalidator(invalidator ManifestInvalidator) {
	s.manifestInvalidator = invalidator
}

// SetBlockReader wires the content store the edit path verifies commits
// against. Nil disables editing entirely (every session reports disabled).
func (s *MetadataService) SetBlockReader(reader BlockReader, invalidator ConsolidatedInvalidator) {
	s.blockReader = reader
	s.consolidated = invalidator
}

// EditSession is everything the editor needs before it loads a byte: which
// version it is about to edit, the version-pinned URL to fetch it from, and
// the parameters a save must use (block size, cap, sniffed type).
type EditSession struct {
	CanEdit           bool
	UnavailableReason string
	Version           int32
	ContentHash       string
	TicketURL         string
	ExpiresInSeconds  int64
	BlockSize         int64
	MaxEditBytes      int64
	SniffedType       string
	Filename          string
	ContentType       string
	SizeBytes         int64
	BlockHashes       []string
}

// PrepareCommitResult is the advisory pre-flight answer for a save: which of
// the caller's hashes this file already holds, and where the head is.
type PrepareCommitResult struct {
	AlreadyReferenced []string
	HeadVersion       int32
	BaseIsHead        bool
}

// CommitVersionRequest is one save as the upload service reports it: the
// ordered block list the new version consists of, whose bytes were already
// written (and hash-verified) by that service.
type CommitVersionRequest struct {
	FileID      string
	OwnerID     string
	BaseVersion int32
	CommitID    string
	SizeBytes   int64
	BlockSize   int64
	BlockHashes []string
	// BlockSizes is optional; when present it must agree with the sizes the
	// server derives, and the server's derivation wins either way.
	BlockSizes  []int64
	Editor      string
	ContentType string
	Filename    string
}

// CommitVersionResult reports either a committed version or the conflict that
// refused it. Exactly one of Committed/Conflict is true.
type CommitVersionResult struct {
	Committed bool
	// Duplicate is true when these bytes were already committed, so the caller
	// must treat the version as existing rather than newly created (no fresh
	// block references, no second event).
	Duplicate       bool
	Conflict        bool
	Version         int32
	ContentHash     string
	HeadVersion     int32
	HeadContentHash string
	// ContentFamily is the file's preview family, needed by the caller to
	// publish the versioned FileStored event.
	ContentFamily int16
}

// editState is the resolved "what is the current version of this file" view.
// Every versioned read and write goes through it so the pre-version-history
// fallback lives in one place: a file with no version row is version 1,
// described by its own row on files_by_id.
type editState struct {
	Version     int32
	ContentHash string
	BlockHashes []string
	BlockSize   int64
	SniffedType string
}

// resolveEditState reads the head pointer and its version row, falling back to
// the file row for files created before editing existed.
func (s *MetadataService) resolveEditState(ctx context.Context, file repository.File) (editState, error) {
	state := editState{
		Version:     file.Version,
		ContentHash: file.ContentHash,
		BlockHashes: file.BlockHashList,
	}
	if state.Version <= 0 {
		state.Version = 1
	}
	head, err := s.metadataRepo.GetFileHead(ctx, file.FileID)
	if errors.Is(err, repository.ErrNoVersionRow) {
		// Pre-version-history file: the file row is version 1.
		state.BlockSize = s.deriveBlockSize(ctx, file.FileID, state.BlockHashes, file.SizeBytes)
		return state, nil
	}
	if err != nil {
		return editState{}, err
	}
	version, err := s.metadataRepo.GetFileVersion(ctx, file.FileID, head.CurrentVersion)
	if errors.Is(err, repository.ErrNoVersionRow) {
		// A head that points at a row which is not there (a crash between the
		// version insert and the head advance cannot produce this, but a purge
		// race can). Trust the file row rather than inventing a version.
		state.Version = head.CurrentVersion
		state.BlockSize = s.deriveBlockSize(ctx, file.FileID, state.BlockHashes, file.SizeBytes)
		return state, nil
	}
	if err != nil {
		return editState{}, err
	}
	state.Version = version.Version
	if version.ContentHash != "" {
		state.ContentHash = version.ContentHash
	}
	if len(version.BlockHashList) > 0 {
		state.BlockHashes = version.BlockHashList
	}
	state.SniffedType = version.SniffedType
	state.BlockSize = version.BlockSize
	if state.BlockSize <= 0 {
		state.BlockSize = s.deriveBlockSize(ctx, file.FileID, state.BlockHashes, version.SizeBytes)
	}
	return state, nil
}

// deriveBlockSize recovers the block size a file was written with. It is
// recorded on every version row written from now on; for rows that predate it
// the dense manifest carries it (every non-final block is exactly one block
// size), and a single-block file simply is its own block size.
func (s *MetadataService) deriveBlockSize(ctx context.Context, fileID string, blockHashes []string, sizeBytes int64) int64 {
	if len(blockHashes) == 1 && sizeBytes > 0 {
		return sizeBytes
	}
	manifest, err := s.metadataRepo.GetFileManifest(ctx, fileID)
	if err == nil && len(manifest.Blocks) > 1 {
		return manifest.Blocks[0].SizeBytes
	}
	return 0
}

// GetEditSession authorizes an edit and returns the parameters a save must
// use. The ticket it mints is version-pinned: the editor edits exactly the
// bytes the commit will name as its base.
func (s *MetadataService) GetEditSession(ctx context.Context, ownerID, fileID string) (EditSession, error) {
	file, err := s.GetFileOwned(ctx, ownerID, fileID)
	if err != nil {
		return EditSession{}, err
	}
	state, err := s.resolveEditState(ctx, file)
	if err != nil {
		return EditSession{}, err
	}

	sniffedType := state.SniffedType
	if sniffedType == "" {
		sniffedType, err = s.sniffFileVersion(ctx, file.FileID, state.Version, state.BlockHashes, state.BlockSize)
		if err != nil {
			return EditSession{}, err
		}
	}

	session := EditSession{
		Version:           state.Version,
		ContentHash:       state.ContentHash,
		ExpiresInSeconds:  int64(TicketTTL.Seconds()),
		BlockSize:         state.BlockSize,
		MaxEditBytes:      s.editCapFor(sniffedType),
		SniffedType:       sniffedType,
		Filename:          file.Filename,
		ContentType:       contentTypeForEditType(sniffedType, file.ContentType),
		SizeBytes:         file.SizeBytes,
		BlockHashes:       state.BlockHashes,
		CanEdit:           false,
		UnavailableReason: EditUnavailableType,
	}

	switch {
	case sniffedType == "":
		// Not one of the three editable formats (or renamed to look like one).
	case !s.editEnabledFor(sniffedType):
		session.UnavailableReason = EditUnavailableDisabled
	case file.SizeBytes > session.MaxEditBytes || file.SizeBytes <= 0:
		session.UnavailableReason = EditUnavailableTooLarge
	case s.blockReader == nil || len(state.BlockHashes) == 0 || state.BlockSize <= 0:
		// Without a content reader a commit could not verify a single block,
		// and without a block size the client cannot re-split the file to
		// reuse anything.
		session.UnavailableReason = EditUnavailableDisabled
	default:
		session.CanEdit = true
		session.UnavailableReason = ""
	}

	// The ticket is minted even when editing is refused: it is the only way
	// the editor can show the file it refuses to edit (a read of the exact
	// version the refusal is about).
	ticket, err := MintVersionedTicket(fileID, ownerID, "inline", file.Filename, state.Version, state.ContentHash, time.Now())
	if err != nil {
		return EditSession{}, err
	}
	encoded := EncodeTicket(ticket, s.cfg.DownloadTicketKey)
	session.TicketURL = fmt.Sprintf("%s/content/%s?ticket=%s&disposition=inline",
		strings.TrimRight(s.cfg.ClientContentBaseURL(), "/"), url.PathEscape(fileID), url.QueryEscape(encoded))
	return session, nil
}

// PrepareCommit answers, for one save attempt, which of the caller's blocks
// this file already holds and whether the base version is still the head.
//
// It answers only about blocks the caller's own file already references, so it
// cannot be used as a cross-user dedup oracle: a hash that belongs to somebody
// else's file is reported missing, not "already present".
func (s *MetadataService) PrepareCommit(ctx context.Context, ownerID, fileID string, baseVersion int32, blockHashes []string) (PrepareCommitResult, error) {
	file, err := s.GetFileOwned(ctx, ownerID, fileID)
	if err != nil {
		return PrepareCommitResult{}, err
	}
	state, err := s.resolveEditState(ctx, file)
	if err != nil {
		return PrepareCommitResult{}, err
	}
	known := make(map[string]struct{}, len(state.BlockHashes))
	for _, hash := range state.BlockHashes {
		known[hash] = struct{}{}
	}
	already := make([]string, 0, len(blockHashes))
	seen := make(map[string]struct{}, len(blockHashes))
	for _, hash := range blockHashes {
		if _, ok := known[hash]; !ok {
			continue
		}
		if _, dup := seen[hash]; dup {
			continue
		}
		seen[hash] = struct{}{}
		already = append(already, hash)
	}
	return PrepareCommitResult{
		AlreadyReferenced: already,
		HeadVersion:       state.Version,
		BaseIsHead:        baseVersion == state.Version,
	}, nil
}

// CommitVersion records edited bytes as the next version of a file.
//
// The upload service calls this after it has written (and hash-verified) every
// block of the new version, because block writes and ref-counting are its
// responsibility. This function owns the ordering that makes the commit safe:
// validate everything, win the version number with IF NOT EXISTS, and only
// then move the head and rewrite the projections a reader sees.
func (s *MetadataService) CommitVersion(ctx context.Context, req CommitVersionRequest) (CommitVersionResult, error) {
	if req.FileID == "" || req.OwnerID == "" {
		return CommitVersionResult{}, errors.New("file id and owner id are required")
	}
	file, err := s.GetFileOwned(ctx, req.OwnerID, req.FileID)
	if err != nil {
		return CommitVersionResult{}, err
	}
	state, err := s.resolveEditState(ctx, file)
	if err != nil {
		return CommitVersionResult{}, err
	}
	if err := s.validateCommit(req, state); err != nil {
		return CommitVersionResult{}, err
	}

	contentHash := hex.EncodeToString(common.Sha256OfBlocks(req.BlockHashes))
	// The client is behind: another save already took base+1. If the head holds
	// exactly the bytes this save is trying to commit, this is a retry of a save
	// whose response went missing — success, and flagged as a duplicate so the
	// caller does not count the version's blocks as newly referenced. Otherwise
	// the file really has moved on and the caller is told where to.
	if req.BaseVersion < state.Version {
		if state.ContentHash == contentHash {
			return CommitVersionResult{
				Committed: true, Duplicate: true, Version: state.Version, ContentHash: contentHash,
				HeadVersion: state.Version, HeadContentHash: state.ContentHash,
				ContentFamily: int16(file.ContentFamily),
			}, nil
		}
		return CommitVersionResult{Conflict: true, HeadVersion: state.Version, HeadContentHash: state.ContentHash}, nil
	}

	sizes := s.blockSizesForCommit(req)
	if err := s.verifyCommitBlocks(ctx, req.BlockHashes, sizes); err != nil {
		return CommitVersionResult{}, err
	}
	sniffedType, err := s.sniffBlockList(ctx, req.BlockHashes, sizes)
	if err != nil {
		return CommitVersionResult{}, err
	}
	if sniffedType == "" {
		return CommitVersionResult{}, fmt.Errorf("%w: edited bytes are not an editable document (pdf, docx or xlsx)", ErrInvalidEdit)
	}
	if state.SniffedType != "" && state.SniffedType != sniffedType {
		return CommitVersionResult{}, fmt.Errorf("%w: edited bytes are a %s but the file is a %s", ErrInvalidEdit, sniffedType, state.SniffedType)
	}

	filename := strings.TrimSpace(req.Filename)
	if filename == "" {
		filename = file.Filename
	}
	applied, err := s.metadataRepo.CommitVersionRow(ctx, repository.CommitVersionInput{
		FileID:      req.FileID,
		OwnerID:     req.OwnerID,
		BaseVersion: req.BaseVersion,
		BlockHashes: req.BlockHashes,
		BlockSize:   req.BlockSize,
		SizeBytes:   req.SizeBytes,
		ContentHash: contentHash,
		ContentType: contentTypeForEditType(sniffedType, req.ContentType),
		Filename:    filename,
		SniffedType: sniffedType,
		Editor:      req.Editor,
		CommitID:    req.CommitID,
	})
	if err != nil {
		return CommitVersionResult{}, err
	}
	if !applied {
		// Lost the version number. If the head already holds exactly these
		// bytes, this is a retry of a save whose response was lost — success,
		// not a conflict (the client is told about it either way).
		head, headErr := s.resolveEditState(ctx, file)
		if headErr != nil {
			return CommitVersionResult{}, headErr
		}
		if head.ContentHash == contentHash {
			return CommitVersionResult{
				Committed: true, Duplicate: true, Version: head.Version, ContentHash: contentHash,
				HeadVersion: head.Version, HeadContentHash: head.ContentHash,
				ContentFamily: int16(file.ContentFamily),
			}, nil
		}
		return CommitVersionResult{Conflict: true, HeadVersion: head.Version, HeadContentHash: head.ContentHash}, nil
	}

	version := req.BaseVersion + 1
	if err := s.applyCommittedVersion(ctx, file, req, sizes, version, contentHash, sniffedType, filename); err != nil {
		// The version row is durable and immutable; the head already moved.
		// Failing here means a projection lagged, which the next read repairs,
		// so the save is still reported as committed.
		s.logger.Error("commit projections failed after the version was won",
			zap.String("fileID", req.FileID), zap.Int32("version", version), zap.Error(err))
	}
	return CommitVersionResult{
		Committed: true, Version: version, ContentHash: contentHash,
		HeadVersion: version, HeadContentHash: contentHash,
		ContentFamily: int16(file.ContentFamily),
	}, nil
}

// validateCommit rejects everything that must never reach a version row: a
// base from the future, a block size that is not the one the file was written
// with (re-splitting differently would re-send the whole file), a block list
// that does not add up to the declared size, and a size over the type's cap.
func (s *MetadataService) validateCommit(req CommitVersionRequest, state editState) error {
	if req.BaseVersion <= 0 {
		return fmt.Errorf("%w: base version must be positive", ErrInvalidEdit)
	}
	if req.BaseVersion > state.Version {
		return fmt.Errorf("%w: base version %d is ahead of head %d", ErrInvalidEdit, req.BaseVersion, state.Version)
	}
	if req.SizeBytes <= 0 {
		return fmt.Errorf("%w: size_bytes must be positive", ErrInvalidEdit)
	}
	if req.BlockSize <= 0 {
		return fmt.Errorf("%w: block_size must be positive", ErrInvalidEdit)
	}
	if state.BlockSize > 0 && req.BlockSize != state.BlockSize {
		return fmt.Errorf("%w: block size %d does not match the file's block size %d", ErrInvalidEdit, req.BlockSize, state.BlockSize)
	}
	if len(req.BlockHashes) == 0 || len(req.BlockHashes) > maxEditBlocks {
		return fmt.Errorf("%w: block list must hold between 1 and %d blocks", ErrInvalidEdit, maxEditBlocks)
	}
	expected := (req.SizeBytes + req.BlockSize - 1) / req.BlockSize
	if int64(len(req.BlockHashes)) != expected {
		return fmt.Errorf("%w: block list of %d does not cover %d bytes at %d bytes per block", ErrInvalidEdit, len(req.BlockHashes), req.SizeBytes, req.BlockSize)
	}
	if len(req.BlockSizes) > 0 && len(req.BlockSizes) != len(req.BlockHashes) {
		return fmt.Errorf("%w: block_sizes must pair positionally with block_hashes", ErrInvalidEdit)
	}
	cap := s.editCapFor(state.SniffedType)
	if req.SizeBytes > cap {
		return fmt.Errorf("%w: edited size %d exceeds the %d byte cap for %s", ErrInvalidEdit, req.SizeBytes, cap, state.SniffedType)
	}
	for i, hash := range req.BlockHashes {
		if len(hash) != 64 {
			return fmt.Errorf("%w: block %d has an invalid hash", ErrInvalidEdit, i)
		}
		if _, err := hex.DecodeString(hash); err != nil {
			return fmt.Errorf("%w: block %d has an invalid hash: %v", ErrInvalidEdit, i, err)
		}
	}
	return nil
}

// blockSizesForCommit derives each block's exact size from the declared total
// and block size: every non-final block is the block size, the final block is
// the remainder. Derived, never trusted from the client.
func (s *MetadataService) blockSizesForCommit(req CommitVersionRequest) []int64 {
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

// verifyCommitBlocks proves every block of the new version exists in storage
// with the size the version row is about to claim. Bounded parallelism: a
// 25 MiB file is a handful of HEADs, but a legacy 4 MiB-block file can be
// hundreds, and they are independent round trips.
func (s *MetadataService) verifyCommitBlocks(ctx context.Context, hashes []string, sizes []int64) error {
	if s.blockReader == nil {
		return fmt.Errorf("%w: content storage is not configured for editing", ErrEditBlockUnavailable)
	}
	const workers = 8
	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		failure   error
		semaphore = make(chan struct{}, workers)
	)
	for i, hash := range hashes {
		if failure != nil {
			break
		}
		wg.Add(1)
		semaphore <- struct{}{}
		go func(index int, blockHash string) {
			defer wg.Done()
			defer func() { <-semaphore }()
			ok, err := s.blockReader.BlockExists(ctx, blockHash, sizes[index])
			if err != nil {
				mu.Lock()
				failure = fmt.Errorf("check block %d: %w", index, err)
				mu.Unlock()
				return
			}
			if !ok {
				mu.Lock()
				failure = fmt.Errorf("%w: block %d (%s) is missing from storage", ErrEditBlockUnavailable, index, blockHash)
				mu.Unlock()
			}
		}(i, hash)
	}
	wg.Wait()
	return failure
}

// sniffBlockList identifies the document type from the bytes themselves: the
// first KiB for the PDF magic, the tail for the ZIP flavours. A renamed file
// must never reach a parser it does not belong to.
func (s *MetadataService) sniffBlockList(ctx context.Context, hashes []string, sizes []int64) (string, error) {
	if len(hashes) == 0 || s.blockReader == nil {
		return "", nil
	}
	head, err := s.readBlockPrefix(ctx, hashes[0], pdfSniffPrefix)
	if err != nil {
		return "", err
	}
	if bytes.Contains(head, []byte("%PDF-")) {
		return EditTypePDF, nil
	}
	if !bytes.HasPrefix(head, []byte("PK\x03\x04")) {
		return "", nil
	}
	last := len(hashes) - 1
	tailLength := sizes[last]
	if tailLength > zipSniffTail {
		tailLength = zipSniffTail
	}
	tail, err := s.readBlockSuffix(ctx, hashes[last], sizes[last], tailLength)
	if err != nil {
		return "", err
	}
	return zipFlavour(tail), nil
}

// zipFlavour tells the editable ZIP-based formats apart from their part names
// in the central directory, and refuses macro-enabled workbooks: those carry a
// vbaProject.bin and are not something an experimental editor should rewrite.
func zipFlavour(tail []byte) string {
	switch {
	case bytes.Contains(tail, []byte("xl/workbook.xml")):
		if bytes.Contains(tail, []byte("vbaProject.bin")) {
			return ""
		}
		return EditTypeXlsx
	case bytes.Contains(tail, []byte("word/document.xml")):
		return EditTypeDocx
	default:
		return ""
	}
}

// sniffFileVersion sniffs one version's type and caches it. The value is
// derived from bytes, never from the client-supplied content type, and it is
// cached rather than written back so version rows stay immutable.
func (s *MetadataService) sniffFileVersion(ctx context.Context, fileID string, version int32, hashes []string, blockSize int64) (string, error) {
	if len(hashes) == 0 || s.blockReader == nil {
		return "", nil
	}
	cacheKey := fmt.Sprintf("edit-sniff:%s:%d", fileID, version)
	if s.cache != nil {
		if cached, ok := s.cache.Get(cacheKey); ok {
			sniffedType, _ := cached.(string)
			return sniffedType, nil
		}
	}
	sizes := make([]int64, len(hashes))
	if blockSize <= 0 {
		// Unknown block size: sniff only the head, which is enough for PDF and
		// for ruling everything else out.
		sizes[len(sizes)-1] = 0
	} else {
		for i := range sizes {
			sizes[i] = blockSize
		}
	}
	sniffedType, err := s.sniffBlockList(ctx, hashes, sizes)
	if err != nil {
		return "", err
	}
	if s.cache != nil && sniffedType != "" {
		// Sniffing is stable per (file, version): the bytes cannot change.
		s.cache.SetWithTTL(cacheKey, sniffedType, 1, 24*time.Hour)
	}
	return sniffedType, nil
}

func (s *MetadataService) readBlockPrefix(ctx context.Context, hash string, length int64) ([]byte, error) {
	return s.readBlockRange(ctx, hash, 0, length)
}

func (s *MetadataService) readBlockSuffix(ctx context.Context, hash string, size, length int64) ([]byte, error) {
	if size <= 0 || length <= 0 {
		return nil, nil
	}
	offset := size - length
	if offset < 0 {
		offset, length = 0, size
	}
	return s.readBlockRange(ctx, hash, offset, length)
}

// readBlockRange reads at most length bytes from offset. S3 answers a range
// past the end of an object with an error, so a short block is clamped by the
// caller rather than guessed at here.
func (s *MetadataService) readBlockRange(ctx context.Context, hash string, offset, length int64) ([]byte, error) {
	if s.blockReader == nil || length <= 0 {
		return nil, nil
	}
	body, err := s.blockReader.ReadBlockRange(ctx, hash, offset, length)
	if err != nil {
		return nil, fmt.Errorf("read block %s: %w", hash, err)
	}
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, length))
	if err != nil {
		return nil, fmt.Errorf("read block %s body: %w", hash, err)
	}
	return data, nil
}

// applyCommittedVersion rewrites everything that describes the file's CURRENT
// content, best-effort and in an order that leaves a readable file if it stops
// part way: the version row (already written) and head are the truth, the
// projections and derived artifacts follow.
func (s *MetadataService) applyCommittedVersion(
	ctx context.Context,
	file repository.File,
	req CommitVersionRequest,
	sizes []int64,
	version int32,
	contentHash string,
	sniffedType string,
	filename string,
) error {
	now := time.Now().UTC()
	next := file
	next.SizeBytes = req.SizeBytes
	next.ContentHash = contentHash
	next.ContentType = contentTypeForEditType(sniffedType, req.ContentType)
	next.Filename = filename
	next.BlockHashList = req.BlockHashes
	next.Version = version
	next.UpdatedAt = now
	// The thumbnail is content-addressed by the block list, so a new version
	// needs a new key: clearing it and marking the row pending is what makes
	// the preview pipeline regenerate instead of serving the old rendition.
	if hasPreviewRendition(file.ContentFamily) {
		next.ThumbnailKey = ""
		next.ThumbnailStatus = "pending"
	}

	blocks := make([]repository.ManifestBlock, len(req.BlockHashes))
	for i, hash := range req.BlockHashes {
		blocks[i] = repository.ManifestBlock{Index: i, Hash: hash, SizeBytes: sizes[i]}
	}
	manifest := repository.FileManifestRow{
		FileID:      req.FileID,
		TotalSize:   req.SizeBytes,
		ContentType: next.ContentType,
		ContentHash: contentHash,
		Blocks:      blocks,
	}

	var firstErr error
	record := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	record(s.metadataRepo.ApplyHeadContent(ctx, file, next, manifest))
	// The manifest on disk is now the new version, but the content server holds
	// an L1 copy of the old one. Drop it before any reader (the editor's own
	// reload, pinned to the version just committed) can be served the previous
	// version's bytes.
	if s.manifestInvalidator != nil {
		s.manifestInvalidator.InvalidateManifest(req.FileID)
	}
	if hasPreviewRendition(file.ContentFamily) {
		// Best-effort like the create path: the file row is authoritative, so a
		// failed insert only costs the sweeper this one file.
		if err := s.metadataRepo.InsertThumbnailPending(ctx, repository.ThumbnailPendingRef{
			FileID: req.FileID, OwnerID: req.OwnerID, FileVersion: int64(version),
			CreatedAt: now, ContentFamily: int16(file.ContentFamily),
		}); err != nil {
			s.logger.Warn("thumbnail pending insert failed after commit",
				zap.String("fileID", req.FileID), zap.Int64("version", int64(version)), zap.Error(err))
		}
	}
	s.replaceIndexedItem(ctx, folderItemFromFile(file), folderItemFromFile(next))
	s.invalidateConsolidated(ctx, file.FileID)
	return firstErr
}

// invalidateConsolidated drops the Tier B objects of a file whose bytes just
// changed, then queues it for re-consolidation. Consolidated copies are keyed
// per file today (not per content hash), so a stale one would serve the
// previous version's bytes; deleting it is what makes that impossible.
func (s *MetadataService) invalidateConsolidated(ctx context.Context, fileID string) {
	if s.consolidated != nil {
		if err := s.consolidated.DeleteConsolidated(ctx, fileID); err != nil {
			s.logger.Warn("delete consolidated object failed", zap.String("fileID", fileID), zap.Error(err))
		}
	}
	if s.stitchInvalidator != nil {
		if err := s.stitchInvalidator.Delete(ctx, fileID); err != nil {
			s.logger.Warn("delete stitch record failed", zap.String("fileID", fileID), zap.Error(err))
		}
		if err := s.stitchInvalidator.Enqueue(ctx, fileID, time.Now().UTC()); err != nil {
			s.logger.Warn("re-enqueue stitch failed", zap.String("fileID", fileID), zap.Error(err))
		}
	}
}

// editEnabledFor reports whether a type's kill switch is on. A nil-free read of
// config keeps every caller from re-implementing the switch table.
func (s *MetadataService) editEnabledFor(sniffedType string) bool {
	switch sniffedType {
	case EditTypePDF:
		return s.cfg.EditPDFEnabled
	case EditTypeDocx:
		return s.cfg.EditDocxEnabled
	case EditTypeXlsx:
		return s.cfg.EditXlsxEnabled
	default:
		return false
	}
}

func (s *MetadataService) editCapFor(sniffedType string) int64 {
	switch sniffedType {
	case EditTypePDF:
		return s.cfg.EditPDFMaxBytes
	case EditTypeDocx:
		return s.cfg.EditDocxMaxBytes
	case EditTypeXlsx:
		return s.cfg.EditXlsxMaxBytes
	default:
		// An unknown type is not editable at all; the cap it reports is the
		// smallest one so a caller that ignores can_edit still refuses.
		return s.cfg.EditDocxMaxBytes
	}
}

// hasPreviewRendition reports whether a file's family is one the preview
// pipeline renders for. The file row already collapses every non-preview
// family to 0 at create time, so a non-zero family IS a preview family — the
// same gate the create path uses when it decides to write a pending row.
func hasPreviewRendition(family eventsv2.ContentFamily) bool {
	return family != eventsv2.ContentFamily_CONTENT_FAMILY_UNSPECIFIED
}

// contentTypeForEditType is the response MIME for a sniffed type, falling back
// to the client-supplied type only when the bytes said nothing.
func contentTypeForEditType(sniffedType, stored string) string {
	switch sniffedType {
	case EditTypePDF:
		return "application/pdf"
	case EditTypeDocx:
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case EditTypeXlsx:
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	default:
		return stored
	}
}
