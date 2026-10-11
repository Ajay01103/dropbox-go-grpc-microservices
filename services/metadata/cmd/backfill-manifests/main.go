// Command backfill-manifests populates file_manifest (the dense download
// manifest) for files created BEFORE this table existed — the rollout step the
// download plan calls out as "backfill existing files with a one-off job".
//
// Without a manifest row a file cannot be downloaded (the content server has
// no block sizes to map byte ranges onto), so pre-existing files would 404 on
// Download until this runs. Sizes come from upload_ks.blocks, which is the
// only place the block size is recorded.
//
// Idempotent and safe to re-run: files that already have a manifest keep it
// unless -overwrite is passed. Mismatched files (block sizes that do not sum to
// the recorded file size, or a missing block row) are reported and skipped,
// never half-written.
//
// A re-run is also the repair path for the Tier B work index: files enqueued
// before stitched_objects_by_state existed are already in stitched_objects, so
// the manifest writer does not reach them. Enqueue re-asserts their index row
// (idempotently), which is what makes the stitcher able to find them again.
//
//	go run ./cmd/backfill-manifests -dry-run     # report only
//	go run ./cmd/backfill-manifests              # write manifests + enqueue Tier B
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/gocql/gocql"
	"go.uber.org/zap"

	"github.com/Ajay01103/go-dropbox/metadata/config"
	mdb "github.com/Ajay01103/go-dropbox/metadata/db"
	"github.com/Ajay01103/go-dropbox/metadata/internal/repository"
)

const uploadKeyspace = "upload_ks"

func main() {
	dryRun := flag.Bool("dry-run", false, "report what would be written without writing anything")
	overwrite := flag.Bool("overwrite", false, "rewrite manifests that already exist")
	limit := flag.Int("limit", 0, "stop after N files (0 = all)")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	scylla := mdb.Config{
		Hosts:             cfg.ScyllaHosts,
		Port:              cfg.ScyllaPort,
		Username:          cfg.ScyllaUsername,
		Password:          cfg.ScyllaPassword,
		Consistency:       gocql.LocalQuorum,
		Datacenter:        cfg.ScyllaDatacenter,
		ReplicationFactor: cfg.ScyllaReplicationFactor,
	}

	session, err := mdb.Connect(ctx, scylla)
	if err != nil {
		logger.Fatal("connect metadata keyspace", zap.Error(err))
	}
	defer session.Close()

	blocks, err := mdb.OpenSession(scylla, uploadKeyspace)
	if err != nil {
		logger.Fatal("connect upload keyspace (needed for block sizes)", zap.Error(err))
	}
	defer blocks.Close()

	repo := repository.NewMetadataRepo(session)
	stitchRepo := repository.NewStitchRepo(session)

	var scanned, written, skippedExisting, skippedMismatch, skippedMissing, repairedStitch int
	iter := session.Query(
		`SELECT file_id, block_hash_list, size_bytes, content_type, content_hash FROM files_by_id`,
	).WithContext(ctx).PageSize(200).Iter()

	var fileID, contentType, contentHash string
	var blockHashList []string
	var sizeBytes int64
	for iter.Scan(&fileID, &blockHashList, &sizeBytes, &contentType, &contentHash) {
		scanned++
		if *limit > 0 && scanned > *limit {
			break
		}

		if !*overwrite {
			if _, err := repo.GetFileManifest(ctx, fileID); err == nil {
				skippedExisting++
				if *dryRun {
					continue
				}
				// Repair pass. Enqueue is idempotent and re-asserts the Tier B
				// work-index row, so re-running this command is also how files
				// enqueued before that index existed become visible to the
				// stitcher again.
				if err := stitchRepo.Enqueue(ctx, fileID, time.Now().UTC()); err != nil {
					logger.Warn("repair stitch work index", zap.String("fileID", fileID), zap.Error(err))
					continue
				}
				repairedStitch++
				continue
			}
		}
		if len(blockHashList) == 0 {
			skippedMissing++
			logger.Warn("no block hash list; cannot build a manifest", zap.String("fileID", fileID))
			continue
		}

		manifest := repository.FileManifestRow{
			FileID: fileID, TotalSize: sizeBytes, ContentType: contentType, ContentHash: contentHash,
		}
		var sum int64
		ok := true
		for index, hash := range blockHashList {
			var blockSize int64
			if err := blocks.Query(`SELECT size_bytes FROM blocks WHERE block_hash = ?`, hash).
				WithContext(ctx).Scan(&blockSize); err != nil {
				logger.Warn("block size unavailable; skipping file",
					zap.String("fileID", fileID), zap.String("blockHash", hash), zap.Error(err))
				ok = false
				skippedMissing++
				break
			}
			manifest.Blocks = append(manifest.Blocks, repository.ManifestBlock{Index: index, Hash: hash, SizeBytes: blockSize})
			sum += blockSize
		}
		if !ok {
			continue
		}
		if sum != sizeBytes {
			logger.Warn("block sizes do not sum to the recorded file size; skipping",
				zap.String("fileID", fileID), zap.Int64("sumBytes", sum), zap.Int64("recordedBytes", sizeBytes))
			skippedMismatch++
			continue
		}

		if *dryRun {
			written++
			continue
		}
		if err := repo.WriteFileManifest(ctx, manifest); err != nil {
			logger.Error("write manifest", zap.String("fileID", fileID), zap.Error(err))
			continue
		}
		// Enqueue Tier B so the stitcher consolidates this file too.
		if err := stitchRepo.Enqueue(ctx, fileID, time.Now().UTC()); err != nil && !errors.Is(err, gocql.ErrNotFound) {
			logger.Warn("enqueue stitch", zap.String("fileID", fileID), zap.Error(err))
		}
		written++
	}
	if err := iter.Close(); err != nil {
		logger.Fatal("scan files_by_id", zap.Error(err))
	}

	mode := "wrote"
	if *dryRun {
		mode = "would write"
	}
	fmt.Printf("scanned=%d %s=%d already_had_manifest=%d stitch_index_repaired=%d mismatched=%d missing_blocks=%d\n",
		scanned, mode, written, skippedExisting, repairedStitch, skippedMismatch, skippedMissing)
}
